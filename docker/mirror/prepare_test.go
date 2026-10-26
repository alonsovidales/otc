// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

const mount = "mount -t ext4 -o nosuid,nodev /dev/md127 /mnt/storage"

func TestStdoutContract(t *testing.T) {
	f := newFake(t)
	f.array("md127", "raid1", 2, sdb, sdc)
	f.fs["/dev/md127"] = "ext4"
	if err := f.h.prepare(false); err != nil {
		t.Fatal(err)
	}
	want := "array=md127\ndev1=8:16\ndev2=8:32\nlabel1=ata-ONE\nlabel2=ata-TWO\nuuid=" + ourUUID + "\n"
	if got := f.out.String(); got != want {
		t.Fatalf("stdout:\n%s\nwant:\n%s", got, want)
	}
	said := f.said()
	if last := said[len(said)-1]; last != "the mirror md127 on ata-ONE and ata-TWO: both disks in sync" {
		t.Fatalf("summary %q", last)
	}
	wantSaid(t, said, "disk 1: ata-ONE (sdb, 8:16, 4.0 TB); disk 2: ata-TWO (sdc, 8:32, 4.0 TB)")
}

func TestAdoptRunningArray(t *testing.T) {
	f := newFake(t)
	f.array("md127", "raid1", 2, sdb, sdc)
	f.fs["/dev/md127"] = "ext4"
	if err := f.h.prepare(false); err != nil {
		t.Fatal(err)
	}
	// no node in the container for an array that was running: made from sysfs
	if len(f.mknods) != 1 || f.mknods[0] != "/dev/md127 9:127" {
		t.Fatalf("mknod %q", f.mknods)
	}
	f.wantChanges(mount)
	wantSaid(t, f.said(), "the mirror md127 is running already on this server: using it")
}

func TestAdoptRefusesAForeignMember(t *testing.T) {
	f := newFake(t)
	f.array("md127", "raid1", 2, sdb, fm{"sdd", "8:48", "in_sync"})
	err := f.h.prepare(false)
	wantErr(t, err, "ata-ONE belongs to md127, another array on this server (it also holds sdd): OTC won't touch it", "mdadm --stop /dev/md127")
	f.wantChanges()
	if f.out.Len() != 0 {
		t.Fatalf("stdout %q on a refusal", f.out.String())
	}
}

func TestAdoptRefusesTheWrongLevel(t *testing.T) {
	for _, c := range []struct {
		level string
		disks int
	}{{"raid5", 2}, {"raid1", 3}, {"raid0", 2}} {
		f := newFake(t)
		f.array("md0", c.level, c.disks, sdb, sdc)
		wantErr(t, f.h.prepare(false), "belongs to md0, another array on this server ("+c.level, "OTC won't touch it")
		f.wantChanges()
	}
}

func TestAdoptRefusesAnInactiveArrayAndTwoArrays(t *testing.T) {
	f := newFake(t)
	f.array("md126", "raid1", 2, sdc)
	f.set("md126", "array_state", "inactive")
	wantErr(t, f.h.prepare(false), "ata-TWO belongs to md126, an array on this server that isn't running", "mdadm --stop /dev/md126")
	f.wantChanges()

	f = newFake(t)
	f.array("md126", "raid1", 2, sdb)
	f.array("md127", "raid1", 2, sdc)
	wantErr(t, f.h.prepare(false), "two arrays on this server: OTC won't touch them")
	f.wantChanges()
}

func TestAssembleFromBothSuperblocks(t *testing.T) {
	f := newFake(t)
	f.superblock("/dev/otc-disk1", ourUUID, "raid1", "2")
	f.superblock("/dev/otc-disk2", ourUUID, "raid1", "2")
	f.fs["/dev/md127"] = "ext4"
	if err := f.h.prepare(false); err != nil {
		t.Fatal(err)
	}
	f.wantChanges("mdadm --assemble --run /dev/md/otc /dev/otc-disk1 /dev/otc-disk2", mount)
	if len(f.mknods) != 0 {
		t.Fatalf("mknod %q for a node mdadm made", f.mknods)
	}
	wantSaid(t, f.said(), "assembling the mirror from ata-ONE and ata-TWO", "both disks in sync")
}

func TestAssembleDegradedFromOneAndAddTheBlankOther(t *testing.T) {
	f := newFake(t)
	f.superblock("/dev/otc-disk1", ourUUID, "raid1", "2")
	f.fs["/dev/md127"] = "ext4"
	if err := f.h.prepare(false); err != nil {
		t.Fatal(err)
	}
	f.wantChanges("mdadm --assemble --run /dev/md/otc /dev/otc-disk1", mount, "mdadm --add /dev/md127 /dev/otc-disk2")
	said := f.said()
	wantSaid(t, said, "assembling the mirror from ata-ONE alone", "adding ata-TWO to the mirror md127")
	if last := said[len(said)-1]; last != "the mirror md127 on ata-ONE and ata-TWO: rebuilding onto ata-TWO" {
		t.Fatalf("summary %q", last)
	}
}

func TestAssembleRefusesDifferentArraysAndOtherLevels(t *testing.T) {
	f := newFake(t)
	f.superblock("/dev/otc-disk1", ourUUID, "raid1", "2")
	f.superblock("/dev/otc-disk2", "99999999:22222222:33333333:44444444", "raid1", "2")
	wantErr(t, f.h.prepare(false), "ata-ONE and ata-TWO hold parts of two different arrays", "wipefs -a /dev/disk/by-id/ata-ONE", "wipefs -a /dev/disk/by-id/ata-TWO", "only if you are sure")
	f.wantChanges()

	f = newFake(t)
	f.superblock("/dev/otc-disk2", ourUUID, "raid5", "3")
	wantErr(t, f.h.prepare(false), "ata-TWO holds part of another kind of array (raid5 of 3 disks)", "wipefs -a /dev/disk/by-id/ata-TWO")
	f.wantChanges()
}

func TestAssembleRefusesABusyDiskAndAnUnreadableOne(t *testing.T) {
	f := newFake(t)
	f.superblock("/dev/otc-disk1", ourUUID, "raid1", "2")
	f.busy["/dev/otc-disk1"] = unix.EBUSY
	wantErr(t, f.h.prepare(false), "ata-ONE is in use on this server")
	f.wantChanges()

	// an unreadable disk, nothing on the other: nothing is built
	f = newFake(t)
	f.on("mdadm --examine --export /dev/otc-disk2", result{code: 2, err: "mdadm: cannot open /dev/otc-disk2: Input/output error"})
	wantErr(t, f.h.prepare(false), "can't read ata-TWO", "Input/output error")
	f.wantChanges()

	// ...the mirror on the other: assembled without it
	f = newFake(t)
	f.superblock("/dev/otc-disk1", ourUUID, "raid1", "2")
	f.on("mdadm --examine --export /dev/otc-disk2", result{code: 2, err: "mdadm: cannot open /dev/otc-disk2: Input/output error"})
	f.fs["/dev/md127"] = "ext4"
	if err := f.h.prepare(false); err != nil {
		t.Fatal(err)
	}
	f.wantChanges("mdadm --assemble --run /dev/md/otc /dev/otc-disk1", mount)
	wantSaid(t, f.said(), "can't read ata-TWO (mdadm --examine: mdadm: cannot open /dev/otc-disk2: Input/output error): check that the disk works and is connected - the mirror is assembled without it",
		"ata-TWO not added to the mirror: can't read ata-TWO", "degraded, only ata-ONE works")
}

func TestAssembledThenRefusedIsStoppedAgain(t *testing.T) {
	f := newFake(t)
	f.superblock("/dev/otc-disk1", ourUUID, "raid1", "2")
	f.superblock("/dev/otc-disk2", ourUUID, "raid1", "2")
	f.fs["/dev/md127"] = "xfs"
	wantErr(t, f.h.prepare(false), "the mirror md127 holds xfs, not ext4: OTC won't format it")
	f.wantChanges("mdadm --assemble --run /dev/md/otc /dev/otc-disk1 /dev/otc-disk2", "mdadm --stop /dev/md127")
}

func TestFreshBuildOnBlankDisks(t *testing.T) {
	f := newFake(t)
	if err := f.h.prepare(false); err != nil {
		t.Fatal(err)
	}
	f.wantChanges(
		"mdadm --create /dev/md/otc --run --level=1 --raid-devices=2 --metadata=1.2 --name=otc --homehost=any --bitmap=internal /dev/otc-disk1 /dev/otc-disk2",
		wipeMD, mkfsMD,
		mount)
	said := f.said()
	wantSaid(t, said, "building a new mirror on ata-ONE and ata-TWO", "making the filesystem on the mirror md127")
	if last := said[len(said)-1]; last != "the mirror md127 on ata-ONE and ata-TWO: resyncing the two disks, 0%" {
		t.Fatalf("summary %q", last)
	}
}

func TestFreshBuildRefusesADiskWithSomethingOnIt(t *testing.T) {
	f := newFake(t)
	f.on("blkid -p -o export /dev/otc-disk2", result{out: "DEVNAME=/dev/otc-disk2\nUUID=abc\nTYPE=ext4\nLABEL=backup\n"})
	f.on("wipefs --no-act --noheadings /dev/otc-disk2", result{out: "/dev/otc-disk2 0x438 ext4 abc backup\n"})
	wantErr(t, f.h.prepare(false), `ata-TWO holds ext4 labelled "backup"`,
		"put this line in .env and run docker compose up -d again: OTC_MIRROR_ERASE=/dev/disk/by-id/ata-TWO")
	f.wantChanges()

	// both: the one line lists both
	f = newFake(t)
	f.on("blkid -p -o export /dev/otc-disk1", result{out: "PTUUID=x\nPTTYPE=gpt\n"})
	f.on("wipefs --no-act --noheadings /dev/otc-disk2", result{out: "/dev/otc-disk2 0x0 something\n"})
	wantErr(t, f.h.prepare(false), `ata-ONE holds a gpt partition table; ata-TWO holds signatures wipefs lists (/dev/otc-disk2 0x0 something)`,
		`OTC_MIRROR_ERASE="/dev/disk/by-id/ata-ONE /dev/disk/by-id/ata-TWO"`)
	f.wantChanges()

	// one listed, the other not: the line lists both
	f = newFake(t)
	f.h.erase = []string{"/dev/disk/by-id/ata-ONE"}
	f.fs["/dev/otc-disk1"], f.fs["/dev/otc-disk2"] = "ntfs", "vfat"
	wantErr(t, f.h.prepare(false), "ata-TWO holds vfat", `OTC_MIRROR_ERASE="/dev/disk/by-id/ata-ONE /dev/disk/by-id/ata-TWO"`)
	f.wantChanges()
}

func TestFreshBuildErasesOnlyTheDisksListed(t *testing.T) {
	f := newFake(t)
	f.h.erase = []string{"/dev/disk/by-id/ata-ONE", "/dev/disk/by-id/ata-TWO"} // ata-ONE is blank
	f.on("blkid -p -o export /dev/otc-disk2", result{out: "PTTYPE=gpt\n"})
	if err := f.h.prepare(false); err != nil {
		t.Fatal(err)
	}
	f.wantChanges(
		"wipefs -a /dev/otc-disk2",
		"mdadm --create /dev/md/otc --run --level=1 --raid-devices=2 --metadata=1.2 --name=otc --homehost=any --bitmap=internal /dev/otc-disk1 /dev/otc-disk2",
		wipeMD, mkfsMD,
		mount)
	wantSaid(t, f.said(), "ata-TWO holds a gpt partition table: erasing it, as OTC_MIRROR_ERASE says")

	// the name must be the exact one
	f = newFake(t)
	f.h.erase = []string{"ata-TWO", "/dev/disk/by-id/ata-TWO/"}
	f.fs["/dev/otc-disk2"] = "ext4"
	wantErr(t, f.h.prepare(false), "OTC_MIRROR_ERASE=/dev/disk/by-id/ata-TWO")
	f.wantChanges()
}

func TestFreshBuildNeverErasesADiskArray(t *testing.T) {
	f := newFake(t)
	f.h.erase = []string{"/dev/disk/by-id/ata-ONE", "/dev/disk/by-id/ata-TWO"}
	f.fs["/dev/otc-disk2"] = "isw_raid_member"
	wantErr(t, f.h.prepare(false), "ata-TWO holds part of a disk array (isw_raid_member)", "it won't erase it", "wipefs -a /dev/disk/by-id/ata-TWO")
	f.wantChanges()
}

func TestFreshBuildRefusals(t *testing.T) {
	// --no-create (restore) never builds
	f := newFake(t)
	wantErr(t, f.h.prepare(true), "there is no mirror on ata-ONE or ata-TWO yet, and restore only uses one")
	f.wantChanges()

	// a disk in use
	f = newFake(t)
	f.h.erase = []string{"/dev/disk/by-id/ata-TWO"}
	f.busy["/dev/otc-disk2"] = unix.EBUSY
	wantErr(t, f.h.prepare(false), "ata-TWO is in use on this server (mounted, or part of another array or volume)")
	f.wantChanges()

	// wipefs failing after the first disk was erased says so
	f = newFake(t)
	f.h.erase = []string{"/dev/disk/by-id/ata-ONE", "/dev/disk/by-id/ata-TWO"}
	f.fs["/dev/otc-disk1"], f.fs["/dev/otc-disk2"] = "ext4", "ext4"
	f.on("wipefs -a /dev/otc-disk2", result{code: 1, err: "wipefs: error: /dev/otc-disk2: probing initialization failed: Device or resource busy"})
	wantErr(t, f.h.prepare(false), "erasing ata-TWO failed", "Device or resource busy", "ata-ONE erased already")
	f.wantChanges("wipefs -a /dev/otc-disk1", "wipefs -a /dev/otc-disk2")

	// mdadm failing
	f = newFake(t)
	f.on("mdadm --create /dev/md/otc --run --level=1 --raid-devices=2 --metadata=1.2 --name=otc --homehost=any --bitmap=internal /dev/otc-disk1 /dev/otc-disk2",
		result{code: 1, err: "mdadm: cannot open /dev/otc-disk1: Device or resource busy"})
	wantErr(t, f.h.prepare(false), "building the mirror on ata-ONE and ata-TWO failed (mdadm: cannot open")
}

func TestDisksMustBeTwoDisks(t *testing.T) {
	f := newFake(t)
	delete(f.nodes, "/dev/otc-disk2")
	wantErr(t, f.h.prepare(false), "ata-TWO (/dev/otc-disk2) is not a disk: check OTC_MIRROR_DISK2 in .env")
	f.wantChanges()

	f = newFake(t)
	f.nodes["/dev/otc-disk2"] = "8:16"
	wantErr(t, f.h.prepare(false), "OTC_MIRROR_DISK1 and OTC_MIRROR_DISK2 are the same disk (8:16)")
	f.wantChanges()
}

func TestFilesystemChecks(t *testing.T) {
	// an array OTC built that holds nothing: a first build that stopped half-way
	// is never formatted: it may be this device's files behind a read error
	f := newFake(t)
	f.array("md127", "raid1", 2, sdb, sdc)
	wantErr(t, f.h.prepare(false), "no filesystem can be read on the mirror md127, and OTC formats only a mirror it has just built",
		"e2fsck -n /dev/md127", "mdadm --stop /dev/md127, then wipefs -a /dev/disk/by-id/ata-ONE and wipefs -a /dev/disk/by-id/ata-TWO")
	f.wantChanges()

	// ...nor for restore
	f = newFake(t)
	f.array("md127", "raid1", 2, sdb, sdc)
	wantErr(t, f.h.prepare(true), "no filesystem can be read on the mirror md127")
	f.wantChanges()

	// ...nor when it is this device's own (config/storage-mode names it)
	f = newFake(t)
	f.h.expect = ourUUID
	f.superblock("/dev/otc-disk1", ourUUID, "raid1", "2")
	f.superblock("/dev/otc-disk2", ourUUID, "raid1", "2")
	wantErr(t, f.h.prepare(false), "no filesystem can be read on the mirror md127")
	f.wantChanges("mdadm --assemble --run /dev/md/otc /dev/otc-disk1 /dev/otc-disk2", "mdadm --stop /dev/md127")

	// an array OTC didn't build is never used, whatever it holds
	f = newFake(t)
	f.array("md127", "raid1", 2, sdb, sdc)
	f.mdName = "nas:0"
	f.fs["/dev/md127"] = "ext4"
	wantErr(t, f.h.prepare(false), `md127 is an array OTC didn't build (it is called "nas:0"): OTC won't use it`)
	f.wantChanges()

	// an otc array blkid finds nothing on but wipefs does
	f = newFake(t)
	f.array("md127", "raid1", 2, sdb, sdc)
	f.on("wipefs --no-act --noheadings /dev/md127", result{out: "/dev/md127 0x1000 zfs_member\n"})
	wantErr(t, f.h.prepare(false), "the mirror md127 holds signatures wipefs lists", "not ext4")
	f.wantChanges()

	// another filesystem
	f = newFake(t)
	f.array("md127", "raid1", 2, sdb, sdc)
	f.fs["/dev/md127"] = "btrfs"
	wantErr(t, f.h.prepare(false), "the mirror md127 holds btrfs, not ext4: OTC won't format it")
	f.wantChanges()

	// a mkfs that fails after a build says the mirror stays
	f = newFake(t)
	f.on(mkfsMD, result{code: 1, err: "mkfs.ext4: Device size reported to be zero."})
	wantErr(t, f.h.prepare(false), "making the filesystem on the mirror md127 failed", "the new mirror md127 on ata-ONE and ata-TWO stays")
}

func TestBusyMirrorIsNotMounted(t *testing.T) {
	f := newFake(t)
	f.array("md127", "raid1", 2, sdb, sdc)
	f.fs["/dev/md127"] = "ext4"
	f.busy["/dev/md127"] = unix.EBUSY
	err := f.h.prepare(false)
	wantErr(t, err, "the mirror md127 is mounted somewhere else on this server (the host or another container): unmount it there first")
	f.wantChanges()
	if f.out.Len() != 0 {
		t.Fatalf("stdout %q on a refusal", f.out.String())
	}

	// and the refusal is the last line on stderr, as the entrypoint reads it
	f = newFake(t)
	f.array("md127", "raid1", 2, sdb, sdc)
	f.fs["/dev/md127"] = "ext4"
	f.busy["/dev/md127"] = unix.EBUSY
	if code := run([]string{"prepare"}, f.h); code != 1 {
		t.Fatalf("exit %d", code)
	}
	said := f.said()
	if !strings.HasPrefix(said[len(said)-1], "the mirror md127 is mounted somewhere else") {
		t.Fatalf("last line %q", said[len(said)-1])
	}
}

func TestReplacement(t *testing.T) {
	degraded := func(t *testing.T) *fake {
		f := newFake(t)
		f.array("md127", "raid1", 2, sdb)
		f.fs["/dev/md127"] = "ext4"
		return f
	}
	add := "mdadm --add /dev/md127 /dev/otc-disk2"

	// it was a member: mdadm --add (mdadm re-adds it)
	f := degraded(t)
	f.superblock("/dev/otc-disk2", ourUUID, "raid1", "2")
	f.on("mdadm --examine /dev/otc-disk2", result{out: "   Array State : AA ('A' == active, '.' == missing, 'R' == replacing)\n"})
	if err := f.h.prepare(false); err != nil {
		t.Fatal(err)
	}
	f.wantChanges(mount, add)

	// ...but not one that ran without the disk the mirror runs on now (each
	// ran alone, one after the other): its changes would be overwritten
	f = degraded(t)
	f.superblock("/dev/otc-disk2", ourUUID, "raid1", "2")
	f.on("mdadm --examine /dev/otc-disk2", result{out: "   Array State : .A ('A' == active, '.' == missing, 'R' == replacing)\n"})
	if err := f.h.prepare(false); err != nil {
		t.Fatal(err)
	}
	f.wantChanges(mount)
	wantSaid(t, f.said(), "ata-TWO not added to the mirror: it ran without the disk the mirror runs on now")

	// ...nor one whose superblock can't say
	f = degraded(t)
	f.superblock("/dev/otc-disk2", ourUUID, "raid1", "2")
	if err := f.h.prepare(false); err != nil {
		t.Fatal(err)
	}
	f.wantChanges(mount)

	// blank
	f = degraded(t)
	if err := f.h.prepare(false); err != nil {
		t.Fatal(err)
	}
	f.wantChanges(mount, add)

	// listed in OTC_MIRROR_ERASE
	f = degraded(t)
	f.h.erase = []string{"/dev/disk/by-id/ata-TWO"}
	f.fs["/dev/otc-disk2"] = "ext4"
	if err := f.h.prepare(false); err != nil {
		t.Fatal(err)
	}
	f.wantChanges(mount, "wipefs -a /dev/otc-disk2", add)

	// something on it, not listed: left, with the line to add
	f = degraded(t)
	f.fs["/dev/otc-disk2"] = "ext4"
	if err := f.h.prepare(false); err != nil {
		t.Fatal(err)
	}
	f.wantChanges(mount)
	wantSaid(t, f.said(), "ata-TWO not added to the mirror: it holds ext4", "OTC_MIRROR_ERASE=/dev/disk/by-id/ata-TWO",
		"the mirror md127 on ata-ONE and ata-TWO: degraded, only ata-ONE works")

	// another array's disk, even listed: never
	f = degraded(t)
	f.h.erase = []string{"/dev/disk/by-id/ata-TWO"}
	f.superblock("/dev/otc-disk2", "99999999:22222222:33333333:44444444", "raid1", "2")
	if err := f.h.prepare(false); err != nil {
		t.Fatal(err)
	}
	f.wantChanges(mount)
	wantSaid(t, f.said(), "ata-TWO not added to the mirror: it holds part of another array", "wipefs -a /dev/disk/by-id/ata-TWO")

	// in use on the host
	f = degraded(t)
	f.busy["/dev/otc-disk2"] = unix.EBUSY
	if err := f.h.prepare(false); err != nil {
		t.Fatal(err)
	}
	f.wantChanges(mount)
	wantSaid(t, f.said(), "ata-TWO not added to the mirror: ata-TWO is in use on this server")

	// an add that fails doesn't fail prepare
	f = degraded(t)
	f.on(add, result{code: 1, err: "mdadm: /dev/otc-disk2 not large enough to join array"})
	if err := f.h.prepare(false); err != nil {
		t.Fatal(err)
	}
	f.wantChanges(mount, add)
	wantSaid(t, f.said(), "adding ata-TWO to the mirror md127 failed (mdadm: /dev/otc-disk2 not large enough to join array): it stays degraded")
	if !strings.Contains(f.out.String(), "array=md127\n") {
		t.Fatalf("stdout %q", f.out.String())
	}

	// restore adds nothing
	f = degraded(t)
	if err := f.h.prepare(true); err != nil {
		t.Fatal(err)
	}
	f.wantChanges(mount)

	// a mirror that isn't degraded gets nothing added
	f = newFake(t)
	f.array("md127", "raid1", 2, sdb, sdc)
	f.fs["/dev/md127"] = "ext4"
	if err := f.h.prepare(false); err != nil {
		t.Fatal(err)
	}
	f.wantChanges(mount)
}

func TestNodeOfAnotherDeviceIsRefused(t *testing.T) {
	f := newFake(t)
	f.array("md127", "raid1", 2, sdb, sdc)
	f.nodes["/dev/md127"] = "9:5"
	wantErr(t, f.h.prepare(false), "/dev/md127 in the container is 9:5, not the mirror (9:127)")
	f.wantChanges()

	f = newFake(t)
	f.array("md127", "raid1", 2, sdb, sdc)
	f.h.mknod = func(string, string) error { return errors.New("operation not permitted") }
	wantErr(t, f.h.prepare(false), "can't make /dev/md127 for the mirror (operation not permitted)")
}

func TestLabels(t *testing.T) {
	for in, want := range map[string]string{
		"":                               "disk 2",
		"/dev/disk/by-id/ata-WDC_WD40-X": "ata-WDC_WD40-X",
		"/dev/disk/by-id/usb-A B\nC/":    "usb-A_B_C",
		"/dev/sdb":                       "sdb",
	} {
		if got := labelOf(in, 2); got != want {
			t.Errorf("labelOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnvironment(t *testing.T) {
	t.Setenv("OTC_MIRROR_DISK1", "/dev/disk/by-id/ata-ONE")
	t.Setenv("OTC_MIRROR_DISK2", "")
	t.Setenv("OTC_MIRROR_DEV1", "/dev/loop7")
	t.Setenv("OTC_MIRROR_DEV2", "8:32") // the device process's form: not a path
	t.Setenv("OTC_MIRROR_ERASE", " /dev/disk/by-id/ata-ONE  /dev/disk/by-id/ata-TWO ")
	h := newHost()
	if d := h.disks[0]; d.path != "/dev/loop7" || d.label != "ata-ONE" || d.name != "/dev/disk/by-id/ata-ONE" {
		t.Fatalf("disk 1 %+v", d)
	}
	if d := h.disks[1]; d.path != "/dev/otc-disk2" || d.label != "disk 2" {
		t.Fatalf("disk 2 %+v", d)
	}
	if len(h.erase) != 2 || h.erase[1] != "/dev/disk/by-id/ata-TWO" {
		t.Fatalf("erase %q", h.erase)
	}
}

func TestCommandLine(t *testing.T) {
	f := newFake(t)
	for _, args := range [][]string{nil, {"grow"}, {"watch"}, {"watch", "--array", "../sda"}, {"release", "--array", "md127", "x"},
		{"prepare", "--array", "md127"}, {"watch", "--array", "md127", "--no-create"}} {
		if code := run(args, f.h); code != 1 {
			t.Errorf("%q: exit %d", args, code)
		}
	}
	f.wantChanges()
}

func TestADiskSetToDevNullIsLeftOut(t *testing.T) {
	// The mirror runs on the other disk alone, until a replacement is named.
	f := newFake(t)
	f.h.disks[1].name, f.h.disks[1].label = "/dev/null", labelOf("/dev/null", 2)
	f.superblock("/dev/otc-disk1", ourUUID, "raid1", "2")
	f.fs["/dev/md127"] = "ext4"
	if err := f.h.prepare(false); err != nil {
		t.Fatal(err)
	}
	f.wantChanges("mdadm --assemble --run /dev/md/otc /dev/otc-disk1", mount)
	wantSaid(t, f.said(), "OTC_MIRROR_DISK2 is /dev/null in .env (no disk) - the mirror is assembled without it", "assembling the mirror from ata-ONE alone")
	if !strings.Contains(f.out.String(), "dev2=\nlabel1=ata-ONE\nlabel2=disk 2\n") {
		t.Fatalf("stdout %q", f.out.String())
	}

	// Nothing to run on: never a new mirror with one disk.
	f = newFake(t)
	f.h.disks[1].name, f.h.disks[1].label = "/dev/null", labelOf("/dev/null", 2)
	wantErr(t, f.h.prepare(false), "OTC_MIRROR_DISK2 is /dev/null in .env, and ata-ONE holds no mirror to run on alone: name both disks in .env")
	f.wantChanges()

	f = newFake(t)
	f.h.disks[0].name, f.h.disks[1].name = "/dev/null", " /dev/null"
	wantErr(t, f.h.prepare(false), "OTC_MIRROR_DISK1 and OTC_MIRROR_DISK2 are both /dev/null in .env: name the mirror's disks")
	f.wantChanges()
}

func TestOnlyThisDevicesMirror(t *testing.T) {
	// Its own mirror: assembled as always.
	f := newFake(t)
	f.h.expect = ourUUID
	f.superblock("/dev/otc-disk1", ourUUID, "raid1", "2")
	f.superblock("/dev/otc-disk2", ourUUID, "raid1", "2")
	f.fs["/dev/md127"] = "ext4"
	if err := f.h.prepare(false); err != nil {
		t.Fatal(err)
	}

	// Blank disks: no new mirror in place of the missing one.
	f = newFake(t)
	f.h.expect = ourUUID
	wantErr(t, f.h.prepare(false), "this device's mirror ("+ourUUID+") isn't on ata-ONE or ata-TWO: name its disks in .env and run docker compose up -d again. To start over with a new, empty mirror on these disks, delete storage-mode in the device's config folder first")
	f.wantChanges()
}

func TestAnotherMirrorIsStoppedAgain(t *testing.T) {
	f := newFake(t)
	f.h.expect = "99999999:99999999:99999999:99999999"
	f.superblock("/dev/otc-disk1", ourUUID, "raid1", "2")
	f.superblock("/dev/otc-disk2", ourUUID, "raid1", "2")
	f.fs["/dev/md127"] = "ext4"
	wantErr(t, f.h.prepare(false), "ata-ONE holds part of another mirror ("+ourUUID+"), not this device's (99999999:99999999:99999999:99999999)")
	f.wantChanges() // refused before it is started

	// running already (the host assembled it): never used either
	f = newFake(t)
	f.h.expect = "99999999:99999999:99999999:99999999"
	f.array("md127", "raid1", 2, sdb, sdc)
	f.fs["/dev/md127"] = "ext4"
	wantErr(t, f.h.prepare(false), "the mirror md127 on these disks ("+ourUUID+") isn't this device's (99999999:99999999:99999999:99999999)")
	f.wantChanges()
}

const (
	wipeMD = "wipefs -a /dev/md127"
	mkfsMD = "mkfs.ext4 -q -E nodiscard -L otc-storage /dev/md127"
)

func TestOnlyArraysOTCBuiltAndReadableDisks(t *testing.T) {
	// half of another system's mirror on a disk: never assembled, first start or not
	f := newFake(t)
	f.on("mdadm --examine --export /dev/otc-disk1", result{out: "MD_LEVEL=raid1\nMD_DEVICES=2\nMD_NAME=oldnas:0\nMD_UUID=" + ourUUID + "\n"})
	wantErr(t, f.h.prepare(false), `ata-ONE holds part of an array OTC didn't build (it is called "oldnas:0")`, "wipefs -a /dev/disk/by-id/ata-ONE")
	f.wantChanges()

	// a disk that can't be read is never taken for blank
	f = newFake(t)
	f.unreadable["/dev/otc-disk2"] = unix.EIO
	wantErr(t, f.h.prepare(false), "can't read ata-TWO (input/output error)")
	f.wantChanges()

	// OTC_MIRROR_ERASE takes only /dev/disk/by-id/ names
	f = newFake(t)
	f.h.disks[1].name, f.h.disks[1].label = "/dev/sdc", "sdc"
	f.h.erase = []string{"/dev/sdc"}
	f.fs["/dev/otc-disk2"] = "ext4"
	wantErr(t, f.h.prepare(false), "sdc holds ext4", "by its /dev/disk/by-id/ name", "in OTC_MIRROR_DISK2 and in OTC_MIRROR_ERASE")
	f.wantChanges()
}

// A disk that failed and dropped off the bus: md still lists it, faulty and
// with no device number. The mirror is still this device's: adopted, and
// stopped at release.
func TestAFaultyMemberGoneIsNotForeign(t *testing.T) {
	f := newFake(t)
	f.array("md127", "raid1", 2, sdb, fm{"sdz", "", "faulty"})
	f.fs["/dev/md127"] = "ext4"
	if err := f.h.prepare(false); err != nil {
		t.Fatal(err)
	}
	wantSaid(t, f.said(), "the mirror md127 is running already on this server: using it")
	f.h.release("md127")
	if !slices.Contains(f.changes(), "mdadm --stop /dev/md127") {
		t.Fatalf("not stopped: %q", f.changes())
	}
}
