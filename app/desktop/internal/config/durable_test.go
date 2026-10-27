// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A config.json left unreadable (an empty file after a power cut) is
// replaced by the copy kept at the save before; one that can't be read at
// all, or has no copy, is an error - never an empty config to save over it.
func TestLoadFallsBackToTheCopy(t *testing.T) {
	withConfigDir(t)
	first := &Config{Domain: "cala.off-the.cloud", RemoteFolders: []RemoteFolder{{ID: "r1", RemotePath: "/x", LocalPath: "/home/a/x"}}}
	if err := first.Save(); err != nil {
		t.Fatal(err)
	}
	second := *first
	second.Language = "es"
	if err := second.Save(); err != nil {
		t.Fatal(err)
	}
	p, _ := path("config.json")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil || got.Domain != "cala.off-the.cloud" || len(got.RemoteFolders) != 1 {
		t.Fatalf("after an empty config.json: %+v %v", got, err)
	}
	if err := os.Remove(p + ".bak"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("an unreadable config.json with no copy read as a config")
	}
}

// The temporary files are each writer's own and none is left behind.
func TestSaveLeavesNoTemporaryFiles(t *testing.T) {
	withConfigDir(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := (&Config{Domain: strings.Repeat("x", i+1)}).Save(); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	dir, _ := Dir()
	left, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if len(left) > 0 {
		t.Fatalf("left behind: %v", left)
	}
	if _, err := Load(); err != nil {
		t.Fatalf("config.json after concurrent saves: %v", err)
	}
}

// Edits made at once by several writers (the tray, the service, the
// command line) all stay: each reads what the last one wrote.
func TestEditsDontLoseEachOther(t *testing.T) {
	withConfigDir(t)
	var wg sync.WaitGroup
	const n = 12
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := Edit(func(c *Config) (bool, error) {
				c.Folders = append(c.Folders, Folder{ID: NewID(), Path: "/f"})
				time.Sleep(2 * time.Millisecond)

				return true, nil
			})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	cfg, err := Load()
	if err != nil || len(cfg.Folders) != n {
		t.Fatalf("%d of %d folders kept (%v)", len(cfg.Folders), n, err)
	}
}

// Reset Settings… puts the unreadable file aside, renamed, and starts over.
func TestResetKeepsTheFile(t *testing.T) {
	withConfigDir(t)
	p, _ := path("config.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	aside, err := Reset(time.Date(2026, 10, 27, 15, 4, 5, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(aside); err != nil || string(raw) != "{not json" {
		t.Fatalf("not kept: %v", err)
	}
	if cfg, err := Load(); err != nil || cfg.Domain != "" {
		t.Fatalf("after the reset: %+v %v", cfg, err)
	}
}
