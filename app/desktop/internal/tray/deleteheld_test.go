// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import (
	"reflect"
	"strings"
	"testing"

	"fyne.io/systray"
	"github.com/ncruces/zenity"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/i18n"
)

// "Delete Them on the Device Too…" is in a two-way folder's submenu only
// while the engine says it holds deletes there, with the key to approve -
// never for a backup, and gone again once nothing is held.
func TestDeleteHeldItemShowsWhileDeletesAreHeld(t *testing.T) {
	log := watchWrites(t)
	u := newTestUI()
	fi := &folderItem{deleteHeld: &systray.MenuItem{}, id: "r1", remote: true}
	u.hidden(fi.deleteHeld) // as build adds it
	held := config.FolderStatus{ID: "r1", Path: "/home/ana/Pics", RemotePath: "/linux/pc/home/ana/Pics", State: "error",
		DeletesHeld: 25, DeletesHeldKey: strings.Repeat("a", 64)}
	for name, c := range map[string]struct {
		f    config.FolderStatus
		want bool
	}{
		"held":                {held, true},
		"nothing held":        {config.FolderStatus{ID: "r1", Path: held.Path, RemotePath: held.RemotePath, State: "watching"}, false},
		"a count with no key": {config.FolderStatus{ID: "r1", Path: held.Path, RemotePath: held.RemotePath, DeletesHeld: 25}, false},
		"a backup":            {config.FolderStatus{ID: "b1", Path: held.Path, DeletesHeld: 25, DeletesHeldKey: held.DeletesHeldKey}, false},
	} {
		if got := deleteHeldShown(c.f); got != c.want {
			t.Errorf("%s: shown %v, want %v", name, got, c.want)
		}
	}

	u.applyDeleteHeld(fi, held)
	if !u.shown[fi.deleteHeld] || !strings.Contains(strings.Join(*log, "\n"), "title Delete Them on the Device Too…") {
		t.Fatalf("not shown while held: %q", *log)
	}
	*log = nil
	u.applyDeleteHeld(fi, config.FolderStatus{ID: "r1", Path: held.Path, RemotePath: held.RemotePath, State: "watching"})
	if u.shown[fi.deleteHeld] || !reflect.DeepEqual(*log, []string{"shown false"}) {
		t.Fatalf("once nothing is held: %q", *log)
	}
}

// The question names how many and puts Cancel first: a stray Enter never
// deletes anything on the device.
func TestDeleteHeldQuestion(t *testing.T) {
	if got := i18n.DeskMainDeleteHeldTitle(25).Render(""); got != "Delete 25 files on the device too?" {
		t.Errorf("title %q", got)
	}
	if got := i18n.DeskMainDeleteHeldMessage().Render(""); !strings.Contains(got, "can't be undone") {
		t.Errorf("message %q", got)
	}
	want := reflect.ValueOf(zenity.DefaultCancel()).Pointer()
	for _, o := range deleteHeldOptions(25) {
		if reflect.ValueOf(o).Kind() == reflect.Func && reflect.ValueOf(o).Pointer() == want {
			return
		}
	}
	t.Fatal("the question has no zenity.DefaultCancel()")
}
