// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"bytes"
	"database/sql/driver"
	"image"
	"image/jpeg"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/i18n"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// personCoverReq asks for person's face: a photo of theirs (hash), a
// picture (image, even empty: imageSet) or neither.
func personCoverReq(person, lang, hash string, image []byte, imageSet bool) *pb.ReqEnvelope {
	req := &pb.SetPersonCover{PersonId: person}
	switch {
	case hash != "":
		req.Cover = &pb.SetPersonCover_Hash{Hash: hash}
	case imageSet:
		req.Cover = &pb.SetPersonCover_Image{Image: image}
	}
	return &pb.ReqEnvelope{Id: 9, Lang: lang, Payload: &pb.ReqEnvelope_ReqSetPersonCover{ReqSetPersonCover: req}}
}

func smallJPEG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 64, 64)), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// The face chosen for a person: a photo of theirs, a picture of the
// owner's own or neither, each answered with the generic Ack; what can't
// be done is refused in the request's language.
func TestSetPersonCoverAnswers(t *testing.T) {
	ch, mock := ownerWithLibrary(t)
	ack := func(name string, resp *pb.RespEnvelope) {
		t.Helper()
		if a, ok := resp.GetPayload().(*pb.RespEnvelope_RespAck); resp.Error || !ok || !a.RespAck.Ok {
			t.Errorf("%s: %+v", name, resp)
		}
	}
	refused := func(name string, resp *pb.RespEnvelope, want i18n.Msg, lang string) {
		t.Helper()
		if resp == nil || !resp.Error || resp.ErrorMessage != want.Render(lang) {
			t.Errorf("%s: %+v, want %q", name, resp, want.Render(lang))
		}
	}

	refused("no person", mustProcess(t, ch, personCoverReq("", "es", "", nil, false)), i18n.DevFilesPersonMissing(), "es")
	refused("not a JPEG", mustProcess(t, ch, personCoverReq("p1", "fr", "", []byte("not a picture"), true)), i18n.DevFilesPersonCoverImage(), "fr")
	refused("empty picture", mustProcess(t, ch, personCoverReq("p1", "", "", nil, true)), i18n.DevFilesPersonCoverImage(), "")

	// A photo of theirs: its face.
	mock.ExpectQuery("select `id` from `faces` where `person_id` = \\? and `hash` = \\?").WithArgs("p1", "A").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("f1"))
	mock.ExpectQuery("select 1 from `people` where `id` = \\?").WithArgs("p1").WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectExec("update `people` set `cover_face_id` = \\?, `cover_image` = null, `cover_chosen` = 1 where `id` = \\?").
		WithArgs("f1", "p1").WillReturnResult(sqlmock.NewResult(0, 1))
	ack("a photo of theirs", mustProcess(t, ch, personCoverReq("p1", "", "A", nil, false)))

	// A photo without their face.
	mock.ExpectQuery("select `id` from `faces` where `person_id` = \\? and `hash` = \\?").WithArgs("p1", "B").
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery("select 1 from `people` where `id` = \\?").WithArgs("p1").WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	refused("no face of theirs", mustProcess(t, ch, personCoverReq("p1", "de", "B", nil, false)), i18n.DevFilesPersonCoverNoFace(), "de")

	// A picture of the owner's own: stored encrypted, never as sent.
	pic := smallJPEG(t)
	mock.ExpectQuery("select 1 from `people` where `id` = \\?").WithArgs("p1").WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectExec("update `people` set `cover_image` = \\?, `cover_chosen` = 1 where `id` = \\?").
		WithArgs(notEqualBytes(pic), "p1").WillReturnResult(sqlmock.NewResult(0, 1))
	ack("a picture", mustProcess(t, ch, personCoverReq("p1", "", "", pic, true)))

	// Neither, for a person gone meanwhile.
	mock.ExpectQuery("select 1 from `people` where `id` = \\?").WithArgs("gone").WillReturnRows(sqlmock.NewRows([]string{"1"}))
	refused("gone", mustProcess(t, ch, personCoverReq("gone", "it", "", nil, false)), i18n.DevFilesPersonGone(), "it")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// notEqualBytes matches any []byte argument but b: the picture must be
// encrypted before it is stored.
type notEqualBytes []byte

func (n notEqualBytes) Match(v driver.Value) bool {
	got, ok := v.([]byte)
	return ok && len(got) > 0 && !bytes.Equal(got, n)
}
