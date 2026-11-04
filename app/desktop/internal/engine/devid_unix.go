// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package engine

import (
	"os"
	"syscall"
)

// fileSystemOf is the id of the file system fi is on (its st_dev): a
// folder whose id differs from its parent's has another disk or share
// mounted on it (mounts.go).
func fileSystemOf(fi os.FileInfo) (uint64, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}

	return uint64(st.Dev), true //nolint:unconvert // an int32 on macOS
}
