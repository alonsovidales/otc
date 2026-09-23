// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux && !windows

package autostart

import "errors"

// The macOS client is the Swift app, which registers itself with
// SMAppService; this Go build only runs there during development.
var errUnsupported = errors.New("start at login is handled by the native app on this platform")

func Enable(string) error    { return errUnsupported }
func Disable() error         { return errUnsupported }
func Enabled() (bool, error) { return false, nil }
