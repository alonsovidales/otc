// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build unix

package engine

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The freedesktop.org trash: the file in files/, its .trashinfo in info/
// with where it was (escaped) and when; the same name again is another
// name.
func TestFreedesktopTrash(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	dir := filepath.Join(t.TempDir(), "My Docs")
	writeFiles(t, dir, map[string]string{"été.txt": "1"})
	at := time.Date(2026, 11, 4, 10, 16, 5, 0, time.Local)
	p := filepath.Join(dir, "été.txt")
	for i := 0; i < 2; i++ {
		if i == 1 {
			writeFiles(t, dir, map[string]string{"été.txt": "2"})
		}
		if err := freedesktopTrash(p, at); err != nil {
			t.Fatal(err)
		}
	}
	trash := filepath.Join(data, "Trash")
	for name, want := range map[string]string{"été.txt": "1", "été.2.txt": "2"} {
		if got, err := os.ReadFile(filepath.Join(trash, "files", name)); err != nil || string(got) != want {
			t.Fatalf("%s: %q %v", name, got, err)
		}
	}
	abs, _ := filepath.Abs(p)
	info, err := os.ReadFile(filepath.Join(trash, "info", "été.txt.trashinfo"))
	if err != nil {
		t.Fatal(err)
	}
	want := "[Trash Info]\nPath=" + trashPathEscape(abs) + "\nDeletionDate=2026-11-04T10:16:05\n"
	if string(info) != want {
		t.Fatalf("trashinfo %q, want %q", info, want)
	}
	if got := trashPathEscape("/home/ana/My Docs/été.txt"); got != "/home/ana/My%20Docs/%C3%A9t%C3%A9.txt" {
		t.Fatalf("escaped %q", got)
	}
}

// A disk's own trash (.Trash-<uid> at its top) keeps paths relative to it,
// and one that is a link someone put there is refused.
func TestTopDirTrash(t *testing.T) {
	top := t.TempDir()
	writeFiles(t, top, map[string]string{"photos/a.jpg": "a"})
	dir := filepath.Join(top, ".Trash-"+strconv.Itoa(os.Getuid()))
	if err := ensureTrashDir(dir, true); err != nil {
		t.Fatal(err)
	}
	if err := trashInto(dir, filepath.Join(top, "photos", "a.jpg"), "photos/a.jpg", time.Now()); err != nil {
		t.Fatal(err)
	}
	info, _ := os.ReadFile(filepath.Join(dir, "info", "a.jpg.trashinfo"))
	if want := "Path=photos/a.jpg\n"; !containsLine(string(info), want) {
		t.Fatalf("trashinfo %q", info)
	}
	elsewhere := t.TempDir()
	link := filepath.Join(t.TempDir(), ".Trash-x")
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Skip(err)
	}
	if err := ensureTrashDir(link, true); err == nil {
		t.Fatal("a link taken for a disk's trash")
	}
}

func containsLine(s, line string) bool {
	for len(s) > 0 {
		i := 0
		for i < len(s) && s[i] != '\n' {
			i++
		}
		if i < len(s) {
			i++
		}
		if s[:i] == line {
			return true
		}
		s = s[i:]
	}

	return false
}

// What otc-sync put in the trash is emptied after 30 days - a computer
// running it as a service has nobody to empty the trash - and only that:
// a file restored from the trash, and one the user trashed, stay.
func TestOwnTrashIsEmptiedAfter30Days(t *testing.T) {
	withConfigDir(t)
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	ownTrashPruned = time.Time{}
	t.Cleanup(func() { ownTrashPruned = time.Time{} })
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"old.txt": "o", "new.txt": "n", "restored.txt": "r"})
	now := time.Now()
	for name, at := range map[string]time.Time{"old.txt": now.AddDate(0, 0, -31), "new.txt": now.AddDate(0, 0, -1), "restored.txt": now.AddDate(0, 0, -40)} {
		if err := freedesktopTrash(filepath.Join(dir, name), at); err != nil {
			t.Fatal(err)
		}
	}
	trash := filepath.Join(data, "Trash")
	// Restored by the user, from the file manager.
	if err := os.Rename(filepath.Join(trash, "files", "restored.txt"), filepath.Join(dir, "restored.txt")); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(trash, "info", "restored.txt.trashinfo"))
	// Trashed by the user, long ago.
	writeFiles(t, trash, map[string]string{"files/mine.txt": "m", "info/mine.txt.trashinfo": "[Trash Info]\nPath=/x/mine.txt\nDeletionDate=2020-01-01T00:00:00\n"})

	pruneOwnTrash(now)
	exists := func(p string) bool { _, err := os.Lstat(p); return err == nil }
	for p, want := range map[string]bool{
		filepath.Join(trash, "files", "old.txt"): false, filepath.Join(trash, "info", "old.txt.trashinfo"): false,
		filepath.Join(trash, "files", "new.txt"): true, filepath.Join(trash, "info", "new.txt.trashinfo"): true,
		filepath.Join(trash, "files", "mine.txt"): true, filepath.Join(dir, "restored.txt"): true,
	} {
		if exists(p) != want {
			t.Errorf("%s there: %v, want %v", p, !want, want)
		}
	}
	log, _ := ownTrashPath()
	listed, err := os.ReadFile(log)
	if err != nil || strings.Count(string(listed), "\n") != 1 || !strings.Contains(string(listed), `"new.txt"`) {
		t.Fatalf("still listed: %q %v", listed, err)
	}
	// Not again within the hour; 30 days on, the rest goes and the list too.
	pruneOwnTrash(now.Add(time.Minute))
	if !exists(filepath.Join(trash, "files", "new.txt")) {
		t.Fatal("a file of yesterday emptied")
	}
	pruneOwnTrash(now.AddDate(0, 0, 30))
	if exists(filepath.Join(trash, "files", "new.txt")) || exists(log) {
		t.Fatal("not emptied 31 days on")
	}
}
