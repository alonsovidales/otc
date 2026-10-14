// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	pb "github.com/alonsovidales/otc/proto/generated"
	"google.golang.org/protobuf/proto"
)

func delFileReq(path string, everyCopy *bool, lang string) *pb.ReqEnvelope {
	return &pb.ReqEnvelope{Id: 11, Lang: lang, Payload: &pb.ReqEnvelope_ReqDelFile{ReqDelFile: &pb.DelFile{Path: path, EveryCopy: everyCopy}}}
}

// Issue #201: a DelFile whose hash (the content the client showed) is no
// longer what the path holds deletes nothing and answers error_code
// "changed", with or without every_copy, in the request's language.
func TestDelFileOfChangedContentAnswersChanged(t *testing.T) {
	ch, mock := ownerWithLibrary(t)
	now := time.Now()
	shown, current := strings.Repeat("a", 64), strings.Repeat("b", 64)
	byPath := "select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = \\?"
	for _, c := range []struct {
		everyCopy *bool
		lang      string
		want      string
	}{
		{proto.Bool(true), "", "this file has changed since it was shown, so nothing was deleted: refresh and try again"},
		{nil, "", "this file has changed since it was shown, so nothing was deleted: refresh and try again"},
		{proto.Bool(true), "es", "este archivo ha cambiado desde que se mostró, así que no se ha eliminado nada: actualiza y vuelve a intentarlo"},
		{nil, "fr", "ce fichier a changé depuis son affichage, rien n’a donc été supprimé\u00a0: actualisez et réessayez"},
	} {
		mock.ExpectQuery(byPath).WithArgs("/Phone/a.jpg").
			WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).AddRow(current, "image/jpeg", now, now, "/Phone/a.jpg", 1))
		req := delFileReq("/Phone/a.jpg", c.everyCopy, c.lang)
		req.GetReqDelFile().Hash = proto.String(shown)
		resp, closeConn := ch.processMessage(req)
		if closeConn || resp == nil || !resp.Error || resp.Id != 11 || resp.ErrorCode != "changed" || resp.ErrorMessage != c.want {
			t.Errorf("every copy %v, lang %q: %+v", c.everyCopy, c.lang, resp)
		}
	}
	// Nothing else ran: no copies, no upload-only folders, no delete.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// Issue #201: DelFile with every_copy deletes every copy or nothing; a copy
// in an upload-only folder refuses it with copy_upload_only and a message
// naming that folder, in the request's language. Without the flag (older
// apps, "only this one") the path alone, as before: its own upload-only
// refusal, no copies looked up.
func TestDelFileEveryCopyAnswers(t *testing.T) {
	ch, mock := ownerWithLibrary(t)
	now := time.Now()
	h := strings.Repeat("a", 64)
	byPath := "select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = \\?"
	expectRefusal := func() {
		mock.ExpectQuery(byPath).WithArgs("/Phone/a.jpg").
			WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).AddRow(h, "image/jpeg", now, now, "/Phone/a.jpg", 1))
		mock.ExpectQuery("select `path` from `files` where `hash` = \\?").WithArgs(h).
			WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow("/Backups/Phone/a.jpg").AddRow("/Phone/a.jpg"))
		mock.ExpectQuery("select `path` from `upload_only_folders`").WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow("/Backups/"))
	}
	plain := func(s string) string { return strings.NewReplacer("⁨", "", "⁩", "").Replace(s) }

	expectRefusal()
	resp, closeConn := ch.processMessage(delFileReq("/Phone/a.jpg", proto.Bool(true), ""))
	if closeConn || resp == nil || !resp.Error || resp.Id != 11 || resp.ErrorCode != "copy_upload_only" ||
		plain(resp.ErrorMessage) != "a copy is in the upload-only folder /Backups: no copy was deleted" {
		t.Fatalf("every copy, one upload only: %+v", resp)
	}
	expectRefusal()
	resp, _ = ch.processMessage(delFileReq("/Phone/a.jpg", proto.Bool(true), "es"))
	if resp == nil || resp.ErrorCode != "copy_upload_only" || !strings.Contains(plain(resp.ErrorMessage), "solo subida /Backups") {
		t.Errorf("in Spanish: %+v", resp)
	}

	// Without the flag, or with it false: DelPath, as before.
	for _, flag := range []*bool{nil, proto.Bool(false)} {
		mock.ExpectQuery(byPath).WithArgs("/Backups/Phone/a.jpg").
			WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).AddRow(h, "image/jpeg", now, now, "/Backups/Phone/a.jpg", 1))
		mock.ExpectQuery("select `path` from `upload_only_folders`").WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow("/Backups/"))
		resp, _ = ch.processMessage(delFileReq("/Backups/Phone/a.jpg", flag, ""))
		if resp == nil || resp.ErrorCode != "upload_only" || resp.ErrorMessage != "this folder is upload only: nothing in it can be deleted" {
			t.Errorf("flag %v: %+v", flag, resp)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
