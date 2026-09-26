// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows && !darwin

package engine

import (
	"os"
	"time"
)

// creationTime on Linux: Go's os.FileInfo has no birth time, and most
// filesystems' is not portable to read, so the modification time stands in
// (the device then shows created = modified, as before issue #134).
func creationTime(_ string, fi os.FileInfo) time.Time { return fi.ModTime() }

// setFileTimes sets the modification time; Linux offers no way to set a
// file's creation time.
func setFileTimes(path string, _ time.Time, modified time.Time) error {
	return os.Chtimes(path, modified, modified)
}
