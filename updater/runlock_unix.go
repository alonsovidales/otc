// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build unix

package updater

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// runLockHeld reports whether some process holds the lock on path. Every
// update.sh holds it shared for its whole run, so an exclusive one can't
// be had while any is alive; the kernel drops it with the run however that
// ends, and /run is empty after a reboot. No file is no run since boot.
// Any other doubt counts as held, which keeps a "running" status as
// written.
func runLockHeld(path string) bool {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil {
		return true
	}
	defer f.Close()
	fd := int(f.Fd())
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return true // EWOULDBLOCK: a run holds it
	}
	_ = syscall.Flock(fd, syscall.LOCK_UN)

	return false
}
