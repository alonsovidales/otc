// SPDX-License-Identifier: AGPL-3.0-or-later

package browser

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// Only http and https reach the desktop's opener; these fail before it.
func TestOpenRefusesOtherAddresses(t *testing.T) {
	for _, addr := range []string{"", "file:///etc/passwd", "javascript:alert(1)", "smb://nas/share", "https://", "-a https://x/", "cala.off-the.cloud"} {
		if err := Open(addr); err == nil {
			t.Errorf("Open(%q) = nil, want an error", addr)
		}
	}
}

// fakeLauncher records what reached the desktop's opener, in place of it.
func fakeLauncher(t *testing.T) *[]string {
	t.Helper()
	var launched []string
	old := launchFolder
	launchFolder = func(path string) error {
		launched = append(launched, path)

		return nil
	}
	t.Cleanup(func() { launchFolder = old })

	return &launched
}

// Issue #197: only an existing folder, by its absolute path, reaches the
// opener; a missing one - or a file in its place, which the shell would
// run - is reported as missing, and nothing is launched.
func TestOpenFolderRefuses(t *testing.T) {
	launched := fakeLauncher(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path    string
		missing bool
	}{
		{"", false},
		{"relative/dir", false},
		{"-x", false},
		{".", false},
		{filepath.Join(dir, "gone"), true},
		{file, true},
	}
	for _, c := range cases {
		err := OpenFolder(c.path)
		if err == nil {
			t.Errorf("OpenFolder(%q) = nil, want an error", c.path)

			continue
		}
		if got := errors.Is(err, fs.ErrNotExist); got != c.missing {
			t.Errorf("OpenFolder(%q): missing = %v, want %v (%v)", c.path, got, c.missing, err)
		}
	}
	if len(*launched) != 0 {
		t.Errorf("launched %q, want nothing", *launched)
	}
}

func TestOpenFolderLaunchesTheFolder(t *testing.T) {
	launched := fakeLauncher(t)
	dir := t.TempDir()
	if err := OpenFolder(dir + string(filepath.Separator)); err != nil {
		t.Fatalf("OpenFolder(%q) = %v", dir, err)
	}
	if len(*launched) != 1 || (*launched)[0] != filepath.Clean(dir) {
		t.Errorf("launched %q, want [%q]", *launched, dir)
	}
}

// The opener's own failure comes back as it is, for the dialog to show.
func TestOpenFolderReportsTheOpenersFailure(t *testing.T) {
	old := launchFolder
	t.Cleanup(func() { launchFolder = old })
	boom := errors.New("xdg-open: exit status 3")
	launchFolder = func(string) error { return boom }
	if err := OpenFolder(t.TempDir()); !errors.Is(err, boom) {
		t.Errorf("OpenFolder = %v, want %v", err, boom)
	}
}
