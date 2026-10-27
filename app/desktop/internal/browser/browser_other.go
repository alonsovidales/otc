// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package browser

import (
	"fmt"
	"os/exec"
	"runtime"
	"time"
)

// open hands the address to the desktop: xdg-open on Linux, open on a Mac
// (where this Go build only runs during development).
func open(addr string) error {
	return start(addr)
}

// openFolder hands a folder to the desktop the same way: xdg-open shows it
// in the file manager the desktop has for folders (the Mac's open, in
// Finder).
func openFolder(path string) error {
	return start(path)
}

// openFile opens a file the same way, in the app the desktop has for it.
func openFile(path string) error {
	return start(path)
}

// start runs the desktop's opener on arg. An early failure - no browser
// or file manager set up, say - is reported; what it opens isn't waited
// for, since xdg-open may run it in the foreground until it is closed.
func start(arg string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, arg)
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}

		return nil
	case <-time.After(3 * time.Second):
		return nil
	}
}
