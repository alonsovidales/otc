// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"os"
	"path/filepath"
	"testing"
)

// A folder deleted on the device: its files went one by one, and the tree
// they leave behind goes too - but never the synced folder itself, nor a
// folder that still holds something.
func TestRemoveEmptiedDirs(t *testing.T) {
	root := t.TempDir()
	mk := func(p string) string {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}

		return full
	}
	write := func(p string) {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(p)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gone := mk("lib.fcpbundle/day/Render Files/Peaks Data/abc")
	mk("lib.fcpbundle/day/Original Media")
	write("lib.fcpbundle/day/.DS_Store")
	kept := mk("album/sub")
	write("album/keep.jpg")
	hidden := mk("proj/src")
	mk("proj/.git")

	n := removeEmptiedDirs(map[string]bool{
		gone: true,
		filepath.Join(root, "lib.fcpbundle", "day", "Original Media"): true,
		kept:   true,
		hidden: true,
		root:   true,
	}, root)

	if _, err := os.Stat(filepath.Join(root, "lib.fcpbundle")); !os.IsNotExist(err) {
		t.Errorf("lib.fcpbundle should be gone, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "album", "keep.jpg")); err != nil {
		t.Errorf("album/keep.jpg must stay: %v", err)
	}
	if _, err := os.Stat(kept); !os.IsNotExist(err) {
		t.Errorf("album/sub was emptied and should be gone")
	}
	if _, err := os.Stat(filepath.Join(root, "proj", ".git")); err != nil {
		t.Errorf("proj/.git must stay (only .DS_Store is junk): %v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("the synced folder itself must stay: %v", err)
	}
	// lib.fcpbundle: Peaks Data/abc, Peaks Data, Render Files, Original
	// Media, day, lib.fcpbundle; album/sub; proj/src.
	if n != 8 {
		t.Errorf("removed %d folders, want 8", n)
	}
}
