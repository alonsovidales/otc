// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"bytes"
	"database/sql/driver"
	"image"
	"image/jpeg"
	"math/rand"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/profile"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// photoAsPicked is a JPEG far over dao.MaxProfilePictureBytes, as an app
// before 2026-09-29 sent the owner's picture.
func photoAsPicked(t *testing.T) []byte {
	t.Helper()
	r := rand.New(rand.NewSource(2))
	img := image.NewRGBA(image.Rect(0, 0, 1200, 1600))
	for i := range img.Pix {
		img.Pix[i] = byte(r.Intn(256))
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// pictureMadeSmall matches an image argument that is a JPEG within the cap, its
// longest side 512.
type pictureMadeSmall struct{}

func (pictureMadeSmall) Match(v driver.Value) bool {
	b, _ := v.([]byte)
	if len(b) == 0 || len(b) > dao.MaxProfilePictureBytes {
		return false
	}
	c, err := jpeg.DecodeConfig(bytes.NewReader(b))
	return err == nil && c.Width == 384 && c.Height == 512
}

// The owner's picture sent as picked (several MB) is kept made small: every
// friend gets it again with each of the owner's posts and alerts. One the
// device can't read is refused, and the picture stays as it was.
func TestSetProfileKeepsThePictureSmall(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := dao.NewWithDB(db)
	ch := &connHandler{mg: &Manager{dao: d, profile: profile.InitFromPb(d, &pb.Profile{Name: "Kim", Image: []byte("before")})}}
	ch.setSession(newTestAuthenticatedSession(t))
	set := func(image []byte) *pb.RespEnvelope {
		resp, _ := ch.processMessage(&pb.ReqEnvelope{Id: 1, Payload: &pb.ReqEnvelope_ReqSetProfile{ReqSetProfile: &pb.Profile{Name: "Kim", Image: image}}})
		return resp
	}

	mock.ExpectExec("update `profile` set `name` = \\?, `image` = \\?, `text` = \\?").WithArgs("Kim", pictureMadeSmall{}, "").WillReturnResult(sqlmock.NewResult(0, 1))
	if resp := set(photoAsPicked(t)); resp.Error || !resp.GetRespAck().GetOk() {
		t.Fatalf("a large photo: %+v", resp)
	}
	kept := ch.mg.profile.Image()
	if resp := set([]byte("not a picture")); !resp.Error || resp.ErrorMessage != i18n.DevSocialPictureUnusable().Render("") {
		t.Fatalf("not a picture: %+v", resp)
	}
	if !bytes.Equal(ch.mg.profile.Image(), kept) {
		t.Error("a refused picture changed the one kept")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// At start, the owner's picture stored large (before the cap) is made
// small, and the friends' stored large are dropped - asked for again at the
// next sync.
func TestStoredPicturesFittedAtStart(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := dao.NewWithDB(db)
	mg := &Manager{dao: d, profile: profile.InitFromPb(d, &pb.Profile{Name: "Kim", Text: "hi", Image: photoAsPicked(t)})}
	mock.ExpectExec("update `profile` set `name` = \\?, `image` = \\?, `text` = \\?").WithArgs("Kim", pictureMadeSmall{}, "hi").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("update `social_friendship` set `image` = null where length\\(`image`\\) > \\?").WithArgs(dao.MaxProfilePictureBytes).WillReturnResult(sqlmock.NewResult(0, 3))
	mg.fitStoredPictures()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if n := len(mg.profile.Image()); n > dao.MaxProfilePictureBytes {
		t.Fatalf("kept %d bytes", n)
	}
	// Small already: nothing to do but the friends'.
	mock.ExpectExec("update `social_friendship` set `image` = null").WillReturnResult(sqlmock.NewResult(0, 0))
	mg.fitStoredPictures()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
