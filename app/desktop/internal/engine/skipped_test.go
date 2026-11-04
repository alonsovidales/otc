// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
)

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
}

// A synced file replaced here by a link to the same content is still here:
// its copy on the device is not deleted for it.
func TestLinkedFileIsNotDeletedOnTheDevice(t *testing.T) {
	withConfigDir(t)
	d := storedDevice(map[string][]byte{"/pc/docs/a.txt": []byte("a"), "/pc/docs/b.txt": []byte("b")})
	local := t.TempDir()
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/docs", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)
	e.reconcileRemoteFolder(f)
	elsewhere := filepath.Join(t.TempDir(), "a.txt")
	if err := os.Rename(filepath.Join(local, "a.txt"), elsewhere); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, elsewhere, filepath.Join(local, "a.txt"))
	e.reconcileRemoteFolder(f)
	got, deletes, _ := d.snapshot()
	if len(deletes) != 0 || got["/pc/docs/a.txt"] == nil {
		t.Fatalf("deletes %v", deletes)
	}
	if st := remoteState(e, f.ID); st.Msg == nil || st.Msg.Key != "desk.main.files_not_synced_here" {
		t.Fatalf("state %+v", st)
	}
}

// A synced folder moved to another disk and linked back: its files are
// still here, under the link. Nothing is deleted on the device, and nothing
// the device adds there is written through the link.
func TestLinkedFolderIsNotDeletedOnTheDevice(t *testing.T) {
	withConfigDir(t)
	files := map[string][]byte{}
	for i := 0; i < 10; i++ {
		files[fmt.Sprintf("/pc/pics/2023/p%d.jpg", i)] = []byte(fmt.Sprintf("p%d", i))
		files[fmt.Sprintf("/pc/pics/2024/p%d.jpg", i)] = []byte(fmt.Sprintf("q%d", i))
	}
	d := storedDevice(files)
	local := t.TempDir()
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/pics", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)
	e.reconcileRemoteFolder(f)
	other := filepath.Join(t.TempDir(), "2023")
	if err := os.Rename(filepath.Join(local, "2023"), other); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, other, filepath.Join(local, "2023"))
	d.mu.Lock()
	d.files["/pc/pics/2023/new.jpg"] = []byte("new on the device")
	d.mu.Unlock()
	e.reconcileRemoteFolder(f)
	got, deletes, _ := d.snapshot()
	if len(deletes) != 0 || len(got) != 21 {
		t.Fatalf("%d deletes, %d files on the device", len(deletes), len(got))
	}
	if _, err := os.Lstat(filepath.Join(other, "new.jpg")); !os.IsNotExist(err) {
		t.Fatalf("written through the link: %v", err)
	}
}

// A file deleted here that is back by the time its delete would go (the
// pass is long) is not deleted on the device.
func TestDeviceDeleteWaitsForAFilePutBack(t *testing.T) {
	withConfigDir(t)
	d := storedDevice(map[string][]byte{"/pc/docs/a.txt": []byte("a"), "/pc/docs/b.txt": []byte("b")})
	local := t.TempDir()
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/docs", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)
	e.reconcileRemoteFolder(f)
	if err := os.Remove(filepath.Join(local, "a.txt")); err != nil {
		t.Fatal(err)
	}
	d.afterList = func() { writeFiles(t, local, map[string]string{"a.txt": "a, put back"}) }
	e.reconcileRemoteFolder(f)
	if _, deletes, _ := d.snapshot(); len(deletes) != 0 {
		t.Fatalf("deleted on the device: %v", deletes)
	}
	e.reconcileRemoteFolder(f)
	if got, _, _ := d.snapshot(); string(got["/pc/docs/a.txt"]) != "a, put back" {
		t.Fatalf("the device has %q", got["/pc/docs/a.txt"])
	}
}

func TestScanFolderNotesWhatItSkips(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"a.txt": "a", "sub/b.txt": "b", ".hidden/c.txt": "c", "x.otc-part": "p", "Thumbs.db": "t"})
	symlinkOrSkip(t, filepath.Join(root, "a.txt"), filepath.Join(root, "link.txt"))
	symlinkOrSkip(t, filepath.Join(root, "sub"), filepath.Join(root, "linked"))
	s, err := scanFolder(root)
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, p := range s.files {
		r, _ := filepath.Rel(root, p)
		files = append(files, filepath.ToSlash(r))
	}
	slices.Sort(files)
	slices.Sort(s.skipped)
	if !slices.Equal(files, []string{"a.txt", "sub/b.txt"}) || !slices.Equal(s.skipped, []string{"link.txt", "linked"}) {
		t.Fatalf("files %v, skipped %v", files, s.skipped)
	}
	if len(s.mounts) != 0 || len(s.failed) != 0 {
		t.Fatalf("mounts %v, failed %v", s.mounts, s.failed)
	}
}
