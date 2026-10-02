// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"net"
	"testing"
)

// localAddress is a private address with a port, or nothing at all.
func TestLocalAddressIsPrivateOrEmpty(t *testing.T) {
	a := localAddress()
	if a == "" {
		t.Skip("no route out from here")
	}
	host, port, err := net.SplitHostPort(a)
	if err != nil || port == "" {
		t.Fatalf("not host:port: %q", a)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsPrivate() {
		t.Fatalf("not a private address: %q", a)
	}
	t.Log("local address:", a)
}
