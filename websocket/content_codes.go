// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"errors"

	"github.com/alonsovidales/otc/devtext"
	filesmanager "github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/log"
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

// editError fills resp with an edit's or a revert's refusal (EditImage,
// EditVideo, RevertImage): its code, and its message in lang.
func editError(resp *pb.RespEnvelope, lang string, err error) {
	var changed *filesmanager.ContentChangedError
	resp.Error = true
	resp.ErrorMessage = devtext.Text(lang, err)
	switch {
	case errors.As(err, &changed):
		log.Info("an edit was refused: the path holds other content than the client showed")
		resp.ErrorCode = cCodeContentChanged
	case filesmanager.IsNotEditable(err):
		log.Info("an edit was refused: the content can't be edited")
		resp.ErrorCode = cCodeNotEditable
	case filesmanager.IsNotEdited(err):
		resp.ErrorCode = cCodeNotEdited
	case filesmanager.IsCutTooShort(err):
		resp.ErrorCode = cCodeTooShort
	default:
		log.Error("an edit failed:", err)
		contentCode(resp, err)
	}
}
