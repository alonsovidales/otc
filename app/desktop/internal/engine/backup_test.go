// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
)

// backupFixture: a backup folder of n files and an engine over it, linked
// to a fake device.
func backupFixture(t *testing.T, n int) (*Engine, *fakeDevice, config.Folder) {
	t.Helper()
	withConfigDir(t)
	dir := t.TempDir()
	for i := 0; i < n; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d.txt", i)), []byte(fmt.Sprint("content ", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d := &fakeDevice{files: map[string][]byte{}, pending: map[string]*bytes.Buffer{}, paths: map[string]string{}}
	conn := connectedEngine(t, d)
	f := config.Folder{ID: "b1", Path: dir, OneWay: true}
	e := New(&config.Config{Domain: "dev-a", Folders: []config.Folder{f}}, "", nil)
	e.ws = conn.ws
	t.Cleanup(func() {
		e.mu.Lock()
		e.stopped = true
		for _, tm := range e.errorRetry {
			tm.Stop()
		}
		for _, w := range e.watchers {
			w.Stop()
		}
		e.mu.Unlock()
	})
	return e, d, f
}

// A backup removed while its first pass runs stops uploading, and gets no
// watcher that would go on uploading it.
func TestBackupRemovedDuringItsPass(t *testing.T) {
	e, d, f := backupFixture(t, 5)
	removed := false
	d.onHas = func() {
		if !removed {
			removed = true
			e.UpdateConfig(&config.Config{Domain: "dev-a"}, "")
		}
	}

	e.setupFolder(f)

	if len(d.files) != 1 {
		t.Errorf("%d files uploaded after the folder was removed, want only the one under way", len(d.files)-1)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.watchers[f.ID] != nil {
		t.Error("a watcher was left for the removed folder")
	}
	if e.remoteHashes[f.ID] != nil || e.hashCache[f.ID] != nil {
		t.Error("the removed folder's state came back")
	}
	if p, _ := hashCachePath(f.ID); fileExists(p) {
		t.Error("the removed folder's hash cache was written again")
	}
}

// Pointed at another device mid-pass: nothing more goes to it from this
// pass (whose listing came from the old one); the folder is retried soon,
// with no status of its own (as on the Mac) - a plain scan, not the
// stopped pass's file and progress.
func TestBackupPassStopsWhenTheDeviceChanges(t *testing.T) {
	e, d, f := backupFixture(t, 5)
	switched := false
	d.onHas = func() {
		if !switched {
			switched = true
			e.mu.Lock()
			e.cfg.Domain = "dev-b"
			e.mu.Unlock()
		}
	}

	e.reconcile(f)

	if len(d.files) != 1 {
		t.Errorf("%d files uploaded after the device changed", len(d.files)-1)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if st := e.folderStates[f.ID]; st != (FolderState{Kind: StateScanning}) || e.errorRetry[f.ID] == nil {
		t.Errorf("state %+v, retry scheduled %v", st, e.errorRetry[f.ID] != nil)
	}
}

// A watched backup whose pass stopped for a device change goes again when
// the new device connects (startSync), not 10 minutes later: the retry may
// have found no connection yet.
func TestStoppedBackupPassResumesOnConnect(t *testing.T) {
	e, d, f := backupFixture(t, 5)
	e.startWatcher(f)
	switched := false
	d.onHas = func() {
		if !switched {
			switched = true
			e.mu.Lock()
			e.cfg.Domain = "dev-b"
			e.mu.Unlock()
		}
	}
	e.reconcile(f)
	e.mu.Lock()
	if tm := e.errorRetry[f.ID]; tm != nil {
		tm.Stop() // as if it had fired while offline
		delete(e.errorRetry, f.ID)
	}
	e.mu.Unlock()

	e.startSync()

	d.mu.Lock()
	sent := len(d.files)
	d.mu.Unlock()
	e.mu.Lock()
	st := e.folderStates[f.ID]
	e.mu.Unlock()
	if sent != 5 || st.Kind != StateWatching {
		t.Errorf("%d of 5 files on the new device, state %+v", sent, st)
	}
}

// A change waiting in the debounce when its folder goes is not sent.
func TestChangeOfARemovedBackupIsNotSent(t *testing.T) {
	e, d, f := backupFixture(t, 1)
	e.UpdateConfig(&config.Config{Domain: "dev-a"}, "")
	e.processChangedPath(filepath.Join(f.Path, "f0.txt"), f)
	if len(d.files) != 0 {
		t.Fatalf("uploaded: %v", d.files)
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// A backup the device did not make upload only (refused, or the link
// dropped) is asked again on the next pass, and no more once it is.
func TestUploadOnlyRetriedUntilAcknowledged(t *testing.T) {
	e, d, f := backupFixture(t, 1)
	sent := func() int {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.uploadOnly
	}
	d.mu.Lock()
	d.refuseUploadOnly = true
	d.mu.Unlock()
	e.setupFolder(f)
	tries := sent()
	if tries == 0 {
		t.Fatal("never asked")
	}

	d.mu.Lock()
	d.refuseUploadOnly = false
	d.mu.Unlock()
	e.reconcile(f)
	if got := sent(); got != tries+1 {
		t.Fatalf("asked %d more times on the next pass, want 1", got-tries)
	}
	e.reconcile(f)
	e.startSync()
	if got := sent(); got != tries+1 {
		t.Fatalf("asked again once acknowledged (%d)", got-tries-1)
	}
}

// Many files changing at once in a backup (a directory copied in) go up
// one at a time, every one of them.
func TestBackupChangesUploadOneAtATime(t *testing.T) {
	const n = 30
	e, d, f := backupFixture(t, n)
	for i := 0; i < n; i++ {
		e.debounceChange(filepath.Join(f.Path, fmt.Sprintf("f%d.txt", i)), f)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		d.mu.Lock()
		got, most := len(d.files), d.maxOpen
		d.mu.Unlock()
		e.mu.Lock()
		idle := !e.draining[f.ID] && len(e.changeQueue[f.ID]) == 0
		e.mu.Unlock()
		if got == n && idle {
			if most != 1 {
				t.Fatalf("%d uploads under way at once, want 1", most)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d files uploaded, worker idle: %v", got, n, idle)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
