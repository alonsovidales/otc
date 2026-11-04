// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
)

// heldFolder is a two-way folder of 60 files synced with its device, 25 of
// them then deleted here: the device-side guard holds their deletes. Its
// config.json is on disk, as the approval's clear edits it. pin, when
// given, is the device's pin as this sign-in confirms it.
func heldFolder(t *testing.T, pin []byte) (*Engine, *fakeDevice, config.RemoteFolder) {
	t.Helper()
	withConfigDir(t)
	files := map[string][]byte{}
	for i := 0; i < 60; i++ {
		files[fmt.Sprintf("/pc/pics/p%02d.jpg", i)] = []byte(fmt.Sprintf("photo %d", i))
	}
	d := storedDevice(files)
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/pics", LocalPath: t.TempDir()}
	e := twoWayEngine(t, d, "cala.off-the.cloud", f)
	if pin != nil {
		e.pinSource = func() ([]byte, bool) { return pin, true }
	}
	if err := e.cfg.Save(); err != nil {
		t.Fatal(err)
	}
	e.reconcileRemoteFolder(f)
	deleteHere(t, f, 0, 25)
	e.reconcileRemoteFolder(f)

	return e, d, f
}

// deleteHere deletes p<from>.jpg to p<to-1>.jpg from the folder here.
func deleteHere(t *testing.T, f config.RemoteFolder, from, to int) {
	t.Helper()
	for i := from; i < to; i++ {
		if err := os.Remove(filepath.Join(f.LocalPath, fmt.Sprintf("p%02d.jpg", i))); err != nil {
			t.Fatal(err)
		}
	}
}

// heldStatus is the folder as state.json shows it.
func heldStatus(t *testing.T, e *Engine, id string) config.FolderStatus {
	t.Helper()
	for _, fs := range e.Snapshot().RemoteFolders {
		if fs.ID == id {
			return fs
		}
	}
	t.Fatalf("folder %s not in the snapshot", id)

	return config.FolderStatus{}
}

// approve is "Delete Them on the Device Too…" confirmed for key: in
// config.json, and in the engine's config as the reload of it brings it.
func approve(t *testing.T, e *Engine, id, key string) {
	t.Helper()
	if _, err := config.Edit(func(c *config.Config) (bool, error) { return c.SetDeleteHeldRequest(id, key), nil }); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	ok := e.cfg.SetDeleteHeldRequest(id, key)
	e.mu.Unlock()
	if !ok {
		t.Fatalf("folder %s not configured", id)
	}
}

// approvalLeft is folder id's approval on disk and in memory.
func approvalLeft(t *testing.T, e *Engine, id string) (disk, memory string) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	return cfg.DeleteHeldRequest(id), e.cfg.DeleteHeldRequest(id)
}

// Without an approval the hold stays as it always did, pass after pass,
// and state.json says how many and the key of that very set - the same at
// every pass while nothing changes.
func TestHeldDeletesWithoutApprovalStayHeld(t *testing.T) {
	e, d, f := heldFolder(t, nil)
	fs := heldStatus(t, e, f.ID)
	if fs.DeletesHeld != 25 || len(fs.DeletesHeldKey) != 64 || fs.MsgKey != "desk.main.deletes_held" {
		t.Fatalf("status %+v", fs)
	}
	e.reconcileRemoteFolder(f)
	if again := heldStatus(t, e, f.ID); again.DeletesHeld != 25 || again.DeletesHeldKey != fs.DeletesHeldKey {
		t.Fatalf("the same set read %+v, was %q", again, fs.DeletesHeldKey)
	}
	if got, deletes, _ := d.snapshot(); len(deletes) != 0 || len(got) != 60 {
		t.Fatalf("%d deletes sent, %d files on the device", len(deletes), len(got))
	}
}

// Approved for the set the guard holds: those deletes go, the folder is at
// rest, nothing is held any more, and the approval is gone from
// config.json and from memory.
func TestApprovedHeldDeletesGo(t *testing.T) {
	e, d, f := heldFolder(t, nil)
	approve(t, e, f.ID, heldStatus(t, e, f.ID).DeletesHeldKey)
	e.reconcileRemoteFolder(f)
	got, deletes, _ := d.snapshot()
	if len(deletes) != 25 || len(got) != 35 {
		t.Fatalf("%d deletes sent, %d files on the device", len(deletes), len(got))
	}
	if st := remoteState(e, f.ID); st.Kind != StateWatching {
		t.Fatalf("state %+v", st)
	}
	if fs := heldStatus(t, e, f.ID); fs.DeletesHeld != 0 || fs.DeletesHeldKey != "" {
		t.Fatalf("still held: %+v", fs)
	}
	if disk, mem := approvalLeft(t, e, f.ID); disk != "" || mem != "" {
		t.Fatalf("approval left: %q on disk, %q in memory", disk, mem)
	}
	e.mu.Lock()
	kept := len(e.lastSynced[f.ID])
	e.mu.Unlock()
	if kept != 35 {
		t.Fatalf("record lists %d files, want 35", kept)
	}
}

// Approved for a set that isn't the one held now - one more file deleted
// here since the owner was asked: nothing is deleted on the device, the
// approval is cleared, and the folder shows the new count with its own
// key, which then goes once approved.
func TestStaleApprovalKeepsTheHold(t *testing.T) {
	e, d, f := heldFolder(t, nil)
	shown := heldStatus(t, e, f.ID).DeletesHeldKey
	deleteHere(t, f, 25, 26)
	approve(t, e, f.ID, shown)
	e.reconcileRemoteFolder(f)
	if _, deletes, _ := d.snapshot(); len(deletes) != 0 {
		t.Fatalf("deleted on the device: %v", deletes)
	}
	fs := heldStatus(t, e, f.ID)
	if fs.DeletesHeld != 26 || fs.DeletesHeldKey == shown || fs.MsgKey != "desk.main.deletes_held" || !strings.Contains(fs.Error, "26 files") {
		t.Fatalf("status %+v", fs)
	}
	if disk, mem := approvalLeft(t, e, f.ID); disk != "" || mem != "" {
		t.Fatalf("stale approval left: %q on disk, %q in memory", disk, mem)
	}
	// A key that matches nothing at all is no different.
	approve(t, e, f.ID, strings.Repeat("0", 64))
	e.reconcileRemoteFolder(f)
	if _, deletes, _ := d.snapshot(); len(deletes) != 0 || heldStatus(t, e, f.ID).DeletesHeld != 26 {
		t.Fatalf("a wrong key: %d deletes", len(deletes))
	}

	approve(t, e, f.ID, fs.DeletesHeldKey)
	e.reconcileRemoteFolder(f)
	if got, deletes, _ := d.snapshot(); len(deletes) != 26 || len(got) != 34 {
		t.Fatalf("approved again: %d deletes, %d on the device", len(deletes), len(got))
	}
}

// An approval left over once nothing is held (the files put back here, or
// deleted on the device meanwhile) is cleared, and a later hold of the
// same set is asked about again.
func TestApprovalWithNothingHeldIsCleared(t *testing.T) {
	e, d, f := heldFolder(t, nil)
	key := heldStatus(t, e, f.ID).DeletesHeldKey
	d.mu.Lock()
	for i := 0; i < 25; i++ {
		delete(d.files, fmt.Sprintf("/pc/pics/p%02d.jpg", i))
	}
	d.mu.Unlock()
	approve(t, e, f.ID, key)
	e.reconcileRemoteFolder(f)
	if disk, mem := approvalLeft(t, e, f.ID); disk != "" || mem != "" {
		t.Fatalf("approval left: %q on disk, %q in memory", disk, mem)
	}
	if st := remoteState(e, f.ID); st.Kind != StateWatching {
		t.Fatalf("state %+v", st)
	}
}

// Approved while the device's pin isn't confirmed (record.go): the deletes
// wait as every delete does then, and the approval stays for the pass that
// lets them go - once the pin is confirmed, they do.
func TestApprovedDeletesWaitForTheDevicesPin(t *testing.T) {
	pin := bytes.Repeat([]byte{7}, 32)
	e, d, f := heldFolder(t, pin)
	key := heldStatus(t, e, f.ID).DeletesHeldKey
	approve(t, e, f.ID, key)
	e.pinSource = func() ([]byte, bool) { return nil, false }
	e.reconcileRemoteFolder(f)
	if _, deletes, _ := d.snapshot(); len(deletes) != 0 {
		t.Fatalf("deleted with the pin unconfirmed: %v", deletes)
	}
	if st := remoteState(e, f.ID); st.Msg == nil || st.Msg.Key != "desk.main.device_unconfirmed" {
		t.Fatalf("state %+v", st)
	}
	if disk, mem := approvalLeft(t, e, f.ID); disk != key || mem != key {
		t.Fatalf("approval dropped while waiting: %q on disk, %q in memory", disk, mem)
	}

	e.pinSource = func() ([]byte, bool) { return pin, true }
	e.reconcileRemoteFolder(f)
	if _, deletes, _ := d.snapshot(); len(deletes) != 25 {
		t.Fatalf("deletes once confirmed: %d", len(deletes))
	}
	if disk, mem := approvalLeft(t, e, f.ID); disk != "" || mem != "" {
		t.Fatalf("approval left: %q on disk, %q in memory", disk, mem)
	}
}

// config.json that can't be written: the approval is used once, and a
// reload that brings it back (it is still on disk) uses it no more.
func TestApprovalIsUsedOnceWhenItsClearFails(t *testing.T) {
	e, d, f := heldFolder(t, nil)
	key := heldStatus(t, e, f.ID).DeletesHeldKey
	approve(t, e, f.ID, key)
	saveConfig = func(*config.Config) error { return os.ErrPermission }
	t.Cleanup(func() { saveConfig = (*config.Config).Save })
	e.reconcileRemoteFolder(f)
	if _, deletes, _ := d.snapshot(); len(deletes) != 25 {
		t.Fatalf("%d deletes sent", len(deletes))
	}
	if disk, _ := approvalLeft(t, e, f.ID); disk != key {
		t.Fatalf("config.json was written: %q", disk)
	}
	// The same 25 deleted here again after they came back from elsewhere
	// would be the same set: config.json's stale copy must not delete them.
	e.mu.Lock()
	done := e.approvalDone[f.ID]
	e.mu.Unlock()
	if done != key || judgeDeleteApproval(key, done, key) != approvalStale {
		t.Fatalf("done %q", done)
	}
	// Written again, the next pass clears it for good.
	saveConfig = (*config.Config).Save
	approve(t, e, f.ID, key) // the reload of config.json
	e.reconcileRemoteFolder(f)
	if disk, mem := approvalLeft(t, e, f.ID); disk != "" || mem != "" {
		t.Fatalf("approval left: %q on disk, %q in memory", disk, mem)
	}
	e.mu.Lock()
	_, owed := e.approvalDone[f.ID]
	e.mu.Unlock()
	if owed {
		t.Fatal("still owes a clear")
	}
}

// The key is the set: the same paths and hashes in any order give the
// same key, and a path more, one less or another hash give another; moves
// (not held) and other actions don't count.
func TestHeldDeletesKey(t *testing.T) {
	acts := []action{
		{"b.jpg", actDeleteRemote, "hb"}, {"a.jpg", actDeleteRemote, "ha"},
		{"moved.jpg", actDeleteRemote, "hm"}, {"up.jpg", actUpload, "hu"},
	}
	held := map[string]bool{"a.jpg": true, "b.jpg": true}
	key := heldDeletesKey(acts, held)
	if len(key) != 64 {
		t.Fatalf("key %q", key)
	}
	if heldDeletesKey([]action{acts[3], acts[1], acts[0]}, held) != key {
		t.Fatal("another order, another key")
	}
	if heldDeletesKey(acts, nil) != "" {
		t.Fatal("a key for nothing held")
	}
	for name, other := range map[string]string{
		"a path more":  heldDeletesKey(acts, map[string]bool{"a.jpg": true, "b.jpg": true, "moved.jpg": true}),
		"a path less":  heldDeletesKey(acts, map[string]bool{"a.jpg": true}),
		"another hash": heldDeletesKey([]action{{"b.jpg", actDeleteRemote, "hb2"}, acts[1]}, held),
	} {
		if other == key {
			t.Errorf("%s: the same key", name)
		}
	}
	// The same key as the Mac's SyncPaths.heldDeletesKey for the same set
	// (SyncPathsTests' heldDeletesKeyIsTheSet), byte for byte.
	mac := heldDeletesKey([]action{{"café/été.jpg", actDeleteRemote, "hc"}, acts[0], acts[1]},
		map[string]bool{"a.jpg": true, "b.jpg": true, "café/été.jpg": true})
	if mac != "862349950f42ffd650ac986edd734204b0c9c349e3d2b49c26ff25f61da6d391" {
		t.Errorf("not the Mac's key: %s", mac)
	}
	// "a" + "b\x00…" can't pass for "ab" + "\x00…".
	if heldDeletesKey([]action{{"ab", actDeleteRemote, "h"}}, map[string]bool{"ab": true}) ==
		heldDeletesKey([]action{{"a", actDeleteRemote, "bh"}}, map[string]bool{"a": true}) {
		t.Error("path and hash run together")
	}
}

func TestJudgeDeleteApproval(t *testing.T) {
	for _, c := range []struct {
		approved, done, key string
		want                approvalVerdict
	}{
		{"", "", "k", approvalNone},
		{"", "", "", approvalNone},
		{"k", "", "k", approvalGo},
		{"k", "", "k2", approvalStale},
		{"k", "", "", approvalStale},
		{"k", "k", "k", approvalStale}, // read back after its clear failed
		{"k", "old", "k", approvalGo},
	} {
		if got := judgeDeleteApproval(c.approved, c.done, c.key); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
}

// A config.json that brings an approval runs the folder's pass now; one
// cleared, unchanged or for a folder new to the config doesn't.
func TestChangedApprovals(t *testing.T) {
	e := New(&config.Config{}, "", nil)
	old := &config.Config{RemoteFolders: []config.RemoteFolder{{ID: "r1"}, {ID: "r2", DeleteHeld: "k"}, {ID: "r3", DeleteHeld: "k"}}}
	cfg := &config.Config{RemoteFolders: []config.RemoteFolder{
		{ID: "r1", DeleteHeld: "k"}, {ID: "r2", DeleteHeld: "k"}, {ID: "r3"}, {ID: "r4", DeleteHeld: "k"},
	}}
	e.mu.Lock()
	got := e.changedApprovalsLocked(old, cfg)
	e.mu.Unlock()
	if len(got) != 1 || got[0].ID != "r1" {
		t.Fatalf("got %+v", got)
	}
	cfg.RemoteFolders[1].DeleteHeld = "k2" // asked again for another set
	e.mu.Lock()
	got = e.changedApprovalsLocked(old, cfg)
	e.mu.Unlock()
	if len(got) != 2 || got[1].ID != "r2" {
		t.Fatalf("got %+v", got)
	}
}

// passSoon while the folder's pass is under way: that pass runs again
// once it is over, as it may have looked at the guard already; a pass that
// starts takes the ask over.
func TestPassSoonWhileAPassRuns(t *testing.T) {
	withConfigDir(t)
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/pics", LocalPath: t.TempDir()}
	// Not connected, so the pass passSoon starts returns at once: the ask
	// is kept for the pass under way.
	e := New(&config.Config{RemoteFolders: []config.RemoteFolder{f}}, "", nil)
	e.mu.Lock()
	e.folderBusy[f.ID] = true
	e.mu.Unlock()
	e.passSoon(f)
	e.mu.Lock()
	asked := e.passAgain[f.ID]
	e.mu.Unlock()
	if !asked {
		t.Fatal("not asked again")
	}

	// A pass that starts after the ask is the one asked for: none follows.
	conn := twoWayEngine(t, storedDevice(nil), "dev", f)
	conn.mu.Lock()
	conn.passAgain = map[string]bool{f.ID: true}
	conn.mu.Unlock()
	conn.reconcileRemoteFolder(f)
	conn.mu.Lock()
	left := conn.passAgain[f.ID]
	conn.mu.Unlock()
	if left {
		t.Fatal("the pass didn't take the ask over")
	}
}
