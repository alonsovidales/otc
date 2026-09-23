// SPDX-License-Identifier: AGPL-3.0-or-later

package autostart

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Linux desktops start whatever is in ~/.config/autostart at login (the
// XDG autostart spec, honoured by GNOME, KDE, XFCE and the rest).
func desktopFile() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(base, "autostart", "otc-sync.desktop"), nil
}

// Enable registers the tray app to start at login.
func Enable(exe string) error {
	p, err := desktopFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { // perms: rwxr-xr-x
		return err
	}
	content := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=Off The Cloud Sync
Comment=Keeps folders in sync with your Off The Cloud device
Exec="%s" tray
Terminal=false
X-GNOME-Autostart-enabled=true
`, exe)

	return os.WriteFile(p, []byte(content), 0o644) // perms: rw-r--r--
}

// Disable removes the registration.
func Disable() error {
	p, err := desktopFile()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return nil
}

// Enabled reports whether the registration exists.
func Enabled() (bool, error) {
	p, err := desktopFile()
	if err != nil {
		return false, err
	}
	_, err = os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}

	return err == nil, err
}
