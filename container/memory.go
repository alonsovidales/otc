// SPDX-License-Identifier: AGPL-3.0-or-later

package container

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// EnvMemoryGB sizes the device, in GB, when the container has no memory
// limit, or below the limit it has.
const EnvMemoryGB = "OTC_MEMORY_GB"

// cDefaultMemory is what a container with no memory limit (and no
// EnvMemoryGB) is sized as: an 8 GB Raspberry Pi 5, the machine the device
// is built and tested for. Sized to the whole of a large shared server it
// would take - and lock - far more than it needs.
const cDefaultMemory = 8 << 30

// cNoLimitV1: a cgroup v1 limit this large or more is no limit (the
// kernel writes its page counter's maximum, about 2^63, rounded to pages).
const cNoLimitV1 = 1 << 62

// root is where /proc and /sys are found: "/" but in tests.
var root = "/"

// MemoryTotal is the memory the device sizes itself by in a container
// (files_manager's profile and budgets, the Go limit, Status' MemSize):
// the least of the machine's MemTotal, the container's memory limit and
// EnvMemoryGB - or of MemTotal and 8 GB when the container has no limit
// and EnvMemoryGB is not set. 0 when the machine's memory is unknown.
func MemoryTotal() int64 {
	total, _ := MemorySizing()
	return total
}

// MemorySizing is MemoryTotal and why, for the start-up log: "limit"
// (the container's memory limit), "OTC_MEMORY_GB", "default" (no limit:
// sized as 8 GB) or "machine" (the machine has less than those).
func MemorySizing() (total int64, source string) {
	return memorySizing(memTotal(), memoryLimit(), os.Getenv(EnvMemoryGB))
}

// memorySizing is MemorySizing for the machine's memory (0: unknown), the
// container's limit (0: none) and the EnvMemoryGB setting.
func memorySizing(machine, limit int64, setting string) (int64, string) {
	total, source := machine, "machine"
	lower := func(v int64, why string) {
		if v > 0 && (total <= 0 || v < total) {
			total, source = v, why
		}
	}
	lower(limit, "limit")
	if gb, err := strconv.ParseFloat(strings.TrimSpace(setting), 64); err == nil && gb > 0 && gb < 1<<20 {
		lower(int64(gb*(1<<30)), EnvMemoryGB)
	} else if limit <= 0 {
		lower(cDefaultMemory, "default")
	}
	return total, source
}

// MemoryUsage is the memory the container uses now, as its cgroup counts
// it, less the file cache the kernel can take back at once (as `docker
// stats` shows it). 0 when it can't be read.
func MemoryUsage() int64 {
	cg, ok := memoryCgroup()
	if !ok {
		return 0
	}
	usageFile, inactiveKey := "memory.current", "inactive_file"
	if cg.v1 {
		usageFile, inactiveKey = "memory.usage_in_bytes", "total_inactive_file"
	}
	for _, dir := range cg.dirs() {
		raw, err := os.ReadFile(filepath.Join(dir, usageFile))
		if err != nil {
			continue
		}
		used, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
		if err != nil || used <= 0 {
			return 0
		}
		if inactive := statValue(filepath.Join(dir, "memory.stat"), inactiveKey); inactive > 0 && inactive < used {
			used -= inactive
		}
		return used
	}
	return 0
}

// memTotal is /proc/meminfo's MemTotal in bytes, 0 when unknown.
func memTotal() int64 {
	f, err := os.Open(filepath.Join(root, "proc/meminfo"))
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil && kb > 0 {
				return kb << 10
			}
			return 0
		}
	}
	return 0
}

// memoryLimit is the container's memory limit in bytes, 0 for none: the
// least limit set on its cgroup or any cgroup above it, as far up as this
// process can see. With the default private cgroup namespace that is just
// the namespace's root (the container's own cgroup); with the host's
// namespace the path runs up from /docker/<id> or a systemd slice, whose
// parents usually say "max" and whose root has no limit file at all.
func memoryLimit() int64 {
	cg, ok := memoryCgroup()
	if !ok {
		return 0
	}
	file := "memory.max"
	if cg.v1 {
		file = "memory.limit_in_bytes"
	}
	var limit int64
	for _, dir := range cg.dirs() {
		raw, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			continue // a level without the file: v2's root, a path not mounted here
		}
		v := strings.TrimSpace(string(raw))
		if v == "max" {
			continue
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 || (cg.v1 && n >= cNoLimitV1) {
			continue
		}
		if limit == 0 || n < limit {
			limit = n
		}
	}
	return limit
}

// cgroup is where this process's memory is accounted: the hierarchy's
// mount point and the process's path in it.
type cgroup struct {
	base string // /sys/fs/cgroup, or /sys/fs/cgroup/memory for v1
	path string // as /proc/self/cgroup shows it, cleaned: "/" or "/docker/<id>"
	v1   bool
}

// dirs are the cgroup's directory and each one above it, up to the
// hierarchy's root, leaf first.
func (c cgroup) dirs() []string {
	var out []string
	for p := c.path; ; p = path.Dir(p) {
		out = append(out, filepath.Join(c.base, filepath.FromSlash(p)))
		if p == "/" {
			return out
		}
	}
}

// memoryCgroup reads /proc/self/cgroup: the memory controller's line on
// cgroup v1 (also in a hybrid setup, where memory stays on v1), else the
// unified line ("0::/path") on v2.
func memoryCgroup() (cgroup, bool) {
	raw, err := os.ReadFile(filepath.Join(root, "proc/self/cgroup"))
	if err != nil {
		return cgroup{}, false
	}
	v2, v1, hasV2, hasV1 := parseCgroups(string(raw))
	v1Base := filepath.Join(root, "sys/fs/cgroup/memory")
	if hasV1 {
		if st, err := os.Stat(v1Base); err == nil && st.IsDir() {
			return cgroup{base: v1Base, path: v1, v1: true}, true
		}
	}
	if hasV2 {
		return cgroup{base: filepath.Join(root, "sys/fs/cgroup"), path: v2}, true
	}
	return cgroup{}, false
}

// parseCgroups picks the paths out of /proc/self/cgroup's lines,
// "hierarchy-id:controllers:path": the unified hierarchy's ("0::path") and
// the v1 memory controller's ("N:memory:path", "N:cpu,memory:path"...).
func parseCgroups(content string) (v2, v1 string, hasV2, hasV1 bool) {
	for _, line := range strings.Split(content, "\n") {
		id, rest, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		controllers, p, ok := strings.Cut(rest, ":")
		if !ok {
			continue
		}
		p = cleanCgroupPath(p)
		switch {
		case id == "0" && controllers == "":
			v2, hasV2 = p, true
		case hasController(controllers, "memory"):
			v1, hasV1 = p, true
		}
	}
	return
}

func hasController(list, name string) bool {
	for _, c := range strings.Split(list, ",") {
		if c == name {
			return true
		}
	}
	return false
}

// cleanCgroupPath makes a path from /proc/self/cgroup safe to join under
// the mount point: rooted, without "..", and without the " (deleted)" a
// removed cgroup shows.
func cleanCgroupPath(p string) string {
	p = strings.TrimSuffix(strings.TrimSpace(p), " (deleted)")
	return path.Clean("/" + p)
}

// statValue is key's value in a memory.stat file, 0 when absent.
func statValue(file, key string) int64 {
	f, err := os.Open(file)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && fields[0] == key {
			n, _ := strconv.ParseInt(fields[1], 10, 64)
			return n
		}
	}
	return 0
}

// DescribeSizing is the start-up log's line for a container's size:
// total and source as MemorySizing gives them, and the CPUs the Go runtime
// was given.
func DescribeSizing(total int64, source string, cpus int) string {
	why := "the machine's memory" // i18n-ignore: a log line
	switch source {
	case "limit":
		why = "the container's memory limit" // i18n-ignore: a log line
	case EnvMemoryGB:
		why = EnvMemoryGB
	case "default":
		why = "no memory limit, so sized as 8 GB: set mem_limit or " + EnvMemoryGB + " to change it" // i18n-ignore: a log line
	}
	return fmt.Sprintf("container: sized to %d MB of memory (%s) and %d CPUs", total>>20, why, cpus) // i18n-ignore: a log line
}
