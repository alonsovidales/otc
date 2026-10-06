// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/app/desktop/internal/wsclient"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// isolateConfig points the config directory at a temporary one.
func isolateConfig(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData"))
}

func TestRouteLine(t *testing.T) {
	for _, tc := range []struct{ route, domain, want string }{
		{"local", "cala.off-the.cloud", "Connected over your home network"},
		{"remote", "cala.off-the.cloud", "Connected through off-the.cloud"},
		{"remote", "wss://cala.off-the.cloud/ws", "Connected through off-the.cloud"},
		{"remote", "ws://192.168.1.10:8080/ws", "Connected through 192.168.1.10"},
		{"remote", "wss://[fd00::1]:8443/ws", "Connected through fd00::1"},
		{"remote", "nas.example.org", "Connected through nas.example.org"},
		{"", "cala.off-the.cloud", "Connected"},
	} {
		if got := RouteLine(tc.route, tc.domain); got != tc.want {
			t.Errorf("RouteLine(%q, %q) = %q, want %q", tc.route, tc.domain, got, tc.want)
		}
	}
	if got := StatusLine(config.State{Status: "Disconnected", Route: "local"}, "cala.off-the.cloud"); got != "Disconnected" {
		t.Errorf("a stale route showed on %q", got)
	}
	if got := StatusLine(config.State{Status: "Connected"}, "cala.off-the.cloud"); got != "Connected" {
		t.Errorf("no route (an older engine's state.json): %q", got)
	}
}

func TestNetworkFingerprint(t *testing.T) {
	ip := net.ParseIP
	base := func() []netIface {
		return []netIface{
			{Name: "lo", Up: true, Loopback: true, Addrs: []net.IP{ip("127.0.0.1"), ip("::1")}},
			{Name: "eth0", Up: true, Addrs: []net.IP{ip("192.168.1.20"), ip("2a01:db8:1:2::abcd"), ip("fe80::1")}},
			{Name: "docker0", Up: true, Addrs: []net.IP{ip("172.17.0.1")}},
			{Name: "tailscale0", Up: true, Addrs: []net.IP{ip("100.101.102.103")}},
			{Name: "wlan0", Up: false, Addrs: []net.IP{ip("10.0.0.5")}},
		}
	}
	want := networkFingerprint(base())
	if want == "" {
		t.Fatal("nothing in the fingerprint")
	}

	same := map[string]func([]netIface){
		"a new IPv6 privacy address": func(ifs []netIface) { ifs[1].Addrs[1] = ip("2a01:db8:1:2::9999") },
		"a container network":        func(ifs []netIface) { ifs[2].Addrs[0] = ip("172.18.0.1") },
		"Tailscale":                  func(ifs []netIface) { ifs[3].Addrs = nil },
		"a link-local address":       func(ifs []netIface) { ifs[1].Addrs[2] = ip("fe80::2") },
		"another order":              func(ifs []netIface) { ifs[0], ifs[1] = ifs[1], ifs[0] },
	}
	for name, change := range same {
		ifs := base()
		change(ifs)
		if networkFingerprint(ifs) != want {
			t.Errorf("%s counted as a network change", name)
		}
	}
	changed := map[string]func([]netIface){
		"another IPv4 address": func(ifs []netIface) { ifs[1].Addrs[0] = ip("10.1.1.20") },
		"another IPv6 prefix":  func(ifs []netIface) { ifs[1].Addrs[1] = ip("2a01:db8:1:3::abcd") },
		"Wi-Fi coming up":      func(ifs []netIface) { ifs[4].Up = true },
		"Ethernet going down":  func(ifs []netIface) { ifs[1].Up = false },
	}
	for name, change := range changed {
		ifs := base()
		change(ifs)
		if networkFingerprint(ifs) == want {
			t.Errorf("%s not counted as a network change", name)
		}
	}
}

// The endpoint is stored for the device that gave it, never for another,
// and goes when the device changes - not when only the password does.
func TestLocalEndpointBelongsToItsDevice(t *testing.T) {
	isolateConfig(t)
	domain := "ws://127.0.0.1:1/ws"
	e := New(&config.Config{Domain: domain, ClientID: "test"}, "pw", nil)
	t.Cleanup(e.Stop)
	ep := wsclient.NewLocalEndpoint([]string{"192.168.1.20", "fd00::20"}, 8443, make([]byte, 32))

	e.keepLocalEndpoint(domain, ep)
	if got := LocalEndpointFor(domain); !got.Equal(ep) {
		t.Fatalf("kept %+v, want %+v", got, ep)
	}
	if LocalEndpointFor("pit.off-the.cloud") != nil {
		t.Fatal("another device got this one's endpoint")
	}
	if runtime.GOOS != "windows" {
		dir, _ := config.Dir()
		if fi, err := os.Stat(filepath.Join(dir, "local.json")); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("local.json: %v, mode %v", err, fi.Mode().Perm())
		}
	}

	// An answer that arrives after the device was changed is not kept.
	e.keepLocalEndpoint("pit.off-the.cloud", wsclient.NewLocalEndpoint([]string{"10.0.0.9"}, 8443, make([]byte, 32)))
	if got := LocalEndpointFor(domain); !got.Equal(ep) {
		t.Fatalf("another device's answer replaced it: %+v", got)
	}

	e.UpdateConfig(&config.Config{Domain: domain, ClientID: "test"}, "new password")
	if LocalEndpointFor(domain) == nil {
		t.Fatal("a new password dropped the endpoint")
	}
	e.UpdateConfig(&config.Config{Domain: "ws://127.0.0.1:2/ws", ClientID: "test"}, "new password")
	if LocalEndpointFor(domain) != nil || e.ws.HasLocalEndpoint() {
		t.Fatal("the endpoint outlived a change of device")
	}

	// The device says it has none: forgotten.
	e.keepLocalEndpoint("ws://127.0.0.1:2/ws", ep)
	e.keepLocalEndpoint("ws://127.0.0.1:2/ws", nil)
	if LocalEndpointFor("ws://127.0.0.1:2/ws") != nil {
		t.Fatal("kept after the device said it has none")
	}
}

// A network change chooses the route again only when nothing is being
// transferred, and only when there is a home-network route to choose.
func TestNetworkChangeWaitsForTransfers(t *testing.T) {
	isolateConfig(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	// Home never answers (a private address nothing serves; an endpoint
	// can't name loopback): every connection goes through this "bridge",
	// each after the home try's budget at most.
	ep := wsclient.NewLocalEndpoint([]string{"10.255.255.1"}, 8443, make([]byte, 32))
	var conns, held atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
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
			resp := &pb.RespEnvelope{Id: req.Id, Payload: &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}}
			switch req.Payload.(type) {
			case *pb.ReqEnvelope_ReqGetPubKey:
				resp.Payload = &pb.RespEnvelope_RespPubKey{RespPubKey: &pb.PubKey{PublicKey: pubDER}}
			case *pb.ReqEnvelope_ReqGetLocalEndpoint:
				resp.Payload = &pb.RespEnvelope_RespLocalEndpoint{RespLocalEndpoint: &pb.LocalEndpoint{
					Addresses: ep.Addresses, Port: int32(ep.Port), CertSha256: ep.Pin,
				}}
			case *pb.ReqEnvelope_ReqGetStatus:
				// Never answered, as on a connection that died unnoticed:
				// the RAID poll stays in flight.
				held.Add(1)

				continue
			}
			b, _ := proto.Marshal(resp)
			if c.WriteMessage(websocket.BinaryMessage, b) != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	domain := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	e := New(&config.Config{Domain: domain, ClientID: "test"}, "secret", nil)
	e.keepLocalEndpoint(domain, ep)
	t.Cleanup(e.Stop)
	e.applySettings()

	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitFor("the first connection", func() bool { return e.ws.IsConnected() && e.Snapshot().Status == "Connected" })
	if r := e.Snapshot().Route; r != string(wsclient.RouteRemote) {
		t.Fatalf("route %q, want remote", r)
	}

	e.mu.Lock()
	e.folderBusy["f"] = true
	e.mu.Unlock()
	e.networkChanged()
	time.Sleep(300 * time.Millisecond)
	if conns.Load() != 1 || !e.ws.IsConnected() {
		t.Fatalf("switched during a transfer: %d connections", conns.Load())
	}

	e.mu.Lock()
	delete(e.folderBusy, "f")
	e.mu.Unlock()
	// Requests left unanswered - the RAID poll and one more - are no
	// transfer: one change is enough, and they fail rather than hang.
	waitFor("the RAID poll", func() bool { return held.Load() >= 1 })
	cut := make(chan error, 1)
	go func() {
		_, err := e.request(func(r *pb.ReqEnvelope) { r.Payload = &pb.ReqEnvelope_ReqGetStatus{ReqGetStatus: &pb.GetStatus{}} })
		cut <- err
	}()
	waitFor("the request", func() bool { return held.Load() >= 2 })
	e.networkChanged()
	waitFor("the reconnection", func() bool {
		return conns.Load() == 2 && e.ws.IsConnected() && e.Snapshot().Status == "Connected"
	})
	select {
	case err := <-cut:
		if err == nil {
			t.Fatal("the request in flight got an answer it was never sent")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the request in flight was left hanging")
	}

	// No home-network endpoint (a device before #190): nothing changes.
	e.ws.SetLocalEndpoint(nil)
	e.networkChanged()
	time.Sleep(300 * time.Millisecond)
	if conns.Load() != 2 {
		t.Fatalf("reconnected without a home-network route: %d connections", conns.Load())
	}
}
