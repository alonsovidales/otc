// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// A config.json from before the add flow asked for upload only loads with
// nothing to send and saves without the new key; one written by an older
// version with a request for a folder synced from the device keeps it,
// so it is still sent once.
func TestUploadOnlyRequestCompatible(t *testing.T) {
	withConfigDir(t)
	writeConfigJSON(t, `{
  "domain": "cala.off-the.cloud",
  "client_id": "abc",
  "folders": [{"id": "b1", "path": "/home/ana/Photos", "one_way": true}],
  "remote_folders": [{"id": "r1", "remote_path": "/Docs", "local_path": "/home/ana/Docs", "out_of_images": true}]
}`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Folders[0].UploadOnly != nil || cfg.RemoteFolders[0].UploadOnly != nil {
		t.Fatalf("a request out of nowhere: %+v %+v", cfg.Folders[0], cfg.RemoteFolders[0])
	}
	if want, ok := cfg.PendingRequest(OutOfImagesRequest, "r1"); !ok || want == nil || !*want {
		t.Fatalf("the stored Images request: %v %v", want, ok)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if raw := readConfigJSON(t); strings.Contains(raw, "upload_only") {
		t.Fatalf("saved with the new key:\n%s", raw)
	}
}

// The two kinds of request are kept apart: setting or clearing one leaves
// the other alone, and a clear only takes the value that was sent.
func TestRequestsKeptApart(t *testing.T) {
	withConfigDir(t)
	cfg := &Config{Folders: []Folder{{ID: "f1", Path: "/p"}}, RemoteFolders: []RemoteFolder{{ID: "r1", RemotePath: "/Docs", LocalPath: "/d"}}}
	if !cfg.SetRequest(UploadOnlyRequest, "f1", true) || !cfg.SetRequest(OutOfImagesRequest, "f1", true) ||
		!cfg.SetRequest(UploadOnlyRequest, "r1", true) || cfg.SetRequest(UploadOnlyRequest, "nope", true) {
		t.Fatal("SetRequest")
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if u := got.Folders[0].UploadOnly; u == nil || !*u {
		t.Fatalf("folder's upload only: %v", u)
	}
	if u := got.RemoteFolders[0].UploadOnly; u == nil || !*u || got.RemoteFolders[0].OutOfImages != nil {
		t.Fatalf("two-way folder's requests: %+v", got.RemoteFolders[0])
	}
	if got.ClearRequest(UploadOnlyRequest, "f1", false) {
		t.Fatal("cleared a request other than the one sent")
	}
	if !got.ClearRequest(UploadOnlyRequest, "f1", true) || got.Folders[0].UploadOnly != nil || got.Folders[0].OutOfImages == nil {
		t.Fatalf("clearing upload only: %+v", got.Folders[0])
	}
	if want, ok := got.PendingRequest(UploadOnlyRequest, "f1"); !ok || want != nil {
		t.Fatalf("still pending: %v %v", want, ok)
	}
	if _, ok := got.PendingRequest(UploadOnlyRequest, "nope"); ok {
		t.Fatal("a folder that isn't there")
	}
}

// state.json says nothing about upload only while nothing is on its way.
func TestStateUploadOnlyFields(t *testing.T) {
	st := State{Status: "Connected", Folders: []FolderStatus{{ID: "r1", Path: "/d", State: "watching"}}}
	raw, _ := json.Marshal(st)
	if strings.Contains(string(raw), "upload_only") {
		t.Fatalf("empty fields written: %s", raw)
	}
	st.Folders[0].UploadOnly, st.UploadOnlyUnsupported = "unsupported", true
	raw, _ = json.Marshal(st)
	var back State
	if err := json.Unmarshal(raw, &back); err != nil || back.Folders[0].UploadOnly != "unsupported" || !back.UploadOnlyUnsupported {
		t.Fatalf("%s: %+v %v", raw, back, err)
	}
}
