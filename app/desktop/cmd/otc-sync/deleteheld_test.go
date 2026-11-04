// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
)

func deleteHeldCmd(t *testing.T, input string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runDeleteHeld(args, strings.NewReader(input), &out)

	return out.String(), err
}

// `otc-sync delete-held` asks with the count the running sync saw, and a
// yes - typed, or --yes - records that set's key for the sync; anything
// else records nothing. Nothing held, a backup, an unknown folder and a
// sync that isn't running are said so.
func TestDeleteHeldCommand(t *testing.T) {
	withFakes(t, &fakeLoginItems{})
	cfg := &config.Config{Domain: "cala.off-the.cloud",
		Folders:       []config.Folder{{ID: "b1", Path: "/home/ana/Scans", OneWay: true}},
		RemoteFolders: []config.RemoteFolder{{ID: "r1", RemotePath: "/linux/pc/home/ana/Pics", LocalPath: "/home/ana/Pics"}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := deleteHeldCmd(t, "", "r1", "--yes"); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("no sync running: %v", err)
	}
	key := strings.Repeat("ab", 32)
	st := &config.State{Status: "Connected", RemoteFolders: []config.FolderStatus{{ID: "r1", Path: "/home/ana/Pics",
		RemotePath: "/linux/pc/home/ana/Pics", State: "error", DeletesHeld: 25, DeletesHeldKey: key}}}
	if err := config.SaveState(st); err != nil {
		t.Fatal(err)
	}

	out, err := deleteHeldCmd(t, "no\n", "r1")
	if err == nil || !strings.Contains(out, "Delete 25 files on the device too?") || !strings.Contains(out, "/home/ana/Pics") {
		t.Fatalf("not asked, or not cancelled: %q %v", out, err)
	}
	if got, _ := config.Load(); got.DeleteHeldRequest("r1") != "" {
		t.Fatal("recorded without a yes")
	}
	if _, err := deleteHeldCmd(t, "yes\n", "/home/ana/Pics"); err != nil {
		t.Fatal(err)
	}
	if got, _ := config.Load(); got.DeleteHeldRequest("r1") != key {
		t.Fatalf("recorded %q", got.DeleteHeldRequest("r1"))
	}
	if !strings.Contains(deleteHeldLabel(st.RemoteFolders[0]), "otc-sync delete-held r1") || deleteHeldLabel(config.FolderStatus{ID: "r1"}) != "" {
		t.Fatal("the folders' label")
	}

	st.RemoteFolders[0].DeletesHeld, st.RemoteFolders[0].DeletesHeldKey = 0, ""
	if err := config.SaveState(st); err != nil {
		t.Fatal(err)
	}
	if out, err := deleteHeldCmd(t, "", "r1", "--yes"); err != nil || !strings.Contains(out, "Nothing deleted from") {
		t.Fatalf("nothing held: %q %v", out, err)
	}
	if _, err := deleteHeldCmd(t, "", "b1"); err == nil || !strings.Contains(err.Error(), "backup") {
		t.Fatalf("a backup: %v", err)
	}
	if _, err := deleteHeldCmd(t, "", "nope", "--yes"); err == nil || !strings.Contains(err.Error(), "no two-way folder") {
		t.Fatalf("unknown folder: %v", err)
	}
	if _, err := deleteHeldCmd(t, ""); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("no folder: %v", err)
	}
}
