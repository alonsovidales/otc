// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build unix

package updater

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// update.sh holds the run lock shared for as long as it runs: held means
// a run is alive, and testing it must not disturb the holder.
func TestRunLockHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "otc-update.lock")
	if runLockHeld(path) {
		t.Error("no lock file (no run since boot) read as held")
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil { // perms: rw-r--r--
		t.Fatal(err)
	}
	if runLockHeld(path) {
		t.Error("a lock file nobody holds read as held")
	}

	// What update.sh's `flock -s 9` does, on a description of its own.
	run, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Close()
	if err := syscall.Flock(int(run.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if !runLockHeld(path) {
		t.Error("a lock held by a live run read as free")
	}
	// A second run (another update.sh) still gets its shared lock.
	other, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := syscall.Flock(int(other.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		t.Errorf("checking the lock kept a second run from taking it: %v", err)
	}

	// The runs end (the kernel drops a lock with its last descriptor).
	run.Close()
	other.Close()
	if runLockHeld(path) {
		t.Error("a lock left by runs that ended read as held")
	}
}
