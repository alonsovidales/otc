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
// people and their crops decrypted, then the people added to it by hand,
// in the order added, with no box and the face People shows for them (a
// person whose face is listed isn't again); none while face recognition
// is off, and a photo that isn't there is an error. The device always
// says it takes AddToPerson.
func TestGetPhotoFaces(t *testing.T) {
	ch, mock := photoFacesOwner(t, false)
	resp := mustProcess(t, ch, photoFacesReq("/Phone/a.jpg"))
	if got := resp.GetRespPhotoFaces(); resp.Error || got == nil || got.FaceRecognition || len(got.Faces) != 0 || !got.AddsPeople {
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
	bo := []byte("Bo's face")
	mock.ExpectQuery("select `b`.`person_id`, `p`.`name`, .* from `people_by_hand` as `b` join `people` as `p` on `p`.`id` = `b`.`person_id` where `b`.`hash` = \\? order by `b`.`created`").WithArgs("H").
		WillReturnRows(sqlmock.NewRows([]string{"person_id", "name", "thumbnail"}).
			AddRow("bo", "Bo", ses.Encrypt(bo)).
			AddRow("ana", "Ana", ses.Encrypt(crop)). // a face of hers is listed
			AddRow("p3", "", nil))
	resp = mustProcess(t, ch, photoFacesReq("/Phone/a.jpg"))
	got := resp.GetRespPhotoFaces()
	if resp.Error || got == nil || !got.FaceRecognition || got.Hash != "H" || len(got.Faces) != 4 || !got.AddsPeople {
		t.Fatalf("faces: %+v", resp)
	}
	if f := got.Faces[0]; f.PersonId != "ana" || f.Name != "Ana" || !bytes.Equal(f.Thumbnail, crop) || f.X != 10 || f.Y != 20 || f.W != 30 || f.H != 40 || f.ByHand {
		t.Errorf("Ana's face: %+v", f)
	}
	if f := got.Faces[1]; f.PersonId != "p2" || f.Name != "" || f.Thumbnail != nil || f.ByHand {
		t.Errorf("an unnamed face with a crop that won't open: %+v", f)
	}
	if f := got.Faces[2]; f.PersonId != "bo" || f.Name != "Bo" || !bytes.Equal(f.Thumbnail, bo) || !f.ByHand || f.X != 0 || f.Y != 0 || f.W != 0 || f.H != 0 {
		t.Errorf("Bo, added by hand: %+v", f)
	}
	if f := got.Faces[3]; f.PersonId != "p3" || f.Name != "" || f.Thumbnail != nil || !f.ByHand {
		t.Errorf("an unnamed person added by hand, with no face to show: %+v", f)
	}

	// A video: no face is looked for, the people added by hand are listed.
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = \\?").WithArgs("/Phone/v.mp4").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).AddRow("V", "video/mp4", testTime, testTime, "/Phone/v.mp4", 10))
	mock.ExpectQuery("from `faces` as `f`").WithArgs("V").
		WillReturnRows(sqlmock.NewRows([]string{"person_id", "name", "thumbnail", "bbox_x", "bbox_y", "bbox_w", "bbox_h"}))
	mock.ExpectQuery("from `people_by_hand` as `b`").WithArgs("V").
		WillReturnRows(sqlmock.NewRows([]string{"person_id", "name", "thumbnail"}).AddRow("bo", "Bo", ses.Encrypt(bo)))
	resp = mustProcess(t, ch, photoFacesReq("/Phone/v.mp4"))
	if got := resp.GetRespPhotoFaces(); resp.Error || got == nil || got.Hash != "V" || len(got.Faces) != 1 || !got.Faces[0].ByHand || got.Faces[0].PersonId != "bo" || !got.AddsPeople {
		t.Errorf("a video: %+v", resp)
	}

	// Not a photo or a video: no one can be added to it.
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = \\?").WithArgs("/Docs/a.pdf").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).AddRow("D", "application/pdf", testTime, testTime, "/Docs/a.pdf", 10))
	mock.ExpectQuery("from `faces` as `f`").WithArgs("D").
		WillReturnRows(sqlmock.NewRows([]string{"person_id", "name", "thumbnail", "bbox_x", "bbox_y", "bbox_w", "bbox_h"}))
	mock.ExpectQuery("from `people_by_hand` as `b`").WithArgs("D").
		WillReturnRows(sqlmock.NewRows([]string{"person_id", "name", "thumbnail"}))
	resp = mustProcess(t, ch, photoFacesReq("/Docs/a.pdf"))
	if got := resp.GetRespPhotoFaces(); resp.Error || got == nil || got.Hash != "D" || len(got.Faces) != 0 || got.AddsPeople {
		t.Errorf("a PDF: %+v", resp)
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
