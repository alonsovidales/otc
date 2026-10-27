// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
)

// withConfigDir points the config directory at a temporary one.
func withConfigDir(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", home)
}

func TestAddFlags(t *testing.T) {
	rest, out, up, seen := addFlags([]string{"--upload-only", "docs", "-keep-out-of-images"})
	if !slices.Equal(rest, []string{"docs"}) || out == nil || !*out || up == nil || !*up || len(seen) != 2 {
		t.Fatalf("rest %v, images %v, upload only %v, seen %v", rest, out, up, seen)
	}
	// A folder named like the flag, without its dashes, is a folder.
	rest, out, up, _ = addFlags([]string{"upload-only"})
	if !slices.Equal(rest, []string{"upload-only"}) || out != nil || up != nil {
		t.Fatalf("rest %v, images %v, upload only %v", rest, out, up)
	}
}

// add --upload-only records the request with the folder; a backup is
// always upload only, so nothing is recorded for it; add-remote refuses
// both options and adds nothing.
func TestAddOptions(t *testing.T) {
	withConfigDir(t)
	docs, scans := t.TempDir(), t.TempDir()
	if err := cmdAdd([]string{"--upload-only", docs}, false); err != nil {
		t.Fatal(err)
	}
	if err := cmdAdd([]string{scans, "--upload-only", "--keep-out-of-images"}, true); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--upload-only", "--keep-out-of-images"} {
		err := cmdAddRemote([]string{flag, "/Photos", filepath.Join(t.TempDir(), "p")})
		if err == nil || !strings.Contains(err.Error(), "add-remote takes no "+flag) {
			t.Fatalf("add-remote %s: %v", flag, err)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Folders) != 2 || len(cfg.RemoteFolders) != 0 {
		t.Fatalf("folders %+v, remote %+v", cfg.Folders, cfg.RemoteFolders)
	}
	if f := cfg.Folders[0]; f.OneWay || f.UploadOnly == nil || !*f.UploadOnly || f.OutOfImages != nil {
		t.Fatalf("add --upload-only: %+v", f)
	}
	if f := cfg.Folders[1]; !f.OneWay || f.UploadOnly != nil || f.OutOfImages == nil {
		t.Fatalf("backup: %+v", f)
	}
}

func TestUploadOnlyLabels(t *testing.T) {
	on := true
	if got := pendingUploadOnlyLabel(&on); got != " (making it upload only…)" {
		t.Fatalf("pending: %q", got)
	}
	if got := pendingUploadOnlyLabel(nil); got != "" {
		t.Fatalf("none: %q", got)
	}
	if got := uploadOnlyLabel(config.FolderStatus{UploadOnly: "unsupported"}); !strings.Contains(got, "needs an update") {
		t.Fatalf("unsupported: %q", got)
	}
}

// Another device while folders are synced is refused - the folders
// would start syncing with, or deleting on, the other one; Disconnect
// first. The same device spelled another way, or a password alone, is
// fine. An unencrypted address for a device elsewhere needs
// --allow-unencrypted.
func TestCheckNewDevice(t *testing.T) {
	withFolders := &config.Config{Domain: "cala.off-the.cloud", RemoteFolders: []config.RemoteFolder{{ID: "r1", RemotePath: "/x", LocalPath: "/home/a/x"}}}
	for _, tc := range []struct {
		cfg      *config.Config
		domain   string
		insecure bool
		ok       bool
	}{
		{withFolders, "pit.off-the.cloud", false, false},
		{withFolders, "wss://cala.off-the.cloud/ws", false, true},
		{withFolders, "Cala.off-the.cloud", false, true},
		{&config.Config{Domain: "cala.off-the.cloud"}, "pit.off-the.cloud", false, true},
		{&config.Config{}, "ws://203.0.113.7:8080/ws", false, false},
		{&config.Config{}, "ws://203.0.113.7:8080/ws", true, true},
		{&config.Config{}, "ws://192.168.1.10:8080/ws", false, true},
	} {
		err := checkNewDevice(tc.cfg, tc.domain, tc.insecure)
		if (err == nil) != tc.ok {
			t.Errorf("%q from %q (insecure %v): %v", tc.domain, tc.cfg.Domain, tc.insecure, err)
		}
		if err != nil && !tc.insecure && len(tc.cfg.RemoteFolders) > 0 && !strings.Contains(err.Error(), "otc-sync disconnect") {
			t.Errorf("the refusal doesn't say how: %v", err)
		}
	}
}
