// SPDX-License-Identifier: AGPL-3.0-or-later

package wsclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
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

// deviceCert is a certificate like the device's own (lantls): self-signed
// ECDSA P-256, CN otc-lan.
func deviceCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "otc-lan"},
		DNSNames:     []string{"otc-lan"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(100, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func pinOf(c tls.Certificate) []byte {
	sum := sha256.Sum256(c.Certificate[0])

	return sum[:]
}

// fakeDevice answers sign-in and GetLocalEndpoint, and counts what it got.
type fakeDevice struct {
	pubDER []byte
	// local answers GetLocalEndpoint; nil never answers it.
	local func() *pb.RespEnvelope
	// requests counts HTTP requests of any kind, upgrades included.
	requests, conns, auths, asks atomic.Int32
	closed                       chan struct{} // one per connection that ended
}

func newFakeDevice(t *testing.T) *fakeDevice {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)

	return &fakeDevice{pubDER: pubDER, closed: make(chan struct{}, 16)}
}

func (d *fakeDevice) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.requests.Add(1)
	c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() {
		select {
		case d.closed <- struct{}{}:
		default:
		}
	}()
	defer c.Close()
	d.conns.Add(1)
	for {
		_, data, err := c.ReadMessage()
		if err != nil {
			return
		}
		req := &pb.ReqEnvelope{}
		if proto.Unmarshal(data, req) != nil {
			return
		}
		resp := &pb.RespEnvelope{Payload: &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}}
		switch req.Payload.(type) {
		case *pb.ReqEnvelope_ReqGetPubKey:
			resp.Payload = &pb.RespEnvelope_RespPubKey{RespPubKey: &pb.PubKey{PublicKey: d.pubDER}}
		case *pb.ReqEnvelope_ReqAuth:
			d.auths.Add(1)
		case *pb.ReqEnvelope_ReqGetLocalEndpoint:
			d.asks.Add(1)
			if d.local == nil {
				continue
			}
			resp = d.local()
		}
		resp.Id = req.Id
		b, _ := proto.Marshal(resp)
		if c.WriteMessage(websocket.BinaryMessage, b) != nil {
			return
		}
	}
}

// homeServer is the device's home-network listener: TLS with cert.
func homeServer(t *testing.T, h http.Handler, cert tls.Certificate) (*httptest.Server, int) {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	return srv, srv.Listener.Addr().(*net.TCPAddr).Port
}

func bridgeServer(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
}

// closedPort is a port on 127.0.0.1 nothing listens on.
func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	return port
}

func endpointAnswer(ep *LocalEndpoint) func() *pb.RespEnvelope {
	return func() *pb.RespEnvelope {
		return &pb.RespEnvelope{Payload: &pb.RespEnvelope_RespLocalEndpoint{RespLocalEndpoint: &pb.LocalEndpoint{
			Addresses: ep.Addresses, Port: int32(ep.Port), CertSha256: ep.Pin,
		}}}
	}
}

func errorAnswer(code string) func() *pb.RespEnvelope {
	return func() *pb.RespEnvelope {
		return &pb.RespEnvelope{Error: true, ErrorCode: code, ErrorMessage: "no"}
	}
}

// connected configures c for url with ep kept (as the engine does: the
// endpoint after Configure), starts it and waits for its OnConnect,
// counting them.
func connected(t *testing.T, c *Client, url string, ep *LocalEndpoint) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	up := make(chan struct{}, 4)
	c.OnConnect = func() { n.Add(1); up <- struct{}{} }
	c.Configure(url, "test", "secret")
	c.SetLocalEndpoint(ep)
	c.Connect()
	t.Cleanup(c.Disconnect)
	select {
	case <-up:
	case <-time.After(15 * time.Second):
		t.Fatal("never connected")
	}

	return &n
}

func TestPinAcceptsOnlyThatCertificate(t *testing.T) {
	a, b := deviceCert(t), deviceCert(t)
	certA, _ := x509.ParseCertificate(a.Certificate[0])
	certB, _ := x509.ParseCertificate(b.Certificate[0])
	check := verifyPin(pinOf(a))
	if err := check(tls.ConnectionState{PeerCertificates: []*x509.Certificate{certA}}); err != nil {
		t.Errorf("the pinned certificate was refused: %v", err)
	}
	if err := check(tls.ConnectionState{PeerCertificates: []*x509.Certificate{certB}}); err == nil {
		t.Error("another certificate was accepted")
	}
	// Only the leaf counts, not a pinned certificate further up a chain.
	if err := check(tls.ConnectionState{PeerCertificates: []*x509.Certificate{certB, certA}}); err == nil {
		t.Error("a chain ending in the pinned certificate was accepted")
	}
	if err := check(tls.ConnectionState{}); err == nil {
		t.Error("no certificate was accepted")
	}
	if err := verifyPin(pinOf(a)[:16])(tls.ConnectionState{PeerCertificates: []*x509.Certificate{certA}}); err == nil {
		t.Error("a short pin was accepted")
	}
}

// Over a real TLS handshake: another certificate is refused before the
// HTTP upgrade is sent, so the server never sees a request.
func TestPinnedDialSendsNothingToAnotherCertificate(t *testing.T) {
	dev := newFakeDevice(t)
	impostor := deviceCert(t)
	_, port := homeServer(t, dev, impostor)

	_, _, err := dialLocal(context.Background(), NewLocalEndpoint([]string{"127.0.0.1"}, port, pinOf(deviceCert(t))))
	if err == nil {
		t.Fatal("connected to a server with another certificate")
	}
	time.Sleep(100 * time.Millisecond)
	if n := dev.requests.Load(); n != 0 {
		t.Fatalf("the impostor got %d request(s)", n)
	}

	conn, _, err := dialLocal(context.Background(), NewLocalEndpoint([]string{"127.0.0.1"}, port, pinOf(impostor)))
	if err != nil {
		t.Fatalf("the pinned certificate was refused: %v", err)
	}
	conn.Close()
}

func TestLocalEndpointKeepsOnlyWhatIsUsable(t *testing.T) {
	pin := make([]byte, 32)
	if NewLocalEndpoint([]string{"192.168.1.5"}, 0, pin) != nil || NewLocalEndpoint([]string{"192.168.1.5"}, 70000, pin) != nil {
		t.Error("a bad port was kept")
	}
	if NewLocalEndpoint([]string{"192.168.1.5"}, 8443, pin[:20]) != nil {
		t.Error("a short pin was kept")
	}
	if NewLocalEndpoint([]string{"otc.local", ""}, 8443, pin) != nil {
		t.Error("an endpoint without an IP address was kept")
	}
	ep := NewLocalEndpoint([]string{"192.168.1.5", "evil.example", "192.168.1.5", "fd12:3456::7"}, 8443, pin)
	if !ep.Equal(NewLocalEndpoint([]string{"fd12:3456::7", "192.168.1.5"}, 8443, pin)) {
		t.Error("the same addresses in another order are another endpoint")
	}
	if ep.Equal(NewLocalEndpoint([]string{"192.168.1.5"}, 8443, pin)) || ep.Equal(nil) {
		t.Error("fewer addresses, or none, are the same endpoint")
	}
	got := ep.urls()
	want := []string{"wss://192.168.1.5:8443/ws", "wss://[fd12:3456::7]:8443/ws"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("urls %v, want %v", got, want)
	}
}

func TestJudgeLocal(t *testing.T) {
	ep := NewLocalEndpoint([]string{"10.0.0.2"}, 8443, make([]byte, 32))
	for _, tc := range []struct {
		name string
		resp *pb.RespEnvelope
		want localVerdict
	}{
		{"an endpoint", endpointAnswer(ep)(), localStore},
		{"an unusable endpoint", &pb.RespEnvelope{Payload: &pb.RespEnvelope_RespLocalEndpoint{RespLocalEndpoint: &pb.LocalEndpoint{Port: 8443}}}, localClear},
		{"a device before the request", errorAnswer("unknown_payload")(), localClear},
		{"no listener at home", errorAnswer("local_unavailable")(), localClear},
		{"the bridge losing the device", &pb.RespEnvelope{Error: true, Payload: &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Code: "device_unreachable"}}}, localKeep},
		{"a lost session", errorAnswer("not_authenticated")(), localKeep},
		{"an answer without a payload", &pb.RespEnvelope{}, localKeep},
	} {
		if got, _ := judgeLocal(tc.resp); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// With an endpoint kept, the client reaches the device at home and never
// touches the bridge.
func TestHomeNetworkFirst(t *testing.T) {
	cert := deviceCert(t)
	home, bridge := newFakeDevice(t), newFakeDevice(t)
	_, port := homeServer(t, home, cert)
	url := bridgeServer(t, bridge)

	c := New()
	connected(t, c, url, NewLocalEndpoint([]string{"127.0.0.1"}, port, pinOf(cert)))
	if r := c.Route(); r != RouteLocal {
		t.Fatalf("route %q, want local", r)
	}
	if bridge.requests.Load() != 0 || home.auths.Load() != 1 {
		t.Fatalf("bridge requests %d, sign-ins at home %d", bridge.requests.Load(), home.auths.Load())
	}
	if home.asks.Load() != 0 {
		t.Error("GetLocalEndpoint was asked at home; it is only asked elsewhere")
	}
}

// An impostor at the kept address (another network with the same range)
// and an address that doesn't answer leave the client on the bridge, in
// about the budget, and the impostor sees nothing.
func TestFallsBackToTheBridge(t *testing.T) {
	impostor, bridge := newFakeDevice(t), newFakeDevice(t)
	bridge.local = errorAnswer("local_unavailable")
	_, port := homeServer(t, impostor, deviceCert(t))
	url := bridgeServer(t, bridge)

	c := New()
	var cleared atomic.Int32
	c.OnLocalEndpoint = func(_ string, ep *LocalEndpoint) {
		if ep == nil {
			cleared.Add(1)
		}
	}
	start := time.Now()
	connected(t, c, url, NewLocalEndpoint([]string{"127.0.0.1"}, port, pinOf(deviceCert(t))))
	if d := time.Since(start); d > localBudget+5*time.Second {
		t.Errorf("took %v", d)
	}
	if r := c.Route(); r != RouteRemote {
		t.Fatalf("route %q, want remote", r)
	}
	if impostor.requests.Load() != 0 {
		t.Fatal("the impostor got a request")
	}
	// local_unavailable: the endpoint kept is forgotten.
	if c.HasLocalEndpoint() || cleared.Load() != 1 {
		t.Fatalf("endpoint kept %v, cleared %d times", c.HasLocalEndpoint(), cleared.Load())
	}
}

// Signed in through the bridge, the client learns the endpoint, keeps it
// and moves home before announcing the connection.
func TestLearnsTheEndpointAndMovesHome(t *testing.T) {
	cert := deviceCert(t)
	home, bridge := newFakeDevice(t), newFakeDevice(t)
	_, port := homeServer(t, home, cert)
	url := bridgeServer(t, bridge)
	ep := NewLocalEndpoint([]string{"127.0.0.1"}, port, pinOf(cert))
	bridge.local = endpointAnswer(ep)

	c := New()
	var learnt atomic.Pointer[LocalEndpoint]
	var domain atomic.Value
	c.OnLocalEndpoint = func(d string, got *LocalEndpoint) { domain.Store(d); learnt.Store(got) }
	var disconnects atomic.Int32
	c.OnDisconnect = func(error) { disconnects.Add(1) }
	n := connected(t, c, url, nil)
	if r := c.Route(); r != RouteLocal {
		t.Fatalf("route %q, want local", r)
	}
	if !learnt.Load().Equal(ep) || domain.Load() != url {
		t.Fatalf("learnt %+v for %v", learnt.Load(), domain.Load())
	}
	select {
	case <-bridge.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the bridge connection was left open")
	}
	time.Sleep(200 * time.Millisecond)
	if n.Load() != 1 || disconnects.Load() != 0 {
		t.Fatalf("OnConnect %d times, OnDisconnect %d times", n.Load(), disconnects.Load())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Request(ctx, func(r *pb.ReqEnvelope) { r.Payload = &pb.ReqEnvelope_ReqGetStatus{ReqGetStatus: &pb.GetStatus{}} }); err != nil {
		t.Fatalf("request over the home network: %v", err)
	}
}

// A device from before the request has no endpoint: the one kept is
// forgotten and the client stays on the bridge.
func TestOldDeviceForgetsTheEndpoint(t *testing.T) {
	bridge := newFakeDevice(t)
	bridge.local = errorAnswer("unknown_payload")
	url := bridgeServer(t, bridge)
	c := New()
	var calls atomic.Int32
	var last atomic.Pointer[LocalEndpoint]
	c.OnLocalEndpoint = func(_ string, ep *LocalEndpoint) { calls.Add(1); last.Store(ep) }
	connected(t, c, url, NewLocalEndpoint([]string{"127.0.0.1"}, closedPort(t), make([]byte, 32)))
	if c.Route() != RouteRemote || c.HasLocalEndpoint() || calls.Load() != 1 || last.Load() != nil {
		t.Fatalf("route %q, kept %v, reported %d times", c.Route(), c.HasLocalEndpoint(), calls.Load())
	}
}

// A home route that connects but can't sign in (here it never answers)
// doesn't hold the client: the next dial goes through the bridge.
func TestStalledHomeSignInFallsBackToTheBridge(t *testing.T) {
	old := localSignInTO
	localSignInTO = 500 * time.Millisecond
	t.Cleanup(func() { localSignInTO = old }) // after the client's Disconnect
	cert := deviceCert(t)
	var homeConns atomic.Int32
	_, port := homeServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		homeConns.Add(1)
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}), cert)
	bridge := newFakeDevice(t)
	ep := NewLocalEndpoint([]string{"127.0.0.1"}, port, pinOf(cert))
	bridge.local = endpointAnswer(ep)
	url := bridgeServer(t, bridge)

	c := New()
	n := connected(t, c, url, ep)
	if c.Route() != RouteRemote || homeConns.Load() != 1 || !c.HasLocalEndpoint() {
		t.Fatalf("route %q, home tried %d times, kept %v", c.Route(), homeConns.Load(), c.HasLocalEndpoint())
	}
	// A network change tries home again, and ends on the bridge again.
	c.Reconnect()
	deadline := time.Now().Add(10 * time.Second)
	for n.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n.Load() != 2 || homeConns.Load() != 2 || c.Route() != RouteRemote {
		t.Fatalf("after Reconnect: connected %d times, home tried %d times, route %q", n.Load(), homeConns.Load(), c.Route())
	}
}

// No answer to the question (a busy device, a lost frame): the endpoint
// kept stays, and the sign-in goes ahead.
func TestUnansweredAskKeepsTheEndpoint(t *testing.T) {
	old := localAskTO
	localAskTO = 300 * time.Millisecond
	t.Cleanup(func() { localAskTO = old }) // after the client's Disconnect
	bridge := newFakeDevice(t)             // local nil: never answers
	url := bridgeServer(t, bridge)
	c := New()
	var calls atomic.Int32
	c.OnLocalEndpoint = func(string, *LocalEndpoint) { calls.Add(1) }
	connected(t, c, url, NewLocalEndpoint([]string{"127.0.0.1"}, closedPort(t), make([]byte, 32)))
	if !c.HasLocalEndpoint() || calls.Load() != 0 || bridge.asks.Load() != 1 {
		t.Fatalf("kept %v, reported %d times, asked %d times", c.HasLocalEndpoint(), calls.Load(), bridge.asks.Load())
	}
}

// Another device configured: the endpoint kept for the first one goes.
func TestConfigureForAnotherDeviceDropsTheEndpoint(t *testing.T) {
	c := New()
	c.Configure("cala.off-the.cloud", "id", "pw")
	c.SetLocalEndpoint(NewLocalEndpoint([]string{"10.0.0.2"}, 8443, make([]byte, 32)))
	c.Configure("cala.off-the.cloud", "id", "new password")
	if !c.HasLocalEndpoint() {
		t.Fatal("a new password dropped the endpoint")
	}
	c.Configure("pit.off-the.cloud", "id", "pw")
	if c.HasLocalEndpoint() {
		t.Fatal("the endpoint outlived a change of device")
	}
}

// Reconnect (a network change) chooses the route again: on the bridge
// while home didn't answer, at home once it does.
func TestReconnectChoosesTheRouteAgain(t *testing.T) {
	cert := deviceCert(t)
	home, bridge := newFakeDevice(t), newFakeDevice(t)
	var atHome atomic.Bool
	_, port := homeServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !atHome.Load() {
			http.Error(w, "not yet", http.StatusServiceUnavailable)

			return
		}
		home.ServeHTTP(w, r)
	}), cert)
	url := bridgeServer(t, bridge)
	ep := NewLocalEndpoint([]string{"127.0.0.1"}, port, pinOf(cert))
	bridge.local = endpointAnswer(ep)

	c := New()
	var reconnecting atomic.Int32
	c.OnDisconnect = func(err error) {
		if errors.Is(err, ErrReconnecting) {
			reconnecting.Add(1)
		}
	}
	n := connected(t, c, url, ep)
	if c.Route() != RouteRemote {
		t.Fatalf("route %q, want remote while home doesn't answer", c.Route())
	}
	atHome.Store(true)
	if !c.Reconnect() {
		t.Fatal("Reconnect refused")
	}
	deadline := time.Now().Add(10 * time.Second)
	for n.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if c.Route() != RouteLocal || reconnecting.Load() != 1 {
		t.Fatalf("route %q after reconnecting (%d reported), want local", c.Route(), reconnecting.Load())
	}

	c.Disconnect()
	if c.Reconnect() {
		t.Fatal("Reconnect after Disconnect")
	}
}
