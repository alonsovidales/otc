// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import filesmanager "github.com/alonsovidales/otc/files_manager"

// What a device's memory allows (release 160; owner, 2026-11-01: 4 GB
// without Image Tagging, 2 GB without it and without other users, 8 GB
// everything - files_manager/lowmem.go): Settings and the users list say
// so, and turning tagging on or adding a user is refused with these codes.
const (
	cCodeTaggingUnavailable = "tagging_unavailable"
	cCodeUsersUnavailable   = "users_unavailable"
)

// deviceMemory is this device's memory profile; the tests set their own.
var deviceMemory = filesmanager.CurrentMemoryProfile
