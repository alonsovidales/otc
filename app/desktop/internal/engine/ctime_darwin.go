// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build darwin

package engine

import (
	"os"
	"syscall"
)

// changeTime is the file's change time (ctime) in nanoseconds, as on
// Linux (ctime_linux.go).
func changeTime(fi os.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ctimespec.Sec*1e9 + st.Ctimespec.Nsec
	}

	return 0
}
