// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux && !windows

package flasher

import (
	"errors"
	"os"
)

// On macOS the App Store app's wizard hands the image to Raspberry Pi
// Imager instead (a sandboxed app can't write raw disks); otc-sync on a Mac
// is a development build only.
var errUnsupported = errors.New("writing SD cards is supported on Windows and Linux")

func ListDisks() ([]Disk, error)                     { return nil, errUnsupported }
func openDisk(Disk) (device, error)                  { return nil, errUnsupported }
func dropCache(device)                               {}
func eject(Disk)                                     {}
func openStatus(path string) (*os.File, error)       { return nil, errUnsupported }
func elevate(string, []string) (func() error, error) { return nil, errUnsupported }
