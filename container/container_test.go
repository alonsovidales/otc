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
		"1800001800":           true,  // the start-up script's 30 minutes
		"1800001860":           true,  // and a minute for clocks
		"1800001861":           false, // further: a clock set back, or not the script's
		"1900000000":           false,
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

// registryCalls counts a fake registry's requests.
type registryCalls struct{ tokens, heads int32 }

// fakeRegistry answers like ghcr.io: a token (tokenStatus, 200 when 0),
// then a HEAD per tag with status's answer (404 when not listed).
func fakeRegistry(t *testing.T, tokenStatus int, status map[string]int) *registryCalls {
	t.Helper()
	calls := &registryCalls{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			atomic.AddInt32(&calls.tokens, 1)
			if r.URL.Query().Get("scope") != "repository:alonsovidales/otc:pull" {
				http.Error(w, "scope", http.StatusBadRequest)
				return
			}
			if tokenStatus != 0 {
				http.Error(w, "denied", tokenStatus)
				return
			}
			w.Write([]byte(`{"token":"anonymous"}`))
		case strings.HasPrefix(r.URL.Path, "/v2/alonsovidales/otc/manifests/"):
			atomic.AddInt32(&calls.heads, 1)
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
	return calls
}

func forgetImages(t *testing.T) {
	t.Helper()
	reset := func() {
		images.Lock()
		images.found = map[int]bool{}
		images.missing = map[int]time.Time{}
		images.failure, images.failedAt = nil, time.Time{}
		images.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

// forgetFailure ages the last failed lookup past cFailedFor.
func forgetFailure() {
	images.Lock()
	images.failedAt = time.Now().Add(-cFailedFor - time.Second)
	images.Unlock()
}

func TestImageAvailable(t *testing.T) {
	calls := fakeRegistry(t, 0, map[string]int{"140": http.StatusOK, "142": http.StatusForbidden})

	if ok, err := ImageAvailable(140); !ok || err != nil {
		t.Errorf("140: published, got %v %v", ok, err)
	}
	if ok, err := ImageAvailable(141); ok || err != nil {
		t.Errorf("141: not published, got %v %v", ok, err)
	}
	if ok, err := ImageAvailable(142); ok || err != nil {
		t.Errorf("142: turned away is not published, got %v %v", ok, err)
	}
	for _, r := range []int{0, -3} {
		if ok, err := ImageAvailable(r); ok || err != nil {
			t.Errorf("release %d: got %v %v", r, ok, err)
		}
	}
	if n := atomic.LoadInt32(&calls.heads); n != 3 {
		t.Errorf("%d lookups, want 3", n)
	}

	// Found stays found and not published is believed for a while.
	ImageAvailable(140)
	ImageAvailable(141)
	ImageAvailable(142)
	if n := atomic.LoadInt32(&calls.heads); n != 3 {
		t.Errorf("%d lookups after asking again, want 3", n)
	}

	images.Lock()
	images.missing[141] = time.Now().Add(-cMissingFor - time.Second)
	images.Unlock()
	ImageAvailable(141)
	if n := atomic.LoadInt32(&calls.heads); n != 4 {
		t.Errorf("%d lookups once missing is old, want 4", n)
	}
}

// One lookup - one update check - asks for one token, whatever it asks
// about.
func TestImageLookupOneToken(t *testing.T) {
	calls := fakeRegistry(t, 0, map[string]int{"140": http.StatusOK})
	l := new(ImageLookup)
	for _, r := range []int{143, 142, 141, 140} {
		l.Available(r)
	}
	if tokens, heads := atomic.LoadInt32(&calls.tokens), atomic.LoadInt32(&calls.heads); tokens != 1 || heads != 4 {
		t.Errorf("%d tokens and %d lookups, want 1 and 4", tokens, heads)
	}
}

// No token for the repository - how ghcr.io answers for a package that
// doesn't exist or isn't public - is no image, kept as such, and the
// lookup asks for no other token.
func TestImageAvailableTokenTurnedAway(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		calls := fakeRegistry(t, code, nil)
		l := new(ImageLookup)
		for _, r := range []int{141, 140} {
			if ok, err := l.Available(r); ok || err != nil {
				t.Errorf("token %d, release %d: got %v %v", code, r, ok, err)
			}
		}
		if ok, err := ImageAvailable(141); ok || err != nil {
			t.Errorf("token %d, asked again: got %v %v", code, ok, err)
		}
		if tokens, heads := atomic.LoadInt32(&calls.tokens), atomic.LoadInt32(&calls.heads); tokens != 1 || heads != 0 {
			t.Errorf("token %d: %d tokens and %d lookups, want 1 and 0", code, tokens, heads)
		}
	}
}

// A registry that fails is an error, and is left alone for cFailedFor:
// every lookup in that time fails at once, whatever release it is about.
func TestImageAvailableFailureIsKept(t *testing.T) {
	calls := fakeRegistry(t, 0, map[string]int{"140": http.StatusOK, "142": http.StatusBadGateway})

	if ok, err := ImageAvailable(140); !ok || err != nil {
		t.Fatalf("140: got %v %v", ok, err)
	}
	if ok, err := ImageAvailable(142); ok || err == nil {
		t.Errorf("142: the registry failed, got %v %v", ok, err)
	}
	if ok, err := ImageAvailable(141); ok || err == nil {
		t.Errorf("141 while the failure is kept: got %v %v", ok, err)
	}
	if ok, err := ImageAvailable(140); !ok || err != nil {
		t.Errorf("140, found before: got %v %v", ok, err)
	}
	if n := atomic.LoadInt32(&calls.heads); n != 2 {
		t.Errorf("%d lookups, want 2", n)
	}
	images.Lock()
	_, kept := images.missing[142]
	images.Unlock()
	if kept {
		t.Error("a failed lookup was kept as not published")
	}

	forgetFailure()
	if ok, err := ImageAvailable(141); ok || err != nil {
		t.Errorf("141 once the failure is old: got %v %v", ok, err)
	}
	if n := atomic.LoadInt32(&calls.heads); n != 3 {
		t.Errorf("%d lookups once the failure is old, want 3", n)
	}
}

// A registry out of reach fails the lookup within the client's timeout.
func TestImageAvailableUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close()
	saved := registry
	registry = addr
	t.Cleanup(func() { registry = saved })
	forgetImages(t)

	if ok, err := ImageAvailable(140); ok || err == nil {
		t.Errorf("got %v %v", ok, err)
	}
	if registryClient.Timeout > 5*time.Second {
		t.Errorf("a lookup may wait %v", registryClient.Timeout)
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
