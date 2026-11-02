// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	filesmanager "github.com/alonsovidales/otc/files_manager"
	"google.golang.org/protobuf/proto"
)

// What a device's memory allows (release 160; owner, 2026-11-01: 2 GB
// without Image Tagging and without other users, 8 GB everything; since
// release 164, issue #209, "Opt-in on 4 GB": a 4 GB device may turn Image
// Tagging on, off until its user does - files_manager/lowmem.go): Settings
// and the users list say so, and turning tagging on or adding a user is
// refused with these codes.
const (
	cCodeTaggingUnavailable = "tagging_unavailable"
	cCodeUsersUnavailable   = "users_unavailable"
)

// deviceMemory is this device's memory profile; the tests set their own.
var deviceMemory = filesmanager.CurrentMemoryProfile

// taggingHeavy is Settings.image_tagging_heavy: true where Image Tagging
// costs this device more than it is comfortable with (a 4 GB board, or
// [otc] low-memory = on), unset elsewhere.
func taggingHeavy() *bool {
	if deviceMemory().TaggingHeavy() {
		return proto.Bool(true)
	}
	return nil
}
