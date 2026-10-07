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
// (where this Go build only runs during development). An early failure -
// no browser set up, say - is reported; the browser itself isn't waited
// for, since xdg-open may run it in the foreground until it is closed.
func open(addr string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, addr)
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
