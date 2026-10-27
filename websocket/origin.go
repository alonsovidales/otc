// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/container"
)

// The device's own socket refused no page: any site open in a browser on
// the home network could drive ws://otc.local:8080/ws - guess passwords up
// to the device-wide limit, flood it, choose the first password of a
// device that has none. A browser always says which page opened a socket
// (Origin); the apps, otc-sync and friends' devices send none.
//
// What every first-party client sends, connecting to the device directly
// (the bridge's relays have no request and never come here):
//   - the web app at otc.local:8080, an IP address, a tunnel to
//     127.0.0.1, the TLS ports: its own page, Origin host = Host;
//   - Tailscale Funnel: https://<name>.ts.net, and tailscaled passes the
//     Host on (and X-Forwarded-Host, from loopback);
//   - the container behind the owner's reverse proxy: Origin host = Host
//     when the proxy passes the Host on, else X-Forwarded-Host from a
//     trusted proxy, else [otc-api] allowed-origins (OTC_ALLOWED_ORIGINS);
//   - the iPhone, Android and Mac apps (URLSession, OkHttp,
//     Network.framework), otc-sync and friends' devices (gorilla): none.
//     The apps' only web views are the Bluetooth setup's, which talk to
//     the setup wizard, never to this socket.

// EnvAllowedOrigins is the container's setting for [otc-api]
// allowed-origins.
const EnvAllowedOrigins = "OTC_ALLOWED_ORIGINS"

// originAllowed is the upgrader's CheckOrigin: a socket is taken
//   - with no Origin (the apps, otc-sync, friends' devices);
//   - from a page on the host the request was made to, at any scheme (a
//     TLS proxy in front of the plain port), as gorilla's own default;
//   - from a page on the host a proxy says it was asked for, when the
//     proxy is one X-Forwarded-For is believed from (clientAddr: loopback,
//     or a container's [otc-api] trusted-proxies);
//   - from an origin listed in [otc-api] allowed-origins.
//
// "null" (a sandboxed frame, a local file), any other scheme (file://, a
// browser extension's, an app's own: no first-party client sends one) and
// every other site's page are refused.
func originAllowed(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	if o == "null" {
		return false
	}
	u, err := url.Parse(o)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	if sameHost(u, r.Host) {
		return true
	}
	if fwd := forwardedHost(r); fwd != "" && sameHost(u, fwd) {
		return true
	}
	return allowedOrigins()[normalOrigin(u)]
}

// sameHost reports whether host (a Host header: name or address, maybe a
// port) is the origin u's, a port left out being the scheme's default.
func sameHost(u *url.URL, host string) bool {
	name, port, err := net.SplitHostPort(host)
	if err != nil {
		name, port = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"), ""
	}
	if port == "" {
		port = defaultPort(u.Scheme)
	}
	uPort := u.Port()
	if uPort == "" {
		uPort = defaultPort(u.Scheme)
	}
	return name != "" && strings.EqualFold(u.Hostname(), name) && uPort == port
}

func defaultPort(scheme string) string {
	if scheme == "https" {
		return "443"
	}
	return "80"
}

// forwardedHost is the host a proxy says the request was made to (the
// first X-Forwarded-Host), believed only from the peers clientAddr
// believes X-Forwarded-For from: in a container its trusted-proxies,
// elsewhere loopback (Tailscale Funnel's tailscaled). "" otherwise.
func forwardedHost(r *http.Request) string {
	fwd := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0])
	if fwd == "" {
		return ""
	}
	peer, err := netip.ParseAddr(peerHost(r))
	if err != nil {
		return ""
	}
	peer = peer.Unmap()
	if container.Enabled() {
		if !inPrefixes(peer, trustedProxies()) {
			return ""
		}
	} else if !peer.IsLoopback() {
		return ""
	}
	return fwd
}

// normalOrigin is u as scheme://host[:port], lower case, the scheme's
// default port left out - how allowed-origins are compared.
func normalOrigin(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if p := u.Port(); p != "" && p != defaultPort(u.Scheme) {
		host += ":" + p
	}
	return strings.ToLower(u.Scheme) + "://" + host
}

// allowedOrigins is [otc-api] allowed-origins, else EnvAllowedOrigins (a
// container's ini is written from its environment at every start): pages
// on other addresses that may open the socket - a reverse proxy that
// passes neither the Host nor X-Forwarded-Host on, a developer's own
// server. Origins (https://photos.example.com), separated by commas or
// spaces; one that isn't is left out. Read once.
var allowedOrigins = sync.OnceValue(func() map[string]bool {
	v := ""
	if cfg.HasSection("otc-api") {
		v = cfg.GetStr("otc-api", "allowed-origins")
	}
	if strings.TrimSpace(v) == "" {
		v = os.Getenv(EnvAllowedOrigins)
	}
	return parseAllowedOrigins(v)
})

func parseAllowedOrigins(v string) map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		u, err := url.Parse(strings.TrimSuffix(f, "/"))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") {
			continue
		}
		out[normalOrigin(u)] = true
	}
	return out
}
