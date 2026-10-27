// SPDX-License-Identifier: AGPL-3.0-or-later

package raidwatch

import (
	"testing"

	"github.com/alonsovidales/otc/alerttext"
	"github.com/alonsovidales/otc/push"
)

// With AlertText and PushText the watcher hands its Alerts whole - their
// English, as Alert and Push got it, and their keys - and its pushes as
// texts rendered in the push language when they are delivered.
func TestAlertsAndPushesCarryTheirKeys(t *testing.T) {
	f := newFakeSys(t)
	store := t.TempDir()
	var alerts []alerttext.Alert
	var pushes []push.Text
	n := Notify{
		AlertText: func(a alerttext.Alert) { alerts = append(alerts, a) },
		PushText:  func(p push.Text) { pushes = append(pushes, p) },
		Alert:     func(string, string) { t.Error("the English seam was used") },
		Push:      func(string, string) { t.Error("the English seam was used") },
	}
	check(store, n)

	f.unplug("sdb")
	f.write("block/md0/md/degraded", "1")
	check(store, n)
	top := bluePort["xhci-hcd.1"]
	if len(alerts) != 1 || len(pushes) != 1 {
		t.Fatalf("alerts %+v, pushes %d", alerts, len(pushes))
	}
	a := alerts[0]
	wantDetail := "Your files are safe on the other disk, but they are no longer mirrored. Replace it with a blank card at least as large, with nothing at all on it (a new card usually has to be fully erased on a computer first): the device adds it and copies everything onto it by itself. (sdb BBB xhci-hcd.1 usb4 4-1)"
	if a.Title != "The disk in the "+top+" blue USB port stopped working" || a.Detail != wantDetail {
		t.Errorf("English %q | %q", a.Title, a.Detail)
	}
	if a.Key != "dev.alert.raid.stopped_"+top || a.DetailKey != "dev.alert.raid.replace_detail_disk" || string(a.DetailArgs) != `{"disk":"sdb BBB xhci-hcd.1 usb4 4-1"}` {
		t.Errorf("keys %+v", a)
	}
	if title, body := pushes[0](""); title != "Storage disk stopped working" || body != "The disk in the "+top+" blue USB port failed. Your files are safe on the other one: replace it soon." {
		t.Errorf("push %q | %q", title, body)
	}

	f.plug("sdc", "xhci-hcd.1", "usb4", "4-1", "CCC")
	f.member("sdc", "1", "in_sync")
	f.write("block/md0/md/degraded", "0")
	check(store, n)
	if len(alerts) != 2 || len(pushes) != 2 {
		t.Fatalf("recovery: alerts %+v, pushes %d", alerts, len(pushes))
	}
	if a := alerts[1]; a.Title != "Both storage disks are working again" || a.Detail != "The mirror is complete: every file is on both disks again." ||
		a.Key != "dev.alert.raid.working_title" || a.DetailKey != "dev.alert.raid.working_detail" {
		t.Errorf("recovery alert %+v", a)
	}
	if title, body := pushes[1](""); title != "Storage disks working again" || body != "The mirror is complete: every file is on both disks again." {
		t.Errorf("recovery push %q | %q", title, body)
	}
}

// Every shape of the stopped Alert: a disk on either port or on neither,
// with or without what is known of it, keyed; several disks only in
// English, from their labels, as before.
func TestStoppedAlert(t *testing.T) {
	const mirror = "Your files are safe on the other disk, but they are no longer mirrored. Replace it with a blank card at least as large, with nothing at all on it (a new card usually has to be fully erased on a computer first): the device adds it and copies everything onto it by itself."
	topDisk := Member{Slot: 1, Dev: "sdb", Serial: "BBB", Port: "xhci-hcd.1 usb4 4-1", Position: "top"}
	bottomDisk := Member{Slot: 0, Dev: "sda", Serial: "AAA", Port: "xhci-hcd.0 usb2 2-1", Position: "bottom"}
	for _, c := range []struct {
		name               string
		missing            []Member
		title, detail, key string
		detailKey          string
	}{
		{"top", []Member{topDisk}, "The disk in the top blue USB port stopped working", mirror + " (sdb BBB xhci-hcd.1 usb4 4-1)", "dev.alert.raid.stopped_top", "dev.alert.raid.replace_detail_disk"},
		{"bottom", []Member{bottomDisk}, "The disk in the bottom blue USB port stopped working", mirror + " (sda AAA xhci-hcd.0 usb2 2-1)", "dev.alert.raid.stopped_bottom", "dev.alert.raid.replace_detail_disk"},
		// The fields are joined as they always were, an empty one too.
		{"a black port", []Member{{Slot: 1, Dev: "sdb", Port: "xhci-hcd.0 usb3 3-2"}}, "One of the two storage disks stopped working", mirror + " (sdb  xhci-hcd.0 usb3 3-2)", "dev.alert.raid.stopped_unknown", "dev.alert.raid.replace_detail_disk"},
		{"nothing known", []Member{{Slot: 1}}, "One of the two storage disks stopped working", mirror, "dev.alert.raid.stopped_unknown", "dev.alert.raid.replace_detail"},
		{"none at all", nil, "One of the two storage disks stopped working", mirror, "dev.alert.raid.stopped_unknown", "dev.alert.raid.replace_detail"},
		{"both", []Member{bottomDisk, topDisk}, "The disk in the bottom blue USB port and the disk in the top blue USB port stopped working", mirror + " (sda AAA xhci-hcd.0 usb2 2-1) (sdb BBB xhci-hcd.1 usb4 4-1)", "", ""},
	} {
		a := stoppedAlert(c.missing, describe(c.missing))
		if a.Title != c.title || a.Detail != c.detail || a.Key != c.key || a.DetailKey != c.detailKey {
			t.Errorf("%s:\n got %q | %q (%q, %q)\nwant %q | %q (%q, %q)", c.name, a.Title, a.Detail, a.Key, a.DetailKey, c.title, c.detail, c.key, c.detailKey)
		}
	}
}
