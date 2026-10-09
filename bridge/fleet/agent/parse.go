// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"bufio"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/alonsovidales/otc/bridge/fleet"
)

// The parsers here take a file's or a command's text and never touch the
// system, so they are tested on any machine (parse_test.go).

// ParseMeminfo reads /proc/meminfo (values in kB).
func ParseMeminfo(s string) (*fleet.Memory, error) {
	kv := map[string]uint64{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		v, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			continue
		}
		if len(f) > 1 && f[1] == "kB" {
			v *= 1024
		}
		kv[strings.TrimSpace(name)] = v
	}
	total, ok := kv["MemTotal"]
	if !ok || total == 0 {
		return nil, errors.New("no MemTotal in /proc/meminfo")
	}
	m := &fleet.Memory{Total: total, SwapTotal: kv["SwapTotal"], SwapFree: kv["SwapFree"]}
	if a, ok := kv["MemAvailable"]; ok {
		m.Available = a
	} else { // kernels before 3.14
		m.Available = kv["MemFree"] + kv["Buffers"] + kv["Cached"]
	}
	return m, nil
}

var (
	reMDHead  = regexp.MustCompile(`^(md[^\s]*) : (active|inactive)\b(.*)$`)
	reMDCount = regexp.MustCompile(`\[(\d+)/(\d+)\]\s+\[([U_]+)\]`)
	reMDSync  = regexp.MustCompile(`\b(recovery|resync|reshape|check|repair)\s*=\s*([0-9.]+%)`)
	reMDWait  = regexp.MustCompile(`\b(resync|recovery|reshape|check)\s*=\s*(DELAYED|PENDING)`)
)

// ParseMdstat reads /proc/mdstat. An empty text, or one listing no array,
// is "no RAID" (Present false), not an error.
func ParseMdstat(s string) *fleet.RAID {
	r := &fleet.RAID{Arrays: []fleet.MDArray{}}
	var cur *fleet.MDArray
	flush := func() {
		if cur != nil {
			r.Arrays = append(r.Arrays, *cur)
			cur = nil
		}
	}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := sc.Text()
		if m := reMDHead.FindStringSubmatch(line); m != nil {
			flush()
			cur = &fleet.MDArray{Name: m[1], Active: m[2] == "active"}
			for _, f := range strings.Fields(m[3]) {
				switch {
				case strings.HasPrefix(f, "("): // (read-only), (auto-read-only)
				case strings.Contains(f, "["): // a member: sda2[0], sdb2[1](F)
					cur.Devices++
					if strings.HasSuffix(f, "(F)") {
						cur.Failed++
					}
				default:
					cur.Level = f
				}
			}
			continue
		}
		if cur == nil {
			continue
		}
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if m := reMDCount.FindStringSubmatch(line); m != nil {
			cur.Want, _ = strconv.Atoi(m[1])
			cur.Have, _ = strconv.Atoi(m[2])
			cur.Status = "[" + m[3] + "]"
		}
		if m := reMDSync.FindStringSubmatch(line); m != nil {
			cur.Sync = m[1] + " " + m[2]
		} else if m := reMDWait.FindStringSubmatch(line); m != nil {
			cur.Sync = m[1] + " " + strings.ToLower(m[2])
		}
	}
	flush()
	r.Present = len(r.Arrays) > 0
	return r
}

// ParseLoadavg reads /proc/loadavg.
func ParseLoadavg(s string) ([3]float64, error) {
	var out [3]float64
	f := strings.Fields(s)
	if len(f) < 3 {
		return out, errors.New("short /proc/loadavg")
	}
	for i := 0; i < 3; i++ {
		v, err := strconv.ParseFloat(f[i], 64)
		if err != nil {
			return out, err
		}
		out[i] = v
	}
	return out, nil
}

// CPUTimes is the aggregate "cpu" line of /proc/stat, in jiffies.
type CPUTimes struct {
	Total, Idle, IOWait, Steal uint64
}

// ParseCPUStat reads the aggregate line and the boot time (btime) of
// /proc/stat.
func ParseCPUStat(s string) (t CPUTimes, btime int64, err error) {
	found := false
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "cpu":
			// user nice system idle iowait irq softirq steal [guest
			// guest_nice] - guest time is already in user and nice.
			var v [8]uint64
			for i := 0; i < 8 && i+1 < len(f); i++ {
				v[i], _ = strconv.ParseUint(f[i+1], 10, 64)
			}
			for _, x := range v {
				t.Total += x
			}
			t.Idle, t.IOWait, t.Steal = v[3], v[4], v[7]
			found = true
		case "btime":
			if len(f) > 1 {
				btime, _ = strconv.ParseInt(f[1], 10, 64)
			}
		}
	}
	if !found {
		return t, btime, errors.New("no cpu line in /proc/stat")
	}
	return t, btime, nil
}

// CPUPercent is the CPU use between two samples; nil when no time passed.
func CPUPercent(a, b CPUTimes) *fleet.CPUUsage {
	if b.Total <= a.Total {
		return nil
	}
	d := float64(b.Total - a.Total)
	pct := func(x, y uint64) float64 {
		if y < x {
			return 0
		}
		return round1(float64(y-x) / d * 100)
	}
	idle := pct(a.Idle+a.IOWait, b.Idle+b.IOWait)
	return &fleet.CPUUsage{
		Busy:   round1(100 - idle),
		IOWait: pct(a.IOWait, b.IOWait),
		Steal:  pct(a.Steal, b.Steal),
	}
}

func round1(f float64) float64 {
	return float64(int64(f*10+0.5)) / 10
}

// NetCounters is one interface's counters from /proc/net/dev.
type NetCounters struct {
	Rx, Tx, RxErrs, TxErrs uint64
}

// ParseNetDev reads /proc/net/dev, loopback left out.
func ParseNetDev(s string) map[string]NetCounters {
	out := map[string]NetCounters{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		f := strings.Fields(rest)
		if name == "lo" || len(f) < 11 {
			continue
		}
		n := func(i int) uint64 { v, _ := strconv.ParseUint(f[i], 10, 64); return v }
		out[name] = NetCounters{Rx: n(0), RxErrs: n(2), Tx: n(8), TxErrs: n(10)}
	}
	return out
}

// NetRates turns two samples secs apart into rates, sorted by name.
func NetRates(a, b map[string]NetCounters, secs float64) []fleet.NetIf {
	var out []fleet.NetIf
	for name, nb := range b {
		i := fleet.NetIf{Name: name, RxErrs: nb.RxErrs, TxErrs: nb.TxErrs}
		if na, ok := a[name]; ok && secs > 0 && nb.Rx >= na.Rx && nb.Tx >= na.Tx {
			i.RxBps = float64(int64(float64(nb.Rx-na.Rx) / secs))
			i.TxBps = float64(int64(float64(nb.Tx-na.Tx) / secs))
		}
		out = append(out, i)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ParseUptime reads /proc/uptime (seconds).
func ParseUptime(s string) (int64, error) {
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0, errors.New("empty /proc/uptime")
	}
	v, err := strconv.ParseFloat(f[0], 64)
	return int64(v), err
}

// Mount is one line of /proc/self/mounts.
type Mount struct{ Device, Point, FS string }

// diskFS are the filesystems that hold data; everything else (tmpfs,
// proc, overlay, squashfs snaps...) is left out.
var diskFS = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true,
	"zfs": true, "f2fs": true, "vfat": true, "jfs": true, "reiserfs": true,
}

// ParseMounts reads /proc/self/mounts: real filesystems only, each device
// and each mount point once (the first line for it).
func ParseMounts(s string) []Mount {
	var out []Mount
	seen := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 || !diskFS[f[2]] {
			continue
		}
		point := unescapeMount(f[1])
		if strings.HasPrefix(point, "/snap/") || strings.HasPrefix(point, "/run/") ||
			strings.HasPrefix(point, "/proc/") || strings.HasPrefix(point, "/sys/") || strings.HasPrefix(point, "/dev/") {
			continue
		}
		// Each device once, and each mount point once (mounts stacked on
		// one point all statfs the same, the top one).
		dev := f[0]
		if seen["dev:"+dev] || seen["at:"+point] {
			continue
		}
		seen["dev:"+dev], seen["at:"+point] = true, true
		out = append(out, Mount{Device: dev, Point: point, FS: f[2]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Point < out[j].Point })
	return out
}

// unescapeMount undoes the kernel's octal escapes (\040 for a space).
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ParseOSRelease is PRETTY_NAME from /etc/os-release.
func ParseOSRelease(s string) string {
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "PRETTY_NAME="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

// ParseAptCheck reads update-notifier's apt-check output: "<updates>;<security>".
func ParseAptCheck(s string) (total, security int, err error) {
	a, b, ok := strings.Cut(strings.TrimSpace(s), ";")
	if !ok {
		return 0, 0, errors.New("unexpected apt-check output")
	}
	if total, err = strconv.Atoi(strings.TrimSpace(a)); err != nil {
		return 0, 0, err
	}
	if security, err = strconv.Atoi(strings.TrimSpace(b)); err != nil {
		return 0, 0, err
	}
	return total, security, nil
}

// ParseFailedUnits reads `systemctl list-units --state=failed --no-legend
// --plain`: the unit names.
func ParseFailedUnits(s string) []string {
	out := []string{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(strings.TrimLeft(sc.Text(), "●* \t"))
		if len(f) > 0 && strings.Contains(f[0], ".") {
			out = append(out, f[0])
		}
	}
	return out
}

// ParseIsActive pairs `systemctl is-active a b c`'s lines with the units
// asked for.
func ParseIsActive(units []string, s string) map[string]string {
	out := map[string]string{}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, u := range units {
		st := "unknown"
		if i < len(lines) && strings.TrimSpace(lines[i]) != "" {
			st = strings.TrimSpace(lines[i])
		}
		out[u] = st
	}
	return out
}

// ParseRedisInfo reads INFO's "key:value" lines (sections ignored) and
// sums the keys of every database.
func ParseRedisInfo(s string) *fleet.RedisStatus {
	kv := map[string]string{}
	var keys int64
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		k, v, ok := strings.Cut(line, ":")
		if !ok || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(k, "db") {
			// db0:keys=12,expires=10,avg_ttl=0
			for _, part := range strings.Split(v, ",") {
				if n, ok := strings.CutPrefix(part, "keys="); ok {
					x, _ := strconv.ParseInt(n, 10, 64)
					keys += x
				}
			}
			continue
		}
		kv[k] = v
	}
	n := func(k string) int64 { x, _ := strconv.ParseInt(kv[k], 10, 64); return x }
	return &fleet.RedisStatus{
		Up:        true,
		Version:   kv["redis_version"],
		Uptime:    n("uptime_in_seconds"),
		Used:      n("used_memory"),
		MaxMemory: n("maxmemory"),
		Clients:   n("connected_clients"),
		Keys:      keys,
		Evicted:   n("evicted_keys"),
		Rejected:  n("rejected_connections"),
	}
}
