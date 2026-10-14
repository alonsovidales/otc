// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
)

// Issue #197: Open finds its folder by id among both kinds.
func TestFindFolder(t *testing.T) {
	st := config.State{
		Folders:       []config.FolderStatus{{ID: "b1", Path: "/home/ana/Scans"}},
		RemoteFolders: []config.FolderStatus{{ID: "r1", Path: "/home/ana/Photos", RemotePath: "/Photos"}},
	}
	for id, want := range map[string]string{"b1": "/home/ana/Scans", "r1": "/home/ana/Photos"} {
		f, ok := findFolder(st, id)
		if !ok || f.Path != want {
			t.Errorf("findFolder(%q) = %q, %v; want %q", id, f.Path, ok, want)
		}
	}
	if _, ok := findFolder(st, "gone"); ok {
		t.Error("findFolder found a folder that was removed")
	}
}

// strip drops the isolation marks around user text (names, paths).
func strip(s string) string {
	return strings.NewReplacer("⁨", "", "⁩", "").Replace(s)
}

// A folder that is gone - or a file in its place - says so, with where it
// was expected; any other failure gives the system's reason and the path.
func TestOpenFolderError(t *testing.T) {
	f := config.FolderStatus{ID: "r1", Path: "/home/ana/Photos", RemotePath: "/Photos"}
	missing := fmt.Errorf("stat /home/ana/Photos: %w", fs.ErrNotExist)
	if got, want := strip(openFolderError("", f, missing)),
		"The folder “Photos” is no longer at /home/ana/Photos - it may have been moved, renamed or deleted."; got != want {
		t.Errorf("missing:\n got %q\nwant %q", got, want)
	}
	failed := errors.New("xdg-open: exit status 3")
	if got, want := strip(openFolderError("", f, failed)),
		"The folder could not be opened:\n\nxdg-open: exit status 3\n\nIt is at /home/ana/Photos"; got != want {
		t.Errorf("failed:\n got %q\nwant %q", got, want)
	}
	// In the menu's language.
	if got := strip(openFolderError("es", f, missing)); !strings.HasPrefix(got, "La carpeta “Photos” ya no está en /home/ana/Photos") {
		t.Errorf("es: %q", got)
	}
}

// Windows' message box shows the isolation marks as boxes: its dialog
// leaves them out (winText, native.go); Linux's keeps them.
func TestDialogText(t *testing.T) {
	f := config.FolderStatus{ID: "b1", Path: "/home/ana/Shared Docs"}
	text := openFolderError("", f, fs.ErrNotExist)
	if got := winText("linux", text); got != text {
		t.Errorf("linux: %q, want it as it is", got)
	}
	got := winText("windows", text)
	if strings.ContainsAny(got, "\u2068\u2069") {
		t.Errorf("windows: isolation marks left in %q", got)
	}
	if want := "The folder “Shared Docs” is no longer at /home/ana/Shared Docs - it may have been moved, renamed or deleted."; got != want {
		t.Errorf("windows:\n got %q\nwant %q", got, want)
	}
}
