// SPDX-License-Identifier: AGPL-3.0-or-later

package raidwatch

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/alerttext"
	"github.com/alonsovidales/otc/container"
	"github.com/alonsovidales/otc/push"
)

const (
	disk1 = "ata-WDC_WD40EFRX-68N32N0_WD-WCC7K1111111"
	disk2 = "ata-WDC_WD40EFRX-68N32N0_WD-WCC7K2222222"
	disk3 = "ata-ST4000VN006-3CW104_ZW6000AAA"

	namedDetail   = "Your files are safe on the other disk, but they are no longer mirrored. Replace it with an empty disk at least as large, put the new disk's name in .env in place of the old one (OTC_MIRROR_DISK1 or OTC_MIRROR_DISK2) and run docker compose up -d: the device adds it and copies everything onto it."
	anonymousPush = "Storage disk stopped working | One of the two storage disks failed. Your files are safe on the other one: replace it soon."
)

// newMirrorSys is a container's view of the machine: the host's sysfs -
// here a Raspberry Pi's, its own md0 on sda and sdb in the blue USB ports,
// no business of the device's - and the container's mirror, md127, on sdc
// (8:32) and sdd (8:48), named in .env as disk1 and disk2. sdd sits in a
// blue USB port too, which the mirror never names it by.
func newMirrorSys(t *testing.T) *fakeSys {
	f := newFakeSys(t)
	t.Setenv(container.EnvVar, "1")
	t.Setenv(container.EnvMirrorArray, "md127")
	t.Setenv(container.EnvMirrorDev1, "8:32")
	t.Setenv(container.EnvMirrorLabel1, disk1)
	t.Setenv(container.EnvMirrorDev2, "8:48")
	t.Setenv(container.EnvMirrorLabel2, disk2)
	f.write("block/md127/md/raid_disks", "2")
	f.write("block/md127/md/sync_action", "idle")
	f.write("block/md127/md/degraded", "0")
	f.disk("sdc", "8:32")
	f.disk("sdd", "8:48")
	f.plug("sdd", "xhci-hcd.1", "usb4", "4-1", "DDD")
	f.mirrorMember("sdc", "0", "in_sync")
	f.mirrorMember("sdd", "1", "in_sync")
	return f
}

// diskDir is where a SATA disk's sysfs directory is.
func (f *fakeSys) diskDir(dev string) string {
	return filepath.Join(f.root, "devices/pci0000:00/0000:00:17.0/ata3/host2/target2:0:0/2:0:0:0/block", dev)
}

// disk is a block device the kernel numbers num ("major:minor").
func (f *fakeSys) disk(dev, num string) {
	rel, err := filepath.Rel(f.root, f.diskDir(dev))
	if err != nil {
		f.t.Fatal(err)
	}
	f.write(filepath.Join(rel, "dev"), num)
}

// mirrorMember puts dev in md127's slot, as md lists its members: a
// directory per disk whose block link leads to the disk's own.
func (f *fakeSys) mirrorMember(dev, slot, state string) {
	f.write(filepath.Join("block/md127/md/dev-"+dev, "slot"), slot)
	f.write(filepath.Join("block/md127/md/dev-"+dev, "state"), state)
	link := filepath.Join(f.root, "block/md127/md/dev-"+dev, "block")
	_ = os.Remove(link)
	if err := os.Symlink(f.diskDir(dev), link); err != nil {
		f.t.Fatal(err)
	}
}

// pull takes dev out of the machine: md may still list it, its block link
// leading nowhere.
func (f *fakeSys) pull(dev string) {
	_ = os.RemoveAll(f.diskDir(dev))
}

// keyedRecorder keeps the Alerts whole and the pushes in English.
type keyedRecorder struct {
	alerts []alerttext.Alert
	pushes []string
}

func (r *keyedRecorder) notify() Notify {
	return Notify{
		AlertText: func(a alerttext.Alert) { r.alerts = append(r.alerts, a) },
		PushText: func(t push.Text) {
			title, body := t("")
			r.pushes = append(r.pushes, title+" | "+body)
		},
	}
}

// A disk of the container's mirror fails, is taken out, replaced in .env
// and rebuilt: named by its .env name all along, in the Alert (keyed) and
// the status (MissingDisks), never in the push; the host's own degraded
// md0 is never mentioned.
func TestMirrorFailureAndRecovery(t *testing.T) {
	f := newMirrorSys(t)
	store := t.TempDir()
	r := &keyedRecorder{}

	// The host's array degraded, the mirror whole: nothing to say, and
	// the mirror's disks recorded by their names, no USB port.
	f.unplug("sdb")
	f.write("block/md0/md/degraded", "1")
	check(store, r.notify())
	if len(r.alerts)+len(r.pushes) != 0 || Missing() != "" || MissingDisks() != nil {
		t.Fatalf("whole mirror: %+v %v %q %v", r.alerts, r.pushes, Missing(), MissingDisks())
	}
	known := loadKnown(store)
	if len(known) != 2 || known[0].Disk != disk1 || known[1].Disk != disk2 || known[1].Dev != "sdd" || known[1].Port != "" || known[1].Position != "" {
		t.Fatalf("recorded: %+v", known)
	}

	// sdd fails: md marks it faulty and still lists it.
	f.mirrorMember("sdd", "1", "faulty")
	f.write("block/md127/md/degraded", "1")
	check(store, r.notify())
	if len(r.alerts) != 1 || len(r.pushes) != 1 {
		t.Fatalf("failure: %+v %v", r.alerts, r.pushes)
	}
	a := r.alerts[0]
	if a.Title != "The disk "+disk2+" stopped working" || a.Detail != namedDetail {
		t.Errorf("English %q | %q", a.Title, a.Detail)
	}
	if a.Key != "dev.alert.raid.stopped_named" || string(a.Args) != `{"disk":"`+disk2+`"}` || a.DetailKey != "dev.alert.raid.stopped_detail_named" || string(a.DetailArgs) != "{}" {
		t.Errorf("keys %+v", a)
	}
	if r.pushes[0] != anonymousPush || strings.Contains(r.pushes[0], "WCC7K") {
		t.Errorf("push %q", r.pushes[0])
	}
	if Missing() != "the disk "+disk2 || !slices.Equal(MissingDisks(), []string{disk2}) || StillWorkingDisk() != "" {
		t.Errorf("Missing() %q, MissingDisks() %v, StillWorkingDisk() %q", Missing(), MissingDisks(), StillWorkingDisk())
	}
	if !slices.Equal(MissingPorts(), []Port{PortUnknown}) || StillWorking() != PortUnknown {
		t.Errorf("ports in the mirror: %v %q", MissingPorts(), StillWorking())
	}

	// Taken out of the machine and out of the array: still named, from
	// the record; said once.
	f.pull("sdd")
	check(store, r.notify())
	_ = os.RemoveAll(filepath.Join(f.root, "block/md127/md/dev-sdd"))
	check(store, r.notify())
	if len(r.alerts) != 1 || !slices.Equal(MissingDisks(), []string{disk2}) {
		t.Fatalf("taken out: %d alerts, MissingDisks() %v", len(r.alerts), MissingDisks())
	}

	// The new disk named in .env in place of the old one, the container
	// recreated, otc-mirror adding it: rebuilding into the slot, the disk
	// that failed is still the one missing.
	t.Setenv(container.EnvMirrorDev2, "8:64")
	t.Setenv(container.EnvMirrorLabel2, disk3)
	f.disk("sde", "8:64")
	f.mirrorMember("sde", "1", "spare")
	f.write("block/md127/md/sync_action", "recover")
	check(store, r.notify())
	if len(r.alerts) != 1 || len(r.pushes) != 1 || !slices.Equal(MissingDisks(), []string{disk2}) {
		t.Fatalf("rebuilding: %d alerts, %d pushes, MissingDisks() %v", len(r.alerts), len(r.pushes), MissingDisks())
	}

	// Rebuilt: whole again, said once, the new disk recorded.
	f.mirrorMember("sde", "1", "in_sync")
	f.write("block/md127/md/sync_action", "idle")
	f.write("block/md127/md/degraded", "0")
	check(store, r.notify())
	check(store, r.notify())
	if len(r.alerts) != 2 || r.alerts[1].Key != "dev.alert.raid.working_title" || r.alerts[1].DetailKey != "dev.alert.raid.working_detail" {
		t.Fatalf("recovery: %+v", r.alerts)
	}
	if len(r.pushes) != 2 || r.pushes[1] != "Storage disks working again | The mirror is complete: every file is on both disks again." {
		t.Fatalf("recovery push: %v", r.pushes)
	}
	if known := loadKnown(store); known[1].Disk != disk3 || known[1].Dev != "sde" {
		t.Errorf("replacement not recorded: %+v", known[1])
	}
	if Missing() != "" || MissingDisks() != nil || StillWorkingDisk() != "" {
		t.Errorf("whole again: %q %v %q", Missing(), MissingDisks(), StillWorkingDisk())
	}
}

// Degraded already when the device starts, with nothing recorded (the
// disk died while the container was down, and otc-mirror started the
// mirror from the other): the disk still working is named, in every
// language, by its .env name.
func TestMirrorDegradedAtStartNamesTheDiskStillWorking(t *testing.T) {
	f := newMirrorSys(t)
	_ = os.RemoveAll(filepath.Join(f.root, "block/md127/md/dev-sdd"))
	f.pull("sdd")
	f.write("block/md127/md/degraded", "1")
	store := t.TempDir()
	r := &keyedRecorder{}
	check(store, r.notify())
	check(store, r.notify())
	if len(r.alerts) != 1 || len(r.pushes) != 1 {
		t.Fatalf("alerts %+v, pushes %v", r.alerts, r.pushes)
	}
	a := r.alerts[0]
	wantDetail := "The disk " + disk1 + " still works and holds all your files, but they are no longer mirrored. Replace the other one with an empty disk at least as large, put the new disk's name in .env in place of the old one and run docker compose up -d: the device adds it and copies everything onto it."
	if a.Title != "One of the two storage disks stopped working" || a.Detail != wantDetail {
		t.Errorf("English %q | %q", a.Title, a.Detail)
	}
	if a.Key != "dev.alert.raid.stopped_unknown" || a.DetailKey != "dev.alert.raid.stopped_detail_working_named" || string(a.DetailArgs) != `{"disk":"`+disk1+`"}` {
		t.Errorf("keys %+v", a)
	}
	if r.pushes[0] != anonymousPush {
		t.Errorf("push %q", r.pushes[0])
	}
	if Missing() != unknownLabel || !slices.Equal(MissingDisks(), []string{""}) || StillWorkingDisk() != disk1 {
		t.Errorf("Missing() %q, MissingDisks() %q, StillWorkingDisk() %q", Missing(), MissingDisks(), StillWorkingDisk())
	}
}

// A member md lists as faulty is named by its device number even with
// nothing recorded; one whose number is none of the .env disks' isn't
// named, and the disk still working is.
func TestMirrorMemberNamedByItsNumber(t *testing.T) {
	f := newMirrorSys(t)
	f.mirrorMember("sdc", "0", "faulty")
	f.write("block/md127/md/degraded", "1")
	check(t.TempDir(), (&keyedRecorder{}).notify())
	if !slices.Equal(MissingDisks(), []string{disk1}) || StillWorkingDisk() != "" {
		t.Errorf("faulty sdc: MissingDisks() %v, StillWorkingDisk() %q", MissingDisks(), StillWorkingDisk())
	}

	// Recorded under another name (.env named the same disk by its wwn-
	// link then): today's name wins.
	store := t.TempDir()
	saveKnown(store, []Member{{Slot: 0, Dev: "sdc", Disk: "wwn-0x50014ee2b5a1c3d4"}, {Slot: 1, Dev: "sdd", Disk: disk2}})
	check(store, (&keyedRecorder{}).notify())
	if !slices.Equal(MissingDisks(), []string{disk1}) {
		t.Errorf("renamed in .env: MissingDisks() %v", MissingDisks())
	}

	f.disk("sdc", "8:80")
	check(t.TempDir(), (&keyedRecorder{}).notify())
	if !slices.Equal(MissingDisks(), []string{""}) || StillWorkingDisk() != disk2 {
		t.Errorf("not an .env disk: MissingDisks() %q, StillWorkingDisk() %q", MissingDisks(), StillWorkingDisk())
	}
}

// Only the container's own mirror is watched there: none without it,
// whatever the host's arrays do; outside a container the variables mean
// nothing and md0 is watched as always.
func TestMirrorOnlyItsOwn(t *testing.T) {
	f := newMirrorSys(t)
	f.unplug("sdb")
	f.write("block/md0/md/degraded", "1")

	t.Setenv(container.EnvMirrorArray, "")
	r := &keyedRecorder{}
	check(t.TempDir(), r.notify())
	if len(r.alerts)+len(r.pushes) != 0 {
		t.Fatalf("a container without its mirror alerted: %+v %v", r.alerts, r.pushes)
	}

	t.Setenv(container.EnvMirrorArray, "md127")
	t.Setenv(container.EnvVar, "")
	check(t.TempDir(), r.notify())
	if len(r.alerts) != 1 || r.alerts[0].Key != "dev.alert.raid.stopped_unknown" || MissingDisks() != nil {
		t.Fatalf("outside a container: %+v, MissingDisks() %v", r.alerts, MissingDisks())
	}
}

// The Pi's record of its disks is what it always was: no disk names.
func TestPiRecordUnchanged(t *testing.T) {
	newFakeSys(t)
	store := t.TempDir()
	check(store, (&recorder{}).notify())
	raw, err := os.ReadFile(filepath.Join(store, membersFile))
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"slot":0,"dev":"sda","port":"xhci-hcd.0 usb2 2-1","position":"` + bluePort["xhci-hcd.0"] + `","serial":"AAA"},{"slot":1,"dev":"sdb","port":"xhci-hcd.1 usb4 4-1","position":"` + bluePort["xhci-hcd.1"] + `","serial":"BBB"}]`
	if string(raw) != want {
		t.Errorf("record:\n got %s\nwant %s", raw, want)
	}
}

// Every shape of the mirror's stopped Alert.
func TestNamedStoppedAlert(t *testing.T) {
	for _, c := range []struct {
		name                     string
		f                        failure
		title, detail            string
		key, detailKey, argsJSON string
	}{
		{"named", failure{label: "the disk " + disk2, disks: []string{disk2}},
			"The disk " + disk2 + " stopped working", namedDetail,
			"dev.alert.raid.stopped_named", "dev.alert.raid.stopped_detail_named", `{"disk":"` + disk2 + `"}`},
		{"the other works", failure{label: unknownLabel, disks: []string{""}, workingDisk: disk1},
			"One of the two storage disks stopped working", "The disk " + disk1 + " still works and holds all your files, but they are no longer mirrored. Replace the other one with an empty disk at least as large, put the new disk's name in .env in place of the old one and run docker compose up -d: the device adds it and copies everything onto it.",
			"dev.alert.raid.stopped_unknown", "dev.alert.raid.stopped_detail_working_named", "{}"},
		{"nothing known", failure{label: unknownLabel, disks: []string{""}},
			"One of the two storage disks stopped working", namedDetail,
			"dev.alert.raid.stopped_unknown", "dev.alert.raid.stopped_detail_named", "{}"},
		{"both", failure{label: "the disk " + disk1 + " and the disk " + disk2, disks: []string{disk1, disk2}},
			"The disk " + disk1 + " and the disk " + disk2 + " stopped working", namedDetail, "", "", ""},
	} {
		a := namedStoppedAlert(c.f)
		if a.Title != c.title || a.Detail != c.detail || a.Key != c.key || a.DetailKey != c.detailKey || string(a.Args) != c.argsJSON {
			t.Errorf("%s:\n got %q | %q (%q %s, %q)\nwant %q | %q (%q %s, %q)", c.name, a.Title, a.Detail, a.Key, a.Args, a.DetailKey, c.title, c.detail, c.key, c.argsJSON, c.detailKey)
		}
	}
}

// md lists a disk that failed as faulty with no slot ("none"): with nothing
// recorded yet (it failed in the first minute), its name still says which
// it was - seen with mdadm --fail on a loop-device mirror in a VM.
func TestMirrorFaultyWithNoSlotIsNamed(t *testing.T) {
	f := newMirrorSys(t)
	f.mirrorMember("sdd", "none", "faulty")
	f.write("block/md127/md/degraded", "1")
	r := &keyedRecorder{}
	check(t.TempDir(), r.notify())
	if !slices.Equal(MissingDisks(), []string{disk2}) || StillWorkingDisk() != "" {
		t.Errorf("MissingDisks() %v, StillWorkingDisk() %q", MissingDisks(), StillWorkingDisk())
	}
	if len(r.alerts) != 1 || r.alerts[0].Key != "dev.alert.raid.stopped_named" || string(r.alerts[0].Args) != `{"disk":"`+disk2+`"}` {
		t.Errorf("alerts %+v", r.alerts)
	}
}
