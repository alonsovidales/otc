// SPDX-License-Identifier: AGPL-3.0-or-later

package browser

import "golang.org/x/sys/windows"

// open asks the shell to open the address, which starts the default
// browser - no child process of ours, and no console window.
func open(addr string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(addr)
	if err != nil {
		return err
	}

	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}
