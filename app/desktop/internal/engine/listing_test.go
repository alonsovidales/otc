// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// twoWayDevice is a fake device that lists and serves files (path ->
// content) under a two-way folder.
func twoWayDevice(files map[string][]byte) *fakeDevice {
	d := &fakeDevice{files: map[string][]byte{}, pending: map[string]*bytes.Buffer{}, paths: map[string]string{}}
	for p, b := range files {
		d.files[p] = b
		d.list = append(d.list, &pb.File{Path: p, Hash: sha(b), Size: int32(len(b))})
	}
	return d
}

// A device listing is data: entries that climb out of the folder, or are
// not under it at all, are never written here (nor deleted from the
// device on the next pass).
func TestTwoWayIgnoresPathsOutsideTheFolder(t *testing.T) {
	withConfigDir(t)
	top := t.TempDir()
	local := filepath.Join(top, "sync", "folder")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	d := twoWayDevice(map[string][]byte{
		"/r/a.txt":             []byte("fine"),
		"/r/../../escape.txt":  []byte("up two"),
		"/r/sub/../../out.txt": []byte("up one"),
		"/r/":                  []byte("the root itself"),
		"/r//double.txt":       []byte("empty component"),
		"/elsewhere/x.txt":     []byte("not under the folder"),
	})
	conn := connectedEngine(t, d)
	f := config.RemoteFolder{ID: "r1", RemotePath: "/r", LocalPath: local}
	e := New(&config.Config{RemoteFolders: []config.RemoteFolder{f}}, "", nil)
	e.ws = conn.ws

	for pass := 0; pass < 2; pass++ {
		e.reconcileRemoteFolder(f)
	}

	if got, _ := os.ReadFile(filepath.Join(local, "a.txt")); string(got) != "fine" {
		t.Fatalf("the ordinary file was not downloaded: %q", got)
	}
	for _, p := range []string{filepath.Join(top, "escape.txt"), filepath.Join(top, "sync", "out.txt"), filepath.Join(local, "double.txt"), filepath.Join(local, "x.txt")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s was written (%v)", p, err)
		}
	}
	if len(d.deletes) != 0 {
		t.Errorf("deleted from the device: %v", d.deletes)
	}
}

func TestSafeRelative(t *testing.T) {
	for rel, want := range map[string]bool{
		"a.txt": true, "sub/a.txt": true, "a b/c.d": true,
		"": false, "..": false, "../a": false, "a/../../b": false, "a/./b": false,
		"a//b": false, "/abs": false, "a/": false, "a\x00b": false,
	} {
		if got := safeRelative(rel); got != want {
			t.Errorf("safeRelative(%q) = %v, want %v", rel, got, want)
		}
	}
}
