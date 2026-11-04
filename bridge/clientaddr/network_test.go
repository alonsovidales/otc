// SPDX-License-Identifier: AGPL-3.0-or-later

package clientaddr

import "testing"

func TestNetBucket(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.7":                "203.0.113.0/24",
		"203.0.113.250":              "203.0.113.0/24",
		"::ffff:203.0.113.7":         "203.0.113.0/24",
		"2001:db8:1:2::1":            "2001:db8:1::/48",
		"2001:db8:1:ffff:a:b:c:d":    "2001:db8:1::/48",
		"2001:db8:1:2::/64":          "2001:db8:1::/48", // a Bucket key
		Bucket("2001:db8:7:9::abcd"): "2001:db8:7::/48",
		"2001:db8:2::1":              "2001:db8:2::/48",
		"pipe":                       "pipe",
	} {
		if got := NetBucket(in); got != want {
			t.Errorf("NetBucket(%q) = %q, want %q", in, got, want)
		}
	}
}
