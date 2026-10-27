// SPDX-License-Identifier: AGPL-3.0-or-later

package raidwatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/alerttext"
	"github.com/alonsovidales/otc/container"
)

// A disk raid_watch.py left out of the degraded mirror gets one Alert per
// time it is plugged in: not again while it stays listed, again once it
// was gone from the list (unplugged) and is back. A list that can't be
// read changes nothing.
func TestNotAddedAlertsOncePerPlugIn(t *testing.T) {
	f := newFakeSys(t)
	store := t.TempDir()
	var alerts []alerttext.Alert
	n := Notify{AlertText: func(a alerttext.Alert) { alerts = append(alerts, a) }}
	list := func(body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(notAddedPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(notAddedPath, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.unplug("sdb")
	f.write("block/md0/md/degraded", "1")

	check(store, n) // no list yet: only the stopped disk's alert
	if len(alerts) != 1 {
		t.Fatalf("before any list: %+v", alerts)
	}
	alerts = nil

	disk := "sdc 512.1 GB (Generic Card Reader, xhci-hcd.1 usb4 4-1)"
	list(`{"disks": [{"id": "sdc|512110190592|Generic Card Reader|xhci-hcd.1 usb4 4-1", "disk": "` + disk + `"}]}`)
	check(store, n)
	if len(alerts) != 1 {
		t.Fatalf("one disk left alone: %+v", alerts)
	}
	a := alerts[0]
	if a.Key != "dev.alert.raid.not_added_title" || a.DetailKey != "dev.alert.raid.not_added_detail" ||
		string(a.DetailArgs) != `{"disk":"`+disk+`"}` || a.Title != "A disk was plugged in but not added to the mirror" ||
		!strings.HasPrefix(a.Detail, "The device adds a disk to the mirror by itself only when nothing at all is on it") ||
		!strings.HasSuffix(a.Detail, "("+disk+")") {
		t.Errorf("the alert: %+v", a)
	}
	check(store, n)
	check(store, n)
	if len(alerts) != 1 {
		t.Fatalf("still plugged in, alerted again: %+v", alerts)
	}

	// Unreadable (garbage, or bigger than anything raid_watch.py writes):
	// the record stays as it was.
	list("{not json")
	check(store, n)
	list(`{"disks": [], "pad": "` + strings.Repeat("x", notAddedMaxBytes) + `"}`)
	check(store, n)
	if len(alerts) != 1 || !loadNotAdded(store)["sdc|512110190592|Generic Card Reader|xhci-hcd.1 usb4 4-1"] {
		t.Fatalf("an unreadable list changed things: %+v %v", alerts, loadNotAdded(store))
	}

	// Unplugged, then plugged in again: a new alert.
	list(`{"disks": []}`)
	check(store, n)
	if len(loadNotAdded(store)) != 0 {
		t.Errorf("an empty list kept %v", loadNotAdded(store))
	}
	list(`{"disks": [{"id": "sdc|512110190592|Generic Card Reader|xhci-hcd.1 usb4 4-1", "disk": "` + disk + `"}]}`)
	check(store, n)
	if len(alerts) != 2 {
		t.Fatalf("plugged in again: %+v", alerts)
	}

	// Control characters never reach the text, nor more than 8 disks.
	var many []string
	for i := 0; i < 12; i++ {
		many = append(many, `{"id": "d`+strings.Repeat("x", i)+`", "disk": "sd\u001b[31mz\n"}`)
	}
	list(`{"disks": [` + strings.Join(many, ",") + `]}`)
	alerts = nil
	check(store, n)
	if len(alerts) != notAddedMaxDisks {
		t.Fatalf("%d alerts for 12 disks", len(alerts))
	}
	if strings.ContainsAny(string(alerts[0].DetailArgs), "\u001b\n") || !strings.Contains(string(alerts[0].DetailArgs), `"sd[31mz"`) {
		t.Errorf("a control character got through: %s", alerts[0].DetailArgs)
	}
}

// In a container (its own mirror, named by .env) the list is never read:
// otc-mirror adds a disk only when .env names it.
func TestNotAddedNotInAContainer(t *testing.T) {
	newFakeSys(t)
	t.Setenv(container.EnvVar, "1")
	t.Setenv(container.EnvMirrorArray, "md127")
	if err := os.MkdirAll(filepath.Dir(notAddedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notAddedPath, []byte(`{"disks": [{"id": "x", "disk": "sdc"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var alerts []alerttext.Alert
	check(t.TempDir(), Notify{AlertText: func(a alerttext.Alert) { alerts = append(alerts, a) }})
	if len(alerts) != 0 {
		t.Fatalf("in a container: %+v", alerts)
	}
}
