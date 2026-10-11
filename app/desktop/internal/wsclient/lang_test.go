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
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/alonsovidales/otc/app/desktop/internal/oslang"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// Every request carries the language otc-sync shows (docs/i18n.md),
// the sign-in's included - also when the build replaces the whole
// envelope: lang is set after it, in Request alone.
func TestEveryRequestCarriesTheLanguage(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	var mu sync.Mutex
	langs := map[string]string{} // payload kind -> lang it came with
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
			resp := &pb.RespEnvelope{Id: req.Id}
			kind := "other"
			switch req.Payload.(type) {
			case *pb.ReqEnvelope_ReqGetPubKey:
				kind = "pubkey"
				resp.Payload = &pb.RespEnvelope_RespPubKey{RespPubKey: &pb.PubKey{PublicKey: pubDER}}
			case *pb.ReqEnvelope_ReqAuth:
				kind = "auth"
				resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
			case *pb.ReqEnvelope_ReqGetStatus:
				kind = "status"
				resp.Payload = &pb.RespEnvelope_RespStatus{RespStatus: &pb.Status{}}
			default:
				resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
			}
			mu.Lock()
			langs[kind] = req.Lang
			mu.Unlock()
			b, _ := proto.Marshal(resp)
			if c.WriteMessage(websocket.BinaryMessage, b) != nil {
				return
			}
		}
	}))
	defer srv.Close()

	c := New()
	c.SetLang("es")
	c.Configure("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", "test", "secret")
	connected := make(chan struct{}, 1)
	c.OnConnect = func() { connected <- struct{}{} }
	c.Connect()
	defer c.Disconnect()
	select {
	case <-connected:
	case <-time.After(10 * time.Second):
		t.Fatal("never connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.Request(ctx, func(r *pb.ReqEnvelope) {
		// A build that starts the envelope over, dropping what Request
		// had put in it.
		proto.Reset(r)
		proto.Merge(r, &pb.ReqEnvelope{Payload: &pb.ReqEnvelope_ReqGetStatus{ReqGetStatus: &pb.GetStatus{}}})
	})
	if err != nil || resp.GetRespStatus() == nil {
		t.Fatalf("the replaced envelope: %v %v (its id must survive too)", resp, err)
	}
	c.SetLang("en")
	if _, err := c.Request(ctx, func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqListFiles{ReqListFiles: &pb.ListFiles{Path: "/"}}
	}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := map[string]string{"pubkey": "es", "auth": "es", "status": "es", "other": "en"}
	for kind, lang := range want {
		if langs[kind] != lang {
			t.Errorf("%s went with lang %q, want %q", kind, langs[kind], lang)
		}
	}
}

// Until SetLang, the computer's language - never nothing, which a device
// would take for an app that predates localization.
func TestNewClientSendsTheComputersLanguage(t *testing.T) {
	if got := New().lang; got == "" || got != oslang.System() {
		t.Fatalf("lang %q, want the computer's %q", got, oslang.System())
	}
}
