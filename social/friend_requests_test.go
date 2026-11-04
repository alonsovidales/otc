// SPDX-License-Identifier: AGPL-3.0-or-later

package social

import (
	"bytes"
	"database/sql/driver"
	"image"
	"image/jpeg"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/settings"
	"github.com/alonsovidales/otc/wsframe"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

// largeJPEG is a JPEG of noise, far over dao.MaxProfilePictureBytes: a
// photo as an app before 2026-09-29 sent it, or a stranger's 7.5 MB.
func largeJPEG(t *testing.T) []byte {
	t.Helper()
	r := rand.New(rand.NewSource(1))
	img := image.NewRGBA(image.Rect(0, 0, 1600, 1200))
	for i := range img.Pix {
		img.Pix[i] = byte(r.Intn(256))
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	if buf.Len() <= dao.MaxProfilePictureBytes {
		t.Fatalf("a test picture of %d bytes is within the cap", buf.Len())
	}
	return buf.Bytes()
}

// pictureArg matches an image argument: none (empty), or a JPEG within
// the cap.
type pictureArg struct{ none bool }

func (p pictureArg) Match(v driver.Value) bool {
	b, _ := v.([]byte)
	if p.none {
		return len(b) == 0
	}
	if len(b) == 0 || len(b) > dao.MaxProfilePictureBytes {
		return false
	}
	_, err := jpeg.DecodeConfig(bytes.NewReader(b))
	return err == nil
}

// answeringDevice makes every device this test dials one that answers
// each request with answer's reply.
func answeringDevice(t *testing.T, answer func(*pb.ReqEnvelope) *pb.RespEnvelope) {
	t.Helper()
	up := gorilla.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			var req pb.ReqEnvelope
			if proto.Unmarshal(data, &req) != nil {
				return
			}
			b, _ := proto.Marshal(answer(&req))
			if c.WriteMessage(gorilla.BinaryMessage, b) != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	saved := dialDevice
	dialDevice = func(_ string, h http.Header) (*wsframe.Client, error) {
		return wsframe.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), h)
	}
	t.Cleanup(func() { dialDevice = saved })
}

// A stranger's friend request sent again and again - each one confirmed by
// their device - is one alert in Alerts, moved up each time, and its
// picture over the cap is never decoded nor kept: it used to be stored
// whole (7.5 MB written each time) with a new alert and push every time.
func TestRepeatedFriendRequestsAreOneAlertWithoutALargePicture(t *testing.T) {
	answeringDevice(t, func(req *pb.ReqEnvelope) *pb.RespEnvelope {
		return &pb.RespEnvelope{Id: req.Id, Payload: &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}}
	})
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sc := &Social{dao: dao.NewWithDB(db), settings: settings.Offline("me", "me.off-the.cloud", "x")}
	picture := largeJPEG(t)
	const from = "ann.off-the.cloud"

	// The first: a new friendship, a new alert.
	mock.ExpectQuery("select `status` from `social_friendship` where `domain` = \\?").WithArgs(from).WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectQuery("select `last_seen` from `social_friendship`").WithArgs(from).WillReturnRows(sqlmock.NewRows([]string{"last_seen"}))
	mock.ExpectExec("insert into `social_friendship`").WithArgs(from, "Ann", pictureArg{none: true}, "", "s1", false).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("select `uuid` from `notifications` where `type` = \\? and `actor_domain` = \\? and `acknowledged` = 0").
		WithArgs("FriendRequest", from).WillReturnRows(sqlmock.NewRows([]string{"uuid"}))
	mock.ExpectExec("insert into `notifications`").WillReturnResult(sqlmock.NewResult(1, 1))
	if err := sc.ExternalFriendshipRequest(from, "s1", "Ann", "", picture); err != nil {
		t.Fatal(err)
	}

	// Again, pending: the new secret taken, the alert moved up.
	mock.ExpectQuery("select `status` from `social_friendship` where `domain` = \\?").WithArgs(from).WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("pending"))
	mock.ExpectQuery("select `last_seen` from `social_friendship`").WithArgs(from).WillReturnRows(sqlmock.NewRows([]string{"last_seen"}))
	mock.ExpectExec("update `social_friendship` set `secret` = \\?").WithArgs("s2", "Ann", "", pictureArg{none: true}, "pending", false, from).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("select `uuid` from `notifications` where `type` = \\? and `actor_domain` = \\? and `acknowledged` = 0").
		WithArgs("FriendRequest", from).WillReturnRows(sqlmock.NewRows([]string{"uuid"}).AddRow("n1"))
	mock.ExpectExec("update `notifications` set `dt` = now\\(\\), `actor_name` = \\? where `uuid` = \\?").WithArgs("Ann", "n1").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := sc.ExternalFriendshipRequest(from, "s2", "Ann", "", picture); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// An accepted friend's device that sends a large picture (set before the
// apps cropped it) has it kept made small, once: the next passes compare
// what is kept and write nothing.
func TestFriendsLargePictureKeptSmallOnce(t *testing.T) {
	picture := largeJPEG(t)
	conn := fakeFriendDevice(t, func(req *pb.ReqEnvelope) *pb.RespEnvelope {
		return &pb.RespEnvelope{Id: req.Id, Payload: &pb.RespEnvelope_RespProfile{RespProfile: &pb.Profile{Name: "Bo", Image: picture}}}
	})
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := dao.NewWithDB(db)
	sc := &Social{dao: d}
	fr := &friendship{conn: conn, dao: d, sc: sc, data: &pb.Friendship{OriginProfile: &pb.Profile{Domain: "bo.off-the.cloud", Name: "Bo"}}}
	mock.ExpectExec("update `social_friendship` set `name` = \\?, `text` = \\?, `image` = \\? where `domain` = \\?").
		WithArgs("Bo", "", pictureArg{}, "bo.off-the.cloud").WillReturnResult(sqlmock.NewResult(0, 1))
	for range 3 {
		if err := fr.refreshProfile(); err != nil {
			t.Fatal(err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if n := len(fr.data.OriginProfile.Image); n == 0 || n > dao.MaxProfilePictureBytes {
		t.Fatalf("kept %d bytes", n)
	}
}
