// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux

package engine

import (
	"math"

	"golang.org/x/sys/unix"
)

// volumeLimits is the free space on dir's file system and the largest file
// it can hold (FAT: 4 GiB less a byte), when the system says (freeOf).
func volumeLimits(dir string) (free uint64, maxFile int64, ok bool) {
	var st unix.Statfs_t
	if unix.Statfs(dir, &st) != nil {
		return 0, 0, false
	}
	maxFile = math.MaxInt64
	if int64(st.Type) == unix.MSDOS_SUPER_MAGIC { //nolint:unconvert // an int32 on some architectures
		maxFile = 1<<32 - 1
	}

	return freeOf(st.Blocks, st.Bavail, uint64(st.Bsize)), maxFile, true //nolint:unconvert // an int32 on some architectures
}
