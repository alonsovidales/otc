// SPDX-License-Identifier: AGPL-3.0-or-later

// Package service runs the sync engine as a systemd user service on Linux:
// the headless counterpart of the tray app, for a machine nobody logs into
// a desktop on. `loginctl enable-linger` makes the user's services start
// at boot rather than at login.
package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const unitName = "otc-sync.service"

func unitPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(base, "systemd", "user", unitName), nil
}

func systemctl(args ...string) error {
	cmd := exec.Command("systemctl", append([]string{"--user"}, args...)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr

	return cmd.Run()
}

// Install writes the unit, enables it and starts it now, and asks logind to
// keep this user's services running without a login session.
func Install(exe string) error {
	p, err := unitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { // perms: rwxr-xr-x
		return err
	}
	unit := fmt.Sprintf(`[Unit]
Description=Off The Cloud folder sync
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%s run
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`, exe)
	if err := os.WriteFile(p, []byte(unit), 0o644); err != nil { // perms: rw-r--r--
		return err
	}
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl("enable", "--now", unitName); err != nil {
		return err
	}
	// Best effort: needs polkit approval on some systems, and the service
	// still starts at login without it.
	linger := exec.Command("loginctl", "enable-linger", os.Getenv("USER"))
	if out, err := linger.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "note: could not enable lingering (%s); the service starts at login instead of at boot\n", string(out))
	}

	return nil
}

// Uninstall stops and removes the unit.
func Uninstall() error {
	_ = systemctl("disable", "--now", unitName)
	p, err := unitPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}

	return systemctl("daemon-reload")
}

// Status shows systemd's view of the service.
func Status() error { return systemctl("status", "--no-pager", unitName) }

// Supported is whether this platform has the service mode at all.
const Supported = true
