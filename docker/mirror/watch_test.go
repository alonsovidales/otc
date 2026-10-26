// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func newWatcher(f *fake) *watcher {
	f.nodes["/dev/md127"] = "9:127" // made by prepare
	f.h.lookUp()
	return &watcher{h: f.h, md: "md127"}
}

func TestWatchStateLines(t *testing.T) {
	f := newFake(t)
	f.array("md127", "raid1", 2, sdb, sdc)
	w := newWatcher(f)
	t0 := clock
	w.step(t0)
	if got := f.said(); !slices.Equal(got, []string{"the mirror md127: both disks in sync"}) {
		t.Fatalf("said %q", got)
	}
	w.step(t0.Add(5 * time.Second))
	if got := f.said(); got != nil {
		t.Fatalf("said %q with nothing changed", got)
	}

	// ata-TWO fails
	f.member("md127", sdc.is("faulty"))
	f.set("md127", "degraded", "1")
	w.step(t0.Add(10 * time.Second))
	if got := f.said(); !slices.Equal(got, []string{"the mirror md127: ata-TWO stopped working (faulty)", "the mirror md127: degraded, only ata-ONE works"}) {
		t.Fatalf("said %q", got)
	}

	// it goes (removed, or the disk unplugged), and a replacement is rebuilt
	_ = os.RemoveAll(filepath.Join(f.h.sys, "block/md127/md/dev-sdc"))
	w.step(t0.Add(15 * time.Second))
	if got := f.said(); got != nil {
		t.Fatalf("said %q for a faulty member that went", got)
	}
	f.member("md127", sdc.is("spare"))
	f.set("md127", "sync_action", "recover")
	f.set("md127", "sync_completed", "39062500 / 3906250000")
	w.step(t0.Add(20 * time.Second))
	if got := f.said(); !slices.Equal(got, []string{"the mirror md127: rebuilding onto ata-TWO, 1%"}) {
		t.Fatalf("said %q", got)
	}
	// progress: every 10 minutes, not on every look
	f.set("md127", "sync_completed", "78125000 / 3906250000")
	w.step(t0.Add(5 * time.Minute))
	if got := f.said(); got != nil {
		t.Fatalf("said %q before 10 minutes", got)
	}
	w.step(t0.Add(20*time.Second + 10*time.Minute))
	if got := f.said(); !slices.Equal(got, []string{"the mirror md127: rebuilding onto ata-TWO, 2%"}) {
		t.Fatalf("said %q", got)
	}

	// done
	f.member("md127", sdc)
	f.set("md127", "sync_action", "idle")
	f.set("md127", "sync_completed", "none")
	f.set("md127", "degraded", "0")
	w.step(t0.Add(11 * time.Minute))
	if got := f.said(); !slices.Equal(got, []string{"the mirror md127: both disks in sync"}) {
		t.Fatalf("said %q", got)
	}

	// a member that vanishes while working, then the array stopped on the host
	_ = os.RemoveAll(filepath.Join(f.h.sys, "block/md127/md/dev-sdb"))
	f.set("md127", "degraded", "1")
	w.step(t0.Add(12 * time.Minute))
	if got := f.said(); !slices.Equal(got, []string{"the mirror md127: ata-ONE stopped working (gone)", "the mirror md127: degraded, only ata-TWO works"}) {
		t.Fatalf("said %q", got)
	}
	_ = os.RemoveAll(filepath.Join(f.h.sys, "block/md127"))
	w.step(t0.Add(13 * time.Minute))
	if got := f.said(); !slices.Equal(got, []string{"the mirror md127: not running on this server any more"}) {
		t.Fatalf("said %q", got)
	}
	f.wantChanges()
}

func TestWatchStartsOnAFaultyMemberQuietly(t *testing.T) {
	f := newFake(t)
	f.array("md127", "raid1", 2, sdb, sdc.is("faulty"))
	w := newWatcher(f)
	w.step(clock)
	if got := f.said(); !slices.Equal(got, []string{"the mirror md127: degraded, only ata-ONE works"}) {
		t.Fatalf("said %q", got)
	}
}

func TestWatchResyncAndCheck(t *testing.T) {
	f := newFake(t)
	f.array("md127", "raid1", 2, sdb, sdc)
	f.set("md127", "sync_action", "resync")
	f.set("md127", "sync_completed", "1953125000 / 3906250000")
	w := newWatcher(f)
	w.step(clock)
	f.set("md127", "sync_action", "check")
	w.step(clock.Add(5 * time.Second))
	if got := f.said(); !slices.Equal(got, []string{"the mirror md127: resyncing the two disks, 50%", "the mirror md127: checking the two disks, 50%"}) {
		t.Fatalf("said %q", got)
	}
	f.wantChanges() // never grown while copying
}

const (
	grow   = growCmd + " /dev/md127 --size=max"
	resize = "resize2fs /dev/md127"
)

// bigger is a 2 TB mirror with a 2 TB filesystem on two 4 TB disks.
func bigger(t *testing.T) *fake {
	f := newFake(t)
	f.array("md127", "raid1", 2, sdb, sdc)
	f.set("md127", "component_size", "1953125000")
	f.on("blockdev --getsize64 /dev/md127", result{out: "2000000000000\n"})
	f.on("dumpe2fs -h /dev/md127", result{out: "Filesystem volume name:   otc-storage\nBlock count:              488281250\nBlock size:               4096\n"})
	return f
}

func TestGrowOntoBiggerDisks(t *testing.T) {
	f := bigger(t)
	f.on("blockdev --getsize64 /dev/md127", result{out: "3999865782272\n"}) // once grown
	newWatcher(f).step(clock)
	f.wantChanges(grow, resize)
	wantSaid(t, f.said(), "the mirror md127: both disks have room to spare, growing it", "the mirror md127 grown",
		"growing the filesystem on the mirror md127", "the filesystem on the mirror md127 grown")
}

func TestGrowDecision(t *testing.T) {
	// one disk still the old size: nothing
	f := bigger(t)
	f.disk(2, "sdc", "8:32", 2e12+(262144*512)+100<<20)
	newWatcher(f).step(clock)
	f.wantChanges()

	// room enough on both
	f = bigger(t)
	f.disk(2, "sdc", "8:32", 2e12+(262144*512)+300<<20)
	newWatcher(f).step(clock)
	f.wantChanges(grow)

	// degraded, a member not in sync, a copy under way, no size: no grow
	for _, c := range []func(f *fake){
		func(f *fake) { f.set("md127", "degraded", "1") },
		func(f *fake) { f.member("md127", sdc.is("spare")) },
		func(f *fake) { f.set("md127", "sync_action", "recover") },
		func(f *fake) { f.set("md127", "component_size", "none") },
	} {
		f := bigger(t)
		c(f)
		newWatcher(f).step(clock)
		f.wantChanges()
	}

	// a member that isn't ours, a node in the container that isn't the
	// array's: neither the array nor the filesystem
	for _, c := range []func(f *fake){
		func(f *fake) {
			f.array("md127", "raid1", 2, sdb, fm{"sdd", "8:48", "in_sync"})
			f.set("md127", "component_size", "1953125000")
			f.write("class/block/sdd/size", "7812500000")
		},
		func(f *fake) { f.write("block/md127/dev", "9:5") },
	} {
		f := bigger(t)
		f.on("blockdev --getsize64 /dev/md127", result{out: "3999865782272\n"})
		c(f)
		newWatcher(f).step(clock)
		f.wantChanges()
	}
}

func TestGrowIsLookedAtEvery10Minutes(t *testing.T) {
	f := newFake(t)
	f.array("md127", "raid1", 2, sdb, sdc) // all of both disks
	w := newWatcher(f)
	w.step(clock)
	f.wantChanges()
	f.set("md127", "component_size", "1953125000")
	w.step(clock.Add(5 * time.Second))
	w.step(clock.Add(9 * time.Minute))
	f.wantChanges()
	w.step(clock.Add(10 * time.Minute))
	f.wantChanges(grow)
}

func TestGrowBacksOff(t *testing.T) {
	f := bigger(t)
	f.on(grow, result{code: 1, err: "mdadm: Cannot set device size in this type of array."})
	w := newWatcher(f)
	at := func(m int) time.Time { return clock.Add(time.Duration(m) * time.Minute) }
	tries := func() int {
		n := 0
		for _, c := range f.calls {
			if c == grow {
				n++
			}
		}
		return n
	}
	// fails at 0, tried at 10 (after 10 min), 30 (20), 70 (40), 130 and 190 (an hour)
	want := map[int]int{0: 1, 10: 2, 20: 2, 30: 3, 60: 3, 70: 4, 120: 4, 130: 5, 180: 5, 190: 6}
	for _, m := range []int{0, 10, 20, 30, 40, 50, 60, 70, 80, 90, 100, 110, 120, 130, 140, 150, 160, 170, 180, 190} {
		w.step(at(m))
		if n, ok := want[m]; ok && tries() != n {
			t.Fatalf("at %d min: %d tries, want %d", m, tries(), n)
		}
	}
	wantSaid(t, f.said(), "growing the mirror md127 failed (mdadm: Cannot set device size in this type of array.): tried again in 10 minutes",
		"tried again in 20 minutes", "tried again in 40 minutes", "tried again in 60 minutes")

	// a success clears it
	f.on(grow, result{})
	w.step(at(250))
	if w.grow != (backoff{}) {
		t.Fatalf("backoff %+v after a success", w.grow)
	}
}

func TestGrowWithoutTheBitmap(t *testing.T) {
	f := bigger(t)
	f.on(grow, result{code: 1, err: "mdadm: Cannot change component_size: internal bitmap present"}, result{})
	newWatcher(f).step(clock)
	f.wantChanges(grow, growCmd+" /dev/md127 --bitmap=none", grow, growCmd+" /dev/md127 --bitmap=internal")
	wantSaid(t, f.said(), "the mirror md127 grown")
}

func TestResizeBacksOff(t *testing.T) {
	f := newFake(t)
	f.array("md127", "raid1", 2, sdb, sdc)
	f.on("blockdev --getsize64 /dev/md127", result{out: "3999866552320\n"})
	f.on("dumpe2fs -h /dev/md127", result{out: "Block count:              488281250\nBlock size:               4096\n"})
	f.on(resize, result{code: 1, err: "resize2fs: Permission denied to resize filesystem"})
	w := newWatcher(f)
	w.step(clock)
	w.step(clock.Add(5 * time.Minute))
	w.step(clock.Add(10 * time.Minute))
	w.step(clock.Add(20 * time.Minute))
	f.wantChanges(resize, resize) // at 0 and at 10; 20 waits for 30
	wantSaid(t, f.said(), "growing the filesystem on the mirror md127 failed (resize2fs: Permission denied to resize filesystem): tried again in 10 minutes")

	// a filesystem that is the array's size: nothing
	f = newFake(t)
	f.array("md127", "raid1", 2, sdb, sdc)
	f.on("blockdev --getsize64 /dev/md127", result{out: "2000100000000\n"})
	f.on("dumpe2fs -h /dev/md127", result{out: "Block count:              488281250\nBlock size:               4096\n"})
	newWatcher(f).step(clock)
	f.wantChanges()
}

func TestRelease(t *testing.T) {
	mountinfo := func(f *fake, lines ...string) {
		p := filepath.Join(f.h.proc, "self/mountinfo")
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	volume := "612 598 0:41 /otc/storage /mnt/storage rw,relatime - ext4 /dev/vda1 rw"
	mirror := "640 612 9:127 / /mnt/storage rw,nosuid,nodev,relatime - ext4 /dev/md127 rw"

	f := newFake(t)
	f.array("md127", "raid1", 2, sdb, sdc)
	f.nodes["/dev/md127"] = "9:127"
	mountinfo(f, volume, mirror)
	f.h.release("md127")
	f.wantChanges("umount /mnt/storage", "mdadm --stop /dev/md127")
	if !f.synced {
		t.Fatal("no sync")
	}
	wantSaid(t, f.said(), "the mirror md127 stopped")

	// the mirror isn't mounted: the volume under it is never unmounted
	f = newFake(t)
	f.array("md127", "raid1", 2, sdb)
	f.nodes["/dev/md127"] = "9:127"
	mountinfo(f, volume)
	f.h.release("md127")
	f.wantChanges("mdadm --stop /dev/md127")

	// a node in the container that isn't the array's is never used
	f = newFake(t)
	f.array("md127", "raid1", 2, sdb, sdc)
	f.nodes["/dev/md127"] = "9:5"
	f.h.release("md127")
	f.wantChanges()
	wantSaid(t, f.said(), "/dev/md127 is not the mirror's device: the mirror md127 is left running")

	// busy: said, and left running
	f = newFake(t)
	f.array("md127", "raid1", 2, sdb, sdc)
	f.nodes["/dev/md127"] = "9:127"
	mountinfo(f, volume, mirror)
	f.on("umount /mnt/storage", result{code: 32, err: "umount: /mnt/storage: target is busy."})
	f.on("mdadm --stop /dev/md127", result{code: 1, err: "mdadm: Cannot get exclusive access to /dev/md127:Perhaps a running process, mounted filesystem or active volume group?"})
	f.h.release("md127")
	f.wantChanges("umount /mnt/storage", "mdadm --stop /dev/md127")
	wantSaid(t, f.said(), "unmounting the mirror md127 failed (umount: /mnt/storage: target is busy.)",
		"the mirror md127 is still in use, left running")

	// an array that isn't ours is never stopped; one that is gone, nothing
	f = newFake(t)
	f.array("md127", "raid1", 2, sdb, fm{"sdd", "8:48", "in_sync"})
	f.h.release("md127")
	f.wantChanges()
	wantSaid(t, f.said(), "md127 holds disks other than ata-ONE and ata-TWO: left running")
	f.h.release("md5")
	f.wantChanges()
	if code := run([]string{"release", "--array", "md5"}, f.h); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// growCmd is how mdadm --grow is run: on a sysfs of its own (mdadmGrow).
const growCmd = `unshare --mount --propagation private -- sh -c mount -t sysfs sysfs /sys && exec mdadm --grow "$@" mdadm-grow`
