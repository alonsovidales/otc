// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build unix

package hardening

import (
	"io/fs"
	"os"
	"syscall"
)

// ownedByUs reports whether info is of a file this process's user owns.
func ownedByUs(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
