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
