// SPDX-License-Identifier: AGPL-3.0-or-later

package clientaddr

import (
	"net"
	"net/http"
	"strings"
)

// NetBucket is the key a network-wide limit counts addr against: an IPv4
// address's /24, an IPv6 one's /48. Bucket bounds one host, which may own a
// whole /64; a network bound is for what many hosts together can do - a
// /48, which tunnel brokers hand out free, holds 65,536 /64s, each a
// Bucket of its own. addr is an address as Of returns it, or a Bucket key
// ("2001:db8:1:2::/64"); anything else is its own key.
func NetBucket(addr string) string {
	ip := net.ParseIP(strings.TrimSuffix(addr, "/64"))
	if ip == nil {
		return addr
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.Mask(net.CIDRMask(24, 32)).String() + "/24"
	}
	return ip.Mask(net.CIDRMask(48, 128)).String() + "/48"
}

// NetBucketOf is NetBucket(Of(r)): the network-wide key of r's client.
func NetBucketOf(r *http.Request) string { return NetBucket(Of(r)) }
