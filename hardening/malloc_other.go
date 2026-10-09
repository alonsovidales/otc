// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux || !cgo

package hardening

// ReturnFreedMemory is glibc's (malloc_linux.go); elsewhere there is
// nothing to set.
func ReturnFreedMemory() {}
