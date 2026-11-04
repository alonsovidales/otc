// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package engine

import "syscall"

// hideFile gives p the Hidden attribute, as Explorer hides a dot-named file
// nowhere else; best effort.
func hideFile(p string) {
	name, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return
	}
	attrs, err := syscall.GetFileAttributes(name)
	if err != nil {
		return
	}
	_ = syscall.SetFileAttributes(name, attrs|syscall.FILE_ATTRIBUTE_HIDDEN)
}
