// SPDX-License-Identifier: AGPL-3.0-or-later

package wsclient

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	pb "github.com/alonsovidales/otc/proto/generated"
)

// Issue #205: an upload cut by a dropped connection goes on once the
// client has signed in again. The engine tells a drop from an answer with
// Dropped, and the new connection from the one that dropped with SignIns.
func TestDroppedRequestAndANewSignIn(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	var conns atomic.Int32
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		n := conns.Add(1)
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			req := &pb.ReqEnvelope{}
			if proto.Unmarshal(data, req) != nil {
				return
			}
			resp := &pb.RespEnvelope{Id: req.Id}
			switch req.Payload.(type) {
			case *pb.ReqEnvelope_ReqGetPubKey:
				resp.Payload = &pb.RespEnvelope_RespPubKey{RespPubKey: &pb.PubKey{PublicKey: pubDER}}
			case *pb.ReqEnvelope_ReqAuth:
				resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
			case *pb.ReqEnvelope_ReqUploadChunk:
				if n == 1 {
					return // the first connection drops under it
				}
				resp.Payload = &pb.RespEnvelope_RespUploadProgress{RespUploadProgress: &pb.UploadProgress{Received: 1}}
			default:
				resp.Error, resp.ErrorCode = true, "unknown_payload"
			}
			b, _ := proto.Marshal(resp)
			if c.WriteMessage(websocket.BinaryMessage, b) != nil {
				return
			}
		}
	}))
	defer srv.Close()

	c := New()
	c.Configure("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", "test", "secret")
	connected := make(chan struct{}, 4)
	c.OnConnect = func() { connected <- struct{}{} }
	if c.SignIns() != 0 {
		t.Fatal("signed in before connecting")
	}
	c.Connect()
	defer c.Disconnect()
	wait := func(what string) {
		select {
		case <-connected:
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for %s", what)
		}
	}
	wait("the first sign-in")
	first := c.SignIns()
	if first != 1 {
		t.Fatalf("SignIns %d after the first sign-in", first)
	}

	chunk := func() (*pb.RespEnvelope, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		return c.Request(ctx, func(r *pb.ReqEnvelope) {
			r.Payload = &pb.ReqEnvelope_ReqUploadChunk{ReqUploadChunk: &pb.UploadChunk{UploadId: "u1", Data: []byte{1}}}
		})
	}
	if _, err := chunk(); !Dropped(err) {
		t.Fatalf("a request whose connection dropped: %v, want Dropped", err)
	}
	wait("signing in again")
	if c.SignIns() != first+1 {
		t.Fatalf("SignIns %d after signing in again, want %d", c.SignIns(), first+1)
	}
	if resp, err := chunk(); err != nil || resp.GetRespUploadProgress() == nil {
		t.Fatalf("the request on the new connection: %v %v", resp, err)
	}
}

func TestDroppedTellsADropFromTheRest(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{nil, false},
		{ErrNotConnected, true},
		{&sendError{err: errors.New("write: broken pipe")}, true},
		{context.DeadlineExceeded, false},
		{errors.New("upload rejected"), false},
	} {
		if got := Dropped(tc.err); got != tc.want {
			t.Errorf("Dropped(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
	// The write's own words, for the log.
	if e := (&sendError{err: errors.New("write: broken pipe")}); e.Error() != "write: broken pipe" {
		t.Errorf("sendError says %q", e.Error())
	}
}
