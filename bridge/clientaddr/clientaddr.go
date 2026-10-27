// SPDX-License-Identifier: AGPL-3.0-or-later

// Package clientaddr works out which address a request actually came from,
// and the key the bridge's per-client throttles bucket it on (the login
// limiters in bridge/admin and bridge/accounts, the sign-up, email and
// sign-in start limits, the contact and claim cooldowns in bridge/api, the
// relay's pairing limits).
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
//
// This is the address itself, for what needs it (a log line, what a device
// is told about its client, anything stored); a throttle counts Bucket's.
func Of(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// No port to split (a test, or an unusual transport) - the whole
		// value is the best key available.
		return r.RemoteAddr
	}
	return host
}

// Bucket is the key a per-client throttle counts addr (an address as Of
// returns it) against: an IPv4 address as it is, an IPv6 one by its /64.
// A host is routinely handed a whole /64 (SLAAC, privacy addresses) and can
// use a new address of it for every request, so counting full IPv6
// addresses would give it 2^64 buckets - no throttle at all. A /64 is one
// network, as an IPv4 address behind a NAT always was. An IPv4-mapped
// IPv6 address counts as its IPv4 one; anything that isn't an IP is its
// own key.
func Bucket(addr string) string {
	ip := net.ParseIP(addr)
	if ip == nil {
		return addr
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// BucketOf is Bucket(Of(r)): the throttle key of r's client.
func BucketOf(r *http.Request) string { return Bucket(Of(r)) }
