// SPDX-License-Identifier: AGPL-3.0-or-later

package wsclient

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
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

// A device that restarted forgets every session while the bridge may keep
// the client's socket: its answer is an Ack with code "not_authenticated".
// The client must reconnect and sign in again rather than keep failing.
func TestReconnectsWhenTheSessionIsGone(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)

	var conns, auths, lists atomic.Int32
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
				auths.Add(1)
				resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
			default:
				lists.Add(1)
				if n == 1 {
					// The first connection "lost" its session.
					resp.Error = true
					resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: false, ErrorMsg: "not authenticated", Code: "not_authenticated"}}
				} else {
					resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
				}
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.Request(ctx, func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqListFiles{ReqListFiles: &pb.ListFiles{Path: "/"}}
	})
	if err != nil || resp.GetRespAck().GetCode() != "not_authenticated" {
		t.Fatalf("first request: %v %v", resp, err)
	}

	wait("signing in again after the lost session")
	if conns.Load() != 2 || auths.Load() != 2 {
		t.Fatalf("connections %d, sign-ins %d; want 2 and 2", conns.Load(), auths.Load())
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	resp, err = c.Request(ctx2, func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqListFiles{ReqListFiles: &pb.ListFiles{Path: "/"}}
	})
	if err != nil || !resp.GetRespAck().GetOk() {
		t.Fatalf("request after reconnecting: %v %v", resp, err)
	}
}
