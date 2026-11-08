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

// The error codes of Compress (files_manager/compress.go, compress_job.go)
// and of what it leaves:
//
//   - "too_many": GetCompressOptions or CompressFiles with more than 500
//     files; nothing was looked at.
//   - "not_found": GetCompressJob of no job (none since the device
//     started, or another id).
//   - "original_removed": RevertImage of a compressed file, whose original
//     the device deleted - there is nothing to go back to.
//
// A file the options can't compress isn't an error: its CompressOption
// says why (reason_code, "changed" among them).
const (
	cCodeTooMany         = "too_many"
	cCodeNotFound        = "not_found"
	cCodeOriginalRemoved = "original_removed"
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
	case errors.Is(err, filesmanager.ErrOriginalRemoved):
		resp.ErrorCode = cCodeOriginalRemoved
	case filesmanager.IsCutTooShort(err):
		resp.ErrorCode = cCodeTooShort
	default:
		log.Error("an edit failed:", err)
		contentCode(resp, err)
	}
}

// compressError fills resp with a refusal of a compress request
// (GetCompressOptions, CompressFiles, GetCompressJob): its code, and its
// message in lang.
func compressError(resp *pb.RespEnvelope, lang string, err error) {
	resp.Error = true
	resp.ErrorMessage = errText(lang, err)
	switch {
	case filesmanager.IsCompressTooMany(err):
		resp.ErrorCode = cCodeTooMany
	case filesmanager.IsCompressNoJob(err):
		resp.ErrorCode = cCodeNotFound
	default:
		log.Error("a compress request failed:", err)
		contentCode(resp, err)
	}
}
