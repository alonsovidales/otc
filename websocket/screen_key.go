// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"

	"github.com/alonsovidales/otc/container"
	"github.com/alonsovidales/otc/supervisor"
)

// A screen plugged into the device shows the web app in a kiosk browser on
// the device itself (scripts/screen-runner), at
// http://127.0.0.1:<port>/?screen=1. It stays signed in with a screen key
// (session/screenkey.go), which only that browser may be given or sign in
// with: a key copied off the device opens nothing from anywhere else.

// localScreen reports whether r is the device's own browser: a direct
// connection from loopback to the primary instance, outside a container,
// with no proxy's headers (Tailscale Funnel's tailscaled comes from
// loopback too, with them), from the device's page at a loopback address
// (http://127.0.0.1:<port>, or localhost) asking for that same host.
func localScreen(r *http.Request) bool {
	if r == nil || container.Enabled() || os.Getenv(supervisor.ChildEnvVar) != "" || proxyHeaders(r) {
		return false
	}
	peer, err := netip.ParseAddr(peerHost(r))
	if err != nil || !peer.Unmap().IsLoopback() {
		return false
	}
	origin := r.Header.Get("Origin")
	u, err := url.Parse(origin)
	if origin == "" || err != nil || u.Scheme != "http" || !strings.EqualFold(u.Host, r.Host) {
		return false
	}
	return loopbackName(u.Hostname())
}

// loopbackName reports whether host is a loopback address or localhost.
func loopbackName(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.Unmap().IsLoopback()
}
