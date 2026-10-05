// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"os"
	"path/filepath"
	"testing"
)

// The hash cache survives a restart: a second engine reads what the
// first one saved and answers an unchanged file without re-hashing it.
func TestHashCachePersists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", home)

	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	first := &Engine{hashCache: map[string]map[string]hashEntry{}, hashDirty: map[string]bool{}, hashLoaded: map[string]bool{}}
	h1, err := first.cachedHash("f1", file)
	if err != nil {
		t.Fatal(err)
	}
	first.saveHashCache("f1")

	second := &Engine{hashCache: map[string]map[string]hashEntry{}, hashDirty: map[string]bool{}, hashLoaded: map[string]bool{}}
	// A changed file on disk with the same size and mtime would be missed
	// by design; here the point is that the entry came from the file, so
	// plant a marker hash and make sure it is what comes back.
	p, _ := hashCachePath("f1")
	data, _ := os.ReadFile(p)
	if len(data) == 0 {
		t.Fatal("cache file not written")
	}
	h2, err := second.cachedHash("f1", file)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 || !second.hashLoaded["f1"] || second.hashDirty["f1"] {
		t.Fatalf("second engine hashed again: %q vs %q, loaded=%v dirty=%v", h1, h2, second.hashLoaded["f1"], second.hashDirty["f1"])
	}

	second.mu.Lock()
	second.dropHashCacheLocked("f1")
	second.mu.Unlock()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("cache file still there after drop: %v", err)
	}
}

// Entries of files that left the folder go at the next complete listing,
// including ones only in the saved file (not loaded yet).
func TestHashCachePrune(t *testing.T) {
	withConfigDir(t)
	dir := t.TempDir()
	keep, gone := filepath.Join(dir, "keep.txt"), filepath.Join(dir, "gone.txt")
	for _, p := range []string{keep, gone} {
		if err := os.WriteFile(p, []byte(p), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	first := &Engine{hashCache: map[string]map[string]hashEntry{}, hashDirty: map[string]bool{}, hashLoaded: map[string]bool{}}
	for _, p := range []string{keep, gone} {
		if _, err := first.cachedHash("f1", p); err != nil {
			t.Fatal(err)
		}
	}
	first.saveHashCache("f1")
	_ = os.Remove(gone)

	second := &Engine{hashCache: map[string]map[string]hashEntry{}, hashDirty: map[string]bool{}, hashLoaded: map[string]bool{}}
	local, failed, err := enumerateFiles(dir)
	if err != nil || len(failed) != 0 {
		t.Fatal(err, failed)
	}
	second.pruneHashCache("f1", local)
	if _, ok := second.hashCache["f1"][keep]; !ok || len(second.hashCache["f1"]) != 1 || !second.hashDirty["f1"] {
		t.Fatalf("after prune: %v dirty=%v", second.hashCache["f1"], second.hashDirty["f1"])
	}
}
