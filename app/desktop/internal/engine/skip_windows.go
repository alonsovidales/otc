// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package engine

import (
	"os"
	"syscall"
)

// hiddenAttr: the Hidden or System attribute, which Explorer hides.
func hiddenAttr(fi os.FileInfo) bool {
	if d, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok {
		return d.FileAttributes&(syscall.FILE_ATTRIBUTE_HIDDEN|syscall.FILE_ATTRIBUTE_SYSTEM) != 0
	}

	return false
}
