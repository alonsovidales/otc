// SPDX-License-Identifier: AGPL-3.0-or-later

package clientaddr

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The bridge terminates TLS itself with nothing in front of it, so these
// headers are attacker-controlled - honouring either would give anyone a
// fresh throttle bucket per request.
func TestOfIgnoresForwardedHeaders(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/contact", nil)
	r.RemoteAddr = "203.0.113.7:54321"
	r.Header.Set("X-Forwarded-For", "10.0.0.1")
	r.Header.Set("X-Real-IP", "10.0.0.2")

	if got := Of(r); got != "203.0.113.7" {
		t.Errorf("Of() = %q, want %q", got, "203.0.113.7")
	}
}

// The bug this package was extracted to fix: one host opening a new
// connection per request must not get a new bucket per request.
func TestOfBucketsEveryPortFromOneHostTogether(t *testing.T) {
	first := httptest.NewRequest(http.MethodPost, "/api/contact", nil)
	first.RemoteAddr = "203.0.113.7:1111"
	second := httptest.NewRequest(http.MethodPost, "/api/contact", nil)
	second.RemoteAddr = "203.0.113.7:2222"

	if Of(first) != Of(second) {
		t.Errorf("Of() = %q and %q, want both ports to share one bucket", Of(first), Of(second))
	}
}

func TestOfHandlesIPv6(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/contact", nil)
	r.RemoteAddr = "[2001:db8::1]:54321"

	if got := Of(r); got != "2001:db8::1" {
		t.Errorf("Of() = %q, want %q", got, "2001:db8::1")
	}
}

func TestOfFallsBackWhenThereIsNoPort(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/contact", nil)
	r.RemoteAddr = "203.0.113.7"

	if got := Of(r); got != "203.0.113.7" {
		t.Errorf("Of() = %q, want %q", got, "203.0.113.7")
	}
}

// One host can use any address of its IPv6 /64, a new one per request:
// a throttle counts the /64. IPv4 stays the address itself.
func TestBucketCountsAnIPv6SlashSixtyFour(t *testing.T) {
	for addr, want := range map[string]string{
		"203.0.113.7":                             "203.0.113.7",
		"::ffff:203.0.113.7":                      "203.0.113.7", // IPv4-mapped: the same IPv4 host
		"2001:db8:1:2::1":                         "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff:ffff:ffff:ffff":        "2001:db8:1:2::/64",
		"2001:0db8:0001:0002:0a0b:0c0d:0e0f:1011": "2001:db8:1:2::/64", // the same /64, spelled long
		"2001:db8:1:3::1":                         "2001:db8:1:3::/64",
		"not an address":                          "not an address",
	} {
		if got := Bucket(addr); got != want {
			t.Errorf("Bucket(%q) = %q, want %q", addr, got, want)
		}
	}

	first := httptest.NewRequest(http.MethodPost, "/api/account/login", nil)
	first.RemoteAddr = "[2001:db8:1:2::1]:1111"
	second := httptest.NewRequest(http.MethodPost, "/api/account/login", nil)
	second.RemoteAddr = "[2001:db8:1:2:a:b:c:d]:2222"
	other := httptest.NewRequest(http.MethodPost, "/api/account/login", nil)
	other.RemoteAddr = "[2001:db8:1:3::1]:1111"
	if BucketOf(first) != BucketOf(second) {
		t.Errorf("BucketOf() = %q and %q, want one bucket for one /64", BucketOf(first), BucketOf(second))
	}
	if BucketOf(first) == BucketOf(other) {
		t.Errorf("two /64s share the bucket %q", BucketOf(first))
	}
	// Of itself keeps the address, for what needs it.
	if got := Of(second); got != "2001:db8:1:2:a:b:c:d" {
		t.Errorf("Of() = %q, want the address itself", got)
	}
}
