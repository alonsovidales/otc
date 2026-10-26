// SPDX-License-Identifier: AGPL-3.0-or-later

package status

import (
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/container"
	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/raidwatch"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
	"golang.org/x/sys/unix"
)

// mdstatPath is a var, not a const, so tests can point it at a fixture
// file instead of the real /proc/mdstat.
var mdstatPath = "/proc/mdstat"

// raidInfo is what parseMdstat extracts from /proc/mdstat's content for the
// first array found there (this device only ever has the one, per
// CLAUDE.md's "RAID1 disk pair") — issue #65: the status widget should
// show RAID health (in sync, syncing, degraded) instead of the device's
// IP, which nothing ever actually populated anyway.
type raidInfo struct {
	state         pb.RaidState
	level         string
	devicesTotal  int32
	devicesActive int32
	syncPercent   float32
}

var (
	// e.g. "md0 : active raid1 sdb[2] sda[3]" or "md0 : inactive raid1 sda[3]"
	reMdLine = regexp.MustCompile(`(?m)^md\d+\s*:\s*(active|inactive)\s+(\S+)`)
	// e.g. "1001257984 blocks super 1.2 [2/2] [UU]" - device counts, not
	// the block count itself.
	reDeviceCount = regexp.MustCompile(`\[(\d+)/(\d+)\]`)
	reUpDown      = regexp.MustCompile(`\[([U_]+)\]`)
	// e.g. "[=====>...............]  resync = 25.5% (250071232/976630464)
	// finish=45.2min speed=90876K/sec" - "recovery" instead of "resync"
	// when rebuilding onto a freshly replaced disk rather than catching up
	// after an unclean shutdown, but the percentage means the same thing
	// either way for display purposes.
	reSyncPercent = regexp.MustCompile(`(?:resync|recovery)\s*=\s*([\d.]+)%`)
)

// parseMdstat reads /proc/mdstat's own text format (no library needed -
// it's plain text meant to be human-read, and the kernel guarantees this
// layout, unlike shelling out to `mdadm --detail`, which also needs a
// permission this device's own service user doesn't have).
func parseMdstat(content string) (info raidInfo, found bool) {
	m := reMdLine.FindStringSubmatch(content)
	if m == nil {
		return raidInfo{}, false
	}
	found = true
	active := m[1] == "active"
	info.level = m[2]

	if dc := reDeviceCount.FindStringSubmatch(content); dc != nil {
		// /proc/mdstat's "[N/M]" is [raid_disks/working_disks] - N is how
		// many devices the array is configured for, M how many are
		// currently active. Had this backwards initially (confirmed
		// against a real degraded array: mdadm reported "[2/1]" for a
		// 2-disk RAID1 down to 1 working disk, which this misread as "2
		// active out of 1 total").
		totalCount, _ := strconv.ParseInt(dc[1], 10, 32)
		activeCount, _ := strconv.ParseInt(dc[2], 10, 32)
		info.devicesTotal = int32(totalCount)
		info.devicesActive = int32(activeCount)
	}

	if sp := reSyncPercent.FindStringSubmatch(content); sp != nil {
		pct, _ := strconv.ParseFloat(sp[1], 32)
		info.syncPercent = float32(pct)
		info.state = pb.RaidState_RaidSyncing
		return info, true
	}

	switch {
	case !active:
		info.state = pb.RaidState_RaidDegraded
	case info.devicesTotal > 0 && info.devicesActive < info.devicesTotal:
		info.state = pb.RaidState_RaidDegraded
	default:
		// Belt and suspenders: the counts above should already catch a
		// missing device, but a literal "_" in the up/down string (e.g.
		// "[U_]") is the more direct signal mdadm itself uses for it.
		if ud := reUpDown.FindStringSubmatch(content); ud != nil && strings.Contains(ud[1], "_") {
			info.state = pb.RaidState_RaidDegraded
		} else {
			info.state = pb.RaidState_RaidInSync
		}
	}

	return info, true
}

// readRaidStatus reports this device's RAID health, or RaidState_NoRaid if
// there's simply no md array here at all (a single-disk test device, or
// any non-Linux/non-RAID setup) — not an error worth logging, just nothing
// to report.
//
// In a container /proc/mdstat is the host's, and its first array could be
// anything (a NAS's own system array): only the container's own mirror
// (docker/compose.mirror.yaml, container.MirrorArray) is reported, found by
// its name. Without one it is RaidState_RaidNone: the folder the device is
// given sits on whatever redundancy the host provides, and the host
// watches that.
func readRaidStatus() raidInfo {
	array := ""
	if container.Enabled() {
		if array = container.MirrorArray(); array == "" {
			return raidInfo{state: pb.RaidState_RaidNone}
		}
	}
	raw, err := os.ReadFile(mdstatPath)
	if err != nil {
		return raidInfo{state: pb.RaidState_RaidNone}
	}
	content := string(raw)
	if array != "" {
		// Only the mirror's own lines, its name put as parseMdstat (the
		// Raspberry Pi's md0) reads it: a host's mdadm may call it md_otc.
		if content = mdstatArray(content, array); content != "" {
			content = "md0" + content[len(array):]
		}
	}
	info, found := parseMdstat(content)
	if !found {
		return raidInfo{state: pb.RaidState_RaidNone}
	}
	return info
}

// mdstatArray is the part of /proc/mdstat's content about the array name
// ("md127"): its own line ("md127 : active raid1 ...") and the indented
// ones under it (its size and disks, a rebuild's progress, its bitmap), or
// "" when it isn't there.
func mdstatArray(content, name string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		head, rest, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(head) != name || strings.TrimLeft(line, " \t") != line {
			continue
		}
		if f := strings.Fields(rest); len(f) == 0 || (f[0] != "active" && f[0] != "inactive") {
			continue
		}
		end := i + 1
		for end < len(lines) && strings.TrimSpace(lines[end]) != "" && strings.TrimLeft(lines[end], " \t") != lines[end] {
			end++
		}
		return strings.Join(lines[i:end], "\n")
	}
	return ""
}

func diskUsage(path string) (all, used, free uint64, err error) {
	var stat unix.Statfs_t
	if err = unix.Statfs(path, &stat); err != nil {
		return
	}

	// block size * number of blocks
	all = stat.Blocks * uint64(stat.Bsize)
	free = stat.Bfree * uint64(stat.Bsize)
	used = all - free

	return
}

// GetStatus reports this device's health, its text (Status.errors) in
// lang, the request's language ("" for today's English).
func GetStatus(lang string) (st *pb.Status, err error) {
	sizeDisk, usedDisk, _, err := diskUsage("/")
	if err != nil {
		log.Error("error reading disk stats:", err)
	}
	sizeRaid, usedRaid, _, err := diskUsage(cfg.GetStr("otc", "storage-path"))
	if err != nil {
		log.Error("error reading RAID stats:", err)
	}
	// Non-blocking: cpu.Percent with an interval sleeps for that long to
	// take its sample, which made every status request take a full second
	// - felt directly by anything that polls this (the web status widget,
	// the Mac app's RAID icon). With a zero interval gopsutil reports the
	// usage since the *previous* call instead, so the first answer after
	// startup is 0 and every one after that is a real figure, at no cost.
	cpuPerc, err := cpu.Percent(0, false)
	if err != nil {
		log.Error("error reading CPU stats:", err)
	}
	vmStat, err := mem.VirtualMemory()
	if err != nil {
		log.Error("error reading memory usage:", err)
	}

	memSize, memUsed := vmStat.Total, vmStat.Used
	if container.Enabled() {
		memSize, memUsed = containerMemory(memSize, memUsed)
	}

	raid := readRaidStatus()
	var statusErrors []*pb.StatusErrors
	if raid.state == pb.RaidState_RaidDegraded {
		// Issue #65: reuses the existing StatusErrorCode_MissingDisk (the
		// widget already renders `errors` as a banner) rather than adding
		// a parallel notification path just for this one case.
		statusErrors = append(statusErrors, &pb.StatusErrors{
			StatusErrorCode: pb.StatusErrorCode_MissingDisk,
			Message:         degradedMessage(lang, raid),
		})
	}

	st = &pb.Status{
		Online:            true,
		Errors:            statusErrors,
		RaidSize:          int32(sizeRaid / 1024 / 1000),
		RaidUsage:         int32(usedRaid / 1024 / 1000),
		DiskSize:          int32(sizeDisk / 1024 / 1000),
		DiskUsage:         int32(usedDisk / 1024 / 1000),
		CpuUsagePrc:       float32(cpuPerc[0]),
		MemSize:           int32(memSize / 1024 / 1000),
		MemUsage:          int32(memUsed / 1024 / 1000),
		Disks:             raid.devicesTotal,
		RaidState:         raid.state,
		RaidLevel:         raid.level,
		RaidDevicesActive: raid.devicesActive,
		RaidSyncPercent:   raid.syncPercent,
	}

	return
}

// containerMemory is the memory figures (bytes) in a container: the memory
// the device is sized to (container.MemoryTotal: its limit, or 8 GB) and
// what its cgroup uses, each total and used - the machine's - when the
// container's can't be read.
func containerMemory(total, used uint64) (uint64, uint64) {
	if t := container.MemoryTotal(); t > 0 {
		total = uint64(t)
	}
	if u := container.MemoryUsage(); u > 0 {
		used = uint64(u)
	}
	return total, used
}

// degradedMessage is the degraded mirror's status line in lang, naming
// the disk that stopped working when raidwatch knows where it was plugged
// in (raidwatch.MissingPorts: the ports as data, raidwatch.Missing: the
// same in English), or else the one still working (raidwatch.StillWorking).
// The container's mirror names its disks as in .env instead
// (raidwatch.MissingDisks, raidwatch.StillWorkingDisk).
func degradedMessage(lang string, raid raidInfo) string {
	if container.MirrorArray() != "" {
		return degradedNamedText(lang, int(raid.devicesActive), int(raid.devicesTotal), raidwatch.MissingDisks(), raidwatch.Missing(), raidwatch.StillWorkingDisk())
	}
	return degradedText(lang, int(raid.devicesActive), int(raid.devicesTotal), raidwatch.MissingPorts(), raidwatch.Missing(), raidwatch.StillWorking())
}

// degradedText is degradedMessage for the disks missing at ports, label
// being raidwatch's English for them and working where the disk still
// working is.
func degradedText(lang string, active, total int, ports []raidwatch.Port, label string, working raidwatch.Port) string {
	switch {
	case len(ports) == 0:
		return i18n.DevStatusRaidDegraded(active, total).Render(lang)
	case len(ports) == 1:
		switch ports[0] {
		case raidwatch.PortTop:
			return i18n.DevStatusRaidDegradedTop(active, total).Render(lang)
		case raidwatch.PortBottom:
			return i18n.DevStatusRaidDegradedBottom(active, total).Render(lang)
		}
		switch working {
		case raidwatch.PortTop:
			return i18n.DevStatusRaidDegradedWorkingTop(active, total).Render(lang)
		case raidwatch.PortBottom:
			return i18n.DevStatusRaidDegradedWorkingBottom(active, total).Render(lang)
		}
		return i18n.DevStatusRaidDegradedUnknown(active, total).Render(lang)
	}
	return degradedSeveral(lang, active, total, label)
}

// degradedSeveral is the line for several disks missing, which a two-disk
// mirror that still runs can't have: named in English as they always were,
// from label, left out in other languages rather than joined from pieces.
func degradedSeveral(lang string, active, total int, label string) string {
	msg := i18n.DevStatusRaidDegraded(active, total).Render(lang)
	if label != "" && devtext.English(lang) {
		msg += " - " + label + " stopped working" // i18n-ignore: English only, for several missing disks, which no key names
	}
	return msg
}

// degradedNamedText is degradedText for the container's mirror: disks are
// the missing disks' names in .env ("" for one nothing is known of), label
// raidwatch's English for them and working the name of the disk still
// working, when the missing one's isn't known.
func degradedNamedText(lang string, active, total int, disks []string, label, working string) string {
	switch {
	case len(disks) == 0:
		return i18n.DevStatusRaidDegraded(active, total).Render(lang)
	case len(disks) == 1:
		if disks[0] != "" {
			return i18n.DevStatusRaidDegradedNamed(active, total, disks[0]).Render(lang)
		}
		if working != "" {
			return i18n.DevStatusRaidDegradedWorkingNamed(active, total, working).Render(lang)
		}
		return i18n.DevStatusRaidDegradedUnknown(active, total).Render(lang)
	}
	return degradedSeveral(lang, active, total, label)
}
