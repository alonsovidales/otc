// SPDX-License-Identifier: AGPL-3.0-or-later

package status

import (
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
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
func readRaidStatus() raidInfo {
	content, err := os.ReadFile(mdstatPath)
	if err != nil {
		return raidInfo{state: pb.RaidState_RaidNone}
	}
	info, found := parseMdstat(string(content))
	if !found {
		return raidInfo{state: pb.RaidState_RaidNone}
	}
	return info
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

func GetStatus() (st *pb.Status, err error) {
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

	raid := readRaidStatus()
	var statusErrors []*pb.StatusErrors
	if raid.state == pb.RaidState_RaidDegraded {
		// Issue #65: reuses the existing StatusErrorCode_MissingDisk (the
		// widget already renders `errors` as a banner) rather than adding
		// a parallel notification path just for this one case.
		statusErrors = append(statusErrors, &pb.StatusErrors{
			StatusErrorCode: pb.StatusErrorCode_MissingDisk,
			Message:         "RAID array degraded: only " + strconv.Itoa(int(raid.devicesActive)) + " of " + strconv.Itoa(int(raid.devicesTotal)) + " disks active",
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
		MemSize:           int32(vmStat.Total / 1024 / 1000),
		MemUsage:          int32(vmStat.Used / 1024 / 1000),
		Disks:             raid.devicesTotal,
		RaidState:         raid.state,
		RaidLevel:         raid.level,
		RaidDevicesActive: raid.devicesActive,
		RaidSyncPercent:   raid.syncPercent,
	}

	return
}
