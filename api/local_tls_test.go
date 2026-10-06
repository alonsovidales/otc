// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/alonsovidales/otc/lantls"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/websocket"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

// pinnedTLS accepts only the certificate whose DER hashes to pin, whatever
// its name or issuer: what the apps do.
func pinnedTLS(pin []byte) *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("no certificate")
			}
			if sum := sha256.Sum256(raw[0]); !bytes.Equal(sum[:], pin) {
				return errors.New("certificate does not match the pin")
			}
			return nil
		},
	}
}

// The home-network listener serves the same mux as the HTTP port (the real
// /ws among it) over TLS with the device's own certificate, HTTP/1.1 only.
func TestLocalTLSServesTheMuxWithTheDeviceCertificate(t *testing.T) {
	id, err := lantls.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &API{
		websocket:     &websocket.Manager{},
		muxHTTPServer: http.NewServeMux(),
		staticPath:    t.TempDir() + "/",
	}
	a.registerAPIs()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ep := a.serveLocalTLS(ln, id)
	port := ln.Addr().(*net.TCPAddr).Port
	if !ep.Serving() || ep.Port() != port || !bytes.Equal(ep.Pin(), id.Pin) {
		t.Fatalf("endpoint: serving %v port %d (want %d)", ep.Serving(), ep.Port(), port)
	}

	// Another pin fails the handshake: the check below is the real one.
	if c, err := tls.Dial("tcp", ln.Addr().String(), pinnedTLS(make([]byte, 32))); err == nil {
		c.Close()
		t.Fatal("a handshake with another pin succeeded")
	}

	// /ws: a request before sign-in gets the device's not_authenticated.
	dialer := gorilla.Dialer{TLSClientConfig: pinnedTLS(id.Pin), HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.Dial(fmt.Sprintf("wss://127.0.0.1:%d/ws", port), nil)
	if err != nil {
		t.Fatalf("pinned WebSocket handshake: %v", err)
	}
	req, _ := proto.Marshal(&pb.ReqEnvelope{Id: 3, Payload: &pb.ReqEnvelope_ReqGetStatus{ReqGetStatus: &pb.GetStatus{}}})
	if err := conn.WriteMessage(gorilla.BinaryMessage, req); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	var resp pb.RespEnvelope
	if err := proto.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Id != 3 || resp.GetRespAck().GetCode() != "not_authenticated" {
		t.Errorf("got %+v, want the not_authenticated answer", &resp)
	}

	// Plain HTTPS on the same mux; HTTP/2 is offered and declined.
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: pinnedTLS(id.Pin), ForceAttemptHTTP2: true}}
	res, err := client.Get(fmt.Sprintf("https://127.0.0.1:%d%s", port, cHealtyPath))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || string(body) != "OK" || res.ProtoMajor != 1 {
		t.Errorf("health check: %d %q over %s", res.StatusCode, body, res.Proto)
	}
	res, err = client.Get(fmt.Sprintf("https://127.0.0.1:%d/media/unknown-token", port))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown media token: %d, want 404", res.StatusCode)
	}

	// TLS 1.2 at least.
	old := pinnedTLS(id.Pin)
	old.MaxVersion = tls.VersionTLS11
	if c, err := tls.Dial("tcp", ln.Addr().String(), old); err == nil {
		c.Close()
		t.Error("a TLS 1.1 handshake was accepted")
	}

	// A listener that fails is reported stopped, and GetLocalEndpoint
	// then answers local_unavailable.
	ln.Close()
	deadline := time.Now().Add(5 * time.Second)
	for ep.Serving() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if ep.Serving() {
		t.Error("the endpoint still reports serving after its listener closed")
	}
}

func TestStartLocalTLSWithoutAPortServesNothing(t *testing.T) {
	a := &API{muxHTTPServer: http.NewServeMux()}
	if ep := a.startLocalTLS(0, t.TempDir()); ep != nil {
		t.Error("expected no listener without a port")
	}
}

// A port another process holds is logged and leaves the endpoint
// unavailable, as for the other listeners.
func TestStartLocalTLSOnABusyPort(t *testing.T) {
	busy, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	a := &API{muxHTTPServer: http.NewServeMux()}
	if ep := a.startLocalTLS(busy.Addr().(*net.TCPAddr).Port, t.TempDir()); ep != nil {
		t.Error("expected no endpoint on a port already in use")
	}
}
