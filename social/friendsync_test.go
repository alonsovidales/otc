// SPDX-License-Identifier: AGPL-3.0-or-later

package social

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/wsframe"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeFriendDevice is a friend's device answering each request with
// answer's reply; a nil reply drops the connection.
func fakeFriendDevice(t *testing.T, answer func(*pb.ReqEnvelope) *pb.RespEnvelope) *wsframe.Client {
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
			resp := answer(&req)
			if resp == nil {
				return
			}
			b, _ := proto.Marshal(resp)
			if c.WriteMessage(gorilla.BinaryMessage, b) != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	conn, err := wsframe.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// A post, then a comment on it, in one second.
func postThenComment() []*pb.Event {
	dt := timestamppb.New(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	return []*pb.Event{
		{Uuid: "e1", Dt: dt, Type: PublicationEvent, Content: `{"uuid":"p1","action":"create"}`},
		{Uuid: "e2", Dt: dt, Type: CommentEvent, Content: `{"uuid":"c1","pub_uuid":"p1","comment":"hi"}`},
	}
}

func notStoredYet(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("select `friend_domain`, `own_publication` from `social_publications`").WithArgs("p1").
		WillReturnRows(sqlmock.NewRows([]string{"friend_domain", "own_publication"}))
}

// A post already here (a re-delivery) isn't fetched or written again; the
// cursor moves past it.
func TestFriendSyncSkipsAPostAlreadyHere(t *testing.T) {
	fr, mock := friendFrom(t, "x.off-the.cloud")
	fr.data.NotificationsStarted = true
	fr.sc = &Social{}
	fr.conn = fakeFriendDevice(t, func(req *pb.ReqEnvelope) *pb.RespEnvelope {
		if _, ok := req.Payload.(*pb.ReqEnvelope_ReqGetEvents); ok {
			return &pb.RespEnvelope{Payload: &pb.RespEnvelope_RespEvents{RespEvents: &pb.Events{Events: postThenComment()[:1]}}}
		}
		t.Errorf("asked the friend for %T of a post already here", req.Payload)
		return nil
	})
	pubRow(mock, "x.off-the.cloud", false)
	mock.ExpectExec("update `social_friendship` set `latest_sync` = \\?").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := fr.updateFriendEvents(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// The connection failing while a post is fetched stops the page there:
// the comment after it must not move the cursor past the post, or the
// post is never asked for again.
func TestFriendSyncStopsAtAPostTheConnectionLost(t *testing.T) {
	fr, mock := friendFrom(t, "x.off-the.cloud")
	fr.data.NotificationsStarted = true
	fr.sc = &Social{}
	fr.conn = fakeFriendDevice(t, func(req *pb.ReqEnvelope) *pb.RespEnvelope {
		if _, ok := req.Payload.(*pb.ReqEnvelope_ReqGetEvents); ok {
			return &pb.RespEnvelope{Payload: &pb.RespEnvelope_RespEvents{RespEvents: &pb.Events{Events: postThenComment()}}}
		}
		return nil // the connection drops
	})
	notStoredYet(mock)

	if err := fr.updateFriendEvents(); !errors.Is(err, errFriendTransport) {
		t.Fatalf("got %v, want the page stopped on the connection failure", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// An answer about the post itself (deleted on the friend's side since)
// skips it, as before, and the page carries on.
func TestFriendSyncSkipsAPostTheFriendNoLongerHas(t *testing.T) {
	fr, mock := friendFrom(t, "x.off-the.cloud")
	fr.data.NotificationsStarted = true
	fr.sc = &Social{}
	fr.conn = fakeFriendDevice(t, func(req *pb.ReqEnvelope) *pb.RespEnvelope {
		if _, ok := req.Payload.(*pb.ReqEnvelope_ReqGetEvents); ok {
			return &pb.RespEnvelope{Payload: &pb.RespEnvelope_RespEvents{RespEvents: &pb.Events{Events: postThenComment()}}}
		}
		return &pb.RespEnvelope{Error: true, ErrorMessage: "no such publication"}
	})
	notStoredYet(mock)
	mock.ExpectExec("insert into `social_publications_comments`").WillReturnResult(sqlmock.NewResult(0, 1))
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
