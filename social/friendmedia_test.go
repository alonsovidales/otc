// SPDX-License-Identifier: AGPL-3.0-or-later

package social

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/wsframe"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// mediaFriend is a friend's device answering every GetPublicationMedia with
// content.
func mediaFriend(t *testing.T, content []byte, asked *atomic.Int32) *friendship {
	t.Helper()
	fr, _ := friendFrom(t, "x.off-the.cloud")
	fr.sc = &Social{}
	fr.conn = fakeFriendDevice(t, func(req *pb.ReqEnvelope) *pb.RespEnvelope {
		if _, ok := req.Payload.(*pb.ReqEnvelope_ReqGetPublicationMedia); !ok {
			return nil
		}
		asked.Add(1)
		return &pb.RespEnvelope{Payload: &pb.RespEnvelope_RespFile{RespFile: &pb.File{Hash: validHash, Content: content}}}
	})
	return fr
}

// A friend's device names its posts' files' types, and the web app shows
// them in this device's own origin: an SVG or HTML "photo" is stored as
// bytes to save, media as it is.
func TestStoreFriendFileKeepsOnlySafeTypes(t *testing.T) {
	dir, _ := unencDir(t)
	if err := os.WriteFile(filepath.Join(dir, validHash), []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	for stored, want := range map[string]string{
		"image/svg+xml": "application/octet-stream",
		"text/html":     "application/octet-stream",
		"image/jpeg":    "image/jpeg",
		"video/mp4":     "video/mp4",
	} {
		fr, mock := friendFrom(t, "x.off-the.cloud")
		mock.ExpectQuery("select count\\(\\*\\) from `social_publications_files`").
			WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
		file := &pb.File{Hash: validHash, Mime: stored, Content: []byte("thumb"), Size64: 5}
		if ok, _, err := fr.storeFriendFile("p1", file, dir); !ok || err != nil {
			t.Fatalf("%s: %v, %v", stored, ok, err)
		}
		if file.Mime != want {
			t.Errorf("%s stored as %q, want %q", stored, file.Mime, want)
		}
	}
}

// The media is read in an answer no larger than the friend says it is
// (plus the envelope around it): a friend's device could make this one read
// a 1000 MiB answer for a file it said was a few bytes, with the content
// budget held for those few. A larger one is refused - the post keeps its
// thumbnail - and one that fits is stored.
func TestFriendMediaIsReadUpToItsDeclaredSize(t *testing.T) {
	declared := int64(100 << 10)
	for _, c := range []struct {
		name   string
		size   int64
		stored bool
	}{
		{"as declared", declared, true},
		{"a little over, within the envelope", declared + 1000, true},
		{"far over", declared + 1<<20, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, _ := unencDir(t)
			var asked atomic.Int32
			content := bytes.Repeat([]byte{7}, int(c.size))
			fr := mediaFriend(t, content, &asked)
			file := &pb.File{Hash: validHash, Mime: "video/mp4", Content: []byte("thumb"), Size64: declared}
			ok, _, err := fr.storeFriendFile("p1", file, dir)
			if !ok || err != nil || asked.Load() != 1 {
				t.Fatalf("got %v, %v after %d asks", ok, err, asked.Load())
			}
			b, readErr := os.ReadFile(filepath.Join(dir, validHash))
			if got := readErr == nil && bytes.Equal(b, content); got != c.stored {
				t.Errorf("media stored %v, want %v", got, c.stored)
			}
			if _, err := os.Stat(filepath.Join(dir, validHash+"_thumbnail")); err != nil {
				t.Error("the thumbnail went:", err)
			}
		})
	}
}

// A file the friend gives no size for has its thumbnail kept and its media
// left out: there is nothing to bound the answer by, nor to reserve.
func TestFriendMediaWithoutASizeIsLeftOut(t *testing.T) {
	dir, _ := unencDir(t)
	var asked atomic.Int32
	fr := mediaFriend(t, []byte("media"), &asked)
	file := &pb.File{Hash: validHash, Mime: "image/jpeg", Content: []byte("thumb")}
	ok, wrote, err := fr.storeFriendFile("p1", file, dir)
	if !ok || !wrote || err != nil || asked.Load() != 0 {
		t.Fatalf("got %v, %v, %v after %d asks; want the thumbnail stored and nothing asked", ok, wrote, err, asked.Load())
	}
	if _, err := os.Stat(filepath.Join(dir, validHash)); err == nil {
		t.Error("media without a size was stored")
	}
	if file.Size64 != int64(len("thumb")) {
		t.Errorf("size %d, want the thumbnail's", file.Size64)
	}
}

// A JPEG may be a HEIC the friend's device converted to show it, larger
// than the HEIC's size it declares; nothing is read past wsframe.MediaLimit.
func TestFriendMediaLimit(t *testing.T) {
	for _, c := range []struct {
		declared int64
		mime     string
		want     int64
	}{
		{1000, "video/mp4", 1000 + cMediaAnswerSlack},
		{1000, "image/png", 1000 + cMediaAnswerSlack},
		{3 << 20, "image/jpeg", 3<<20*cJPEGGrowth + cJPEGSlack},
		{3 << 20, "IMAGE/JPEG", 3<<20*cJPEGGrowth + cJPEGSlack},
		{2 << 30, "video/mp4", wsframe.MediaLimit},
		{500 << 20, "image/jpeg", wsframe.MediaLimit},
	} {
		if got := friendMediaLimit(c.declared, c.mime); got != c.want {
			t.Errorf("%d bytes of %s: %d, want %d", c.declared, c.mime, got, c.want)
		}
	}
}

// A friend's comment is stored under the name this device knows the friend
// by, whatever name its event signs with - "Mum", or the owner's own.
func TestFriendCommentIsStoredUnderTheFriendsName(t *testing.T) {
	fr, mock := friendFrom(t, "bob.off-the.cloud")
	fr.data.OriginProfile.Name = "Bob"
	fr.data.NotificationsStarted = true
	fr.sc = &Social{}
	dt := timestamppb.New(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	fr.conn = eventsThenDrop(t, []*pb.Event{
		{Uuid: "e1", Dt: dt, Type: CommentEvent, Content: `{"uuid":"c1","pub_uuid":"p1","comment":"hi","publisher_name":"Mum"}`},
	})
	mock.ExpectExec("insert into `social_publications_comments`").
		WithArgs("c1", "p1", sqlmock.AnyArg(), "hi", "Bob", false, "bob.off-the.cloud").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("select `own_publication` from `social_publications`").
		WillReturnRows(sqlmock.NewRows([]string{"own_publication"}).AddRow(false))
	mock.ExpectExec("update `social_friendship` set `latest_sync` = \\?").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := fr.updateFriendEvents(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A friend without a name is stored under their domain; a name longer than
// the column is cut there, not dropped with the comment.
func TestCommenterName(t *testing.T) {
	for name, want := range map[string]string{
		"":                      "bob.off-the.cloud",
		"  ":                    "bob.off-the.cloud",
		" Bob ":                 "Bob",
		strings.Repeat("é", 70): strings.Repeat("é", cMaxPublisherName),
	} {
		fr := &friendship{data: &pb.Friendship{OriginProfile: &pb.Profile{Domain: "bob.off-the.cloud", Name: name}}, dao: dao.NewWithDB(nil)}
		if got := fr.commenterName(); got != want {
			t.Errorf("%q: %q, want %q", name, got, want)
		}
	}
}
