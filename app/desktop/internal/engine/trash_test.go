// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
)

// trashedAt is where moveToTrash put a file named name from this test's
// folder: the system's trash in the scratch home, or the folder's own on
// Windows (the tests keep the Recycle Bin out of it).
func trashedAt(t *testing.T, root, name string) string {
	t.Helper()
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(os.Getenv("HOME"), ".Trash", name)
	case "windows":
		return filepath.Join(root, folderTrashName, time.Now().Format("2006-01-02"), name)
	default:
		return filepath.Join(os.Getenv("XDG_DATA_HOME"), "Trash", "files", name)
	}
}

// A file deleted on the device goes to the trash here, not away for good.
func TestDeleteFromTheDeviceGoesToTheTrash(t *testing.T) {
	withConfigDir(t)
	d := storedDevice(map[string][]byte{"/pc/docs/a.txt": []byte("only copy"), "/pc/docs/b.txt": []byte("b")})
	local := t.TempDir()
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/docs", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)
	e.reconcileRemoteFolder(f)
	d.mu.Lock()
	delete(d.files, "/pc/docs/a.txt") // deleted from the web, for good there
	d.mu.Unlock()
	e.reconcileRemoteFolder(f)
	if _, err := os.Stat(filepath.Join(local, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("still here: %v", err)
	}
	got, err := os.ReadFile(trashedAt(t, local, "a.txt"))
	if err != nil || string(got) != "only copy" {
		t.Fatalf("not in the trash: %q %v", got, err)
	}
}

// Where the system's trash can't take a file it goes to the folder's own,
// by day and with its path, a taken name numbered; days older than 30 are
// emptied.
func TestFolderTrash(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"sub/a.txt": "one", "x.txt": "x"})
	day := time.Date(2026, 11, 4, 10, 0, 0, 0, time.Local)
	if err := folderTrash(filepath.Join(root, "sub", "a.txt"), root, day); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, root, map[string]string{"sub/a.txt": "two"})
	if err := folderTrash(filepath.Join(root, "sub", "a.txt"), root, day); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"a.txt": "one", "a 2.txt": "two"} {
		got, err := os.ReadFile(filepath.Join(root, folderTrashName, "2026-11-04", "sub", name))
		if err != nil || string(got) != want {
			t.Fatalf("%s: %q %v", name, got, err)
		}
	}
	old := day.AddDate(0, 0, -31)
	if err := folderTrash(filepath.Join(root, "x.txt"), root, old); err != nil {
		t.Fatal(err)
	}
	pruneFolderTrash(root, day)
	if _, err := os.Stat(filepath.Join(root, folderTrashName, old.Format("2006-01-02"))); !os.IsNotExist(err) {
		t.Fatalf("a day older than 30 was kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, folderTrashName, "2026-11-04")); err != nil {
		t.Fatalf("today's was emptied: %v", err)
	}
	pruneFolderTrash(root, day.AddDate(0, 0, 40))
	if _, err := os.Stat(filepath.Join(root, folderTrashName)); !os.IsNotExist(err) {
		t.Fatalf("the empty trash folder was kept: %v", err)
	}
	// Never synced: it is dot-named.
	if !notSynced(folderTrashName + "/2026-11-04/sub/a.txt") {
		t.Fatal("the folder's trash would be synced")
	}
}
