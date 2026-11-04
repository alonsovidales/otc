// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// withDisk makes the disk say it has free bytes left and holds files up to
// maxFile bytes, for the test.
func withDisk(t *testing.T, free uint64, maxFile int64) {
	t.Helper()
	old := diskLimits
	diskLimits = func(string) (uint64, int64, bool) { return free, maxFile, true }
	t.Cleanup(func() { diskLimits = old })
}

// unBackOff ends every backoff now, as if its time had passed.
func unBackOff(e *Engine) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for p, b := range e.failing {
		b.until = time.Now().Add(-time.Second)
		e.failing[p] = b
	}
}

func readFileRequests(d *fakeDevice) int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.reads
}

// A download the disk has no room for is refused before anything is
// written, said on the folder, and not tried again at every pass.
func TestDownloadWithoutRoomWaits(t *testing.T) {
	withConfigDir(t)
	d := storedDevice(map[string][]byte{"/pc/docs/big.mov": []byte("a big video")})
	local := t.TempDir()
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/docs", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)
	withDisk(t, 100<<20, math.MaxInt64) // under the reserve kept free
	e.reconcileRemoteFolder(f)
	if n := countFiles(t, local); n != 0 {
		t.Fatalf("%d files written", n)
	}
	if left, _ := filepath.Glob(filepath.Join(local, "*.otc-part")); len(left) != 0 {
		t.Fatalf("partial download left: %v", left)
	}
	st := remoteState(e, f.ID)
	if st.Kind != StateError || st.Msg == nil || st.Msg.Key != "desk.main.no_space" {
		t.Fatalf("state %+v", st)
	}
	reads := readFileRequests(d)
	e.reconcileRemoteFolder(f)
	if readFileRequests(d) != reads {
		t.Fatal("tried again at once")
	}
	if st := remoteState(e, f.ID); st.Msg == nil || st.Msg.Key != "desk.main.no_space" {
		t.Fatalf("while it waits: %+v", st)
	}
	// Room again, and its time come: it comes down.
	withDisk(t, 10<<30, math.MaxInt64)
	unBackOff(e)
	e.reconcileRemoteFolder(f)
	if got, _ := os.ReadFile(filepath.Join(local, "big.mov")); string(got) != "a big video" {
		t.Fatalf("not downloaded once there is room: %q", got)
	}
	if st := remoteState(e, f.ID); st.Kind != StateWatching {
		t.Fatalf("state %+v", st)
	}
}

// A file larger than the disk's file system holds (FAT32) is refused and
// said so.
func TestDownloadTooLargeForTheDisk(t *testing.T) {
	withConfigDir(t)
	d := storedDevice(map[string][]byte{"/pc/docs/big.mov": []byte("more than five bytes")})
	local := t.TempDir()
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/docs", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)
	withDisk(t, 10<<30, 5)
	e.reconcileRemoteFolder(f)
	if st := remoteState(e, f.ID); st.Msg == nil || st.Msg.Key != "desk.main.too_large_for_disk" || countFiles(t, local) != 0 {
		t.Fatalf("state %+v", st)
	}
}

// Any other failure counts on the folder too (it used to say Synced).
func TestFailedDownloadIsSaid(t *testing.T) {
	withConfigDir(t)
	d := storedDevice(map[string][]byte{"/pc/docs/a.txt": []byte("a")})
	d.onRead = func() { delete(d.files, "/pc/docs/a.txt") } // gone by the time it is read
	local := t.TempDir()
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/docs", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)
	e.reconcileRemoteFolder(f)
	if st := remoteState(e, f.ID); st.Msg == nil || st.Msg.Key != "desk.main.files_failed" || !strings.Contains(st.Message, "1 file(s)") {
		t.Fatalf("state %+v", st)
	}
}

// A conflict whose upload the device refuses (its storage full): the copy
// of the device's version is made once, not at every pass, and the upload
// waits between tries.
func TestRefusedConflictMakesOneCopy(t *testing.T) {
	withConfigDir(t)
	base, mine, theirs := []byte("base"), []byte("edited here, later"), []byte("edited on the device")
	d := storedDevice(map[string][]byte{"/pc/docs/doc.txt": theirs})
	d.modTime = timestamppb.New(time.Now().Add(-time.Hour))
	d.beginRefusal = "the device's storage is full"
	local := t.TempDir()
	writeFiles(t, local, map[string]string{"doc.txt": string(mine)})
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/docs", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)
	e.saveSynced(f.ID, map[string]string{"doc.txt": sha(base)})
	e.reconcileRemoteFolder(f)
	if c := conflictFiles(t, local); len(c) != 1 {
		t.Fatalf("copies after the first pass: %v", c)
	}
	reads := readFileRequests(d)
	e.reconcileRemoteFolder(f) // backed off
	unBackOff(e)
	e.reconcileRemoteFolder(f) // tried again: the same copy
	if c := conflictFiles(t, local); len(c) != 1 || readFileRequests(d) != reads {
		t.Fatalf("copies %v, %d more downloads", c, readFileRequests(d)-reads)
	}
	if st := remoteState(e, f.ID); st.Msg == nil || st.Msg.Key != "desk.main.files_failed" {
		t.Fatalf("state %+v", st)
	}
	// The device takes it at last: uploaded, and still the one copy.
	d.mu.Lock()
	d.beginRefusal = ""
	d.mu.Unlock()
	unBackOff(e)
	e.reconcileRemoteFolder(f)
	got, _, _ := d.snapshot()
	if string(got["/pc/docs/doc.txt"]) != string(mine) || len(conflictFiles(t, local)) != 1 {
		t.Fatalf("device has %q, copies %v", got["/pc/docs/doc.txt"], conflictFiles(t, local))
	}
}

// A busy file in a conflict gets no copy made for it while it waits.
func TestBusyConflictMakesNoCopy(t *testing.T) {
	withConfigDir(t)
	d := storedDevice(map[string][]byte{"/pc/docs/app.db": []byte("theirs")})
	d.modTime = timestamppb.New(time.Now().Add(-time.Hour))
	local := t.TempDir()
	writeFiles(t, local, map[string]string{"app.db": "mine"})
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/docs", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)
	e.saveSynced(f.ID, map[string]string{"app.db": sha([]byte("base"))})
	e.noteBusy(filepath.Join(local, "app.db"))
	e.reconcileRemoteFolder(f)
	if c := conflictFiles(t, local); len(c) != 0 || readFileRequests(d) != 0 {
		t.Fatalf("copies %v, %d downloads", c, readFileRequests(d))
	}
}

func TestRoomFor(t *testing.T) {
	withDisk(t, 1<<30, 1<<32-1)
	if err := roomFor("/x", 100<<20); err != nil {
		t.Fatalf("room: %v", err)
	}
	if err := roomFor("/x", 900<<20); err == nil || failKindOf(err) != failNoSpace {
		t.Fatalf("over the reserve: %v", err)
	}
	if err := roomFor("/x", 1<<32); failKindOf(err) != failTooLarge {
		t.Fatalf("a 4 GB file on FAT32: %v", err)
	}
}

// A file system that reports no size (total blocks 0: libfuse's default
// statfs, some gvfs mounts) is no limit: every download used to be refused
// there as "Not enough space".
func TestNoSizeIsNoLimit(t *testing.T) {
	if got := freeOf(0, 0, 4096); got != math.MaxUint64 {
		t.Fatalf("no size: %d free", got)
	}
	if got := freeOf(1000, 10, 4096); got != 40960 {
		t.Fatalf("10 blocks of 4 KiB: %d free", got)
	}
	old := diskLimits
	t.Cleanup(func() { diskLimits = old })
	diskLimits = func(string) (uint64, int64, bool) { return freeOf(0, 0, 4096), math.MaxInt64, true }
	if err := roomFor(t.TempDir(), 5<<30); err != nil {
		t.Fatalf("a 5 GB download refused where the size is unknown: %v", err)
	}
	diskLimits = func(string) (uint64, int64, bool) { return freeOf(1000, 10, 4096), math.MaxInt64, true }
	if err := roomFor(t.TempDir(), 1<<20); !errors.Is(err, errNoSpace) {
		t.Fatalf("no room: %v", err)
	}
}
