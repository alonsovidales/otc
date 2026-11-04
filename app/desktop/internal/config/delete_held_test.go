// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// "Delete Them on the Device Too…" is kept with a two-way folder until the
// engine has acted on it: set, read back after a save, and cleared only
// while it is still the key acted on - not one the tray or the command
// line made meanwhile. A backup holds none (its deletes are never held).
func TestDeleteHeldRequest(t *testing.T) {
	withConfigDir(t)
	cfg := &Config{Folders: []Folder{{ID: "b1", Path: "/p", OneWay: true}}, RemoteFolders: []RemoteFolder{{ID: "r1", RemotePath: "/Docs", LocalPath: "/d"}}}
	if cfg.DeleteHeldRequest("r1") != "" {
		t.Fatal("a request out of nowhere")
	}
	if !cfg.SetDeleteHeldRequest("r1", "k1") || cfg.SetDeleteHeldRequest("b1", "k1") || cfg.SetDeleteHeldRequest("nope", "k1") {
		t.Fatal("SetDeleteHeldRequest")
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.DeleteHeldRequest("r1") != "k1" || got.DeleteHeldRequest("b1") != "" {
		t.Fatalf("after a save: %+v", got.RemoteFolders)
	}
	if got.ClearDeleteHeldRequest("r1", "k0") || got.DeleteHeldRequest("r1") != "k1" {
		t.Fatal("cleared another key than the one acted on")
	}
	if got.ClearDeleteHeldRequest("r1", "") {
		t.Fatal("an empty key cleared it")
	}
	if !got.ClearDeleteHeldRequest("r1", "k1") || got.DeleteHeldRequest("r1") != "" {
		t.Fatal("not cleared")
	}
	if got.ClearDeleteHeldRequest("r1", "k1") || got.ClearDeleteHeldRequest("nope", "k1") {
		t.Fatal("cleared twice, or a folder that isn't there")
	}
	// Its other requests are left as they are.
	on := true
	got.RemoteFolders[0].UploadOnly = &on
	got.SetDeleteHeldRequest("r1", "k2")
	got.ClearDeleteHeldRequest("r1", "k2")
	if want, _ := got.PendingRequest(UploadOnlyRequest, "r1"); want == nil || !*want {
		t.Fatal("upload only went with it")
	}
}

// A config.json from before it loads with nothing asked, and saves
// without the key while nothing is.
func TestDeleteHeldRequestCompatible(t *testing.T) {
	withConfigDir(t)
	writeConfigJSON(t, `{
  "domain": "cala.off-the.cloud",
  "client_id": "abc",
  "folders": [],
  "remote_folders": [{"id": "r1", "remote_path": "/Docs", "local_path": "/home/ana/Docs"}]
}`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DeleteHeldRequest("r1") != "" {
		t.Fatalf("a request out of nowhere: %+v", cfg.RemoteFolders[0])
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if raw := readConfigJSON(t); strings.Contains(raw, "delete_held") {
		t.Fatalf("saved with the new key:\n%s", raw)
	}
}

// state.json says nothing about held deletes while none is held, and
// carries the count and the key when some are.
func TestStateDeletesHeldFields(t *testing.T) {
	st := State{Status: "Connected", RemoteFolders: []FolderStatus{{ID: "r1", Path: "/d", RemotePath: "/Docs", State: "watching"}}}
	raw, _ := json.Marshal(st)
	if strings.Contains(string(raw), "deletes_held") {
		t.Fatalf("empty fields written: %s", raw)
	}
	st.RemoteFolders[0].DeletesHeld, st.RemoteFolders[0].DeletesHeldKey = 25, strings.Repeat("a", 64)
	raw, _ = json.Marshal(st)
	var back State
	if err := json.Unmarshal(raw, &back); err != nil || back.RemoteFolders[0].DeletesHeld != 25 || back.RemoteFolders[0].DeletesHeldKey != st.RemoteFolders[0].DeletesHeldKey {
		t.Fatalf("%s: %+v %v", raw, back, err)
	}
}
