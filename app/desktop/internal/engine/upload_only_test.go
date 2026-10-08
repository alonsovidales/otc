// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// sentUploadOnly is what the device was asked, as "path=true".
func sentUploadOnly(d *fakeDevice) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, r := range d.uploadOnlySet {
		out = append(out, fmt.Sprintf("%s=%v", r.Path, r.UploadOnly))
	}
	return out
}

// uploadOnlyOnDisk is the folder's upload-only request in config.json.
func uploadOnlyOnDisk(t *testing.T, id string) *bool {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	want, ok := cfg.PendingRequest(config.UploadOnlyRequest, id)
	if !ok {
		t.Fatalf("folder %s not in config.json", id)
	}
	return want
}

// A two-way folder added from this computer with Upload only (`otc-sync
// add --upload-only` writes a Folder): the request moves with it to the
// two-way folder, goes before the first pass lists or sends anything,
// once, and never again - not by the next pass, nor after a restart.
func TestUploadOnlySentOnceBeforeTheFirstPass(t *testing.T) {
	e, d, dir := imagesFixture(t, 2, func(dir string) *config.Config {
		return &config.Config{Domain: "dev-a", Folders: []config.Folder{{ID: "f1", Path: dir, UploadOnly: ptr(true)}}}
	})
	if len(e.cfg.RemoteFolders) != 1 || e.cfg.RemoteFolders[0].UploadOnly == nil {
		t.Fatalf("not carried to the two-way folder: %+v", e.cfg.RemoteFolders)
	}
	f := e.cfg.RemoteFolders[0]

	e.reconcileRemoteFolder(f)

	want := []string{e.remotePathFor(dir) + "/=true"}
	if got := sentUploadOnly(d); !slices.Equal(got, want) {
		t.Fatalf("asked %v, want %v", got, want)
	}
	d.mu.Lock()
	events := append([]string(nil), d.events...)
	uploaded := len(d.files)
	d.mu.Unlock()
	if slices.Index(events, "upload_only") != 0 || uploaded != 2 {
		t.Fatalf("requests %v, %d files uploaded: the request must come first", events, uploaded)
	}
	if got := uploadOnlyOnDisk(t, "f1"); got != nil {
		t.Fatalf("config.json still asks %v once acknowledged", *got)
	}
	if st := folderImages(e, "f1"); st.UploadOnly != "" {
		t.Fatalf("shown as %q once acknowledged", st.UploadOnly)
	}

	e.reconcileRemoteFolder(f)
	e.uploadOnlyAtConnect()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	e2 := New(cfg, "", nil)
	e2.ws = e.ws
	e2.reconcileRemoteFolder(cfg.RemoteFolders[0])
	e2.uploadOnlyAtConnect()
	if got := sentUploadOnly(d); len(got) != 1 {
		t.Fatalf("asked again: %v", got)
	}
}

// A device that can't (unknown_payload) keeps the request pending, in
// config.json too, without being asked at every pass; the folder says the
// device needs an update. After the update - the next connect - it goes.
func TestUploadOnlyWaitsForADeviceUpdate(t *testing.T) {
	e, d, _ := imagesFixture(t, 1, func(dir string) *config.Config {
		return &config.Config{Domain: "dev-a", RemoteFolders: []config.RemoteFolder{{ID: "r1", RemotePath: "/linux/pc/Docs", LocalPath: dir, UploadOnly: ptr(true)}}}
	})
	f := e.cfg.RemoteFolders[0]
	d.uploadOnlyAnswer = "unknown_payload"

	e.reconcileRemoteFolder(f)

	d.mu.Lock()
	uploaded := len(d.files)
	d.mu.Unlock()
	if uploaded != 1 {
		t.Fatal("an old device must not stop the sync")
	}
	if got := uploadOnlyOnDisk(t, "r1"); got == nil || !*got {
		t.Fatal("the request was dropped")
	}
	st := e.Snapshot()
	if !st.UploadOnlyUnsupported || st.RemoteFolders[0].UploadOnly != string(UploadOnlyUnsupported) {
		t.Fatalf("unsupported %v, folder %q", st.UploadOnlyUnsupported, st.RemoteFolders[0].UploadOnly)
	}
	e.reconcileRemoteFolder(f)
	if got := sentUploadOnly(d); len(got) != 1 {
		t.Fatalf("sent at every pass to a device that can't: %v", got)
	}

	d.mu.Lock()
	d.uploadOnlyAnswer = ""
	d.mu.Unlock()
	e.uploadOnlyAtConnect()
	if got := sentUploadOnly(d); len(got) != 2 {
		t.Fatalf("not sent after the update: %v", got)
	}
	if got := uploadOnlyOnDisk(t, "r1"); got != nil {
		t.Fatal("still pending once acknowledged")
	}
	if st := e.Snapshot(); st.UploadOnlyUnsupported || st.RemoteFolders[0].UploadOnly != "" {
		t.Fatalf("after the update: %+v", st)
	}
}

// Any other failure keeps it for the next pass, shown as on its way; a
// refusal by the folder above (locked_by_parent) drops it with the
// device's message, as Images does.
func TestUploadOnlyRetriedAndRefused(t *testing.T) {
	e, d, _ := imagesFixture(t, 0, func(dir string) *config.Config {
		return &config.Config{Domain: "dev-a", RemoteFolders: []config.RemoteFolder{{ID: "r1", RemotePath: "/Archive/Docs", LocalPath: dir, UploadOnly: ptr(true)}}}
	})
	f := e.cfg.RemoteFolders[0]
	d.uploadOnlyAnswer = "database is busy"
	e.reconcileRemoteFolder(f)
	if got := uploadOnlyOnDisk(t, "r1"); got == nil {
		t.Fatal("dropped after a failure")
	}
	if st := folderImages(e, "r1"); st.UploadOnly != string(UploadOnlyMaking) {
		t.Fatalf("shown as %q while pending", st.UploadOnly)
	}

	d.mu.Lock()
	d.uploadOnlyAnswer = "locked_by_parent"
	d.mu.Unlock()
	e.reconcileRemoteFolder(f)
	if got := uploadOnlyOnDisk(t, "r1"); got != nil {
		t.Fatal("the refused request is still pending")
	}
	if st := folderImages(e, "r1"); st.UploadOnly != "" || !strings.Contains(st.UploadOnlyNote, "/Archive") {
		t.Fatalf("folder shows %+v", st)
	}
	e.reconcileRemoteFolder(f)
	if got := sentUploadOnly(d); len(got) != 2 {
		t.Fatalf("asked %v, want the failure and the refusal only", got)
	}
}

// Recorded from the command line or the tray for a folder already synced:
// it goes as config.json brings it, not at the next pass.
func TestUploadOnlyFromConfigGoesAtOnce(t *testing.T) {
	e, d, _ := imagesFixture(t, 0, func(dir string) *config.Config {
		return &config.Config{Domain: "dev-a", RemoteFolders: []config.RemoteFolder{{ID: "r1", RemotePath: "/linux/pc/Docs", LocalPath: dir}}}
	})
	e.reconcileRemoteFolder(e.cfg.RemoteFolders[0])
	if got := sentUploadOnly(d); len(got) != 0 {
		t.Fatalf("asked %v with nothing requested", got)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetRequest(config.UploadOnlyRequest, "r1", true)
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	e.UpdateConfig(cfg, "")
	deadline := time.Now().Add(10 * time.Second)
	for len(sentUploadOnly(d)) == 0 || uploadOnlyOnDisk(t, "r1") != nil {
		if time.Now().After(deadline) {
			t.Fatalf("not sent: %v, pending %v", sentUploadOnly(d), uploadOnlyOnDisk(t, "r1"))
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Images has nothing to send: its request is apart.
	if got := sentImages(d); len(got) != 0 {
		t.Fatalf("Images asked %v", got)
	}
}

// twoWayUploadOnly: a two-way folder in sync with a device that refuses
// deletes in it - a.txt and b.txt on both sides, recorded as synced.
func twoWayUploadOnly(t *testing.T) (*Engine, *fakeDevice, config.RemoteFolder, string) {
	t.Helper()
	e, d, local := imagesFixture(t, 0, func(dir string) *config.Config {
		return &config.Config{Domain: "dev-a", RemoteFolders: []config.RemoteFolder{{ID: "r1", RemotePath: "/pc/folder", LocalPath: dir}}}
	})
	d.listStored = true
	d.delAnswer = "upload_only"
	for _, name := range []string{"a.txt", "b.txt"} {
		content := []byte("content of " + name)
		d.files["/pc/folder/"+name] = content
		if err := os.WriteFile(filepath.Join(local, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := e.cfg.RemoteFolders[0]
	e.reconcileRemoteFolder(f)
	if rec := e.lastSynced[f.ID]; rec["a.txt"] != sha([]byte("content of a.txt")) {
		t.Fatalf("not in sync to begin with: %v", rec)
	}
	return e, d, f, local
}

func deletesAsked(d *fakeDevice) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.deletes)
}

func readsAsked(d *fakeDevice) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.reads
}

// Issue #132 in a two-way folder: a file deleted here, which the device
// keeps because the folder is upload only there, stays deleted here and
// stays on the device - the delete is asked once, never again, and the
// file isn't downloaded back, not after a restart either. The record says
// so (kept-on-device).
func TestTwoWayUploadOnlyKeepsADeletedFileOnTheDevice(t *testing.T) {
	e, d, f, local := twoWayUploadOnly(t)
	if err := os.Remove(filepath.Join(local, "a.txt")); err != nil {
		t.Fatal(err)
	}

	e.reconcileRemoteFolder(f)

	if got := deletesAsked(d); got != 1 {
		t.Fatalf("asked to delete %d times, want 1", got)
	}
	if rec := e.lastSynced[f.ID]["a.txt"]; rec != keptOnDevice(sha([]byte("content of a.txt"))) {
		t.Fatalf("record %q", rec)
	}
	if st := folderImages(e, "r1"); st.State != string(StateWatching) {
		t.Fatalf("the folder shows %+v: a refused delete is the folder working, not an error", st)
	}
	for i := 0; i < 2; i++ {
		e.reconcileRemoteFolder(f)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	e2 := New(cfg, "", nil)
	e2.ws = e.ws
	e2.reconcileRemoteFolder(cfg.RemoteFolders[0])

	if got := deletesAsked(d); got != 1 {
		t.Fatalf("the delete was sent again (%d)", got)
	}
	if got := readsAsked(d); got != 0 {
		t.Fatalf("downloaded back (%d reads)", got)
	}
	if fileExists(filepath.Join(local, "a.txt")) {
		t.Fatal("a.txt is back here")
	}
	d.mu.Lock()
	_, still := d.files["/pc/folder/a.txt"]
	d.mu.Unlock()
	if !still {
		t.Fatal("gone from the device")
	}
}

// What changes afterwards still syncs: a new version on the device comes
// down; put back here, it goes up; gone from the device too, the record
// forgets it.
func TestTwoWayUploadOnlyLaterChanges(t *testing.T) {
	t.Run("changed on the device", func(t *testing.T) {
		e, d, f, local := twoWayUploadOnly(t)
		_ = os.Remove(filepath.Join(local, "a.txt"))
		e.reconcileRemoteFolder(f)
		d.mu.Lock()
		d.files["/pc/folder/a.txt"] = []byte("edited on a phone")
		d.mu.Unlock()

		e.reconcileRemoteFolder(f)

		if got, _ := os.ReadFile(filepath.Join(local, "a.txt")); string(got) != "edited on a phone" {
			t.Fatalf("a.txt here: %q", got)
		}
		if rec := e.lastSynced[f.ID]["a.txt"]; rec != sha([]byte("edited on a phone")) {
			t.Fatalf("record %q", rec)
		}
	})
	t.Run("put back here", func(t *testing.T) {
		e, d, f, local := twoWayUploadOnly(t)
		p := filepath.Join(local, "a.txt")
		_ = os.Remove(p)
		e.reconcileRemoteFolder(f)
		if err := os.WriteFile(p, []byte("a new a.txt"), 0o644); err != nil {
			t.Fatal(err)
		}

		e.reconcileRemoteFolder(f)

		d.mu.Lock()
		got := d.files["/pc/folder/a.txt"]
		d.mu.Unlock()
		if !bytes.Equal(got, []byte("a new a.txt")) {
			t.Fatalf("the device has %q", got)
		}
		if conflicts := conflictFiles(t, local); len(conflicts) != 0 {
			t.Fatalf("a conflict copy: %v", conflicts)
		}
	})
	t.Run("put back here and changed there", func(t *testing.T) {
		e, d, f, local := twoWayUploadOnly(t)
		p := filepath.Join(local, "a.txt")
		_ = os.Remove(p)
		e.reconcileRemoteFolder(f)
		d.mu.Lock()
		d.files["/pc/folder/a.txt"] = []byte("edited on a phone")
		d.modTime = nil
		d.mu.Unlock()
		if err := os.WriteFile(p, []byte("a new a.txt"), 0o644); err != nil {
			t.Fatal(err)
		}

		e.reconcileRemoteFolder(f)

		// Both changed: one keeps the name, the other is kept next to it.
		got, _ := os.ReadFile(p)
		conflicts := conflictFiles(t, local)
		if len(conflicts) != 1 {
			t.Fatalf("conflict copies %v (a.txt is %q)", conflicts, got)
		}
		copyOf, _ := os.ReadFile(filepath.Join(local, conflicts[0]))
		versions := []string{string(got), string(copyOf)}
		slices.Sort(versions)
		if !slices.Equal(versions, []string{"a new a.txt", "edited on a phone"}) {
			t.Fatalf("versions kept: %q", versions)
		}
	})
	t.Run("gone from the device", func(t *testing.T) {
		e, d, f, local := twoWayUploadOnly(t)
		_ = os.Remove(filepath.Join(local, "a.txt"))
		e.reconcileRemoteFolder(f)
		d.mu.Lock()
		delete(d.files, "/pc/folder/a.txt")
		d.mu.Unlock()

		e.reconcileRemoteFolder(f)

		if _, ok := e.lastSynced[f.ID]["a.txt"]; ok {
			t.Fatalf("still recorded: %v", e.lastSynced[f.ID])
		}
		if fileExists(filepath.Join(local, "a.txt")) {
			t.Fatal("a.txt came back")
		}
	})
}

// A device that accepts the delete (the folder isn't upload only) deletes
// it, and the record forgets it, as before.
func TestTwoWayDeleteAcceptedIsForgotten(t *testing.T) {
	e, d, f, local := twoWayUploadOnly(t)
	d.mu.Lock()
	d.delAnswer = ""
	d.mu.Unlock()
	_ = os.Remove(filepath.Join(local, "a.txt"))

	e.reconcileRemoteFolder(f)

	d.mu.Lock()
	_, still := d.files["/pc/folder/a.txt"]
	d.mu.Unlock()
	if still {
		t.Fatal("not deleted on the device")
	}
	if _, ok := e.lastSynced[f.ID]["a.txt"]; ok {
		t.Fatalf("still recorded: %v", e.lastSynced[f.ID])
	}
}

// The mass-deletion guard is unchanged: the device losing most of the
// folder restores it from here, kept-on-device entries or not.
func TestTwoWayUploadOnlyGuardStillRestores(t *testing.T) {
	e, d, local := imagesFixture(t, 0, func(dir string) *config.Config {
		return &config.Config{Domain: "dev-a", RemoteFolders: []config.RemoteFolder{{ID: "r1", RemotePath: "/pc/folder", LocalPath: dir}}}
	})
	d.listStored = true
	d.delAnswer = "upload_only"
	const n = massDeleteMin + 10
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("f%02d.txt", i)
		content := []byte("file " + name)
		d.files["/pc/folder/"+name] = content
		if err := os.WriteFile(filepath.Join(local, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := e.cfg.RemoteFolders[0]
	e.reconcileRemoteFolder(f)
	// One deleted here (kept on the device), then the device loses the rest.
	_ = os.Remove(filepath.Join(local, "f00.txt"))
	e.reconcileRemoteFolder(f)
	d.mu.Lock()
	for name := range d.files {
		if name != "/pc/folder/f00.txt" {
			delete(d.files, name)
		}
	}
	d.mu.Unlock()

	e.reconcileRemoteFolder(f)

	d.mu.Lock()
	restored := len(d.files)
	d.mu.Unlock()
	if restored != n {
		t.Fatalf("%d files on the device, want %d (restored, plus the one kept)", restored, n)
	}
	for i := 1; i < n; i++ {
		if !fileExists(filepath.Join(local, fmt.Sprintf("f%02d.txt", i))) {
			t.Fatalf("f%02d.txt deleted here", i)
		}
	}
	if fileExists(filepath.Join(local, "f00.txt")) {
		t.Fatal("the file deleted here came back")
	}
	if st := folderImages(e, "r1"); st.State != string(StateError) || !strings.Contains(st.Error, "disappeared from the device") {
		t.Fatalf("the guard's note: %+v", st)
	}
}

func TestRecordBaselines(t *testing.T) {
	if l, r := recordBaselines("abc"); l != "abc" || r != "abc" {
		t.Fatalf("plain entry: %q %q", l, r)
	}
	if l, r := recordBaselines(keptOnDevice("abc")); l != "" || r != "abc" {
		t.Fatalf("kept on the device: %q %q", l, r)
	}
}

func TestUploadOnlyRequestState(t *testing.T) {
	yes, no := ptr(true), ptr(false)
	cases := []struct {
		pending, supported *bool
		want               UploadOnlyState
	}{
		{nil, nil, UploadOnlyNothing},
		{nil, no, UploadOnlyNothing},
		{yes, nil, UploadOnlyMaking},
		{yes, yes, UploadOnlyMaking},
		{no, yes, UploadOnlyLifting},
		{yes, no, UploadOnlyUnsupported},
	}
	for _, c := range cases {
		if got := UploadOnlyRequestState(c.pending, c.supported); got != c.want {
			t.Errorf("%v %v: got %q, want %q", c.pending, c.supported, got, c.want)
		}
	}
}

// Asked for Upload only and not acknowledged yet (a failure, or a device
// that can't): a file deleted here is not deleted on the device - the
// delete waits with its baseline, so the file isn't downloaded back
// either - and goes once the device has the flag, which then keeps it.
func TestTwoWayDeletesWaitForUploadOnly(t *testing.T) {
	for _, answer := range []string{"database is busy", "unknown_payload"} {
		t.Run(answer, func(t *testing.T) {
			e, d, local := imagesFixture(t, 2, func(dir string) *config.Config {
				return &config.Config{Domain: "dev-a", RemoteFolders: []config.RemoteFolder{{ID: "r1", RemotePath: "/pc/folder", LocalPath: dir, UploadOnly: ptr(true)}}}
			})
			d.listStored = true
			d.uploadOnlyAnswer = answer
			f := e.cfg.RemoteFolders[0]
			e.reconcileRemoteFolder(f)
			if err := os.Remove(filepath.Join(local, "p0.jpg")); err != nil {
				t.Fatal(err)
			}

			for i := 0; i < 2; i++ {
				e.reconcileRemoteFolder(f)
			}

			if got := deletesAsked(d); got != 0 {
				t.Fatalf("%d delete(s) sent while upload only is pending", got)
			}
			d.mu.Lock()
			_, still := d.files["/pc/folder/p0.jpg"]
			d.mu.Unlock()
			if !still || readsAsked(d) != 0 || fileExists(filepath.Join(local, "p0.jpg")) {
				t.Fatalf("on the device %v, reads %d, back here %v", still, readsAsked(d), fileExists(filepath.Join(local, "p0.jpg")))
			}
			if rec := e.lastSynced[f.ID]["p0.jpg"]; rec != sha([]byte("photo 0")) {
				t.Fatalf("baseline not kept: %q", rec)
			}
			want := UploadOnlyMaking
			if answer == "unknown_payload" {
				want = UploadOnlyUnsupported
			}
			if st := folderImages(e, "r1"); st.UploadOnly != string(want) {
				t.Fatalf("shown as %q", st.UploadOnly)
			}

			// The device takes it (updated, or the failure over): the
			// folder is upload only there, the delete goes and is refused.
			d.mu.Lock()
			d.uploadOnlyAnswer = ""
			d.delAnswer = "upload_only"
			d.mu.Unlock()
			e.uploadOnlyAtConnect()
			e.reconcileRemoteFolder(f)
			e.reconcileRemoteFolder(f)

			if got := deletesAsked(d); got != 1 {
				t.Fatalf("%d delete(s) once upload only, want 1", got)
			}
			if rec := e.lastSynced[f.ID]["p0.jpg"]; rec != keptOnDevice(sha([]byte("photo 0"))) {
				t.Fatalf("record %q", rec)
			}
			if readsAsked(d) != 0 || fileExists(filepath.Join(local, "p0.jpg")) {
				t.Fatal("p0.jpg came back")
			}
		})
	}
}

// Upload only lifted later, from the web or a phone (the listing no longer
// says upload_only): a file kept on the device when it was deleted here
// comes back here - the old delete is never sent, so nothing the lock
// protected is deleted - and the record is a plain one again.
func TestTwoWayUploadOnlyLiftedBringsFilesBack(t *testing.T) {
	e, d, f, local := twoWayUploadOnly(t)
	_ = os.Remove(filepath.Join(local, "a.txt"))
	e.reconcileRemoteFolder(f)
	d.mu.Lock()
	d.delAnswer = "" // lifted
	d.mu.Unlock()

	for i := 0; i < 3; i++ {
		e.reconcileRemoteFolder(f)
	}

	d.mu.Lock()
	_, still := d.files["/pc/folder/a.txt"]
	d.mu.Unlock()
	if !still || deletesAsked(d) != 1 {
		t.Fatalf("on the device %v, deletes asked %d (want the one refused)", still, deletesAsked(d))
	}
	if got, _ := os.ReadFile(filepath.Join(local, "a.txt")); string(got) != "content of a.txt" {
		t.Fatalf("a.txt here: %q", got)
	}
	if rec := e.lastSynced[f.ID]["a.txt"]; rec != sha([]byte("content of a.txt")) {
		t.Fatalf("record %q", rec)
	}
	// Deleted here again, now that it isn't upload only: a plain delete.
	_ = os.Remove(filepath.Join(local, "a.txt"))
	e.reconcileRemoteFolder(f)
	d.mu.Lock()
	_, still = d.files["/pc/folder/a.txt"]
	d.mu.Unlock()
	if still || deletesAsked(d) != 2 {
		t.Fatalf("on the device %v, deletes asked %d", still, deletesAsked(d))
	}
}

func TestBaselinesFor(t *testing.T) {
	kept := keptOnDevice("abc")
	if l, r := baselinesFor(kept, &pb.File{Hash: "abc", UploadOnly: true}); l != "" || r != "abc" {
		t.Fatalf("still upload only: %q %q", l, r)
	}
	if l, r := baselinesFor(kept, &pb.File{Hash: "abc"}); l != "" || r != "" {
		t.Fatalf("lifted: %q %q", l, r)
	}
	if l, r := baselinesFor(kept, nil); l != "" || r != "abc" {
		t.Fatalf("gone from the device: %q %q", l, r)
	}
	if l, r := baselinesFor("abc", &pb.File{Hash: "abc"}); l != "abc" || r != "abc" {
		t.Fatalf("plain entry: %q %q", l, r)
	}
}
