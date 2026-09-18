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
