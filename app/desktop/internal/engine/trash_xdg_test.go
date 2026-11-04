// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build unix

package engine

import (
	"os"
	"path/filepath"
	"strconv"
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
