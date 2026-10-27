// SPDX-License-Identifier: AGPL-3.0-or-later

// Package diskspace is how full a filesystem is, as the device's own
// processes see it: what they can still write is the blocks available to
// an unprivileged user (statfs f_bavail), in the filesystem's fragment
// size (f_frsize, the unit every block count is in). The free blocks
// (f_bfree) also hold ext4's reserve for root - 5% of the disk unless
// tuned, ~200 GB of a 4 TB mirror - which neither otc nor MariaDB can use:
// a device counting it as free said it had room while uploads failed.
package diskspace

import "errors"

// Usage is a filesystem's space, in bytes.
type Usage struct {
	// Total is the filesystem's size, the reserve for root included.
	Total uint64
	// Used is what is written on it (total minus the free blocks).
	Used uint64
	// Avail is what the device can still write.
	Avail uint64
}

// Size is the space the device can use: what is written and what it can
// still write - Total without the part reserved for root. This is the
// size the apps show (Status.raid_size), so that size minus used is
// exactly what can still be written.
func (u Usage) Size() uint64 { return u.Used + u.Avail }

// Percent is how full the filesystem is for the device, as df's Use%:
// what is written out of what it can use, rounded up (a disk with a byte
// left over 99% is 100% full for a 4 GB video).
func (u Usage) Percent() int {
	size := u.Size()
	if size == 0 {
		return 0
	}
	return int((u.Used*100 + size - 1) / size)
}

// Of is the filesystem holding path's usage.
func Of(path string) (Usage, error) { return of(path) }

// fromStatfs is a statfs's counts - all, free and available blocks of
// unit bytes each - in bytes. What is available never counts for more
// than what is free (a filesystem that estimates it may say otherwise).
func fromStatfs(blocks, bfree, bavail, unit uint64) Usage {
	bfree = min(bfree, blocks)
	bavail = min(bavail, bfree)
	return Usage{Total: blocks * unit, Used: (blocks - bfree) * unit, Avail: bavail * unit}
}

var errUnsupported = errors.New("disk space is not known on this system") // i18n-ignore: logged only, never on a device
