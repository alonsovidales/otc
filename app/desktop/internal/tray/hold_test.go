// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import (
	"fmt"
	"reflect"
	"testing"

	"fyne.io/systray"
)

// watchWrites swaps the item writes for a log of them, for one test.
func watchWrites(t *testing.T) *[]string {
	var log []string
	title, tip, shown, checked, enabled := writeTitle, writeTip, writeShown, writeChecked, writeEnabled
	writeTitle = func(_ *systray.MenuItem, s string) { log = append(log, "title "+s) }
	writeTip = func(_ *systray.MenuItem, s string) { log = append(log, "tip "+s) }
	writeShown = func(_ *systray.MenuItem, on bool) { log = append(log, fmt.Sprint("shown ", on)) }
	writeChecked = func(_ *systray.MenuItem, on bool) { log = append(log, fmt.Sprint("checked ", on)) }
	writeEnabled = func(_ *systray.MenuItem, on bool) { log = append(log, fmt.Sprint("enabled ", on)) }
	t.Cleanup(func() {
		writeTitle, writeTip, writeShown, writeChecked, writeEnabled = title, tip, shown, checked, enabled
	})
	return &log
}

func newTestUI() *ui {
	return &ui{
		titles: map[*systray.MenuItem]string{}, tips: map[*systray.MenuItem]string{},
		shown: map[*systray.MenuItem]bool{}, checked: map[*systray.MenuItem]bool{},
		enabled: map[*systray.MenuItem]bool{}, held: map[*systray.MenuItem]bool{},
	}
}

// On Windows a write to a hidden item puts it back in the menu: "No folders
// yet", hidden as the menu was built, came back above the folders once its
// words were written. So a hidden item gets no writes until it is shown.
func TestAHiddenItemGetsNoWrites(t *testing.T) {
	log := watchWrites(t)
	u := newTestUI()
	empty := &systray.MenuItem{}
	u.hidden(empty) // as build hides it when there are folders
	u.setTitle(empty, "No folders yet")
	u.setTip(empty, "tip")
	u.setChecked(empty, true)
	u.setEnabled(empty, false)
	u.setShown(empty, false) // already hidden: no second Hide
	if len(*log) != 0 {
		t.Fatalf("writes to a hidden item: %q", *log)
	}
}

func TestAShownItemGetsWhatItWasGivenWhileHidden(t *testing.T) {
	log := watchWrites(t)
	u := newTestUI()
	web := &systray.MenuItem{}
	u.hidden(web)
	u.setTitle(web, "Open web app")
	u.setTip(web, "https://x.example")
	u.setEnabled(web, true)
	u.setShown(web, true)
	want := []string{"shown true", "title Open web app", "tip https://x.example", "enabled true"}
	if !reflect.DeepEqual(*log, want) {
		t.Fatalf("got %q, want %q", *log, want)
	}
	*log = nil
	u.setTitle(web, "Open web app")
	u.setShown(web, true)
	if len(*log) != 0 {
		t.Fatalf("nothing changed, yet: %q", *log)
	}
	u.setTitle(web, "Abrir la app web")
	if want := []string{"title Abrir la app web"}; !reflect.DeepEqual(*log, want) {
		t.Fatalf("a shown item is written at once: %q", *log)
	}
}

func TestAnItemHiddenAgainHoldsItsWritesAgain(t *testing.T) {
	log := watchWrites(t)
	u := newTestUI()
	upd := &systray.MenuItem{}
	u.setTitle(upd, "Update")
	u.setShown(upd, true)
	u.setShown(upd, false)
	*log = nil
	u.setTitle(upd, "Update 2")
	if len(*log) != 0 {
		t.Fatalf("written while hidden: %q", *log)
	}
	u.setShown(upd, true)
	if want := []string{"shown true", "title Update 2"}; !reflect.DeepEqual(*log, want) {
		t.Fatalf("got %q, want %q", *log, want)
	}
}

// An item never hidden here (shown as built) is written at once, as before.
func TestAnItemNeverHiddenIsWrittenAtOnce(t *testing.T) {
	log := watchWrites(t)
	u := newTestUI()
	status := &systray.MenuItem{}
	u.setTitle(status, "Connected")
	u.setChecked(status, true)
	if want := []string{"title Connected", "checked true"}; !reflect.DeepEqual(*log, want) {
		t.Fatalf("got %q, want %q", *log, want)
	}
}
