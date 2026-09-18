// SPDX-License-Identifier: AGPL-3.0-or-later

// Package clientaddr works out which address a request actually came from,
// for the bridge's per-client throttles to bucket on (the admin login
// limiter in bridge/admin, the contact form cooldown in bridge/api).
//
// It exists as its own package for the same reason staticassets does: it's
// one small security-relevant decision that more than one package depends
// on getting right, and two copies of it are two chances to get it subtly
// wrong in only one of them.
package clientaddr

import (
	"net"
	"net/http"
)

// Of returns the host part of r's remote address.
//
// Deliberately r.RemoteAddr and never X-Forwarded-For/X-Real-IP:
// otc_bridge terminates TLS and binds :80/:443 itself, with no reverse
// proxy in front of it (see [otc-api] ssl-cert/ssl-key), so those headers
// are entirely attacker-controlled here. Honouring one would let anyone
// bypass every throttle that buckets on this simply by sending a
// different fake value each request - and grow the throttles' maps with
// an entry per fake value, forever.
//
// The port is stripped, which is the whole reason this is a function
// rather than a field read: a caller opening a fresh TCP connection per
// request gets a fresh ephemeral port each time, so bucketing on the
// raw host:port gives every request its own bucket and throttles nothing
// at all (issue #99 - the contact form's cooldown had exactly this hole).
func Of(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// No port to split (a test, or an unusual transport) - the whole
		// value is the best key available.
		return r.RemoteAddr
	}
	return host
}
