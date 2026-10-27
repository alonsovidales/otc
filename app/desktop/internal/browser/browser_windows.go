// SPDX-License-Identifier: AGPL-3.0-or-later

package browser

import "golang.org/x/sys/windows"

// open asks the shell to open the address, which starts the default
// browser - no child process of ours, and no console window.
func open(addr string) error {
	return shellExecute("open", addr)
}

// openFolder asks the shell to explore the folder: File Explorer shows it.
// The "explore" verb only opens folders, never runs a file.
func openFolder(path string) error {
	return shellExecute("explore", path)
}

// openFile asks the shell to open a text file of ours (OpenFile checked
// that it is one): Notepad, or the app set for .log files.
func openFile(path string) error {
	return shellExecute("open", path)
}

func shellExecute(verb, target string) error {
	v, err := windows.UTF16PtrFromString(verb)
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}

	return windows.ShellExecute(0, v, file, nil, nil, windows.SW_SHOWNORMAL)
}
