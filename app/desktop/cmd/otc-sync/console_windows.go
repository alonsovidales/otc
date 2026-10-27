// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// attachConsole gives a command typed in a terminal its output. otc-sync is
// built as a GUI program (-H windowsgui, so the tray opens no console
// window), and Windows hands a GUI program no standard handles: `otc-sync
// status` from cmd or PowerShell printed nothing. It attaches to the
// terminal it was started from, if any, and writes there - unless its
// output was redirected to a file or a pipe, which it keeps.
func attachConsole() {
	attach := windows.NewLazySystemDLL("kernel32.dll").NewProc("AttachConsole")
	const attachParentProcess = ^uintptr(0) // (DWORD)-1
	if r, _, _ := attach.Call(attachParentProcess); r == 0 {
		return // not started from a terminal
	}
	usable := func(std uint32) bool {
		h, err := windows.GetStdHandle(std)
		if err != nil || h == 0 || h == windows.InvalidHandle {
			return false
		}
		t, err := windows.GetFileType(h)

		return err == nil && t != windows.FILE_TYPE_UNKNOWN
	}
	if !usable(windows.STD_OUTPUT_HANDLE) || !usable(windows.STD_ERROR_HANDLE) {
		if out, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
			if !usable(windows.STD_OUTPUT_HANDLE) {
				os.Stdout = out
			}
			if !usable(windows.STD_ERROR_HANDLE) {
				os.Stderr = out
			}
		}
	}
	if !usable(windows.STD_INPUT_HANDLE) {
		if in, err := os.OpenFile("CONIN$", os.O_RDWR, 0); err == nil {
			os.Stdin = in
		}
	}
}
