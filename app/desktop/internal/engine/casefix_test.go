// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
)

// ignoresCase skips the test on a disk that tells case apart.
func ignoresCase(t *testing.T, dir string) {
	t.Helper()
	probe := filepath.Join(dir, "case-probe")
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(probe)
	if _, err := os.Stat(filepath.Join(dir, "CASE-PROBE")); err != nil {
		t.Skip("this disk tells case apart")
	}
}

// A folder renamed only in case on the device ("photos" to "Photos") stays
// renamed. It used to be renamed back there, its files deleted on the device
// and sent again.
func TestFolderRenamedInCaseOnTheDevice(t *testing.T) {
	withConfigDir(t)
	local := t.TempDir()
	ignoresCase(t, local)
	d := storedDevice(map[string][]byte{"/pc/docs/photos/a.jpg": []byte("a"), "/pc/docs/photos/b.jpg": []byte("b")})
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/docs", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)
	e.reconcileRemoteFolder(f)
	_, _, reads := d.snapshot()
	d.mu.Lock()
	for k, v := range d.files {
		delete(d.files, k)
		d.files[strings.Replace(k, "/photos/", "/Photos/", 1)] = v
	}
	d.mu.Unlock()
	for pass := 0; pass < 3; pass++ {
		e.reconcileRemoteFolder(f)
		got, deletes, reads2 := d.snapshot()
		if len(deletes) != 0 || reads2 != reads || !slices.Equal(keysSorted(got), []string{"/pc/docs/Photos/a.jpg", "/pc/docs/Photos/b.jpg"}) {
			t.Fatalf("pass %d: device %v, deletes %v, %d downloads", pass+2, keysSorted(got), deletes, reads2-reads)
		}
	}
	names := dirNames(t, local)
	if !slices.Equal(names, []string{"Photos"}) {
		t.Fatalf("folders here %v, want the device's Photos", names)
	}
	if st := remoteState(e, f.ID); st.Kind != StateWatching {
		t.Fatalf("state %+v", st)
	}
}

// The device holding the folder in both cases (two folders there): the one
// here is left as it is.
func TestFolderCaseTwinsOnTheDeviceAreLeftAlone(t *testing.T) {
	root := t.TempDir()
	ignoresCase(t, root)
	writeFiles(t, root, map[string]string{"photos/a.jpg": "a"})
	local := map[string]string{"photos/a.jpg": filepath.Join(root, "photos", "a.jpg")}
	if r := matchFolderCase(root, local, map[string]bool{"Photos/b.jpg": true, "photos/c.jpg": true}); len(r) != 0 {
		t.Fatalf("renamed %v", r)
	}
	if r := matchFolderCase(root, local, map[string]bool{"Photos/b.jpg": true}); len(r) != 1 || r[0].to != "Photos" {
		t.Fatalf("renamed %v", r)
	}
	if names := dirNames(t, root); !slices.Equal(names, []string{"Photos"}) {
		t.Fatalf("folders %v", names)
	}
}

// On a disk that tells case apart nothing is renamed: the two are two
// folders there.
func TestFolderCaseLeftAloneWhereCaseCounts(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"photos/a.jpg": "a"})
	if _, err := os.Stat(filepath.Join(root, "PHOTOS")); err == nil {
		t.Skip("this disk ignores case")
	}
	local := map[string]string{"photos/a.jpg": filepath.Join(root, "photos", "a.jpg")}
	if r := matchFolderCase(root, local, map[string]bool{"Photos/b.jpg": true}); len(r) != 0 {
		t.Fatalf("renamed %v", r)
	}
}

func keysSorted(m map[string][]byte) []string {
	out := keysOf(m)
	slices.Sort(out)

	return out
}

// dirNames: the folders in dir, without the hidden ones.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, en := range entries {
		if en.IsDir() && !strings.HasPrefix(en.Name(), ".") {
			out = append(out, en.Name())
		}
	}

	return out
}
