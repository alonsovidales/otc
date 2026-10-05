// SPDX-License-Identifier: AGPL-3.0-or-later

package wsclient

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"net"
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

// A device the bridge can't reach is not a wrong password: the client says
// so and keeps retrying (issue #141 follow-up - an offline Pit left the
// apps stopped for good).
func TestKeepsRetryingWhileTheDeviceIsOffline(t *testing.T) {
	var conns atomic.Int32
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		conns.Add(1)
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			req := &pb.ReqEnvelope{}
			if proto.Unmarshal(data, req) != nil {
				return
			}
			resp := &pb.RespEnvelope{Id: req.Id, Error: true, Payload: &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: false, ErrorMsg: "offline", Code: "device_unreachable"}}}
			b, _ := proto.Marshal(resp)
			if c.WriteMessage(websocket.BinaryMessage, b) != nil {
				return
			}
		}
	}))
	defer srv.Close()

	c := New()
	c.Configure("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", "test", "secret")
	var unreachable, authFailed atomic.Int32
	c.OnUnreachable = func(string) { unreachable.Add(1) }
	c.OnAuthFailed = func(string, int) { authFailed.Add(1) }
	c.Connect()
	defer c.Disconnect()

	deadline := time.Now().Add(8 * time.Second)
	for conns.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if conns.Load() < 3 || unreachable.Load() < 2 || authFailed.Load() != 0 {
		t.Fatalf("connections %d, unreachable %d, auth failures %d - want retries and no auth failure", conns.Load(), unreachable.Load(), authFailed.Load())
	}
}

// A dial that was superseded (the address corrected while it still hung)
// and fails afterwards says nothing: it used to mark the working
// connection "Disconnected" and stop its RAID polling.
func TestStaleDialFailureIsIgnored(t *testing.T) {
	old := handshakeTO
	handshakeTO = 500 * time.Millisecond
	defer func() { handshakeTO = old }()

	// Accepts and never answers: the handshake hangs until the timeout.
	stall, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer stall.Close()
	go func() {
		for {
			c, err := stall.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	up := websocket.Upgrader{}
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
			req := &pb.ReqEnvelope{}
			if proto.Unmarshal(data, req) != nil {
				return
			}
			resp := &pb.RespEnvelope{Id: req.Id, Payload: &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}}
			if _, ok := req.Payload.(*pb.ReqEnvelope_ReqGetPubKey); ok {
				resp.Payload = &pb.RespEnvelope_RespPubKey{RespPubKey: &pb.PubKey{PublicKey: pubDER}}
			}
			b, _ := proto.Marshal(resp)
			if c.WriteMessage(websocket.BinaryMessage, b) != nil {
				return
			}
		}
	}))
	defer srv.Close()

	c := New()
	var live atomic.Bool
	var lateDisconnects atomic.Int32
	connected := make(chan struct{}, 1)
	c.OnConnect = func() { live.Store(true); connected <- struct{}{} }
	c.OnDisconnect = func(err error) {
		if err != nil && live.Load() {
			lateDisconnects.Add(1)
		}
	}
	c.Configure("ws://"+stall.Addr().String()+"/ws", "test", "secret")
	c.Connect()
	time.Sleep(50 * time.Millisecond) // the first dial is hanging now
	c.Disconnect()
	c.Configure("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", "test", "secret")
	c.Connect()
	defer c.Disconnect()
	select {
	case <-connected:
	case <-time.After(10 * time.Second):
		t.Fatal("never connected to the working address")
	}

	time.Sleep(handshakeTO + 500*time.Millisecond) // the stale dial times out meanwhile
	if n := lateDisconnects.Load(); n != 0 || !c.IsConnected() {
		t.Fatalf("stale failure reported %d time(s), connected %v", n, c.IsConnected())
	}
}

// A rejected password is the first thing reported: closing the socket
// wakes the read loop, whose read error used to reach OnDisconnect first
// at times - and connectOnce (otc-sync ls, the remote folder picker) takes
// the first answer, so a wrong password showed as a socket error.
func TestAuthFailureIsReportedFirst(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	up := websocket.Upgrader{}
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
			req := &pb.ReqEnvelope{}
			if proto.Unmarshal(data, req) != nil {
				return
			}
			resp := &pb.RespEnvelope{Id: req.Id, Payload: &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: false, ErrorMsg: "wrong password"}}}
			if _, ok := req.Payload.(*pb.ReqEnvelope_ReqGetPubKey); ok {
				resp.Payload = &pb.RespEnvelope_RespPubKey{RespPubKey: &pb.PubKey{PublicKey: pubDER}}
			}
			b, _ := proto.Marshal(resp)
			if c.WriteMessage(websocket.BinaryMessage, b) != nil {
				return
			}
		}
	}))
	defer srv.Close()

	for i := 0; i < 20; i++ {
		c := New()
		c.Configure("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", "test", "secret")
		first := make(chan string, 4)
		c.OnAuthFailed = func(msg string, _ int) { first <- "auth: " + msg }
		c.OnDisconnect = func(err error) {
			if err != nil {
				first <- "disconnect: " + err.Error()
			}
		}
		c.Connect()
		select {
		case got := <-first:
			if got != "auth: wrong password" {
				t.Fatalf("attempt %d: first report %q, want the rejected password", i, got)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("no report")
		}
		c.Disconnect()
	}
}
