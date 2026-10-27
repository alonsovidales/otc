// SPDX-License-Identifier: AGPL-3.0-or-later

package hardening

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// tempBaseCandidate is the first place SecureTempDir looks for its base:
// TMPDIR, unless that is another otc process's own temporary directory - a
// supervised child inherits the primary's TMPDIR. Then it is that
// directory's base, so the child's directory is a sibling and its sweep
// never removes the primary's live temporary files.
func tempBaseCandidate(tmp string) string {
	if _, ok := tempDirPid(filepath.Base(tmp)); ok {
		return filepath.Dir(tmp)
	}
	return tmp
}

// tempDirPid is the process a temporary directory's name says it is
// for: otc-<pid>, or otc-<pid>-<random> (ownTempDir).
func tempDirPid(name string) (int, bool) {
	rest, ok := strings.CutPrefix(name, "otc-")
	if !ok {
		return 0, false
	}
	digits, _, _ := strings.Cut(rest, "-")
	pid, err := strconv.Atoi(digits)
	if err != nil || pid <= 0 || strconv.Itoa(pid) != digits {
		return 0, false
	}
	return pid, true
}

// ownTempDir makes this process's temporary directory in base and returns
// it: otc-<pid>, made here - never one found there, which in a
// world-writable /dev/shm another account could have made first (or a
// link to somewhere of its own) and read the decrypted media from - or,
// when that name is taken, otc-<pid>-<random>. Either way a directory of
// this account's, 0700, checked once made.
func ownTempDir(base string) (string, error) {
	dir := filepath.Join(base, fmt.Sprintf("otc-%d", os.Getpid()))
	err := os.Mkdir(dir, 0o700)
	if errors.Is(err, fs.ErrExist) {
		dir, err = os.MkdirTemp(base, fmt.Sprintf("otc-%d-", os.Getpid()))
	}
	if err != nil {
		return "", err
	}
	// The mode as made, whatever the umask, and nothing but a directory
	// of this account's.
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 || !ownedByUs(info) {
		return "", fmt.Errorf("%s is not a private directory of this account", dir)
	}
	return dir, nil
}
