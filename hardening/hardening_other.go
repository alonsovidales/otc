// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux

package hardening

// The device runs on Linux; elsewhere (a development Mac) there is nothing
// to lock or redirect.
func LockMemory()    {}
func SecureTempDir() {}
