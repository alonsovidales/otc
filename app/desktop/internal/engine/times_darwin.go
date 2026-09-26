// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build darwin

package engine

import (
	"os"
	"syscall"
	"time"
)

// creationTime is the file's birth time on macOS (APFS/HFS+ keep one).
func creationTime(_ string, fi os.FileInfo) time.Time {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return time.Unix(st.Birthtimespec.Sec, st.Birthtimespec.Nsec)
	}

	return fi.ModTime()
}

// setFileTimes sets the modification time; the creation time is left to
// the macOS app proper (SyncModel.download), which is the client on this
// platform - this build only exists for development.
func setFileTimes(path string, _ time.Time, modified time.Time) error {
	return os.Chtimes(path, modified, modified)
}
