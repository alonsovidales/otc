// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !unix

package hardening

import "io/fs"

// ownedByUs: no owner to check off Unix, where the device never runs.
func ownedByUs(fs.FileInfo) bool { return true }
