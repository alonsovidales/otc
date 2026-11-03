// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
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

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// Issue #207: a device that can't reach its disks answers every request
// with storage_unavailable. otc-sync shows its own status with the device's
// words (state.json's status_message), keeps them through the socket's
// close, changes nothing about the password, the device or the folders,
// asks again at once on a network change or a wake, and is Connected again
// as soon as the device answers otherwise.
func TestStorageUnavailableStatus(t *testing.T) {
	isolateConfig(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	const words = "Your device can't reach its storage disks (test)."
	var disksBack atomic.Bool
	var asks atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
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
			_, isKey := req.Payload.(*pb.ReqEnvelope_ReqGetPubKey)
			if isKey {
				asks.Add(1)
			}
			switch {
			case !disksBack.Load():
				resp.Error, resp.ErrorCode, resp.ErrorMessage = true, "storage_unavailable", words
			case isKey:
				resp.Payload = &pb.RespEnvelope_RespPubKey{RespPubKey: &pb.PubKey{PublicKey: pubDER}}
			default:
				resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
			}
			b, _ := proto.Marshal(resp)
			if c.WriteMessage(websocket.BinaryMessage, b) != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	domain := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	folder := config.Folder{ID: "f", Path: t.TempDir(), OneWay: true}
	cfg := &config.Config{Domain: domain, ClientID: "test", Folders: []config.Folder{folder}}
	e := New(cfg, "secret", nil)
	t.Cleanup(e.Stop)
	e.applySettings()

	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitFor("the storage status", func() bool { return e.Snapshot().Status == StatusNoStorage })
	// The socket's close comes after the answer: it must not turn the
	// status into "Disconnected".
	time.Sleep(300 * time.Millisecond)
	st := e.Snapshot()
	if st.Status != StatusNoStorage || st.StatusMessage != words {
		t.Fatalf("status %q, message %q; want %q with the device's words", st.Status, st.StatusMessage, StatusNoStorage)
	}
	if got := StatusText("", st, domain); got != "Device storage unavailable" {
		t.Fatalf("status line %q", got)
	}
	if got := StatusWords("es", st); got != words {
		t.Fatalf("StatusWords %q, want the device's own words", got)
	}
	e.mu.Lock()
	pw, sameCfg := e.password, e.cfg == cfg && len(e.cfg.Folders) == 1 && e.cfg.Domain == domain
	e.mu.Unlock()
	if pw != "secret" || !sameCfg {
		t.Fatalf("password %q, config kept %v: nothing may change for it", pw, sameCfg)
	}
	// Asked again only every 30 s: no second ask in the meantime ...
	if n := asks.Load(); n != 1 {
		t.Fatalf("%d asks before the interval, want 1", n)
	}
	// ... but at once on a network change or a wake, still saying so.
	e.networkChanged()
	waitFor("the ask a network change makes", func() bool { return asks.Load() == 2 })
	if s := e.Snapshot().Status; s != StatusNoStorage {
		t.Fatalf("status %q while asking again, want %q", s, StatusNoStorage)
	}

	// The disks are back: the next ask signs in.
	disksBack.Store(true)
	e.woke()
	waitFor("the sign-in", func() bool { return e.Snapshot().Status == StatusConnected })
	if st := e.Snapshot(); st.StatusMessage != "" || StatusWords("", st) != "" {
		t.Fatalf("message %q kept after the device answered", st.StatusMessage)
	}
}

// Without words of its own, otc-sync says it in the user's language.
func TestStorageWordsFallBackToOurs(t *testing.T) {
	st := config.State{Status: StatusNoStorage}
	if got := StatusWords("", st); !strings.HasPrefix(got, "Your device can't reach its storage disks.") {
		t.Fatalf("English fallback %q", got)
	}
	if got := StatusWords("es", st); !strings.HasPrefix(got, "Tu dispositivo no puede acceder") {
		t.Fatalf("Spanish fallback %q", got)
	}
	if got := StatusText("es", st, "cala.off-the.cloud"); got != "Dispositivo sin acceso a sus discos" {
		t.Fatalf("Spanish status %q", got)
	}
	if StatusWords("", config.State{Status: StatusOffline}) != "" {
		t.Fatal("words for another status")
	}
}
