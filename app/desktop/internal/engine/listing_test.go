// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"bytes"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// Hidden files and partial downloads on the device are left there: the
// local scan never sees them, so downloading them made the next pass
// delete them from the device.
func TestTwoWayLeavesHiddenDeviceFilesAlone(t *testing.T) {
	withConfigDir(t)
	local := t.TempDir()
	if err := os.WriteFile(filepath.Join(local, ".env"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := twoWayDevice(map[string][]byte{
		"/r/.env":        []byte("theirs"),
		"/r/.git/config": []byte("[core]"),
		"/r/x.otc-part":  []byte("partial"),
		"/r/a.txt":       []byte("visible"),
	})
	conn := connectedEngine(t, d)
	f := config.RemoteFolder{ID: "r1", RemotePath: "/r", LocalPath: local}
	e := New(&config.Config{RemoteFolders: []config.RemoteFolder{f}}, "", nil)
	e.ws = conn.ws

	for pass := 0; pass < 2; pass++ {
		e.reconcileRemoteFolder(f)
	}

	if got, _ := os.ReadFile(filepath.Join(local, "a.txt")); string(got) != "visible" {
		t.Fatalf("a.txt not downloaded: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(local, ".env")); string(got) != "mine" {
		t.Errorf("the local .env was overwritten: %q", got)
	}
	for _, p := range []string{".git", "x.otc-part"} {
		if _, err := os.Stat(filepath.Join(local, p)); !os.IsNotExist(err) {
			t.Errorf("%s was downloaded (%v)", p, err)
		}
	}
	if len(d.deletes) != 0 {
		t.Errorf("deleted from the device: %v", d.deletes)
	}
}

func TestNotSynced(t *testing.T) {
	for rel, want := range map[string]bool{
		"a.txt": false, "sub/a.txt": false, ".env": true, "sub/.git/config": true,
		"x.otc-part": true, "sub/y.jpg.otc-part": true, "a.b/c": false,
	} {
		if got := notSynced(rel); got != want {
			t.Errorf("notSynced(%q) = %v, want %v", rel, got, want)
		}
	}
}

// A subdirectory that can't be read is not a deleted one: nothing under
// it is deleted from the device, its record is kept, and the folder says
// it could not all be read.
func TestTwoWayUnreadableDirectoryDeletesNothing(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory this user can't read")
	}
	withConfigDir(t)
	local := t.TempDir()
	sub := filepath.Join(local, "sub")
	files := map[string][]byte{"/r/sub/a.txt": []byte("a"), "/r/sub/deeper/b.txt": []byte("b"), "/r/c.txt": []byte("c")}
	record := map[string]string{}
	for p, b := range files {
		rel := strings.TrimPrefix(p, "/r/")
		full := filepath.Join(local, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, b, 0o644); err != nil {
			t.Fatal(err)
		}
		record[rel] = sha(b)
	}
	d := twoWayDevice(files)
	conn := connectedEngine(t, d)
	f := config.RemoteFolder{ID: "r1", RemotePath: "/r", LocalPath: local}
	e := New(&config.Config{RemoteFolders: []config.RemoteFolder{f}}, "", nil)
	e.ws = conn.ws
	e.saveSynced(f.ID, record)

	if err := os.Chmod(sub, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })
	e.reconcileRemoteFolder(f)

	if len(d.deletes) != 0 {
		t.Fatalf("deleted from the device: %v", d.deletes)
	}
	e.mu.Lock()
	kept := maps.Clone(e.lastSynced[f.ID])
	st := e.remoteStates[f.ID]
	e.mu.Unlock()
	if !maps.Equal(kept, record) {
		t.Errorf("record after the pass: %v, want %v", kept, record)
	}
	if st.Kind != StateError || !strings.Contains(st.Message, "could not be read") {
		t.Errorf("state %+v, want an error saying what could not be read", st)
	}
}
