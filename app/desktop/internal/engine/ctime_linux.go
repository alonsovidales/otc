// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux

package engine

import (
	"os"
	"syscall"
)

// changeTime is the file's change time (ctime) in nanoseconds, which the
// hash cache checks next to the size and modification time: an edit whose
// size stayed and whose modification time was put back (touch -r, a copy
// that keeps the dates) still moves it, and no program can set it.
func changeTime(fi os.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ctim.Sec*1e9 + st.Ctim.Nsec
	}

	return 0
}
