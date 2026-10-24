// SPDX-License-Identifier: AGPL-3.0-or-later

package container

import (
	"os"
	"path/filepath"
	"testing"
)

const gib = int64(1) << 30

// fakeRoot points /proc and /sys at a test's own folder, with files given
// relative to it ("proc/self/cgroup": "0::/").
func fakeRoot(t *testing.T, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		write(t, filepath.Join(dir, name), content)
	}
	saved := root
	root = dir
	t.Cleanup(func() { root = saved })
}

const meminfo = "MemTotal:        6053280 kB\nMemFree:         1000000 kB\n"

func TestParseCgroups(t *testing.T) {
	for _, c := range []struct {
		name         string
		in           string
		v2, v1       string
		hasV2, hasV1 bool
	}{
		{"v2, private namespace", "0::/\n", "/", "", true, false},
		{"v2, host namespace", "0::/docker/abc\n", "/docker/abc", "", true, false},
		{"v2, systemd driver", "0::/system.slice/docker-abc.scope\n", "/system.slice/docker-abc.scope", "", true, false},
		{"v2, moved out of the namespace's view", "0::/../../user.slice\n", "/user.slice", "", true, false},
		{"v2, removed", "0::/docker/abc (deleted)\n", "/docker/abc", "", true, false},
		{"v1", "12:memory:/docker/abc\n11:cpu,cpuacct:/docker/abc\n1:name=systemd:/docker/abc\n", "", "/docker/abc", false, true},
		{"v1, memory beside another controller", "5:cpuset,memory:/lxc/x\n", "", "/lxc/x", false, true},
		{"v1, memsw is not memory", "4:memsw:/x\n", "", "", false, false},
		{"hybrid", "12:memory:/docker/abc\n0::/docker/abc\n", "/docker/abc", "/docker/abc", true, true},
		{"nothing", "garbage\n\n", "", "", false, false},
	} {
		v2, v1, hasV2, hasV1 := parseCgroups(c.in)
		if v2 != c.v2 || v1 != c.v1 || hasV2 != c.hasV2 || hasV1 != c.hasV1 {
			t.Errorf("%s: %q %q %v %v", c.name, v2, v1, hasV2, hasV1)
		}
	}
}

func TestMemoryLimit(t *testing.T) {
	for _, c := range []struct {
		name  string
		files map[string]string
		want  int64
	}{
		{"v2, private namespace, a limit", map[string]string{
			"proc/self/cgroup":         "0::/\n",
			"sys/fs/cgroup/memory.max": "3221225472\n",
		}, 3 * gib},
		{"v2, private namespace, no limit", map[string]string{
			"proc/self/cgroup":         "0::/\n",
			"sys/fs/cgroup/memory.max": "max\n",
		}, 0},
		{"v2, host namespace: the root has no file, the parent says max", map[string]string{
			"proc/self/cgroup":                    "0::/docker/abc\n",
			"sys/fs/cgroup/docker/memory.max":     "max\n",
			"sys/fs/cgroup/docker/abc/memory.max": "3221225472\n",
		}, 3 * gib},
		{"v2, nested: the least limit on the way up", map[string]string{
			"proc/self/cgroup":                                       "0::/system.slice/docker-abc.scope\n",
			"sys/fs/cgroup/system.slice/memory.max":                  "2147483648\n",
			"sys/fs/cgroup/system.slice/docker-abc.scope/memory.max": "4294967296\n",
		}, 2 * gib},
		{"v2, host namespace, path not mounted here", map[string]string{
			"proc/self/cgroup":         "0::/docker/abc\n",
			"sys/fs/cgroup/memory.max": "1073741824\n",
		}, gib},
		{"v2, an unreadable value is skipped", map[string]string{
			"proc/self/cgroup":                    "0::/docker/abc\n",
			"sys/fs/cgroup/docker/memory.max":     "lots\n",
			"sys/fs/cgroup/docker/abc/memory.max": "max\n",
		}, 0},
		{"v1, host namespace", map[string]string{
			"proc/self/cgroup":                                      "12:memory:/docker/abc\n11:cpu,cpuacct:/docker/abc\n",
			"sys/fs/cgroup/memory/memory.limit_in_bytes":            "9223372036854771712\n",
			"sys/fs/cgroup/memory/docker/abc/memory.limit_in_bytes": "3221225472\n",
		}, 3 * gib},
		{"v1, the container's own cgroup mounted as the root", map[string]string{
			"proc/self/cgroup":                           "12:memory:/docker/abc\n",
			"sys/fs/cgroup/memory/memory.limit_in_bytes": "3221225472\n",
		}, 3 * gib},
		{"v1, no limit", map[string]string{
			"proc/self/cgroup":                           "12:memory:/\n",
			"sys/fs/cgroup/memory/memory.limit_in_bytes": "9223372036854771712\n",
		}, 0},
		{"hybrid: memory on v1", map[string]string{
			"proc/self/cgroup": "12:memory:/docker/abc\n0::/docker/abc\n",
			"sys/fs/cgroup/memory/docker/abc/memory.limit_in_bytes": "2147483648\n",
			"sys/fs/cgroup/unified/docker/abc/memory.max":           "max\n",
		}, 2 * gib},
		{"v1 line without its mount: v2's", map[string]string{
			"proc/self/cgroup":         "12:memory:/docker/abc\n0::/\n",
			"sys/fs/cgroup/memory.max": "1073741824\n",
		}, gib},
		{"no /proc/self/cgroup", map[string]string{
			"sys/fs/cgroup/memory.max": "1073741824\n",
		}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			fakeRoot(t, c.files)
			if got := memoryLimit(); got != c.want {
				t.Errorf("limit %d, want %d", got, c.want)
			}
		})
	}
}

func TestMemorySizingRule(t *testing.T) {
	for _, c := range []struct {
		name           string
		machine, limit int64
		setting        string
		want           int64
		source         string
	}{
		{"a limit below the machine", 6 * gib, 3 * gib, "", 3 * gib, "limit"},
		{"a limit above the machine", 6 * gib, 16 * gib, "", 6 * gib, "machine"},
		{"no limit on a large server: 8 GB", 64 * gib, 0, "", 8 * gib, "default"},
		{"no limit on a small machine", 6 * gib, 0, "", 6 * gib, "machine"},
		{"no limit, OTC_MEMORY_GB", 64 * gib, 0, "16", 16 * gib, EnvMemoryGB},
		{"OTC_MEMORY_GB in halves", 64 * gib, 0, "2.5", 5 * gib / 2, EnvMemoryGB},
		{"OTC_MEMORY_GB above the limit", 64 * gib, 4 * gib, "16", 4 * gib, "limit"},
		{"OTC_MEMORY_GB below the limit", 64 * gib, 32 * gib, "16", 16 * gib, EnvMemoryGB},
		{"OTC_MEMORY_GB above the machine", 6 * gib, 0, "16", 6 * gib, "machine"},
		{"OTC_MEMORY_GB not a number", 64 * gib, 0, "lots", 8 * gib, "default"},
		{"OTC_MEMORY_GB zero", 64 * gib, 0, "0", 8 * gib, "default"},
		{"OTC_MEMORY_GB negative", 64 * gib, 0, "-4", 8 * gib, "default"},
		{"OTC_MEMORY_GB not a number, with a limit", 64 * gib, 4 * gib, "x", 4 * gib, "limit"},
		{"machine unknown, a limit", 0, 3 * gib, "", 3 * gib, "limit"},
	} {
		got, source := memorySizing(c.machine, c.limit, c.setting)
		if got != c.want || source != c.source {
			t.Errorf("%s: %d (%s), want %d (%s)", c.name, got, source, c.want, c.source)
		}
	}
}

func TestMemoryTotal(t *testing.T) {
	fakeRoot(t, map[string]string{
		"proc/meminfo":             meminfo,
		"proc/self/cgroup":         "0::/\n",
		"sys/fs/cgroup/memory.max": "3221225472\n",
	})
	os.Unsetenv(EnvMemoryGB)
	if got := MemoryTotal(); got != 3*gib {
		t.Errorf("with a 3 GB limit: %d", got)
	}
	t.Setenv(EnvMemoryGB, "2")
	if got, source := MemorySizing(); got != 2*gib || source != EnvMemoryGB {
		t.Errorf("with OTC_MEMORY_GB=2: %d (%s)", got, source)
	}

	fakeRoot(t, map[string]string{
		"proc/meminfo":             meminfo,
		"proc/self/cgroup":         "0::/\n",
		"sys/fs/cgroup/memory.max": "max\n",
	})
	os.Unsetenv(EnvMemoryGB)
	if got := MemoryTotal(); got != 6053280<<10 {
		t.Errorf("no limit, a 6 GB machine: %d", got)
	}
}

func TestMemoryUsage(t *testing.T) {
	fakeRoot(t, map[string]string{
		"proc/self/cgroup":                        "0::/docker/abc\n",
		"sys/fs/cgroup/docker/abc/memory.current": "1000000\n",
		"sys/fs/cgroup/docker/abc/memory.stat":    "anon 700000\nfile 300000\ninactive_file 200000\nactive_file 100000\n",
	})
	if got := MemoryUsage(); got != 800000 {
		t.Errorf("v2: %d, want 800000", got)
	}

	fakeRoot(t, map[string]string{
		"proc/self/cgroup":             "0::/\n",
		"sys/fs/cgroup/memory.current": "500000\n",
	})
	if got := MemoryUsage(); got != 500000 {
		t.Errorf("v2 without memory.stat: %d, want 500000", got)
	}

	fakeRoot(t, map[string]string{
		"proc/self/cgroup": "9:memory:/docker/abc\n",
		"sys/fs/cgroup/memory/docker/abc/memory.usage_in_bytes": "2000000\n",
		"sys/fs/cgroup/memory/docker/abc/memory.stat":           "cache 900000\ninactive_file 1\ntotal_inactive_file 500000\n",
	})
	if got := MemoryUsage(); got != 1500000 {
		t.Errorf("v1: %d, want 1500000", got)
	}

	fakeRoot(t, map[string]string{"proc/self/cgroup": "0::/docker/abc\n"})
	if got := MemoryUsage(); got != 0 {
		t.Errorf("nothing to read: %d", got)
	}
}

func TestMemTotal(t *testing.T) {
	fakeRoot(t, map[string]string{"proc/meminfo": meminfo})
	if got := memTotal(); got != 6053280<<10 {
		t.Errorf("%d", got)
	}
	fakeRoot(t, map[string]string{"proc/meminfo": "MemFree: 1 kB\n"})
	if got := memTotal(); got != 0 {
		t.Errorf("without MemTotal: %d", got)
	}
}
