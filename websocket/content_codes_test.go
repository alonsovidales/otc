// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	filesmanager "github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/i18n"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// The codes the apps act on reach them however the error is wrapped: a
// disk that filled while a write went on is storage_full as much as a
// write refused before it started; a reply too large to send whole is
// too_large; anything else carries no code.
func TestContentCode(t *testing.T) {
	enospc := &fs.PathError{Op: "write", Path: "/mnt/storage/.blob-1", Err: syscall.ENOSPC}
	for _, c := range []struct {
		err  error
		want string
	}{
		{enospc, cCodeStorageFull},
		{fmt.Errorf("error loading file %q: %w", "/a.jpg", enospc), cCodeStorageFull},
		{filesmanager.TooLarge(i18n.DevFilesTooLargeToOpen(1 << 30)), cCodeTooLarge},
		{fmt.Errorf("wrapped: %w", filesmanager.TooLarge(i18n.DevSocialMediaTooLarge(1<<30))), cCodeTooLarge},
		{errors.New("EOF"), ""},
	} {
		resp := &pb.RespEnvelope{}
		contentCode(resp, c.err)
		if resp.ErrorCode != c.want {
			t.Errorf("%v: code %q, want %q", c.err, resp.ErrorCode, c.want)
		}
	}
}

// A photo too large to send whole: GetFile answers too_large with the
// device's words, in the request's language, and nothing is read.
func TestGetFileTooLargeAnswersItsCode(t *testing.T) {
	ch, mock := ownerWithLibrary(t)
	now := time.Now()
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = \\?").WithArgs("/scans/big.tif").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).
			AddRow(strings.Repeat("c", 64), "image/tiff", now, now, "/scans/big.tif", int64(4<<30)))
	resp, _ := ch.processMessage(&pb.ReqEnvelope{Id: 3, Lang: "es", Payload: &pb.ReqEnvelope_ReqGetFile{
		ReqGetFile: &pb.GetFile{Path: "/scans/big.tif"},
	}})
	if resp == nil || !resp.Error || resp.ErrorCode != cCodeTooLarge || resp.GetRespFile() != nil {
		t.Fatalf("got %+v", resp)
	}
	if !strings.Contains(resp.ErrorMessage, "demasiado grande") {
		t.Errorf("message %q, want the Spanish", resp.ErrorMessage)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Compress's codes: too many files at once (nothing read), no job, and a
// revert of a compressed file - each with the device's words in the
// request's language; the demo answers the reads and refuses the job.
func TestCompressCodes(t *testing.T) {
	ch, _ := ownerWithLibrary(t)
	resp, _ := ch.processMessage(&pb.ReqEnvelope{Id: 1, Lang: "es", Payload: &pb.ReqEnvelope_ReqGetCompressOptions{
		ReqGetCompressOptions: &pb.GetCompressOptions{Items: make([]*pb.CompressItem, 501)},
	}})
	if resp == nil || !resp.Error || resp.ErrorCode != cCodeTooMany || resp.ErrorMessage != i18n.DevFilesCompressTooMany().Render("es") {
		t.Errorf("501 items: %+v", resp)
	}
	resp, _ = ch.processMessage(&pb.ReqEnvelope{Id: 2, Payload: &pb.ReqEnvelope_ReqGetCompressJob{ReqGetCompressJob: &pb.GetCompressJob{}}})
	if resp == nil || !resp.Error || resp.ErrorCode != cCodeNotFound || resp.ErrorMessage != "there is no compress job" {
		t.Errorf("no job: %+v", resp)
	}
	resp = &pb.RespEnvelope{}
	editError(resp, "en", filesmanager.ErrOriginalRemoved)
	if resp.ErrorCode != cCodeOriginalRemoved || resp.ErrorMessage != "this file was compressed and its original deleted, so there is nothing to go back to" {
		t.Errorf("a revert of a compressed file: %+v", resp)
	}
	for req, want := range map[*pb.ReqEnvelope]demoRule{
		{Payload: &pb.ReqEnvelope_ReqListLargestFiles{}}:   demoAllow,
		{Payload: &pb.ReqEnvelope_ReqGetCompressOptions{}}: demoAllow,
		{Payload: &pb.ReqEnvelope_ReqGetCompressJob{}}:     demoAllow,
		{Payload: &pb.ReqEnvelope_ReqCompressFiles{}}:      demoRefuse,
	} {
		if got := demoRules[demoReq(req.Payload)]; got != want {
			t.Errorf("%T: demo rule %v, want %v", req.Payload, got, want)
		}
	}
}
