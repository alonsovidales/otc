// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package engine

import (
	"math"
	"strings"

	"golang.org/x/sys/windows"
)

// volumeLimits is the free space on dir's volume and the largest file it
// can hold (FAT and FAT32: 4 GiB less a byte), when the system says.
func volumeLimits(dir string) (free uint64, maxFile int64, ok bool) {
	name, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, 0, false
	}
	var total, totalFree uint64
	if windows.GetDiskFreeSpaceEx(name, &free, &total, &totalFree) != nil {
		return 0, 0, false
	}
	maxFile = math.MaxInt64
	vol := make([]uint16, windows.MAX_PATH+1)
	if windows.GetVolumePathName(name, &vol[0], uint32(len(vol))) == nil {
		fs := make([]uint16, windows.MAX_PATH+1)
		if windows.GetVolumeInformation(&vol[0], nil, 0, nil, nil, nil, &fs[0], uint32(len(fs))) == nil {
			if n := strings.ToUpper(windows.UTF16ToString(fs)); n == "FAT" || n == "FAT32" {
				maxFile = 1<<32 - 1
			}
		}
	}

	return free, maxFile, true
}
