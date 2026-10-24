// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"runtime"
	"testing"
)

// In a container the memory every budget is sized from is the
// container's (container.MemoryTotal), which OTC_MEMORY_GB caps whatever
// the machine has; elsewhere it is still the machine's own MemTotal.
func TestMemTotalBytesInAContainer(t *testing.T) {
	t.Setenv("OTC_CONTAINER", "")
	machine := memTotalBytes()

	t.Setenv("OTC_CONTAINER", "1")
	t.Setenv("OTC_MEMORY_GB", "2")
	want := int64(2 << 30)
	if machine > 0 && machine < want {
		want = machine
	}
	if got := memTotalBytes(); got != want {
		t.Errorf("in a container with OTC_MEMORY_GB=2: %d, want %d", got, want)
	}
	if got := MemTotalBytes(); got != memTotalBytes() {
		t.Errorf("MemTotalBytes %d, memTotalBytes %d", got, memTotalBytes())
	}

	t.Setenv("OTC_CONTAINER", "")
	if got := memTotalBytes(); got != machine {
		t.Errorf("elsewhere: %d, want the machine's %d", got, machine)
	}
}

// In a container the CPUs are the runtime's (its CPU limit), not the
// host's; elsewhere the machine's.
func TestCPUCountInAContainer(t *testing.T) {
	saved := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(saved) })

	t.Setenv("OTC_CONTAINER", "1")
	if got := cpuCount(); got != 1 {
		t.Errorf("in a container with GOMAXPROCS 1: %d", got)
	}
	t.Setenv("OTC_CONTAINER", "")
	if got := cpuCount(); got != runtime.NumCPU() {
		t.Errorf("elsewhere: %d, want NumCPU %d", got, runtime.NumCPU())
	}
}

// ffmpeg's threads in a container: the container's CPUs, at most four, and
// no more than low-memory mode's two.
func TestContainerFFmpegThreads(t *testing.T) {
	for _, c := range []struct {
		procs   int
		current string
		want    string
	}{
		{1, "", "1"},
		{2, "", "2"},
		{4, "", "4"},
		{16, "", "4"},
		{16, cFFmpegLowThreads, "2"},
		{1, cFFmpegLowThreads, "1"},
		{8, "junk", "4"},
		{0, "", "1"},
	} {
		if got := containerFFmpegThreads(c.procs, c.current); got != c.want {
			t.Errorf("%d CPUs, %q set: %q, want %q", c.procs, c.current, got, c.want)
		}
	}
}
