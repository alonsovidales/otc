// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !unix

package updater

// runLockHeld: the device runs on Linux; elsewhere there is no update.sh
// to find, and doubt keeps a "running" status as written.
func runLockHeld(string) bool { return true }
