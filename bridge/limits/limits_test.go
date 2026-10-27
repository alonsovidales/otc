// SPDX-License-Identifier: AGPL-3.0-or-later

package limits

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRateBurstThenRefill(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewRate(1, 3)
	l.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if !l.Allow("a") {
			t.Fatalf("request %d refused within the burst", i)
		}
	}
	if l.Allow("a") {
		t.Fatal("allowed past the burst")
	}
	if !l.Allow("b") {
		t.Fatal("one key's limit applied to another")
	}
	now = now.Add(time.Second)
	if !l.Allow("a") {
		t.Fatal("no token after a second")
	}
	now = now.Add(time.Hour)
	l.Allow("c")
	if _, ok := l.buckets["a"]; ok {
		t.Fatal("an idle key was kept")
	}
}

// A flood of distinct keys within one refill window can't be swept (none
// is idle yet): the map is scanned each time it doubles, not on every call.
func TestRateSweepIsAmortised(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewRate(5.0/3600, 5)
	l.now = func() time.Time { return now }
	for i := 0; i < 3*cSweepKeys; i++ {
		l.Allow(strconv.Itoa(i))
	}
	// The first call (time-based), then at 100k and 200k keys.
	if l.sweeps > 4 {
		t.Fatalf("%d sweeps for %d keys", l.sweeps, 3*cSweepKeys)
	}
	// However big the flood, a new address still gets its own bucket.
	if !l.Allow("newcomer") {
		t.Fatal("a new key was refused after a flood of others")
	}
	now = now.Add(2 * time.Hour)
	l.Allow("late")
	if len(l.buckets) != 1 {
		t.Fatalf("%d keys kept after the window, want 1", len(l.buckets))
	}
}

// A client that stops reading is cut after the stall; a writer without
// deadlines still gets everything.
func TestWriteAll(t *testing.T) {
	body := make([]byte, 3*writeChunk+5)
	rec := httptest.NewRecorder()
	if err := WriteAll(rec, body, time.Second); err != nil || rec.Body.Len() != len(body) {
		t.Fatalf("recorder: %v, %d bytes", err, rec.Body.Len())
	}

	done := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Far more than the socket buffers hold.
		done <- WriteAll(w, make([]byte, 32<<20), 200*time.Millisecond)
	}))
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "GET / HTTP/1.1\r\nHost: x\r\n\r\n") // and never read
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a stalled client took the whole body")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the write to a stalled client never ended")
	}
}

// A refused key is told when its next token comes, which a 429 passes on
// as Retry-After in whole seconds, rounded up.
func TestRateTakeSaysWhenToComeBack(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewRate(5.0/3600, 5) // the sign-up limit: one every 12 minutes
	l.now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		if ok, wait := l.Take("a"); !ok || wait != 0 {
			t.Fatalf("request %d: %v %s", i, ok, wait)
		}
	}
	near := func(got, want time.Duration) bool { return got > want-time.Millisecond && got < want+time.Millisecond }
	if ok, wait := l.Take("a"); ok || !near(wait, 12*time.Minute) {
		t.Fatalf("past the burst: %v, wait %s, want 12m", ok, wait)
	}
	now = now.Add(10 * time.Minute)
	if ok, wait := l.Take("a"); ok || !near(wait, 2*time.Minute) {
		t.Fatalf("10 minutes on: %v, wait %s, want 2m", ok, wait)
	}
	now = now.Add(2*time.Minute + time.Millisecond)
	if ok, _ := l.Take("a"); !ok {
		t.Fatal("no token once the wait was over")
	}

	for d, want := range map[time.Duration]string{
		12 * time.Minute:        "720",
		90*time.Second + 1:      "91",
		time.Millisecond:        "1",
		0:                       "1",
		-time.Second:            "1",
		599*time.Second + 999e6: "600",
	} {
		w := httptest.NewRecorder()
		RetryAfter(w, d)
		if got := w.Header().Get("Retry-After"); got != want {
			t.Errorf("RetryAfter(%s) = %q, want %q", d, got, want)
		}
	}
}

// deadlineRecorder records the read deadline a handler sets through
// http.ResponseController.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	read time.Time
}

func (d *deadlineRecorder) SetReadDeadline(t time.Time) error { d.read = t; return nil }

// A form body is bounded like a JSON one: a read deadline and a size; a
// GET has no body to wait for.
func TestParseFormBoundsTheBody(t *testing.T) {
	form := func(body string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/cb?a=1", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return r
	}
	w := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	r := form("state=s&code=c")
	start := time.Now()
	if err := ParseForm(w, r, 50); err != nil || r.Form.Get("code") != "c" || r.Form.Get("a") != "1" {
		t.Fatalf("a small form: %v %v", err, r.Form)
	}
	if w.read.Before(start.Add(BodyReadTimeout)) || w.read.After(time.Now().Add(BodyReadTimeout)) {
		t.Errorf("read deadline %s, want %s from now", w.read, BodyReadTimeout)
	}
	if err := ParseForm(httptest.NewRecorder(), form("code="+strings.Repeat("x", 100)), 50); err == nil {
		t.Error("a form over the limit was parsed")
	}
	w = &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	r = httptest.NewRequest(http.MethodGet, "/cb?state=s&code=c", nil)
	if err := ParseForm(w, r, 50); err != nil || r.Form.Get("code") != "c" || !w.read.IsZero() {
		t.Errorf("a GET: %v %v, deadline %s", err, r.Form, w.read)
	}
}

func TestDecodeJSONLimit(t *testing.T) {
	var v map[string]string
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"a":"`+strings.Repeat("x", 100)+`"}`))
	if err := DecodeJSON(httptest.NewRecorder(), r, &v, 50); err == nil {
		t.Fatal("a body over the limit was decoded")
	}
	r = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"a":"b"}`))
	if err := DecodeJSON(httptest.NewRecorder(), r, &v, 50); err != nil || v["a"] != "b" {
		t.Fatalf("a small body failed: %v %v", err, v)
	}
}
