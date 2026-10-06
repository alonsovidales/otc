// SPDX-License-Identifier: AGPL-3.0-or-later

package raidwatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSys builds the sysfs a Raspberry Pi 5 shows for a two-disk md0:
// sda in the controller-0 blue port, sdb in the controller-1 one.
type fakeSys struct {
	t    *testing.T
	root string
}

func newFakeSys(t *testing.T) *fakeSys {
	f := &fakeSys{t: t, root: t.TempDir()}
	f.write("bus/usb/devices/usb2/speed", "5000")
	f.write("bus/usb/devices/usb4/speed", "5000")
	f.write("bus/usb/devices/usb3/speed", "480")
	f.write("block/md0/md/raid_disks", "2")
	f.write("block/md0/md/sync_action", "idle")
	f.write("block/md0/md/degraded", "0")
	f.plug("sda", "xhci-hcd.0", "usb2", "2-1", "AAA")
	f.plug("sdb", "xhci-hcd.1", "usb4", "4-1", "BBB")
	f.member("sda", "0", "in_sync")
	f.member("sdb", "1", "in_sync")
	old := sysRoot
	sysRoot = f.root
	t.Cleanup(func() { sysRoot = old })
	return f
}

func (f *fakeSys) write(rel, s string) {
	p := filepath.Join(f.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s+"\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeSys) plug(dev, ctrl, bus, port, serial string) {
	ctrlDir := map[string]string{"xhci-hcd.0": "1f00200000.usb", "xhci-hcd.1": "1f00300000.usb"}[ctrl]
	dir := filepath.Join(f.root, "devices/platform/axi/1000120000.pcie", ctrlDir, ctrl, bus, port, port+":1.0/host0/target0:0:0/0:0:0:0/block", dev)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	link := filepath.Join(f.root, "class/block", dev)
	_ = os.MkdirAll(filepath.Dir(link), 0o755)
	_ = os.Remove(link)
	if err := os.Symlink(dir, link); err != nil {
		f.t.Fatal(err)
	}
	f.write(filepath.Join("bus/usb/devices", port, "serial"), serial)
}

func (f *fakeSys) member(dev, slot, state string) {
	f.write(filepath.Join("block/md0/md/dev-"+dev, "slot"), slot)
	f.write(filepath.Join("block/md0/md/dev-"+dev, "state"), state)
}

func (f *fakeSys) unplug(dev string) {
	_ = os.RemoveAll(filepath.Join(f.root, "block/md0/md/dev-"+dev))
	_ = os.Remove(filepath.Join(f.root, "class/block", dev))
}

type recorder struct{ alerts, pushes []string }

func (r *recorder) notify() Notify {
	return Notify{
		Alert: func(title, detail string) { r.alerts = append(r.alerts, title+" | "+detail) },
		Push:  func(title, body string) { r.pushes = append(r.pushes, title+" | "+body) },
	}
}

func TestLocate(t *testing.T) {
	f := newFakeSys(t)
	port, pos, serial := locate("sdb")
	if port != "xhci-hcd.1 usb4 4-1" || pos != bluePort["xhci-hcd.1"] || serial != "BBB" {
		t.Errorf("sdb: got %q %q %q", port, pos, serial)
	}
	// A partition resolves through its disk.
	f.plug("sda1", "xhci-hcd.0", "usb2", "2-1", "AAA")
	if _, pos, _ := locate("sda1"); pos != bluePort["xhci-hcd.0"] {
		t.Errorf("sda1 position %q", pos)
	}
	// A black (USB 2) port, and a hub, have no position.
	f.plug("sdc", "xhci-hcd.0", "usb3", "3-2", "CCC")
	if _, pos, _ := locate("sdc"); pos != "" {
		t.Errorf("USB 2 port got position %q", pos)
	}
	f.plug("sdd", "xhci-hcd.1", "usb4", "4-1.3", "DDD")
	if _, pos, _ := locate("sdd"); pos != "" {
		t.Errorf("hub port got position %q", pos)
	}
}

func TestFailureAndRecovery(t *testing.T) {
	f := newFakeSys(t)
	store := t.TempDir()
	r := &recorder{}

	// Healthy: where the disks are is recorded, nothing is said.
	check(store, r.notify())
	if len(r.alerts)+len(r.pushes) != 0 {
		t.Fatalf("healthy array alerted: %v %v", r.alerts, r.pushes)
	}
	if known := loadKnown(store); known[1].Position != bluePort["xhci-hcd.1"] || known[0].Serial != "AAA" {
		t.Fatalf("not recorded: %+v", known)
	}

	// The reader in the controller-1 port vanishes.
	f.unplug("sdb")
	f.write("block/md0/md/degraded", "1")
	check(store, r.notify())
	want := "The disk in the " + bluePort["xhci-hcd.1"] + " blue USB port stopped working"
	if len(r.alerts) != 1 || !strings.HasPrefix(r.alerts[0], want) || !strings.Contains(r.alerts[0], "BBB") {
		t.Fatalf("alerts: %v", r.alerts)
	}
	if len(r.pushes) != 1 || !strings.Contains(r.pushes[0], bluePort["xhci-hcd.1"]+" blue USB port") {
		t.Fatalf("pushes: %v", r.pushes)
	}
	if !strings.Contains(Missing(), bluePort["xhci-hcd.1"]) {
		t.Errorf("Missing() = %q", Missing())
	}

	// Still degraded, a replacement rebuilding into the slot: said once.
	f.plug("sdc", "xhci-hcd.1", "usb4", "4-1", "CCC")
	f.member("sdc", "1", "spare")
	f.write("block/md0/md/sync_action", "recover")
	check(store, r.notify())
	if len(r.alerts) != 1 || len(r.pushes) != 1 {
		t.Fatalf("repeated: %v %v", r.alerts, r.pushes)
	}
	if !strings.Contains(Missing(), bluePort["xhci-hcd.1"]) {
		t.Errorf("during the rebuild Missing() = %q", Missing())
	}

	// Rebuilt: the mirror is whole again, said once, and the new disk is
	// what is recorded now.
	f.member("sdc", "1", "in_sync")
	f.write("block/md0/md/sync_action", "idle")
	f.write("block/md0/md/degraded", "0")
	check(store, r.notify())
	check(store, r.notify())
	if len(r.alerts) != 2 || !strings.HasPrefix(r.alerts[1], "Both storage disks are working again") || len(r.pushes) != 2 {
		t.Fatalf("recovery: %v %v", r.alerts, r.pushes)
	}
	if known := loadKnown(store); known[1].Serial != "CCC" {
		t.Errorf("replacement not recorded: %+v", known[1])
	}
	if Missing() != "" {
		t.Errorf("Missing() after recovery = %q", Missing())
	}
}

// Degraded already when the service starts, with nothing recorded (a
// failure while it was down, or a device updated while degraded): said
// once, without a position.
func TestDegradedAtStartUnknownPort(t *testing.T) {
	f := newFakeSys(t)
	f.unplug("sdb")
	f.write("block/md0/md/degraded", "1")
	store := t.TempDir()
	r := &recorder{}
	check(store, r.notify())
	check(store, r.notify())
	if len(r.alerts) != 1 || !strings.HasPrefix(r.alerts[0], "One of the two storage disks stopped working") {
		t.Fatalf("alerts: %v", r.alerts)
	}
}

func TestNoArray(t *testing.T) {
	f := newFakeSys(t)
	_ = os.RemoveAll(filepath.Join(f.root, "block/md0"))
	r := &recorder{}
	check(t.TempDir(), r.notify())
	if len(r.alerts)+len(r.pushes) != 0 {
		t.Fatalf("no array alerted: %v", r.alerts)
	}
}
