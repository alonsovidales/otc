// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"bytes"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	filesmanager "github.com/alonsovidales/otc/files_manager"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/settings"
)

// photoFacesOwner is a signed-in owner whose settings say whether face
// recognition is on.
func photoFacesOwner(t *testing.T, faces bool) (*connHandler, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	mock.ExpectQuery("select `subdomain`, `device_uuid`, `bridge_secret` from `settings`").
		WillReturnRows(sqlmock.NewRows([]string{"subdomain", "device_uuid", "bridge_secret"}).AddRow("me.off-the.cloud", "dev-uuid", "bridge-secret"))
	mock.ExpectQuery("select `face_recognition_enabled` from `settings`").
		WillReturnRows(sqlmock.NewRows([]string{"face_recognition_enabled"}).AddRow(faces))
	mock.ExpectQuery("select `image_tagging_enabled` from `settings`").
		WillReturnRows(sqlmock.NewRows([]string{"image_tagging_enabled"}).AddRow(true))
	mock.ExpectQuery("select `language`, `last_ui_language` from `settings`").
		WillReturnRows(sqlmock.NewRows([]string{"language", "last_ui_language"}).AddRow("", ""))
	d := dao.NewWithDB(db)
	st, err := settings.Init(d)
	if err != nil {
		t.Fatal(err)
	}
	ch := &connHandler{mg: &Manager{dao: d, settings: st, filesManager: filesmanager.NewWithDAO(d)}}
	ch.setSession(newTestAuthenticatedSession(t))
	return ch, mock
}

func photoFacesReq(path string) *pb.ReqEnvelope {
	return &pb.ReqEnvelope{Id: 5, Payload: &pb.ReqEnvelope_ReqGetPhotoFaces{ReqGetPhotoFaces: &pb.GetPhotoFaces{Path: path}}}
}

// The viewer's Info lists a photo's faces, left to right, with their
// people and their crops decrypted; none while face recognition is off,
// and a photo that isn't there is an error.
func TestGetPhotoFaces(t *testing.T) {
	ch, mock := photoFacesOwner(t, false)
	resp := mustProcess(t, ch, photoFacesReq("/Phone/a.jpg"))
	if got := resp.GetRespPhotoFaces(); resp.Error || got == nil || got.FaceRecognition || len(got.Faces) != 0 {
		t.Errorf("face recognition off: %+v", resp)
	}

	ch, mock = photoFacesOwner(t, true)
	ses := ch.getSession()
	testTime := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	crop := []byte("a small jpeg")
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = \\?").WithArgs("/Phone/a.jpg").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).AddRow("H", "image/jpeg", testTime, testTime, "/Phone/a.jpg", 10))
	mock.ExpectQuery("select `f`.`person_id`, coalesce\\(`p`.`name`, ''\\), `f`.`thumbnail`.* where `f`.`hash` = \\? and `f`.`person_id` <> '' order by `f`.`bbox_x`").WithArgs("H").
		WillReturnRows(sqlmock.NewRows([]string{"person_id", "name", "thumbnail", "bbox_x", "bbox_y", "bbox_w", "bbox_h"}).
			AddRow("ana", "Ana", ses.Encrypt(crop), 10, 20, 30, 40).
			AddRow("p2", "", []byte("not sealed"), 90, 20, 30, 40))
	resp = mustProcess(t, ch, photoFacesReq("/Phone/a.jpg"))
	got := resp.GetRespPhotoFaces()
	if resp.Error || got == nil || !got.FaceRecognition || got.Hash != "H" || len(got.Faces) != 2 {
		t.Fatalf("faces: %+v", resp)
	}
	if f := got.Faces[0]; f.PersonId != "ana" || f.Name != "Ana" || !bytes.Equal(f.Thumbnail, crop) || f.X != 10 || f.Y != 20 || f.W != 30 || f.H != 40 {
		t.Errorf("Ana's face: %+v", f)
	}
	if f := got.Faces[1]; f.PersonId != "p2" || f.Name != "" || f.Thumbnail != nil {
		t.Errorf("an unnamed face with a crop that won't open: %+v", f)
	}

	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = \\?").WithArgs("/gone.jpg").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}))
	if resp := mustProcess(t, ch, photoFacesReq("/gone.jpg")); !resp.Error {
		t.Errorf("a photo that isn't there: %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
