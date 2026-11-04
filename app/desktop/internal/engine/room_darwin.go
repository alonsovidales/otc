// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build darwin

package engine

import (
	"math"

	"golang.org/x/sys/unix"
)

// volumeLimits is the free space on dir's file system and the largest file
// it can hold (FAT: 4 GiB less a byte), when the system says.
func volumeLimits(dir string) (free uint64, maxFile int64, ok bool) {
	var st unix.Statfs_t
	if unix.Statfs(dir, &st) != nil {
		return 0, 0, false
	}
	maxFile = math.MaxInt64
	if unix.ByteSliceToString(st.Fstypename[:]) == "msdos" {
		maxFile = 1<<32 - 1
	}

	return st.Bavail * uint64(st.Bsize), maxFile, true
}
