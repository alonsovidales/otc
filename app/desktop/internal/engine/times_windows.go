// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package engine

import (
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// creationTime is the file's creation time as NTFS keeps it.
func creationTime(_ string, fi os.FileInfo) time.Time {
	if d, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok {
		return time.Unix(0, d.CreationTime.Nanoseconds())
	}

	return fi.ModTime()
}

// setFileTimes sets both the creation and the last-write time (issue #134)
// - Windows is the one platform where a creation time can be set.
func setFileTimes(path string, created, modified time.Time) error {
	if err := os.Chtimes(path, modified, modified); err != nil {
		return err
	}
	if created.IsZero() {
		return nil
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.FILE_WRITE_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	c := windows.NsecToFiletime(created.UnixNano())
	m := windows.NsecToFiletime(modified.UnixNano())

	return windows.SetFileTime(h, &c, nil, &m)
}
