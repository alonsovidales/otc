// SPDX-License-Identifier: AGPL-3.0-or-later

package autostart

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

// Windows starts at login whatever the current user's Run key lists - the
// same mechanism the Dropbox/OneDrive tray apps use. No admin rights.
const (
	runKey    = `Software\Microsoft\Windows\CurrentVersion\Run`
	valueName = "OffTheCloudSync"
)

// Enable registers the tray app to start at login.
func Enable(exe string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	return k.SetStringValue(valueName, `"`+exe+`" tray`)
}

// Disable removes the registration.
func Disable() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(valueName); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}

	return nil
}

// Enabled reports whether the registration exists.
func Enabled() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false, err
	}
	defer k.Close()
	_, _, err = k.GetStringValue(valueName)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}

	return err == nil, err
}
