// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux

package service

import "errors"

var errUnsupported = errors.New("running as a service is only available on Linux; on Windows and macOS the tray app starts at login instead")

func Install(string) error { return errUnsupported }
func Uninstall() error     { return errUnsupported }
func Status() error        { return errUnsupported }

const Supported = false
