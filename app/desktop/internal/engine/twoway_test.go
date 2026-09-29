// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
)

func withConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", home)
}

// Upload-only folders from older versions become two-way on the same
// device path and id; config.json is saved that way.
func TestMigrateFoldersToTwoWay(t *testing.T) {
	withConfigDir(t)
	cfg := &config.Config{Folders: []config.Folder{{ID: "f1", Path: "/home/ana/Docs"}}}
	e := New(cfg, "", nil)
	if len(cfg.Folders) != 0 || len(cfg.RemoteFolders) != 1 {
		t.Fatalf("folders %v remote %v", cfg.Folders, cfg.RemoteFolders)
	}
	r := cfg.RemoteFolders[0]
	if r.ID != "f1" || r.LocalPath != "/home/ana/Docs" || r.RemotePath != e.remotePathFor("/home/ana/Docs") {
		t.Fatalf("migrated to %+v", r)
	}
	saved, err := config.Load()
	if err != nil || len(saved.Folders) != 0 || len(saved.RemoteFolders) != 1 {
		t.Fatalf("config.json not saved migrated: %+v %v", saved, err)
	}
	// Running it again changes nothing.
	e.migrateFolders(cfg)
	if len(cfg.RemoteFolders) != 1 {
		t.Fatalf("migrated twice: %v", cfg.RemoteFolders)
	}
}

// One-way backups stay one-way; only the other folders become two-way.
func TestMigrateKeepsBackups(t *testing.T) {
	withConfigDir(t)
	cfg := &config.Config{Folders: []config.Folder{
		{ID: "b1", Path: "/home/ana/Photos", OneWay: true},
		{ID: "f1", Path: "/home/ana/Docs"},
	}}
	New(cfg, "", nil)
	if len(cfg.Folders) != 1 || cfg.Folders[0].ID != "b1" || !cfg.Folders[0].OneWay {
		t.Fatalf("backups after migration: %+v", cfg.Folders)
	}
	if len(cfg.RemoteFolders) != 1 || cfg.RemoteFolders[0].ID != "f1" {
		t.Fatalf("two-way after migration: %+v", cfg.RemoteFolders)
	}
	saved, err := config.Load()
	if err != nil || len(saved.Folders) != 1 || !saved.Folders[0].OneWay {
		t.Fatalf("config.json: %+v %v", saved, err)
	}
}

// The sync record survives a restart, and goes with its folder.
func TestSyncedRecordPersists(t *testing.T) {
	withConfigDir(t)
	e := New(&config.Config{}, "", nil)
	e.saveSynced("r1", map[string]string{"a.txt": "h1"})

	e2 := New(&config.Config{}, "", nil)
	e2.mu.Lock()
	e2.loadSyncedLocked("r1")
	got := e2.lastSynced["r1"]
	e2.dropSyncedLocked("r1")
	e2.mu.Unlock()
	if got["a.txt"] != "h1" {
		t.Fatalf("record after restart: %v", got)
	}
	p, _ := syncedPath("r1")
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("record still on disk after drop: %v", err)
	}
}
