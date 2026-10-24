// SPDX-License-Identifier: AGPL-3.0-or-later

package container

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnabledOnlyWithOne(t *testing.T) {
	for v, want := range map[string]bool{"1": true, "": false, "0": false, "true": false, "yes": false} {
		t.Setenv(EnvVar, v)
		if got := Enabled(); got != want {
			t.Errorf("%s=%q: %v, want %v", EnvVar, v, got, want)
		}
	}
	os.Unsetenv(EnvVar)
	if Enabled() {
		t.Error("unset: enabled")
	}
}

func TestHomeAddress(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1":          true,
		"127.10.0.1":         true,
		"::1":                true,
		"10.1.2.3":           true,
		"172.16.0.1":         true,
		"172.31.255.254":     true,
		"172.32.0.1":         false,
		"192.168.1.5":        true,
		"fd00::1":            true,
		"fc12:3456::1":       true,
		"100.64.0.1":         true,
		"100.127.255.255":    true,
		"100.63.255.255":     false,
		"100.128.0.1":        false,
		"8.8.8.8":            false,
		"2001:db8::1":        false,
		"2a00:1450::1":       false,
		"::ffff:192.168.1.5": true,
		"::ffff:100.64.1.1":  true,
		"::ffff:8.8.8.8":     false,
		"fe80::1":            false,
		"fe80::1%eth0":       false,
		"169.254.1.1":        false,
		"0.0.0.0":            false,
		"::":                 false,
		"":                   false,
		"not-an-address":     false,
		"192.168.1.5:8080":   false,
		"[::1]":              false,
	} {
		if got := HomeAddress(addr); got != want {
			t.Errorf("HomeAddress(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestSetupWindow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for v, want := range map[string]bool{
		"1800000001":           true,
		" 1800000600 ":         true,
		"1800000000":           false, // over at that second
		"1799999999":           false,
		"":                     false,
		"soon":                 false,
		"1800000001s":          false,
		"18e8":                 false,
		"0":                    false,
		"-1":                   false,
		"99999999999999999999": false,
	} {
		if got := setupWindowOpen(v, now); got != want {
			t.Errorf("until %q: open %v, want %v", v, got, want)
		}
	}

	t.Setenv(EnvSetupUntil, "1800000060")
	if !SetupWindowOpen(now) {
		t.Error("from the environment: closed before the end")
	}
	if SetupWindowOpen(now.Add(time.Minute)) {
		t.Error("from the environment: open at the end")
	}
	os.Unsetenv(EnvSetupUntil)
	if SetupWindowOpen(now) {
		t.Error("unset: open")
	}
}

func TestPlatform(t *testing.T) {
	dir := t.TempDir()
	saved := shareDir
	shareDir = dir
	t.Cleanup(func() { shareDir = saved })

	if got, want := Platform(), "docker unknown "+runtime.GOARCH+" unknown"; got != want {
		t.Errorf("no files: %q, want %q", got, want)
	}
	write(t, filepath.Join(dir, "release"), "140\n")
	write(t, filepath.Join(dir, "revision"), "7a96a765abc\n")
	if got, want := Platform(), "docker 140 "+runtime.GOARCH+" 7a96a765abc"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
	write(t, filepath.Join(dir, "revision"), "a b\n")
	if got := Platform(); !strings.HasSuffix(got, " unknown") {
		t.Errorf("odd revision: %q", got)
	}
}

// fakeRegistry answers like ghcr.io: a token, then a HEAD per tag.
func fakeRegistry(t *testing.T, status map[string]int) *int32 {
	t.Helper()
	var heads int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			if r.URL.Query().Get("scope") != "repository:alonsovidales/otc:pull" {
				http.Error(w, "scope", http.StatusBadRequest)
				return
			}
			w.Write([]byte(`{"token":"anonymous"}`))
		case strings.HasPrefix(r.URL.Path, "/v2/alonsovidales/otc/manifests/"):
			atomic.AddInt32(&heads, 1)
			if r.Method != http.MethodHead || r.Header.Get("Authorization") != "Bearer anonymous" ||
				!strings.Contains(r.Header.Get("Accept"), "application/vnd.oci.image.index.v1+json") {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			code, ok := status[strings.TrimPrefix(r.URL.Path, "/v2/alonsovidales/otc/manifests/")]
			if !ok {
				code = http.StatusNotFound
			}
			w.WriteHeader(code)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	saved := registry
	registry = srv.URL
	t.Cleanup(func() { registry = saved })
	forgetImages(t)
	return &heads
}

func forgetImages(t *testing.T) {
	t.Helper()
	reset := func() {
		images.Lock()
		images.found = map[int]bool{}
		images.missing = map[int]time.Time{}
		images.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

func TestImageAvailable(t *testing.T) {
	heads := fakeRegistry(t, map[string]int{"140": http.StatusOK, "142": http.StatusInternalServerError})

	if !ImageAvailable(140) {
		t.Error("140: published, not found")
	}
	if ImageAvailable(141) {
		t.Error("141: not published, found")
	}
	if ImageAvailable(142) {
		t.Error("142: the registry failed, found")
	}
	if ImageAvailable(0) || ImageAvailable(-3) {
		t.Error("no release: found")
	}
	if n := atomic.LoadInt32(heads); n != 3 {
		t.Errorf("%d lookups, want 3", n)
	}

	// Found stays found and missing is believed for a while; a failure is
	// asked again.
	ImageAvailable(140)
	ImageAvailable(141)
	ImageAvailable(142)
	if n := atomic.LoadInt32(heads); n != 4 {
		t.Errorf("%d lookups after asking again, want 4 (only the failed one)", n)
	}

	images.Lock()
	images.missing[141] = time.Now().Add(-cMissingFor - time.Second)
	images.Unlock()
	ImageAvailable(141)
	if n := atomic.LoadInt32(heads); n != 5 {
		t.Errorf("%d lookups once missing is old, want 5", n)
	}
}

func TestImageAvailableWithoutToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "denied", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	saved := registry
	registry = srv.URL
	t.Cleanup(func() { registry = saved })
	forgetImages(t)

	if ImageAvailable(140) {
		t.Error("no token: found")
	}
	images.Lock()
	_, kept := images.missing[140]
	images.Unlock()
	if kept {
		t.Error("a failed lookup was kept as missing")
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
