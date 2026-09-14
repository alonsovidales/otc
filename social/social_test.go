// SPDX-License-Identifier: AGPL-3.0-or-later

package social

import (
	"testing"

	pb "github.com/alonsovidales/otc/proto/generated"
)

func TestSocialStatusToPb(t *testing.T) {
	sc := &Social{}

	cases := map[string]pb.FriendShipStatus{
		"pending":  pb.FriendShipStatus_Pending,
		"accepted": pb.FriendShipStatus_Accepted,
		"blocked":  pb.FriendShipStatus_Blocked,
		"unknown":  pb.FriendShipStatus_Pending, // zero value fallback
	}

	for in, want := range cases {
		if got := sc.statusToPb(in); got != want {
			t.Errorf("statusToPb(%q) = %v, want %v", in, got, want)
		}
	}
}

// isAllowedFriendDomain is the one guard between an inbound friend request
// (reachable pre-auth, from anyone) and this device dialing out to
// whatever domain it names — the connectToDevice callers all route
// through it precisely so a request naming a LAN address or arbitrary
// internal hostname can't turn into an SSRF primitive.
func TestIsAllowedFriendDomain(t *testing.T) {
	cases := []struct {
		domain string
		want   bool
	}{
		{"pit.off-the.cloud", true},
		{"cala.off-the.cloud", true},
		{"off-the.cloud", true},     // the bare TLD itself
		{"PIT.OFF-THE.CLOUD", true}, // domains are case-insensitive
		{"evil.com", false},
		{"off-the.cloud.evil.com", false}, // suffix trick
		{"192.168.1.1", false},            // LAN address
		{"169.254.169.254", false},        // cloud metadata address
		{"localhost", false},
		{"notoff-the.cloud", false}, // must be a *sub*domain, not just a suffix match on the raw string
		{"", false},
	}

	for _, c := range cases {
		if got := isAllowedFriendDomain(c.domain); got != c.want {
			t.Errorf("isAllowedFriendDomain(%q) = %v, want %v", c.domain, got, c.want)
		}
	}
}
