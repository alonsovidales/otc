// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux && !darwin && !windows

package engine

// volumeLimits: not read on this system (nothing is refused for room).
func volumeLimits(string) (free uint64, maxFile int64, ok bool) { return 0, 0, false }
