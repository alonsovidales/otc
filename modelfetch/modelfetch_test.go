// SPDX-License-Identifier: AGPL-3.0-or-later

package modelfetch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// served is a model's files on a test server: content by asset name.
type served struct {
	mu      sync.Mutex
	content map[string][]byte
	// handle, when set, answers instead (the asset's name and the request).
	handle   func(w http.ResponseWriter, r *http.Request, asset string) bool
	requests []string // "asset range"
}

func (s *served) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	asset := strings.TrimPrefix(r.URL.Path, "/models/")
	s.mu.Lock()
	s.requests = append(s.requests, asset+" "+r.Header.Get("Range"))
	handle := s.handle
	s.mu.Unlock()
	if handle != nil && handle(w, r, asset) {
		return
	}
	s.mu.Lock()
	b, ok := s.content[asset]
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, asset, time.Time{}, bytes.NewReader(b))
}

func (s *served) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

func randomBytes(n int, seed int64) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func pin(name string, b []byte) File {
	sum := sha256.Sum256(b)
	return File{Name: name, Asset: "m-" + name, Size: int64(len(b)), SHA256: hex.EncodeToString(sum[:])}
}

// fixture is a server with two files and a Fetcher for them that records
// its waits instead of waiting.
type fixture struct {
	t      *testing.T
	srv    *served
	files  []File
	data   map[string][]byte
	f      *Fetcher
	ready  chan struct{}
	mu     sync.Mutex
	waits  []time.Duration
	states []State // State() at each wait
}

func newFixture(t *testing.T, sizes ...int) *fixture {
	t.Helper()
	if len(sizes) == 0 {
		sizes = []int{3000, 200 << 10}
	}
	fx := &fixture{t: t, srv: &served{content: map[string][]byte{}}, data: map[string][]byte{}, ready: make(chan struct{}, 1)}
	for i, n := range sizes {
		name := fmt.Sprintf("file%d.bin", i)
		b := randomBytes(n, int64(i+1))
		p := pin(name, b)
		fx.files = append(fx.files, p)
		fx.data[name] = b
		fx.srv.content[p.Asset] = b
	}
	ts := httptest.NewServer(fx.srv)
	t.Cleanup(ts.Close)
	fx.f = New(Config{
		Name:    "test model",
		Dir:     filepath.Join(t.TempDir(), "model"),
		BaseURL: ts.URL + "/models",
		Files:   fx.files,
		OnReady: func() { fx.ready <- struct{}{} },
	})
	fx.f.sleep = func(ctx context.Context, d time.Duration) error {
		fx.mu.Lock()
		fx.waits = append(fx.waits, d)
		fx.mu.Unlock()
		fx.mu.Lock()
		fx.states = append(fx.states, fx.f.State())
		fx.mu.Unlock()
		return ctx.Err()
	}
	return fx
}

func (fx *fixture) waitReady() {
	fx.t.Helper()
	select {
	case <-fx.ready:
	case <-time.After(10 * time.Second):
		fx.t.Fatalf("not ready: %+v, requests %v", fx.f.State(), fx.srv.seen())
	}
}

// waitStopped waits for the download's goroutine to end by itself.
func (fx *fixture) waitStopped() {
	fx.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for fx.f.State().Downloading {
		if time.Now().After(deadline) {
			fx.t.Fatal("still downloading")
		}
		time.Sleep(5 * time.Millisecond)
	}
	fx.f.mu.Lock()
	done := fx.f.done
	fx.f.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (fx *fixture) checkFiles() {
	fx.t.Helper()
	for _, p := range fx.files {
		b, err := os.ReadFile(filepath.Join(fx.f.c.Dir, p.Name))
		if err != nil || !bytes.Equal(b, fx.data[p.Name]) {
			fx.t.Errorf("%s: %v, %d bytes", p.Name, err, len(b))
		}
		if _, err := os.Stat(fx.f.partPath(p)); err == nil {
			fx.t.Errorf("%s's part was left", p.Name)
		}
	}
}

func (fx *fixture) recorded() ([]time.Duration, []State) {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	return append([]time.Duration(nil), fx.waits...), append([]State(nil), fx.states...)
}

// The files arrive whole, under their names, and the state says so; a
// second Start has nothing to do.
func TestDownloadsEveryFile(t *testing.T) {
	fx := newFixture(t)
	if fx.f.Ready() {
		t.Fatal("ready before downloading")
	}
	fx.f.Start()
	fx.waitReady()
	fx.checkFiles()
	if st := fx.f.State(); !st.Ready || st.Downloading || st.Percent() != 100 || st.Done != st.Total || st.Total != 3000+200<<10 {
		t.Errorf("state %+v", st)
	}
	if !fx.f.Ready() {
		t.Error("not Ready")
	}
	n := len(fx.srv.seen())
	fx.f.Start()
	if len(fx.srv.seen()) != n {
		t.Error("asked again once ready")
	}
	// A new Fetcher finds them in place: ready without a request.
	g := New(fx.f.c)
	if !g.Ready() {
		t.Error("a new Fetcher doesn't see the files")
	}
}

// A file that doesn't match its pin is thrown away and tried again a
// minute later.
func TestAFileThatDoesntMatchIsTriedAgain(t *testing.T) {
	fx := newFixture(t)
	bad := fx.files[1]
	var once sync.Once
	fx.srv.handle = func(w http.ResponseWriter, r *http.Request, asset string) bool {
		served := false
		if asset == bad.Asset {
			once.Do(func() {
				// The right size, the wrong bytes.
				w.Header().Set("Content-Length", fmt.Sprint(bad.Size))
				w.Write(randomBytes(int(bad.Size), 99))
				served = true
			})
		}
		return served
	}
	fx.f.Start()
	fx.waitReady()
	fx.checkFiles()
	waits, states := fx.recorded()
	if len(waits) != 1 || waits[0] != time.Minute {
		t.Fatalf("waits %v", waits)
	}
	if p := states[0].Problem; p == nil || p.Kind != Failed || !states[0].Downloading {
		t.Errorf("while waiting: %+v", states[0])
	}
}

// A part left by an earlier run is resumed from where it ends; a server
// that answers the whole file instead starts it again.
func TestResumesAPart(t *testing.T) {
	for _, ranges := range []bool{true, false} {
		t.Run(fmt.Sprint("ranges=", ranges), func(t *testing.T) {
			fx := newFixture(t, 64<<10)
			p := fx.files[0]
			if err := os.MkdirAll(fx.f.c.Dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fx.f.partPath(p), fx.data[p.Name][:10000], 0o644); err != nil {
				t.Fatal(err)
			}
			if !ranges {
				fx.srv.handle = func(w http.ResponseWriter, r *http.Request, asset string) bool {
					w.Header().Set("Content-Length", fmt.Sprint(p.Size))
					w.Write(fx.data[p.Name])
					return true
				}
			}
			fx.f.Start()
			fx.waitReady()
			fx.checkFiles()
			if seen := fx.srv.seen(); len(seen) != 1 || seen[0] != p.Asset+" bytes=10000-" {
				t.Errorf("requests %v", seen)
			}
		})
	}
}

// A transfer cut halfway is resumed by the next try.
func TestATransferCutHalfwayGoesOn(t *testing.T) {
	fx := newFixture(t, 300<<10)
	p := fx.files[0]
	var once sync.Once
	fx.srv.handle = func(w http.ResponseWriter, r *http.Request, asset string) bool {
		cut := false
		once.Do(func() {
			w.Header().Set("Content-Length", fmt.Sprint(p.Size))
			w.Write(fx.data[p.Name][:100<<10])
			w.(http.Flusher).Flush()
			cut = true
		})
		if cut {
			panic(http.ErrAbortHandler)
		}
		return false
	}
	fx.f.Start()
	fx.waitReady()
	fx.checkFiles()
	seen := fx.srv.seen()
	if len(seen) != 2 || !strings.HasPrefix(seen[1], p.Asset+" bytes=") || seen[1] == p.Asset+" bytes=0-" {
		t.Errorf("requests %v", seen)
	}
	_, states := fx.recorded()
	if len(states) != 1 || states[0].Problem == nil || states[0].Problem.Kind != Unreachable || states[0].Done == 0 {
		t.Errorf("after the cut: %+v", states)
	}
}

// The transfer keeps to its cap: 1 MiB at 256 KiB/s takes about 4 s on
// the Fetcher's clock.
func TestKeepsToTheSpeedCap(t *testing.T) {
	fx := newFixture(t, 1<<20)
	var clockMu sync.Mutex
	clock := time.Unix(1e9, 0)
	slept := time.Duration(0)
	fx.f.now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clock
	}
	fx.f.sleep = func(ctx context.Context, d time.Duration) error {
		clockMu.Lock()
		clock = clock.Add(d)
		slept += d
		clockMu.Unlock()
		return ctx.Err()
	}
	fx.f.c.BytesPerSecond = 256 << 10
	fx.f.Start()
	fx.waitReady()
	fx.checkFiles()
	clockMu.Lock()
	defer clockMu.Unlock()
	if slept < 3900*time.Millisecond || slept > 4100*time.Millisecond {
		t.Errorf("1 MiB at 256 KiB/s took %v", slept)
	}
}

// The limiter builds no credit while the link is slower than the cap.
func TestTheLimiterKeepsNoCredit(t *testing.T) {
	now := time.Unix(1e9, 0)
	var slept time.Duration
	l := limiter{rate: 1000, now: func() time.Time { return now }, sleep: func(_ context.Context, d time.Duration) error {
		slept += d
		now = now.Add(d)
		return nil
	}}
	l.wait(context.Background(), 1000) // a second's worth
	if slept != time.Second {
		t.Fatalf("slept %v", slept)
	}
	now = now.Add(time.Minute) // a slow minute
	slept = 0
	l.wait(context.Background(), 500)
	if slept != 500*time.Millisecond {
		t.Errorf("after a slow minute: slept %v, want the half second those bytes take", slept)
	}
}

// Failures are tried again a minute later, then two, four... at most an
// hour apart, while someone wants the model; then the download ends.
func TestRetriesWithBackoffWhileWanted(t *testing.T) {
	fx := newFixture(t)
	fx.srv.handle = func(w http.ResponseWriter, r *http.Request, asset string) bool {
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return true
	}
	var askedMu sync.Mutex
	asked := 0
	fx.f.c.Wanted = func() bool {
		askedMu.Lock()
		defer askedMu.Unlock()
		asked++
		return asked < 9
	}
	fx.f.Start()
	fx.waitStopped()
	waits, states := fx.recorded()
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute, time.Hour, time.Hour, time.Hour}
	if fmt.Sprint(waits) != fmt.Sprint(want) {
		t.Errorf("waits %v, want %v", waits, want)
	}
	for _, st := range states {
		if st.Problem == nil || st.Problem.Kind != Unreachable {
			t.Errorf("a 503 is %+v", st.Problem)
		}
	}
	select {
	case <-fx.ready:
		t.Error("ready")
	default:
	}
	if st := fx.f.State(); st.Downloading || st.Problem != nil {
		t.Errorf("after it ended: %+v", st)
	}
	// A file that isn't there is the release's, not the link's.
	fx2 := newFixture(t)
	fx2.f.c.Files = append(fx2.f.c.Files, File{Name: "gone", Asset: "gone", Size: 10, SHA256: strings.Repeat("0", 64)})
	fx2.f.c.Wanted = func() bool { return false }
	fx2.f.Start()
	fx2.waitStopped()
	if _, states := fx2.recorded(); len(states) != 1 || states[0].Problem.Kind != Failed {
		t.Errorf("a 404: %+v", states)
	}
}

// Stop ends a transfer at once, removes the part and keeps the files
// already in place.
func TestStopEndsItAndRemovesTheParts(t *testing.T) {
	fx := newFixture(t)
	big := fx.files[1]
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	fx.srv.handle = func(w http.ResponseWriter, r *http.Request, asset string) bool {
		if asset != big.Asset {
			return false
		}
		w.Header().Set("Content-Length", fmt.Sprint(big.Size))
		w.Write(fx.data[big.Name][:50<<10])
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
		return true
	}
	fx.f.Start()
	deadline := time.Now().Add(10 * time.Second)
	for fx.f.State().Done < 3000+50<<10 {
		if time.Now().After(deadline) {
			t.Fatalf("no progress: %+v", fx.f.State())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st := fx.f.State(); !st.Downloading || st.Percent() <= 0 || st.Percent() >= 99 {
		t.Errorf("halfway: %+v %d%%", st, st.Percent())
	}
	stopped := make(chan struct{})
	go func() { fx.f.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop didn't end the transfer")
	}
	if _, err := os.Stat(fx.f.partPath(big)); err == nil {
		t.Error("the part was kept")
	}
	if b, err := os.ReadFile(filepath.Join(fx.f.c.Dir, fx.files[0].Name)); err != nil || !bytes.Equal(b, fx.data[fx.files[0].Name]) {
		t.Error("the whole file was removed")
	}
	if st := fx.f.State(); st.Downloading || st.Ready || st.Done != 0 {
		t.Errorf("after Stop: %+v", st)
	}
	select {
	case <-fx.ready:
		t.Error("ready after Stop")
	default:
	}
	// Stopping what isn't running is harmless; starting again goes on.
	fx.f.Stop()
	fx.srv.mu.Lock()
	fx.srv.handle = nil
	fx.srv.mu.Unlock()
	fx.f.Start()
	fx.waitReady()
	fx.checkFiles()
}

// Files that wouldn't fit, the reserve kept, aren't asked for.
func TestRefusedWhenItWouldNotFit(t *testing.T) {
	fx := newFixture(t)
	fx.f.c.Room = func(dir string) (int64, error) {
		if dir != fx.f.c.Dir {
			t.Errorf("room asked of %s", dir)
		}
		return 100, nil
	}
	fx.f.c.Wanted = func() bool { return false }
	fx.f.Start()
	fx.waitStopped()
	_, states := fx.recorded()
	if len(states) != 1 {
		t.Fatalf("states %+v", states)
	}
	p := states[0].Problem
	if p == nil || p.Kind != NoSpace || p.Need != 3000+200<<10 || p.Left != 100 {
		t.Errorf("problem %+v", p)
	}
	if seen := fx.srv.seen(); len(seen) != 0 {
		t.Errorf("asked for %v", seen)
	}
}

// A transfer that receives nothing for a while is cut and tried again.
func TestAStalledTransferIsCut(t *testing.T) {
	fx := newFixture(t, 100<<10)
	fx.f.stall = 200 * time.Millisecond
	p := fx.files[0]
	var once sync.Once
	fx.srv.handle = func(w http.ResponseWriter, r *http.Request, asset string) bool {
		stall := false
		once.Do(func() { stall = true })
		if !stall {
			return false
		}
		w.Header().Set("Content-Length", fmt.Sprint(p.Size))
		w.Write(fx.data[p.Name][:1000])
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		return true
	}
	fx.f.Start()
	fx.waitReady()
	fx.checkFiles()
	_, states := fx.recorded()
	if len(states) != 1 || states[0].Problem == nil || states[0].Problem.Kind != Unreachable {
		t.Errorf("states %+v", states)
	}
}

// A server that sends more than the file is refused, and the part goes.
func TestMoreThanTheFileIsRefused(t *testing.T) {
	fx := newFixture(t, 1000)
	p := fx.files[0]
	var once sync.Once
	fx.srv.handle = func(w http.ResponseWriter, r *http.Request, asset string) bool {
		long := false
		once.Do(func() { long = true })
		if long {
			w.Write(append(append([]byte(nil), fx.data[p.Name]...), 1, 2, 3))
		}
		return long
	}
	fx.f.Start()
	fx.waitReady()
	fx.checkFiles()
	if _, states := fx.recorded(); len(states) != 1 || states[0].Problem.Kind != Failed {
		t.Errorf("states %+v", states)
	}
}

func TestPercent(t *testing.T) {
	for _, c := range []struct {
		s    State
		want int
	}{
		{State{}, 0},
		{State{Done: 50, Total: 100}, 50},
		{State{Done: 100, Total: 100}, 99},
		{State{Done: 100, Total: 100, Ready: true}, 100},
		{State{Done: -5, Total: 100}, 0},
	} {
		if got := c.s.Percent(); got != c.want {
			t.Errorf("%+v: %d, want %d", c.s, got, c.want)
		}
	}
}
