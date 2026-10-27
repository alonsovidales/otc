// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	gorilla "github.com/gorilla/websocket"
)

// Every first-party client connecting straight to the device is taken: the
// apps send no Origin, the web app's page is on the address it connects to
// (Tailscale Funnel passes the Host on), a proxy says where it was asked
// when it is one X-Forwarded-For is believed from. Another site's page, a
// sandboxed one and a local file are refused.
func TestOriginAllowed(t *testing.T) {
	saved := allowedOrigins
	allowedOrigins = func() map[string]bool {
		return parseAllowedOrigins("https://photos.example.com, http://dev.example:5173")
	}
	t.Cleanup(func() { allowedOrigins = saved })

	for _, c := range []struct {
		name, remote, host, origin, fwdHost string
		want                                bool
	}{
		{"the apps, otc-sync, a friend's device: no Origin", "192.168.1.30:5000", "192.168.1.20:8080", "", "", true},
		{"the web app at its LAN address", "192.168.1.30:5000", "192.168.1.20:8080", "http://192.168.1.20:8080", "", true},
		{"at otc.local, other case", "192.168.1.30:5000", "OTC.local:8080", "http://otc.local:8080", "", true},
		{"at port 80", "192.168.1.30:5000", "otc.local", "http://otc.local", "", true},
		{"port 80 written out", "192.168.1.30:5000", "otc.local:80", "http://otc.local", "", true},
		{"IPv6", "[fd00::30]:5000", "[fd00::20]:8080", "http://[fd00::20]:8080", "", true},
		{"the TLS port", "192.168.1.30:5000", "192.168.1.20:8443", "https://192.168.1.20:8443", "", true},
		{"a tunnel to loopback", "127.0.0.1:5000", "127.0.0.1:18080", "http://127.0.0.1:18080", "", true},
		{"Tailscale Funnel, Host passed on", "127.0.0.1:5000", "pit.tail1234.ts.net", "https://pit.tail1234.ts.net", "pit.tail1234.ts.net", true},
		{"a proxy on loopback naming the host", "127.0.0.1:5000", "127.0.0.1:8080", "https://pit.tail1234.ts.net", "pit.tail1234.ts.net", true},
		{"a LAN peer naming a host is not believed", "192.168.1.30:5000", "192.168.1.20:8080", "https://evil.example", "evil.example", false},
		{"listed in allowed-origins", "192.168.1.30:5000", "127.0.0.1:8080", "https://photos.example.com", "", true},
		{"listed, with its port", "192.168.1.30:5000", "127.0.0.1:8080", "http://dev.example:5173", "", true},
		{"listed host, other port", "192.168.1.30:5000", "127.0.0.1:8080", "https://photos.example.com:8443", "", false},
		{"another site", "192.168.1.30:5000", "192.168.1.20:8080", "https://evil.example", "", false},
		{"another site naming the device's address", "192.168.1.30:5000", "192.168.1.20:8080", "http://192.168.1.20.evil.example", "", false},
		{"another port of the device", "192.168.1.30:5000", "192.168.1.20:8080", "http://192.168.1.20:9000", "", false},
		{"a sandboxed page", "192.168.1.30:5000", "192.168.1.20:8080", "null", "", false},
		{"a local file", "192.168.1.30:5000", "192.168.1.20:8080", "file://", "", false},
		{"an extension", "192.168.1.30:5000", "192.168.1.20:8080", "chrome-extension://abcdef", "", false},
		{"not a URL", "192.168.1.30:5000", "192.168.1.20:8080", "http://[::1", "", false},
		{"no host", "192.168.1.30:5000", "192.168.1.20:8080", "http://", "", false},
	} {
		r := &http.Request{RemoteAddr: c.remote, Host: c.host, Header: http.Header{}}
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if c.fwdHost != "" {
			r.Header.Set("X-Forwarded-Host", c.fwdHost)
		}
		if got := originAllowed(r); got != c.want {
			t.Errorf("%s: allowed %v, want %v", c.name, got, c.want)
		}
	}
}

// In a container nothing forwards from loopback by itself: X-Forwarded-Host
// is believed from its trusted proxies only, as X-Forwarded-For is.
func TestOriginAllowedInAContainer(t *testing.T) {
	savedProxies, savedOrigins := trustedProxies, allowedOrigins
	trustedProxies = func() []netip.Prefix { return parseTrustedProxies("172.18.0.0/16") }
	allowedOrigins = func() map[string]bool { return map[string]bool{} }
	t.Cleanup(func() { trustedProxies, allowedOrigins = savedProxies, savedOrigins })
	t.Setenv("OTC_CONTAINER", "1")

	req := func(remote string) *http.Request {
		r := &http.Request{RemoteAddr: remote, Host: "otc:8080", Header: http.Header{}}
		r.Header.Set("Origin", "https://photos.example.com")
		r.Header.Set("X-Forwarded-Host", "photos.example.com")
		return r
	}
	if !originAllowed(req("172.18.0.2:5000")) {
		t.Error("the page behind a trusted proxy was refused")
	}
	if originAllowed(req("127.0.0.1:5000")) {
		t.Error("loopback's X-Forwarded-Host was believed in a container")
	}
	if originAllowed(req("192.168.1.30:5000")) {
		t.Error("a LAN peer's X-Forwarded-Host was believed")
	}
}

func TestParseAllowedOrigins(t *testing.T) {
	got := parseAllowedOrigins(" HTTPS://Photos.Example.com/, http://dev.example:5173,https://x.example:443 ftp://no.example, not-an-origin, https://a.example/path")
	want := []string{"https://photos.example.com", "http://dev.example:5173", "https://x.example"}
	if len(got) != len(want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("%s missing from %v", w, got)
		}
	}
}

// Another site's page gets a 403, before any upgrade; the device's own page
// gets its socket.
func TestListenRefusesAnotherSitesPage(t *testing.T) {
	served := make(chan struct{}, 2)
	mg := &Manager{upgrader: gorilla.Upgrader{CheckOrigin: originAllowed}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { served <- struct{}{} }()
		mg.Listen(w, r)
	}))
	t.Cleanup(srv.Close)
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	host := strings.TrimPrefix(srv.URL, "http://")

	h := http.Header{}
	h.Set("Origin", "https://evil.example")
	_, resp, err := gorilla.DefaultDialer.Dial(url, h)
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("another site's page: err %v, response %v; want a 403", err, resp)
	}
	<-served

	h.Set("Origin", "http://"+host)
	c, _, err := gorilla.DefaultDialer.Dial(url, h)
	if err != nil {
		t.Fatalf("the device's own page was refused: %v", err)
	}
	c.Close()
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("the connection was never done")
	}
}

// What strangers' requests cause is written as counts, one line a minute,
// not a line each.
func TestMinuteCountsWriteOneLine(t *testing.T) {
	var lines []string
	c := &minuteCounts{write: func(l string) { lines = append(lines, l) }}
	c.flush()
	if len(lines) != 0 {
		t.Fatalf("a line for nothing: %q", lines)
	}
	c.mu.Lock()
	c.counts = map[string]int{cAnonOrigin: 3, cAnonLockedOut: 1}
	c.mu.Unlock()
	c.flush()
	c.flush()
	want := "in the last minute: 1 " + cAnonLockedOut + ", 3 " + cAnonOrigin
	if len(lines) != 1 || lines[0] != want {
		t.Errorf("got %q, want one line %q", lines, want)
	}
}
