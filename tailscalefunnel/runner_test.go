// SPDX-License-Identifier: AGPL-3.0-or-later

package tailscalefunnel

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsovidales/otc/devtext"
)

// fakeRunner stands in for scripts/tailscale-runner: it takes each
// request as the runner does (the word, then the auth key, both removed)
// and answers with what answerFor says.
type fakeRunner struct {
	t         *testing.T
	answerFor func(word string) map[string]any
	delay     time.Duration

	mu       sync.Mutex
	words    []string
	keys     []string // the key each request came with ("" for none)
	keyModes []os.FileMode
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// Stop ends the runner (once; the test's cleanup calls it too).
func (r *fakeRunner) Stop() {
	r.stopOnce.Do(func() { close(r.stop) })
	<-r.done
}

// withPaths points the package at a temporary directory, with short
// waits, and puts everything back after the test.
func withPaths(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	saved := []any{requestPath, answerPath, authKeyPath, onWait, offWait, statusWait, pollEvery, lookPath}
	requestPath = filepath.Join(dir, "tailscale.request")
	answerPath = filepath.Join(dir, "tailscale-status.json")
	authKeyPath = filepath.Join(dir, "run", "tailscale-auth.key")
	if err := os.Mkdir(filepath.Dir(authKeyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	onWait, offWait, statusWait = 2*time.Second, 2*time.Second, 2*time.Second
	pollEvery = 5 * time.Millisecond
	lookPath = func(string) (string, error) { return "/usr/bin/tailscale", nil }
	last.Store(nil)
	t.Cleanup(func() {
		requestPath = saved[0].(string)
		answerPath = saved[1].(string)
		authKeyPath = saved[2].(string)
		onWait, offWait, statusWait = saved[3].(time.Duration), saved[4].(time.Duration), saved[5].(time.Duration)
		pollEvery = saved[6].(time.Duration)
		lookPath = saved[7].(func(string) (string, error))
		last.Store(nil)
	})
	return dir
}

func startRunner(t *testing.T, answerFor func(word string) map[string]any) *fakeRunner {
	t.Helper()
	r := &fakeRunner{t: t, answerFor: answerFor, stop: make(chan struct{}), done: make(chan struct{})}
	go r.loop()
	t.Cleanup(r.Stop)
	return r
}

func (r *fakeRunner) loop() {
	defer close(r.done)
	for {
		select {
		case <-r.stop:
			return
		case <-time.After(2 * time.Millisecond):
		}
		raw, err := os.ReadFile(requestPath)
		if err != nil {
			continue
		}
		os.Remove(requestPath)
		word := string(raw)
		key, mode := "", os.FileMode(0)
		if info, err := os.Lstat(authKeyPath); err == nil {
			b, _ := os.ReadFile(authKeyPath)
			key, mode = string(b), info.Mode().Perm()
			os.Remove(authKeyPath)
		}
		r.mu.Lock()
		r.words = append(r.words, word)
		r.keys = append(r.keys, key)
		r.keyModes = append(r.keyModes, mode)
		delay := r.delay
		r.mu.Unlock()
		time.Sleep(delay)
		ans := r.answerFor(word)
		if ans == nil {
			continue // a runner that never answers
		}
		writeAnswer(r.t, ans)
	}
}

func (r *fakeRunner) seen() ([]string, []string, []os.FileMode) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.words...), append([]string(nil), r.keys...), append([]os.FileMode(nil), r.keyModes...)
}

func writeAnswer(t *testing.T, ans map[string]any) {
	b, err := json.Marshal(ans)
	if err != nil {
		t.Error(err)
		return
	}
	tmp := answerPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		t.Error(err)
		return
	}
	if err := os.Rename(tmp, answerPath); err != nil {
		t.Error(err)
	}
}

// done answers word the way the runner does once tailscaled says what
// fields holds.
func done(word string, fields map[string]any) map[string]any {
	a := map[string]any{"state": "done", "message": word, "code": "", "args": map[string]any{}, "request": word,
		"running": true, "logged_in": false, "funnel_on": false, "public_url": "", "login_url": ""}
	for k, v := range fields {
		a[k] = v
	}
	return a
}

func failed(word, message, code string, args map[string]any) map[string]any {
	a := done(word, nil)
	a["state"], a["message"], a["code"], a["args"] = "failed", message, code, args
	return a
}

// Status is the runner's answer to "status", and no key goes with it.
func TestStatusAsksTheRunner(t *testing.T) {
	withPaths(t)
	r := startRunner(t, func(word string) map[string]any {
		return done(word, map[string]any{"logged_in": true, "funnel_on": true, "public_url": "https://pi.tail1234.ts.net"})
	})
	st := Status()
	want := State{Installed: true, LoggedIn: true, FunnelOn: true, PublicURL: "https://pi.tail1234.ts.net"}
	if st != want {
		t.Errorf("Status() = %+v, want %+v", st, want)
	}
	words, keys, _ := r.seen()
	if len(words) != 1 || words[0] != "status" || keys[0] != "" {
		t.Errorf("the runner was asked %q with keys %q, want one status and no key", words, keys)
	}
}

// The auth key reaches the runner only as a file of this account's
// (0600), whole and trimmed, next to the word "on" - and is gone once the
// answer is in. A login page waiting for a person comes back as the
// setup's answer, not as an error.
func TestEnableHandsTheKeyAsAFile(t *testing.T) {
	withPaths(t)
	r := startRunner(t, func(word string) map[string]any {
		return done(word, map[string]any{"login_url": "https://login.tailscale.com/a/abc123"})
	})
	st, err := Enable("  tskey-auth-kSECRET-0123456789abcdef \n")
	if err != nil {
		t.Fatal(err)
	}
	if st.LoginURL != "https://login.tailscale.com/a/abc123" || st.LoggedIn || st.FunnelOn {
		t.Errorf("Enable = %+v", st)
	}
	words, keys, modes := r.seen()
	if len(words) != 1 || words[0] != "on" || keys[0] != "tskey-auth-kSECRET-0123456789abcdef" || modes[0] != 0o600 {
		t.Errorf("the runner got %q, key %q (%v); want on with the trimmed key, 0600", words, keys, modes)
	}
	if _, err := os.Lstat(authKeyPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the key's file is still there: %v", err)
	}
	// Nothing is left beside it either: the temporary names were renamed.
	for _, dir := range []string{filepath.Dir(authKeyPath), filepath.Dir(requestPath)} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				t.Errorf("%s left behind in %s", e.Name(), dir)
			}
		}
	}
}

// A key left from a request before this one never goes with it.
func TestEnableWithoutKeyTakesNoOldKey(t *testing.T) {
	withPaths(t)
	if err := os.WriteFile(authKeyPath, []byte("tskey-auth-old"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := startRunner(t, func(word string) map[string]any {
		return done(word, map[string]any{"logged_in": true, "funnel_on": true, "public_url": "https://pi.ts.net"})
	})
	st, err := Enable("")
	if err != nil || !st.FunnelOn || st.PublicURL != "https://pi.ts.net" {
		t.Fatalf("Enable = %+v, %v", st, err)
	}
	if _, keys, _ := r.seen(); len(keys) != 1 || keys[0] != "" {
		t.Errorf("the runner got keys %q, want none", keys)
	}
}

// What the runner reports as failed is the error, worded as it worded it
// (in the request's language when it gave a code the device knows), with
// what tailscaled said meanwhile.
func TestRunnerFailures(t *testing.T) {
	withPaths(t)
	cases := []struct {
		word, message, code string
		args                map[string]any
	}{
		{"on", "could not join the tailnet: tailscale: exit status 1: backend error: invalid key", "join_failed",
			map[string]any{"detail": "tailscale: exit status 1: backend error: invalid key"}},
		{"on", "could not enable Funnel: tailscale: exit status 1: Funnel not available", "enable_failed",
			map[string]any{"detail": "tailscale: exit status 1: Funnel not available"}},
		{"on", "tailscaled could not be started", "start_failed", map[string]any{}},
		{"off", "could not disable Funnel: tailscale timed out", "disable_failed",
			map[string]any{"detail": "tailscale timed out"}},
		{"on", "Tailscale is not installed on this device", "not_installed", map[string]any{}},
	}
	for _, c := range cases {
		r := startRunner(t, func(word string) map[string]any {
			a := failed(word, c.message, c.code, c.args)
			a["logged_in"] = true
			return a
		})
		var st State
		var err error
		if c.word == "on" {
			st, err = Enable("tskey-auth-x")
		} else {
			st, err = Disable()
		}
		if err == nil || err.Error() != c.message || devtext.Text("en", err) != c.message {
			t.Errorf("%s: error %v, want %q", c.code, err, c.message)
		}
		if es := devtext.Text("es", err); es == c.message || (c.args["detail"] != nil && !strings.Contains(es, c.args["detail"].(string))) {
			t.Errorf("%s: in Spanish %q", c.code, es)
		}
		if !st.LoggedIn || !st.Installed {
			t.Errorf("%s: state %+v, want what the runner saw", c.code, st)
		}
		if words, _, _ := r.seen(); len(words) != 1 || words[0] != c.word {
			t.Errorf("%s: the runner was asked %q", c.code, words)
		}
		r.Stop()
		last.Store(nil)
	}
}

// With no runner to answer, the request is withdrawn - it must not be
// acted on later, when nobody asks any more - and so is the key.
func TestNoAnswerWithdrawsTheRequest(t *testing.T) {
	withPaths(t)
	onWait, offWait, statusWait = 150*time.Millisecond, 150*time.Millisecond, 150*time.Millisecond

	_, err := Enable("tskey-auth-x")
	if err == nil || err.Error() != "Tailscale did not switch on in time - try again" {
		t.Errorf("Enable: %v", err)
	}
	for _, p := range []string{requestPath, authKeyPath} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still there: %v", p, err)
		}
	}
	if _, err := Disable(); err == nil || err.Error() != "Tailscale did not switch off in time - try again" {
		t.Errorf("Disable: %v", err)
	}
	if st := Status(); st != (State{Installed: true}) {
		t.Errorf("Status() = %+v", st)
	}
	if _, err := os.Lstat(requestPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the status request is still there: %v", err)
	}
}

// An answer from before the request is never taken for its answer, nor
// one to another word; a runner from before issue #204, which names no
// word, is believed.
func TestOnlyThisRequestsAnswer(t *testing.T) {
	withPaths(t)
	writeAnswer(t, done("status", map[string]any{"logged_in": true, "funnel_on": true}))
	r := startRunner(t, func(word string) map[string]any {
		writeAnswer(t, done("off", map[string]any{"funnel_on": true}))
		time.Sleep(30 * time.Millisecond)
		return done(word, map[string]any{"logged_in": true})
	})
	if st := Status(); st.FunnelOn || !st.LoggedIn {
		t.Errorf("Status() = %+v: took an answer that wasn't its own", st)
	}
	if words, _, _ := r.seen(); len(words) != 1 {
		t.Errorf("asked %q", words)
	}

	last.Store(nil)
	r.Stop()
	r2 := startRunner(t, func(word string) map[string]any {
		a := done(word, map[string]any{"logged_in": true, "funnel_on": true})
		delete(a, "request")
		a["message"] = "on"
		return a
	})
	st, err := Enable("")
	if err != nil || !st.FunnelOn {
		t.Errorf("an older runner's answer: %+v, %v", st, err)
	}
	if words, _, _ := r2.seen(); len(words) != 1 || words[0] != "on" {
		t.Errorf("asked %q", words)
	}
}

// A status asked while Funnel is being switched gets what the runner said
// last, at once, rather than queueing minutes behind it; asked again
// within a moment, the last answer stands in for it.
func TestStatusDuringASwitch(t *testing.T) {
	withPaths(t)
	r := startRunner(t, func(word string) map[string]any {
		return done(word, map[string]any{"logged_in": true, "funnel_on": word != "off"})
	})
	if st := Status(); !st.FunnelOn {
		t.Fatalf("Status() = %+v", st)
	}
	if st := Status(); !st.FunnelOn {
		t.Fatalf("Status() again = %+v", st)
	}
	if words, _, _ := r.seen(); len(words) != 1 {
		t.Errorf("two statuses in a moment asked the runner %q", words)
	}

	r.mu.Lock()
	r.delay = 300 * time.Millisecond
	r.mu.Unlock()
	last.Store(&seen{state: State{Installed: true, LoggedIn: true, FunnelOn: true}, at: time.Now().Add(-time.Minute)})
	off := make(chan State)
	go func() {
		st, _ := Disable()
		off <- st
	}()
	time.Sleep(50 * time.Millisecond)
	begun := time.Now()
	if st := Status(); !st.FunnelOn || time.Since(begun) > 100*time.Millisecond {
		t.Errorf("Status() during the switch = %+v after %v", st, time.Since(begun))
	}
	if st := <-off; st.FunnelOn {
		t.Errorf("Disable = %+v", st)
	}
	if st := Status(); st.FunnelOn {
		t.Errorf("Status() after the switch = %+v", st)
	}
	if words, _, _ := r.seen(); strings.Join(words, ",") != "status,off" {
		t.Errorf("the runner was asked %q", words)
	}
}

// The addresses are links the apps open: only web ones get through.
func TestAddressesAreWebLinks(t *testing.T) {
	cases := []struct {
		public, login     string
		wantPub, wantLink string
	}{
		{"https://pi.ts.net", "https://login.tailscale.com/a/1", "https://pi.ts.net", "https://login.tailscale.com/a/1"},
		{"http://pi.ts.net", "http://127.0.0.1:8080/register/abc", "", "http://127.0.0.1:8080/register/abc"},
		{"javascript:alert(1)", "javascript:alert(1)", "", ""},
		{"https://pi.ts.net x", "https://a b", "", ""},
	}
	for _, c := range cases {
		st := answer{PublicURL: c.public, LoginURL: c.login}.state()
		if st.PublicURL != c.wantPub || st.LoginURL != c.wantLink {
			t.Errorf("%q, %q -> %q, %q", c.public, c.login, st.PublicURL, st.LoginURL)
		}
	}
}

// A device without the CLI says so, and asks the runner nothing.
func TestWithoutTheCLI(t *testing.T) {
	withPaths(t)
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	r := startRunner(t, func(word string) map[string]any { return done(word, nil) })
	if st := Status(); st != (State{}) {
		t.Errorf("Status() = %+v", st)
	}
	const missing = "tailscale is not installed on this device"
	if _, err := Enable("k"); err == nil || err.Error() != missing {
		t.Errorf("Enable: %v", err)
	}
	if _, err := Disable(); err == nil || err.Error() != missing {
		t.Errorf("Disable: %v", err)
	}
	if words, _, _ := r.seen(); len(words) != 0 {
		t.Errorf("asked %q", words)
	}
}

// A request that can't be written says so, and leaves no key behind.
func TestRequestNotWritten(t *testing.T) {
	dir := withPaths(t)
	requestPath = filepath.Join(dir, "missing", "tailscale.request")
	_, err := Enable("tskey-auth-x")
	if err == nil || !strings.HasPrefix(err.Error(), "could not ask for Tailscale to be switched on: ") {
		t.Errorf("Enable: %v", err)
	}
	if _, err := Disable(); err == nil || !strings.HasPrefix(err.Error(), "could not ask for Tailscale to be switched off: ") {
		t.Errorf("Disable: %v", err)
	}
	if _, err := os.Lstat(authKeyPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the key's file is still there: %v", err)
	}
}
