// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// Issue #207: without its storage the port still serves the web app's
// page, says 503 to the health check (update.sh's: a degraded start is
// no start), hands /ws to the handler given, and lets the port go when
// stopped. A port still held is taken once it is free.
func TestServeUnavailable(t *testing.T) {
	static := t.TempDir()
	os.WriteFile(filepath.Join(static, "index.html"), []byte("<html>app</html>"), 0o644)
	port := freePort(t)
	// Held by someone else at first.
	held, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		t.Fatal(err)
	}
	prev := cUnavailableRetry
	cUnavailableRetry = 20 * time.Millisecond
	defer func() { cUnavailableRetry = prev }()
	wsCalls := 0
	stop := ServeUnavailable(port, static+"/", func(w http.ResponseWriter, r *http.Request) {
		wsCalls++
		w.WriteHeader(http.StatusTeapot)
	})
	time.Sleep(50 * time.Millisecond)
	held.Close()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	get := func(path string) (int, string) {
		t.Helper()
		var resp *http.Response
		var err error
		for i := 0; i < 100; i++ {
			if resp, err = http.Get(base + path); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, _ := get("/check_healty"); code != http.StatusServiceUnavailable {
		t.Errorf("health check: %d", code)
	}
	if code, body := get("/"); code != http.StatusOK || body != "<html>app</html>" {
		t.Errorf("the page: %d %q", code, body)
	}
	if code, _ := get("/media/abc"); code != http.StatusServiceUnavailable {
		t.Errorf("a stream: %d", code)
	}
	if code, _ := get("/ws"); code != http.StatusTeapot || wsCalls != 1 {
		t.Errorf("/ws: %d, %d calls", code, wsCalls)
	}
	stop()
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		t.Fatalf("the port wasn't let go: %v", err)
	}
	ln.Close()
}
