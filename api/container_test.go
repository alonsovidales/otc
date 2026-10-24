// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
)

// stubExit records the exits asked for until the test ends.
func stubExit(t *testing.T) *[]int {
	t.Helper()
	var codes []int
	saved := exitProcess
	exitProcess = func(code int) { codes = append(codes, code) }
	t.Cleanup(func() { exitProcess = saved })
	return &codes
}

func busyPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

// In a container a port that can't be listened on ends the process - the
// HTTP port and the home-network TLS port alike - so the container's
// start-up script restarts it, or stops with the reason.
func TestContainerEndsOnABusyPort(t *testing.T) {
	t.Setenv("OTC_CONTAINER", "1")
	codes := stubExit(t)
	a := &API{muxHTTPServer: http.NewServeMux()}

	a.serveOrExit(busyPort(t))
	if len(*codes) != 1 || (*codes)[0] != 1 {
		t.Fatalf("HTTP port: exits %v, want [1]", *codes)
	}
	if ep := a.startLocalTLS(busyPort(t), t.TempDir()); ep != nil || len(*codes) != 2 || (*codes)[1] != 1 {
		t.Errorf("TLS port: endpoint %v, exits %v, want [1 1]", ep, *codes)
	}
}

// Elsewhere a busy TLS port is still only logged.
func TestBusyTLSPortOutsideAContainer(t *testing.T) {
	t.Setenv("OTC_CONTAINER", "")
	codes := stubExit(t)
	a := &API{muxHTTPServer: http.NewServeMux()}
	if ep := a.startLocalTLS(busyPort(t), t.TempDir()); ep != nil || len(*codes) != 0 {
		t.Errorf("endpoint %v, exits %v, want none", ep, *codes)
	}
}

// A free port is served, with no exit.
func TestContainerServesAFreePort(t *testing.T) {
	t.Setenv("OTC_CONTAINER", "1")
	codes := stubExit(t)
	a := &API{muxHTTPServer: http.NewServeMux()}
	a.registerAPIs()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	a.serveOrExit(port)

	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + cHealtyPath)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "OK" || len(*codes) != 0 {
		t.Errorf("answer %q, exits %v", body, *codes)
	}
}

// Port 80 (the captive-portal probe) is the server's in a container: not
// listened on there.
func TestCaptivePortalPort(t *testing.T) {
	t.Setenv("OTC_CONTAINER", "1")
	if captivePortalPort() {
		t.Error("in a container: listens on 80")
	}
	t.Setenv("OTC_CONTAINER", "")
	if !captivePortalPort() {
		t.Error("elsewhere: does not listen on 80")
	}
}
