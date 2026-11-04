// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
)

// saveSynced seeds folder id's record as a pass of this version leaves it:
// made with the device configured now and, for a folder that is there, with
// the folder marked as the one synced (rootid.go).
func (e *Engine) saveSynced(folderID string, synced map[string]string) {
	e.mu.Lock()
	domain := e.cfg.Domain
	e.loadSyncedLocked(folderID)
	meta := e.syncedFiles[folderID].meta
	root := ""
	for _, f := range e.cfg.RemoteFolders {
		if f.ID == folderID {
			root = f.LocalPath
		}
	}
	e.mu.Unlock()
	if fi, err := os.Stat(root); meta.Root == "" && root != "" && err == nil && fi.IsDir() {
		meta.Root = newMarkerToken()
		if err := writeMarker(root, folderID, meta.Root); err != nil {
			panic(err)
		}
	}
	e.saveSyncedFor(folderID, e.deviceFor(domain), meta, synced)
}

// markedMeta marks f's folder as the one synced, for a record a test
// writes itself.
func markedMeta(t *testing.T, f config.RemoteFolder) folderMeta {
	t.Helper()
	tok := newMarkerToken()
	if err := writeMarker(f.LocalPath, f.ID, tok); err != nil {
		t.Fatal(err)
	}

	return folderMeta{Root: tok}
}

func remoteState(e *Engine, id string) FolderState {
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.remoteStates[id]
}

// A two-way folder whose disk (or share) isn't mounted is an empty folder
// at the same path. It used to read as "every file deleted here", and every
// file was deleted on the device. Now it waits, and once the disk is back it
// carries on with nothing lost.
func TestEmptyMountPointDeletesNothing(t *testing.T) {
	withConfigDir(t)
	files := map[string][]byte{}
	for i := 0; i < 100; i++ {
		files[fmt.Sprintf("/linux/pc/mnt/nas/p%d.jpg", i)] = []byte(fmt.Sprintf("photo %d", i))
	}
	d := storedDevice(files)
	local := t.TempDir()
	f := config.RemoteFolder{ID: "r1", RemotePath: "/linux/pc/mnt/nas", LocalPath: local}
	e := twoWayEngine(t, d, "cala.off-the.cloud", f)
	e.reconcileRemoteFolder(f)
	if n := countFiles(t, local); n != 100 {
		t.Fatalf("first pass: %d files here", n)
	}
	if readMarker(local, f.ID) == "" {
		t.Fatal("the folder was not marked as the one synced")
	}
	// Unmounted: what is left at the path is the empty mount point.
	mounted := filepath.Join(t.TempDir(), "disk")
	if err := os.Rename(local, mounted); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(local, 0o755); err != nil {
		t.Fatal(err)
	}
	e.reconcileRemoteFolder(f)
	got, deletes, _ := d.snapshot()
	if len(deletes) != 0 || len(got) != 100 {
		t.Fatalf("%d deletes sent, %d files left on the device", len(deletes), len(got))
	}
	if st := remoteState(e, f.ID); st.Kind != StateError || st.Msg == nil || st.Msg.Key != "desk.main.folder_replaced" {
		t.Fatalf("state %+v", st)
	}
	if n := countFiles(t, local); n != 0 || readMarker(local, f.ID) != "" {
		t.Fatalf("the empty mount point was written to: %d files", n)
	}
	// Mounted again: in sync, nothing sent or fetched.
	if err := os.Remove(local); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(mounted, local); err != nil {
		t.Fatal(err)
	}
	_, _, reads := d.snapshot()
	e.reconcileRemoteFolder(f)
	got, deletes, reads2 := d.snapshot()
	if st := remoteState(e, f.ID); st.Kind != StateWatching || len(deletes) != 0 || len(got) != 100 || reads2 != reads {
		t.Fatalf("back: state %+v, %d deletes, %d files on the device, %d reads", st, len(deletes), len(got), reads2-reads)
	}
	e.mu.Lock()
	watched := e.remoteWatch[f.ID] != nil
	e.mu.Unlock()
	if !watched {
		t.Fatal("not watched again once back")
	}
}

// A folder made afresh where the synced one was (renamed away, another disk
// with a folder of the same name) is not the synced one: its files don't go
// up, the device's don't come down, nothing is deleted.
func TestAnotherFolderAtThePathWaits(t *testing.T) {
	withConfigDir(t)
	d := storedDevice(map[string][]byte{"/pc/docs/a.txt": []byte("a"), "/pc/docs/b.txt": []byte("b")})
	local := t.TempDir()
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/docs", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)
	e.reconcileRemoteFolder(f)
	aside := filepath.Join(t.TempDir(), "docs 2025")
	if err := os.Rename(local, aside); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, local, map[string]string{"other.txt": "another folder's"})
	e.reconcileRemoteFolder(f)
	got, deletes, _ := d.snapshot()
	if len(deletes) != 0 || len(got) != 2 || countFiles(t, local) != 1 {
		t.Fatalf("acted on another folder: deletes %v, device %v, %d here", deletes, keysOf(got), countFiles(t, local))
	}
	if st := remoteState(e, f.ID); st.Msg == nil || st.Msg.Key != "desk.main.folder_replaced" {
		t.Fatalf("state %+v", st)
	}
}

// A record from before the marker: a folder that still holds what it lists
// is marked and synced as before; one that holds none of it waits.
func TestOlderRecordIsMarkedWhenTheFolderMatches(t *testing.T) {
	for _, tc := range []struct {
		name  string
		here  map[string]string
		ok    bool
		marks bool
	}{
		{"the files are here", map[string]string{"a.txt": "a", "b.txt": "b", "c.txt": "c"}, true, true},
		{"one of three is gone", map[string]string{"a.txt": "a", "b.txt": "b"}, true, true},
		{"an empty folder", nil, false, false},
		{"other files", map[string]string{"x.txt": "x", "y.txt": "y"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withConfigDir(t)
			d := storedDevice(map[string][]byte{"/pc/docs/a.txt": []byte("a"), "/pc/docs/b.txt": []byte("b"), "/pc/docs/c.txt": []byte("c")})
			local := t.TempDir()
			writeFiles(t, local, tc.here)
			f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/docs", LocalPath: local}
			e := twoWayEngine(t, d, "dev", f)
			// An older version's record: the bare map, no marker.
			p, _ := syncedPath(f.ID)
			rec := fmt.Sprintf(`{"a.txt": %q, "b.txt": %q, "c.txt": %q}`, sha([]byte("a")), sha([]byte("b")), sha([]byte("c")))
			if err := os.WriteFile(p, []byte(rec), 0o600); err != nil {
				t.Fatal(err)
			}
			e.reconcileRemoteFolder(f)
			_, deletes, _ := d.snapshot()
			marker := readMarker(local, f.ID)
			if (marker != "") != tc.marks {
				t.Fatalf("marker %q", marker)
			}
			if !tc.ok {
				if len(deletes) != 0 || remoteState(e, f.ID).Kind != StateError {
					t.Fatalf("a folder that isn't the synced one: deletes %v, state %+v", deletes, remoteState(e, f.ID))
				}

				return
			}
			data, _ := os.ReadFile(p)
			if _, _, meta, ok := parseRecordMeta(data); !ok || meta.Root != marker {
				t.Fatalf("record %s, marker %q", data, marker)
			}
			// c.txt was deleted here: its delete goes, as before.
			if want := 3 - len(tc.here); len(deletes) != want {
				t.Fatalf("deletes %v, want %d", deletes, want)
			}
		})
	}
}

// The marker deleted (a cleanup of hidden files) in a folder that is
// otherwise the same: written again, and the sync goes on.
func TestMarkerDeletedInTheSameFolder(t *testing.T) {
	withConfigDir(t)
	d := storedDevice(map[string][]byte{"/pc/docs/a.txt": []byte("a"), "/pc/docs/b.txt": []byte("b")})
	local := t.TempDir()
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/docs", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)
	e.reconcileRemoteFolder(f)
	tok := readMarker(local, f.ID)
	removeMarker(local, f.ID)
	writeFiles(t, local, map[string]string{"new.txt": "new"})
	e.reconcileRemoteFolder(f)
	got, _, _ := d.snapshot()
	if readMarker(local, f.ID) != tok || got["/pc/docs/new.txt"] == nil {
		t.Fatalf("marker %q (was %q), device %v", readMarker(local, f.ID), tok, keysOf(got))
	}
	if st := remoteState(e, f.ID); st.Kind != StateWatching {
		t.Fatalf("state %+v", st)
	}
}

// Files deleted here in numbers are not deleted on the device - more than
// 20 and a quarter of the folder - while a folder renamed here (its files
// here under new names) and a handful of deletes go as before.
func TestMassDeleteHereIsHeld(t *testing.T) {
	withConfigDir(t)
	files := map[string][]byte{}
	for i := 0; i < 60; i++ {
		files[fmt.Sprintf("/pc/pics/p%02d.jpg", i)] = []byte(fmt.Sprintf("photo %d", i))
	}
	d := storedDevice(files)
	local := t.TempDir()
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/pics", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)
	e.reconcileRemoteFolder(f)
	for i := 0; i < 25; i++ {
		if err := os.Remove(filepath.Join(local, fmt.Sprintf("p%02d.jpg", i))); err != nil {
			t.Fatal(err)
		}
	}
	e.reconcileRemoteFolder(f)
	got, deletes, _ := d.snapshot()
	if len(deletes) != 0 || len(got) != 60 {
		t.Fatalf("%d deletes sent, %d files on the device", len(deletes), len(got))
	}
	st := remoteState(e, f.ID)
	if st.Msg == nil || st.Msg.Key != "desk.main.deletes_held" || !strings.Contains(st.Message, "25 files") {
		t.Fatalf("state %+v", st)
	}
	e.mu.Lock()
	kept := len(e.lastSynced[f.ID])
	e.mu.Unlock()
	if kept != 60 {
		t.Fatalf("record lists %d files, want the 60 kept", kept)
	}
	// Deleted on the device too (from the web): gone everywhere, at rest.
	d.mu.Lock()
	for i := 0; i < 25; i++ {
		delete(d.files, fmt.Sprintf("/pc/pics/p%02d.jpg", i))
	}
	d.mu.Unlock()
	e.reconcileRemoteFolder(f)
	if st := remoteState(e, f.ID); st.Kind != StateWatching || countFiles(t, local) != 35 {
		t.Fatalf("after the deletes on the device: state %+v, %d here", st, countFiles(t, local))
	}
	// A rename of 30 files here is no loss: the old names go.
	if err := os.Mkdir(filepath.Join(local, "2024"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 25; i < 55; i++ {
		name := fmt.Sprintf("p%02d.jpg", i)
		if err := os.Rename(filepath.Join(local, name), filepath.Join(local, "2024", name)); err != nil {
			t.Fatal(err)
		}
	}
	e.reconcileRemoteFolder(f)
	got, deletes, _ = d.snapshot()
	if len(deletes) != 30 || len(got) != 35 || got["/pc/pics/2024/p30.jpg"] == nil {
		t.Fatalf("after a rename here: %d deletes, device %d files", len(deletes), len(got))
	}
	// And a few deletes go.
	for i := 55; i < 60; i++ {
		_ = os.Remove(filepath.Join(local, fmt.Sprintf("p%02d.jpg", i)))
	}
	e.reconcileRemoteFolder(f)
	if _, deletes, _ = d.snapshot(); len(deletes) != 35 {
		t.Fatalf("a handful of deletes: %d sent in all, want 35", len(deletes))
	}
}

// A disk mounted inside the folder that isn't any more leaves an empty
// folder: what was synced under it stays as it is, on both sides.
func TestGoneMountInsideTheFolderHoldsItsFiles(t *testing.T) {
	withConfigDir(t)
	d := storedDevice(map[string][]byte{"/pc/home/notes.txt": []byte("n"), "/pc/home/nas/a.jpg": []byte("a"), "/pc/home/nas/b.jpg": []byte("b")})
	local := t.TempDir()
	writeFiles(t, local, map[string]string{"notes.txt": "n"})
	if err := os.Mkdir(filepath.Join(local, "nas"), 0o755); err != nil { // the mount point, empty
		t.Fatal(err)
	}
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/home", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)
	rec := map[string]string{"notes.txt": sha([]byte("n")), "nas/a.jpg": sha([]byte("a")), "nas/b.jpg": sha([]byte("b"))}
	meta := markedMeta(t, f)
	meta.Mounts = []string{"nas"} // a file system of its own at the last pass
	e.saveSyncedFor(f.ID, e.deviceFor("dev"), meta, rec)
	e.reconcileRemoteFolder(f)
	got, deletes, reads := d.snapshot()
	if len(deletes) != 0 || len(got) != 3 || reads != 0 || countFiles(t, local) != 1 {
		t.Fatalf("deletes %v, device %d files, %d reads, %d here", deletes, len(got), reads, countFiles(t, local))
	}
	if st := remoteState(e, f.ID); st.Msg == nil || st.Msg.Key != "desk.main.files_unreadable" {
		t.Fatalf("state %+v", st)
	}
	e.mu.Lock()
	mounts := e.syncedFiles[f.ID].meta.Mounts
	e.mu.Unlock()
	if len(mounts) != 1 || mounts[0] != "nas" {
		t.Fatalf("mounts kept %v", mounts)
	}
	// The mount point removed for good: forgotten, and its files' deletes
	// go as any.
	if err := os.Remove(filepath.Join(local, "nas")); err != nil {
		t.Fatal(err)
	}
	e.reconcileRemoteFolder(f)
	if _, deletes, _ := d.snapshot(); len(deletes) != 2 {
		t.Fatalf("deletes after the mount point went: %v", deletes)
	}
}

func TestJudgeRoot(t *testing.T) {
	rec := map[string]string{"a": "1", "b": "2", "c": "3", "d": "kept-on-device:4"}
	for _, tc := range []struct {
		name             string
		recorded, marker string
		here             map[string]string
		ok, mark         bool
	}{
		{"its marker", "t", "t", nil, true, false},
		{"its marker, files gone", "t", "t", map[string]string{}, true, false},
		{"no marker, all here", "", "", map[string]string{"a": "1", "b": "2", "c": "3"}, true, true},
		{"no marker, two of three", "t", "", map[string]string{"a": "1", "b": "2"}, true, true},
		{"no marker, one of three", "t", "", map[string]string{"a": "1"}, false, false},
		{"no marker, changed", "t", "", map[string]string{"a": "9", "b": "9", "c": "9"}, false, false},
		{"another marker, empty", "t", "u", map[string]string{}, false, false},
	} {
		v := judgeRoot(tc.recorded, tc.marker, rec, tc.here, nil)
		if v.ok != tc.ok || v.mark != tc.mark || (v.ok && v.token == "") {
			t.Errorf("%s: %+v", tc.name, v)
		}
		if tc.ok && tc.recorded != "" && v.token != tc.recorded {
			t.Errorf("%s: token %q, want the record's", tc.name, v.token)
		}
	}
	// A record with nothing to lose (empty, or only files the device kept).
	if v := judgeRoot("", "", map[string]string{"d": "kept-on-device:4"}, nil, nil); !v.ok || !v.mark {
		t.Errorf("nothing to lose: %+v", v)
	}
	// What is unknown here counts neither way.
	unknown := func(rel string) bool { return rel != "a" }
	if v := judgeRoot("t", "", rec, map[string]string{"a": "1"}, unknown); !v.ok {
		t.Errorf("with the rest unknown: %+v", v)
	}
}

func TestGoneMounts(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"nas", "usb/disk"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gone, keep := goneMounts(root, []string{"nas", "usb/disk", "removed", "../out"}, []string{"usb/disk", "new"})
	if strings.Join(gone, ",") != "nas" || strings.Join(keep, ",") != "nas,new,usb/disk" {
		t.Fatalf("gone %v keep %v", gone, keep)
	}
	s := newPathSet([]string{"nas"})
	if !s.covers("nas") || !s.covers("nas/a/b.jpg") || s.covers("nasty.txt") || s.covers("x/nas") {
		t.Fatal("pathSet.covers")
	}
}

// A folder removed from otc-sync loses its marker, and the removal never
// holds the engine: an unlink on a share that stopped answering can block
// for good, and the tray, Snapshot and state.json all wait on e.mu.
func TestRemovedFolderLosesItsMarkerOffTheLock(t *testing.T) {
	withConfigDir(t)
	d := storedDevice(map[string][]byte{"/pc/docs/a.txt": []byte("a")})
	local := t.TempDir()
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/docs", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)
	e.reconcileRemoteFolder(f)
	if readMarker(local, f.ID) == "" {
		t.Fatal("not marked")
	}
	real := removeMarker
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() {
		once.Do(func() { close(release) })
		<-done
		removeMarker = real
	})
	removeMarker = func(root, id string) {
		close(started)
		<-release // a dead mount's unlink
		real(root, id)
		close(done)
	}
	go e.UpdateConfig(&config.Config{Domain: "dev"}, "pw")
	<-started
	snap := make(chan struct{})
	go func() {
		e.Snapshot()
		close(snap)
	}()
	select {
	case <-snap:
	case <-time.After(5 * time.Second):
		t.Fatal("the engine waited on the marker's removal")
	}
	once.Do(func() { close(release) })
	<-done
	if _, err := os.Lstat(markerPath(local, f.ID)); !os.IsNotExist(err) {
		t.Fatalf("the marker is still there: %v", err)
	}
}

// A folder that waits as not the synced one keeps its hash cache: once
// its disk is back, its files aren't all read again. Nor does a disk
// mounted in the folder that isn't now lose its files' entries.
func TestHashCacheKeptWhileTheFolderWaits(t *testing.T) {
	withConfigDir(t)
	files := map[string][]byte{"/pc/docs/nas/n.jpg": []byte("n")}
	for i := 0; i < 4; i++ {
		files[fmt.Sprintf("/pc/docs/p%d.jpg", i)] = []byte(fmt.Sprintf("photo %d", i))
	}
	d := storedDevice(files)
	local := t.TempDir()
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/docs", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)
	e.reconcileRemoteFolder(f)
	e.reconcileRemoteFolder(f) // reads what came down
	cached := func() int {
		e.mu.Lock()
		defer e.mu.Unlock()

		return len(e.hashCache[f.ID])
	}
	if n := cached(); n != 5 {
		t.Fatalf("%d hashes cached after two passes", n)
	}
	// Unmounted: an empty mount point at the folder's path.
	mounted := filepath.Join(t.TempDir(), "disk")
	if err := os.Rename(local, mounted); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(local, 0o755); err != nil {
		t.Fatal(err)
	}
	e.reconcileRemoteFolder(f)
	if st := remoteState(e, f.ID); st.Msg == nil || st.Msg.Key != "desk.main.folder_replaced" {
		t.Fatalf("state %+v", st)
	}
	if n := cached(); n != 5 {
		t.Fatalf("%d hashes left while the folder waits, want 5", n)
	}
	// Back, with the disk mounted in it ("nas", in the record) not mounted.
	if err := os.Remove(local); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(mounted, local); err != nil {
		t.Fatal(err)
	}
	nas := filepath.Join(local, "nas")
	if err := os.Rename(nas, filepath.Join(t.TempDir(), "nas")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(nas, 0o755); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	meta := e.syncedFiles[f.ID].meta
	rec := e.lastSynced[f.ID]
	e.mu.Unlock()
	meta.Mounts = []string{"nas"}
	e.saveSyncedFor(f.ID, e.deviceFor("dev"), meta, rec)
	e.reconcileRemoteFolder(f)
	if _, deletes, _ := d.snapshot(); len(deletes) != 0 {
		t.Fatalf("deletes %v", deletes)
	}
	e.mu.Lock()
	_, kept := e.hashCache[f.ID][filepath.Join(nas, "n.jpg")]
	e.mu.Unlock()
	if !kept || cached() != 5 {
		t.Fatalf("the unmounted disk's entry kept: %v, %d cached", kept, cached())
	}
}

// oldRecord writes folder f's record as otc-sync 0.18 left it: version 2
// with its device, no root and no mounts, written at the time given.
func oldRecord(t *testing.T, e *Engine, f config.RemoteFolder, files map[string]string, at time.Time) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"v": 2, "device": map[string]string{"domain": "dev"}, "files": files})
	if err != nil {
		t.Fatal(err)
	}
	p, err := syncedPath(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

// The first pass after the update, on a record from before mounts were
// kept, with a disk mounted in the folder not mounted: its files are held,
// not deleted on the device, until it is back. They used to look deleted
// here, and too few for the mass-deletion guard went.
func TestUnmountedDiskOnAnOlderRecordIsHeld(t *testing.T) {
	withConfigDir(t)
	files := map[string][]byte{}
	rec := map[string]string{}
	here := map[string]string{}
	for i := 0; i < 100; i++ {
		rel := fmt.Sprintf("p%d.jpg", i)
		if i < 22 {
			rel = fmt.Sprintf("nas/photos/p%d.jpg", i)
		} else {
			here[rel] = fmt.Sprintf("photo %d", i)
		}
		content := fmt.Sprintf("photo %d", i)
		files["/pc/home/"+rel] = []byte(content)
		rec[rel] = sha([]byte(content))
	}
	d := storedDevice(files)
	local := t.TempDir()
	writeFiles(t, local, here)
	nas := filepath.Join(local, "nas") // the mount point, empty, untouched since
	if err := os.Mkdir(nas, 0o755); err != nil {
		t.Fatal(err)
	}
	long := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(nas, long, long); err != nil {
		t.Fatal(err)
	}
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/home", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)
	oldRecord(t, e, f, rec, time.Now().Add(-time.Hour))
	for pass := 0; pass < 2; pass++ {
		e.reconcileRemoteFolder(f)
		got, deletes, _ := d.snapshot()
		if len(deletes) != 0 || len(got) != 100 {
			t.Fatalf("pass %d: %d deletes, %d files on the device", pass+1, len(deletes), len(got))
		}
		if st := remoteState(e, f.ID); st.Msg == nil || st.Msg.Key != "desk.main.files_unreadable" {
			t.Fatalf("pass %d: state %+v", pass+1, st)
		}
	}
	e.mu.Lock()
	meta := e.syncedFiles[f.ID].meta
	e.mu.Unlock()
	if meta.Root == "" || len(meta.MaybeMounts) != 1 || meta.MaybeMounts[0] != "nas" {
		t.Fatalf("record %+v", meta)
	}
	// The disk back: its files are there again, in sync, nothing sent.
	if err := os.Remove(nas); err != nil {
		t.Fatal(err)
	}
	back := map[string]string{}
	for rel := range rec {
		if strings.HasPrefix(rel, "nas/") {
			back[rel] = string(files["/pc/home/"+rel])
		}
	}
	writeFiles(t, local, back)
	e.reconcileRemoteFolder(f)
	got, deletes, _ := d.snapshot()
	if len(deletes) != 0 || len(got) != 100 {
		t.Fatalf("back: %d deletes, %d files on the device", len(deletes), len(got))
	}
	if st := remoteState(e, f.ID); st.Kind != StateWatching {
		t.Fatalf("back: state %+v", st)
	}
	e.mu.Lock()
	meta = e.syncedFiles[f.ID].meta
	e.mu.Unlock()
	if len(meta.MaybeMounts) != 0 {
		t.Fatalf("still held: %v", meta.MaybeMounts)
	}
}

// A folder emptied by hand since the older record was written is not
// taken for a disk that isn't mounted: its deletes go, as they did.
func TestFolderEmptiedSinceAnOlderRecordDeletes(t *testing.T) {
	withConfigDir(t)
	files := map[string][]byte{"/pc/home/old/a.txt": []byte("a"), "/pc/home/old/b.txt": []byte("b")}
	rec := map[string]string{"old/a.txt": sha([]byte("a")), "old/b.txt": sha([]byte("b"))}
	here := map[string]string{}
	for i := 0; i < 4; i++ {
		name := fmt.Sprintf("keep%d.txt", i)
		files["/pc/home/"+name] = []byte(name)
		rec[name] = sha([]byte(name))
		here[name] = name
	}
	d := storedDevice(files)
	local := t.TempDir()
	writeFiles(t, local, here)
	if err := os.Mkdir(filepath.Join(local, "old"), 0o755); err != nil { // emptied just now
		t.Fatal(err)
	}
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/home", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)
	oldRecord(t, e, f, rec, time.Now().Add(-time.Hour))
	e.reconcileRemoteFolder(f)
	got, deletes, _ := d.snapshot()
	if len(deletes) != 2 || len(got) != 4 {
		t.Fatalf("deletes %v, device %v", deletes, keysOf(got))
	}
	if st := remoteState(e, f.ID); st.Kind != StateWatching {
		t.Fatalf("state %+v", st)
	}
}

func TestMaybeMounts(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"nas", "usb/disk", "docs", "fresh"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFiles(t, root, map[string]string{"usb/readme.txt": "r", "docs/a.txt": "a"})
	long := time.Now().Add(-48 * time.Hour)
	for _, d := range []string{"nas", "usb/disk", "usb", "docs", root} {
		p := d
		if d != root {
			p = filepath.Join(root, filepath.FromSlash(d))
		}
		if err := os.Chtimes(p, long, long); err != nil {
			t.Fatal(err)
		}
	}
	byRel := map[string]string{"usb/readme.txt": filepath.Join(root, "usb", "readme.txt"), "docs/a.txt": filepath.Join(root, "docs", "a.txt")}
	rec := map[string]string{
		"nas/a/b.jpg": "1", "usb/disk/c.jpg": "2", "usb/readme.txt": "3", "docs/a.txt": "4", "docs/gone.txt": "5",
		"fresh/x.txt": "6", "removed/y.txt": "7", "kept/z.txt": keptOnDevicePrefix + "8",
	}
	written := time.Now().Add(-time.Hour)
	got := maybeMounts(root, folderMeta{}, rec, byRel, nil, written)
	if strings.Join(got, ",") != "nas,usb/disk" {
		t.Fatalf("first pass of an older record: %v", got)
	}
	// A record of this version holds only what it keeps, while it is so.
	meta := folderMeta{Root: "t", MaybeMounts: []string{"nas", "docs", "usb/disk", "removed"}}
	if got := maybeMounts(root, meta, rec, byRel, nil, time.Time{}); strings.Join(got, ",") != "nas,usb/disk" {
		t.Fatalf("kept: %v", got)
	}
	if got := maybeMounts(root, folderMeta{Root: "t"}, rec, byRel, nil, written); len(got) != 0 {
		t.Fatalf("a record of this version without any: %v", got)
	}
}

// The mount points the system declares: fstab (escapes undone) and systemd
// units; one inside the folder is held at an older record's first pass
// even if its folder changed since.
func TestDeclaredMountPoints(t *testing.T) {
	fstab := "# /etc/fstab\nUUID=1 / ext4 defaults 0 1\n//nas/photos /home/ana/Sync/My\\040NAS cifs noauto,user 0 0\nUUID=2 none swap sw 0 0\n\n/dev/sdb1 relative auto 0 0\n"
	if got := strings.Join(fstabMountPoints([]byte(fstab)), "|"); got != "/|/home/ana/Sync/My NAS" {
		t.Fatalf("fstab: %q", got)
	}
	unit := "[Unit]\nDescription=NAS\n[Mount]\nWhat=//nas/x\nWhere=/home/ana/Sync/nas\nType=cifs\n"
	if got := unitWhere([]byte(unit)); got != "/home/ana/Sync/nas" {
		t.Fatalf("unit: %q", got)
	}
	if got := unitWhere([]byte("[Install]\nWhere=/x\n")); got != "" {
		t.Fatalf("Where outside [Mount]: %q", got)
	}
	if got := strings.Join(declaredUnder("/home/ana/Sync", []string{"/", "/home/ana/Sync", "/home/ana/Sync/My NAS", "/home/ana/Syncthing/x"}), "|"); got != "My NAS" {
		t.Fatalf("under: %q", got)
	}

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nas"), 0o755); err != nil { // changed just now
		t.Fatal(err)
	}
	old := declaredMounts
	t.Cleanup(func() { declaredMounts = old })
	declaredMounts = func() []string { return []string{filepath.Join(root, "nas")} }
	rec := map[string]string{"nas/a.jpg": "1"}
	if got := maybeMounts(root, folderMeta{}, rec, map[string]string{}, nil, time.Now().Add(-time.Hour)); strings.Join(got, ",") != "nas" {
		t.Fatalf("declared, first pass: %v", got)
	}
	declaredMounts = func() []string { return nil }
	if got := maybeMounts(root, folderMeta{}, rec, map[string]string{}, nil, time.Now().Add(-time.Hour)); len(got) != 0 {
		t.Fatalf("not declared, changed since: %v", got)
	}
}
