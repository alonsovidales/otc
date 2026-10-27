// SPDX-License-Identifier: AGPL-3.0-or-later

package hardening

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTempBaseCandidateSkipsAnInheritedOtcDir(t *testing.T) {
	for in, want := range map[string]string{
		"/tmp/otc-1234":         "/tmp",     // a child inheriting the primary's TMPDIR
		"/dev/shm/otc-99":       "/dev/shm", // the same, on /dev/shm
		"/dev/shm/otc-99-12345": "/dev/shm", // one made when otc-99 was taken
		"/tmp":                  "/tmp",
		"/var/tmp/otc-scratch":  "/var/tmp/otc-scratch", // not a pid: an operator's choice
		"/run/user/1000":        "/run/user/1000",
	} {
		if got := tempBaseCandidate(in); got != want {
			t.Errorf("tempBaseCandidate(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTempDirPid(t *testing.T) {
	for name, want := range map[string]int{
		"otc-1234":        1234,
		"otc-1234-987654": 1234,
		"otc-scratch":     0,
		"otc-":            0,
		"otc--1":          0,
		"otc-0":           0,
		"otc-007":         0,
		"otc-12x":         0,
		"tmp-1234":        0,
	} {
		pid, ok := tempDirPid(name)
		if ok != (want != 0) || pid != want {
			t.Errorf("tempDirPid(%q) = %d, %v; want %d", name, pid, ok, want)
		}
	}
}

// The process's temporary directory is one it made, private: in a
// world-writable /dev/shm another account could have made otc-<pid> first -
// open to it, or a link to a directory of its own - and read the decrypted
// media written there.
func TestOwnTempDirNeverTakesOneFoundThere(t *testing.T) {
	own := fmt.Sprintf("otc-%d", os.Getpid())
	check := func(t *testing.T, base, dir string, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || filepath.Dir(dir) != base {
			t.Fatalf("%s: %v, %v; want a 0700 directory in %s", dir, info.Mode(), err, base)
		}
		if _, ok := tempDirPid(filepath.Base(dir)); !ok {
			t.Errorf("%s isn't named after this process: the sweep would leave it", dir)
		}
	}

	t.Run("nothing there", func(t *testing.T) {
		base := t.TempDir()
		dir, err := ownTempDir(base)
		check(t, base, dir, err)
		if filepath.Base(dir) != own {
			t.Errorf("made %s, want %s", dir, own)
		}
	})
	t.Run("a directory open to anyone", func(t *testing.T) {
		base := t.TempDir()
		planted := filepath.Join(base, own)
		if err := os.Mkdir(planted, 0o777); err != nil {
			t.Fatal(err)
		}
		os.Chmod(planted, 0o777)
		dir, err := ownTempDir(base)
		check(t, base, dir, err)
		if dir == planted || !strings.HasPrefix(filepath.Base(dir), own+"-") {
			t.Errorf("took %s, want a new %s-<random>", dir, own)
		}
	})
	t.Run("a link to elsewhere", func(t *testing.T) {
		base, elsewhere := t.TempDir(), t.TempDir()
		planted := filepath.Join(base, own)
		if err := os.Symlink(elsewhere, planted); err != nil {
			t.Fatal(err)
		}
		dir, err := ownTempDir(base)
		check(t, base, dir, err)
		if dir == planted {
			t.Error("took the link")
		}
	})
}
