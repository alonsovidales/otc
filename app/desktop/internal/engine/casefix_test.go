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
	if r := matchFolderCase(root, local, map[string]bool{"Photos/b.jpg": true, "photos/c.jpg": true}, nil); len(r) != 0 {
		t.Fatalf("renamed %v", r)
	}
	if r := matchFolderCase(root, local, map[string]bool{"Photos/b.jpg": true}, nil); len(r) != 1 || r[0].to != "Photos" {
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
	if r := matchFolderCase(root, local, map[string]bool{"Photos/b.jpg": true}, nil); len(r) != 0 {
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

// A file renamed only in case here ("a.jpg" to "A.jpg" in Explorer or
// Finder) is renamed so on the device: the new name goes up and the old one
// is deleted there. The old name used to find the new one on a disk that
// ignores case, so its delete was held at every pass and the device kept
// both, for good, while the folder said Synced.
func TestFileRenamedInCaseHere(t *testing.T) {
	withConfigDir(t)
	local := t.TempDir()
	ignoresCase(t, local)
	d := storedDevice(map[string][]byte{"/pc/docs/a.jpg": []byte("a"), "/pc/docs/b.jpg": []byte("b")})
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/docs", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)
	e.reconcileRemoteFolder(f)
	if err := os.Rename(filepath.Join(local, "a.jpg"), filepath.Join(local, "A.jpg")); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 3; pass++ {
		e.reconcileRemoteFolder(f)
	}
	got, deletes, _ := d.snapshot()
	if !slices.Equal(keysSorted(got), []string{"/pc/docs/A.jpg", "/pc/docs/b.jpg"}) || !slices.Equal(deletes, []string{"/pc/docs/a.jpg"}) {
		t.Fatalf("device %v, deletes %v", keysSorted(got), deletes)
	}
	if string(got["/pc/docs/A.jpg"]) != "a" {
		t.Fatalf("A.jpg on the device holds %q", got["/pc/docs/A.jpg"])
	}
	if names := fileNames(t, local); !slices.Equal(names, []string{"A.jpg", "b.jpg"}) {
		t.Fatalf("here %v", names)
	}
	if st := remoteState(e, f.ID); st.Kind != StateWatching {
		t.Fatalf("state %+v", st)
	}
}

// A folder renamed only in case here ("photos" to "Photos") is renamed so
// on the device, not renamed back here: the record has its files under the
// device's spelling, so the rename is this computer's.
func TestFolderRenamedInCaseHere(t *testing.T) {
	withConfigDir(t)
	local := t.TempDir()
	ignoresCase(t, local)
	d := storedDevice(map[string][]byte{"/pc/docs/photos/a.jpg": []byte("a"), "/pc/docs/photos/b.jpg": []byte("b")})
	f := config.RemoteFolder{ID: "r1", RemotePath: "/pc/docs", LocalPath: local}
	e := twoWayEngine(t, d, "dev", f)
	e.reconcileRemoteFolder(f)
	_, _, reads := d.snapshot()
	if err := os.Rename(filepath.Join(local, "photos"), filepath.Join(local, "Photos")); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 3; pass++ {
		e.reconcileRemoteFolder(f)
		if names := dirNames(t, local); !slices.Equal(names, []string{"Photos"}) {
			t.Fatalf("pass %d: folders here %v, want the rename made here kept", pass+2, names)
		}
	}
	got, deletes, reads2 := d.snapshot()
	slices.Sort(deletes)
	if !slices.Equal(keysSorted(got), []string{"/pc/docs/Photos/a.jpg", "/pc/docs/Photos/b.jpg"}) ||
		!slices.Equal(deletes, []string{"/pc/docs/photos/a.jpg", "/pc/docs/photos/b.jpg"}) || reads2 != reads {
		t.Fatalf("device %v, deletes %v, %d downloads", keysSorted(got), deletes, reads2-reads)
	}
	if st := remoteState(e, f.ID); st.Kind != StateWatching {
		t.Fatalf("state %+v", st)
	}
}

// The check before a device delete: only the very name counts, each
// component of it.
func TestPresentAsNamed(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"Photos/A.jpg": "a", "docs/café.txt": "c"})
	for rel, want := range map[string]bool{
		"Photos/A.jpg": true, "docs/café.txt": true, "Photos/b.jpg": false, "gone/x.txt": false,
	} {
		if got, err := presentAsNamed(root, rel); err != nil || got != want {
			t.Errorf("%s: %v %v, want %v", rel, got, err, want)
		}
	}
	probe := filepath.Join(root, "case-probe")
	_ = os.WriteFile(probe, nil, 0o644)
	_, err := os.Stat(filepath.Join(root, "CASE-PROBE"))
	_ = os.Remove(probe)
	if err != nil {
		return // a disk that tells case apart finds no other spelling
	}
	for _, rel := range []string{"Photos/a.jpg", "photos/A.jpg", "PHOTOS/a.JPG"} {
		if got, err := presentAsNamed(root, rel); err != nil || got {
			t.Errorf("%s found for another case: %v %v", rel, got, err)
		}
	}
}

// fileNames: the files in dir, without the hidden ones.
func fileNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, en := range entries {
		if !en.IsDir() && !strings.HasPrefix(en.Name(), ".") {
			out = append(out, en.Name())
		}
	}

	return out
}
