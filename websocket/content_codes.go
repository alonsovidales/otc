// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"errors"

	filesmanager "github.com/alonsovidales/otc/files_manager"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// The error codes of content the device can't take, or can't send in one
// piece (files_manager's freespace.go and membudget.go):
//
//   - "storage_full": a write refused before it started because the disk
//     can't hold it and keep the space MariaDB needs, or one that failed as
//     the disk filled (ENOSPC) - an upload, a share link, a gallery, a
//     post's video. A sync client stops sending until there is room
//     instead of retrying the file.
//   - "too_large": a reply too large to send whole (GetFile,
//     GetPublicationMedia): the apps read the file in parts (ReadFile),
//     stream it (GetMediaURL) or keep showing its thumbnail; a friend's
//     device keeps the thumbnail of a post's file.
const (
	cCodeStorageFull = "storage_full"
	cCodeTooLarge    = "too_large"
)

// contentCode gives resp the error code err calls for, if one of these.
func contentCode(resp *pb.RespEnvelope, err error) {
	switch {
	case filesmanager.IsStorageFull(err):
		resp.ErrorCode = cCodeStorageFull
	case errors.Is(err, filesmanager.ErrTooLarge):
		resp.ErrorCode = cCodeTooLarge
	}
}
