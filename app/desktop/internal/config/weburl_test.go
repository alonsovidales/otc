// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import "testing"

// Issue #193: "Open Web App" opens the configured address's host at "/",
// never anything else that was typed into it.
func TestWebURL(t *testing.T) {
	cases := []struct{ domain, want string }{
		{"cala.off-the.cloud", "https://cala.off-the.cloud/"},
		{" cala.off-the.cloud ", "https://cala.off-the.cloud/"},
		{"wss://cala.off-the.cloud/ws", "https://cala.off-the.cloud/"},
		{"wss://otc.example.com:8443/otc/ws", "https://otc.example.com:8443/"},
		{"ws://192.168.1.10:8080/ws", "http://192.168.1.10:8080/"},
		{"ws://[fd00::5]:8080/ws", "http://[fd00::5]:8080/"},
		{"https://box.tail1234.ts.net/ws", "https://box.tail1234.ts.net/"},
		{"http://mint:8080", "http://mint:8080/"},
		{"10.2.1.171:8080", "http://10.2.1.171:8080/"},
		{"wss://me:secret@cala.off-the.cloud/ws?token=x#frag", "https://cala.off-the.cloud/"},
		{"", ""},
		{"   ", ""},
		{"ftp://cala.off-the.cloud/", ""},
		{"wss:///ws", ""},
		{"not a host", ""},
	}
	for _, c := range cases {
		if got := WebURL(c.domain); got != c.want {
			t.Errorf("WebURL(%q) = %q, want %q", c.domain, got, c.want)
		}
	}
}

// An address typed as people type it is put right where it could never
// connect as typed (a Docker device's user, 2026-11-05), and anything that
// could is kept byte for byte, so saved addresses and sync records stay.
func TestNormalizeAddress(t *testing.T) {
	cases := []struct{ typed, want string }{
		// Put right.
		{"http://mint:8080", "ws://mint:8080/ws"},
		{"http://10.2.1.171:8080", "ws://10.2.1.171:8080/ws"},
		{"HTTP://10.2.1.171:8080/", "ws://10.2.1.171:8080/ws"},
		{"https://otc.example.com", "wss://otc.example.com/ws"},
		{"https://otc.example.com:8443/otc/ws", "wss://otc.example.com:8443/otc/ws"},
		{"ws://10.2.1.171:8080", "ws://10.2.1.171:8080/ws"},
		{"wss://otc.example.com/", "wss://otc.example.com/ws"},
		{"10.2.1.171:8080", "ws://10.2.1.171:8080/ws"},
		{"mint:8080", "ws://mint:8080/ws"},
		{"nas.local:8080", "ws://nas.local:8080/ws"},
		{"nas.lan:8080/ws", "ws://nas.lan:8080/ws"},
		{"localhost:8080", "ws://localhost:8080/ws"},
		{"[fd00::5]:8080", "ws://[fd00::5]:8080/ws"},
		// Kept as typed (trimmed).
		{"cala.off-the.cloud", "cala.off-the.cloud"},
		{" Cala.off-the.cloud ", "Cala.off-the.cloud"},
		{"wss://cala.off-the.cloud/ws", "wss://cala.off-the.cloud/ws"},
		{"ws://192.168.1.10:8080/ws", "ws://192.168.1.10:8080/ws"},
		{"WS://192.168.1.2:8080/ws/", "WS://192.168.1.2:8080/ws/"},
		{"otc.example.com", "otc.example.com"},
		{"otc.example.com:8443", "otc.example.com:8443"},
		{"203.0.113.7:8080", "203.0.113.7:8080"},
		{"192.168.1.10", "192.168.1.10"},
		{"nas.lan", "nas.lan"},
		{"dev", "dev"},
		{"10.2.1.171:443", "10.2.1.171:443"},
		{"10.2.1.171:8443", "10.2.1.171:8443"},
		{"fd00::5", "fd00::5"},
		{"mint:", "mint:"},
		{"ftp://cala.off-the.cloud/", "ftp://cala.off-the.cloud/"},
		{"wss:///ws", "wss:///ws"},
		{"not a host", "not a host"},
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		if got := NormalizeAddress(c.typed); got != c.want {
			t.Errorf("NormalizeAddress(%q) = %q, want %q", c.typed, got, c.want)
		}
	}
}
