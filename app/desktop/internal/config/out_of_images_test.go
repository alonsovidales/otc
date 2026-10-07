// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withConfigDir(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", home)
}

func writeConfigJSON(t *testing.T, raw string) {
	t.Helper()
	p, err := ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readConfigJSON(t *testing.T) string {
	t.Helper()
	p, err := ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// A config.json written before issue #192 loads with nothing to send, and
// saves the same as before: no out_of_images key appears.
func TestOldConfigWithoutOutOfImages(t *testing.T) {
	withConfigDir(t)
	writeConfigJSON(t, `{
  "domain": "cala.off-the.cloud",
  "client_id": "abc",
  "folders": [{"id": "b1", "path": "/home/ana/Photos", "one_way": true}],
  "remote_folders": [{"id": "r1", "remote_path": "/Docs", "local_path": "/home/ana/Docs"}],
  "autostart": false
}`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Folders[0].OutOfImages != nil || cfg.RemoteFolders[0].OutOfImages != nil {
		t.Fatalf("a request out of nowhere: %+v %+v", cfg.Folders[0], cfg.RemoteFolders[0])
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if raw := readConfigJSON(t); strings.Contains(raw, "out_of_images") {
		t.Fatalf("saved with the new key:\n%s", raw)
	}
	again, err := Load()
	if err != nil || again.Domain != cfg.Domain || len(again.Folders) != 1 || !again.Folders[0].OneWay || len(again.RemoteFolders) != 1 || again.AutostartEnabled() {
		t.Fatalf("round trip changed it: %+v %v", again, err)
	}
}

// Both values survive a save: false (show the folder again) is a request
// too, not "nothing to send".
func TestOutOfImagesRoundTrip(t *testing.T) {
	withConfigDir(t)
	cfg := &Config{Domain: "cala.off-the.cloud", Folders: []Folder{{ID: "b1", Path: "/p", OneWay: true}}, RemoteFolders: []RemoteFolder{{ID: "r1", RemotePath: "/Docs", LocalPath: "/d"}}}
	if !cfg.SetOutOfImagesRequest("b1", true) || !cfg.SetOutOfImagesRequest("r1", false) || cfg.SetOutOfImagesRequest("nope", true) {
		t.Fatal("SetOutOfImagesRequest")
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if b := got.Folders[0].OutOfImages; b == nil || !*b {
		t.Fatalf("backup's request: %v", b)
	}
	if r := got.RemoteFolders[0].OutOfImages; r == nil || *r {
		t.Fatalf("two-way folder's request: %v", r)
	}

	// Cleared only while it is still the value that was sent.
	if got.ClearOutOfImagesRequest("b1", false) || got.Folders[0].OutOfImages == nil {
		t.Fatal("cleared a request other than the one sent")
	}
	if !got.ClearOutOfImagesRequest("b1", true) || got.Folders[0].OutOfImages != nil {
		t.Fatal("not cleared")
	}
	if got.ClearOutOfImagesRequest("b1", true) {
		t.Fatal("cleared twice")
	}
	if !got.ClearOutOfImagesRequest("r1", false) || got.RemoteFolders[0].OutOfImages != nil {
		t.Fatal("two-way folder's not cleared")
	}
}

// state.json from an engine before issue #192 reads with nothing known
// about Images; the new fields are left out when empty.
func TestStateOutOfImagesFields(t *testing.T) {
	var st State
	if err := json.Unmarshal([]byte(`{"status":"Connected","raid":"ok","folders":[{"id":"b1","path":"/p","state":"watching"}]}`), &st); err != nil {
		t.Fatal(err)
	}
	if st.OutOfImagesUnsupported || st.Folders[0].OutOfImages != "" {
		t.Fatalf("%+v", st)
	}
	raw, _ := json.Marshal(st)
	if strings.Contains(string(raw), "out_of_images") {
		t.Fatalf("empty fields written: %s", raw)
	}
	st.Folders[0].OutOfImages, st.Folders[0].OutOfImagesBy = "by_parent", "/Photos"
	raw, _ = json.Marshal(st)
	var back State
	if err := json.Unmarshal(raw, &back); err != nil || back.Folders[0].OutOfImagesBy != "/Photos" {
		t.Fatalf("%s: %+v %v", raw, back, err)
	}
}
