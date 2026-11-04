// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// slowSpans is a spanSource answering like fakeMediaDevice after latency,
// counting the requests and the most in flight at once.
type slowSpans struct {
	device  func(string, []byte) ([]byte, error)
	latency time.Duration

	mu             sync.Mutex
	now, most, all int
	closed         bool
}

func (s *slowSpans) Forward(frame []byte) ([]byte, error) {
	s.mu.Lock()
	s.now++
	s.all++
	s.most = max(s.most, s.now)
	s.mu.Unlock()
	time.Sleep(s.latency)
	s.mu.Lock()
	s.now--
	s.mu.Unlock()
	return s.device("pit.otc", frame)
}

func (s *slowSpans) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

func mediaFile(spans int) []byte {
	file := make([]byte, int64(spans)*maxProxiedRange+12345)
	for i := range file {
		file[i] = byte(i * 7)
	}
	return file
}

// The next spans are asked for while one goes out - cMediaReadAhead of
// them, no more - and the response is the file, in order; before, each
// span was asked for only once the one before was written, a round trip
// of the device's per span.
func TestMediaAsksForTheNextSpansWhileOneIsWritten(t *testing.T) {
	const spans = 8
	file := mediaFile(spans)
	var lengths []int64
	src := &slowSpans{device: fakeMediaDevice(file, &lengths), latency: 60 * time.Millisecond}
	m := &mediaFetch{src: src, token: "tok", host: "pit.otc"}
	req := httptest.NewRequest(http.MethodGet, "/media/tok", nil)
	rec := httptest.NewRecorder()
	first, _, _, ok := m.fetch(rec, 0, maxProxiedRange)
	if !ok {
		t.Fatal("first span")
	}
	start := time.Now()
	m.writeSpan(rec, req, 0, first, int64(len(file))-1)
	elapsed := time.Since(start)

	if !bytes.Equal(rec.Body.Bytes(), file) {
		t.Fatalf("the response is %d bytes and not the file", rec.Body.Len())
	}
	if src.most != cMediaReadAhead {
		t.Errorf("at most %d spans asked for at once, want %d", src.most, cMediaReadAhead)
	}

	// The way it was: each span asked for once the one before was written.
	old := httptest.NewRecorder()
	old.Write(first)
	start = time.Now()
	for pos := int64(len(first)); pos < int64(len(file)); {
		content, _, _, ok := m.fetch(nil, pos, min(maxProxiedRange, int64(len(file))-pos))
		if !ok {
			t.Fatal("a span one at a time")
		}
		old.Write(content)
		pos += int64(len(content))
	}
	oneByOne := time.Since(start)
	t.Logf("%d spans after the first: %v asked ahead, %v one at a time", spans, elapsed, oneByOne)
	if elapsed > oneByOne*3/4 {
		t.Errorf("asked ahead the spans took %v, about as long as one at a time (%v)", elapsed, oneByOne)
	}
}

// A client that hangs up stops the fetching: at most what was already on
// its way is asked for after it.
func TestMediaStopsAskingWhenTheClientHangsUp(t *testing.T) {
	file := mediaFile(16)
	var lengths []int64
	src := &slowSpans{device: fakeMediaDevice(file, &lengths), latency: 20 * time.Millisecond}
	m := &mediaFetch{src: src, token: "tok", host: "pit.otc"}
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/media/tok", nil).WithContext(ctx)
	w := &hangUpAfter{ResponseRecorder: httptest.NewRecorder(), limit: int(2 * maxProxiedRange), cancel: cancel}
	first, _, _, _ := m.fetch(nil, 0, maxProxiedRange)
	m.writeSpan(w, req, 0, first, int64(len(file))-1)
	time.Sleep(100 * time.Millisecond) // what was on its way arrives
	src.mu.Lock()
	asked := src.all
	src.mu.Unlock()
	if asked > 1+2+cMediaReadAhead {
		t.Errorf("%d spans asked for a client gone after two, want at most %d", asked, 1+2+cMediaReadAhead)
	}
}

// hangUpAfter is a client that goes away once it has limit bytes.
type hangUpAfter struct {
	*httptest.ResponseRecorder
	limit  int
	cancel func()
}

func (h *hangUpAfter) Write(p []byte) (int, error) {
	n, err := h.ResponseRecorder.Write(p)
	if h.Body.Len() >= h.limit {
		h.cancel()
	}
	return n, err
}

// stalledClient is a client that reads nothing until open is closed.
type stalledClient struct {
	*httptest.ResponseRecorder
	open chan struct{}
}

func (s *stalledClient) Write(p []byte) (int, error) {
	<-s.open
	return s.ResponseRecorder.Write(p)
}

// The spans asked for ahead are held under the node's budget, and an
// address's share of it: a client that reads nothing holds the one span
// it was sent plus what fits, where it held three spans whatever the
// budget; and it all comes back once the response is over.
func TestMediaReadAheadIsHeldUnderABudget(t *testing.T) {
	for _, c := range []struct {
		name         string
		max, perAddr int64
		ahead        int // spans asked ahead while the client reads nothing
	}{
		{"room for both", 64 << 20, 32 << 20, cMediaReadAhead},
		{"the node's budget full but one", maxProxiedRange, 32 << 20, 1},
		{"the address's share full", 64 << 20, 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			file := mediaFile(16)
			var lengths []int64
			src := &slowSpans{device: fakeMediaDevice(file, &lengths)}
			budget := newAheadBudget(c.max, c.perAddr)
			m := &mediaFetch{src: src, token: "tok", host: "pit.otc", ahead: budget, addr: "198.51.100.7"}
			first, _, _, _ := m.fetch(nil, 0, maxProxiedRange)
			w := &stalledClient{ResponseRecorder: httptest.NewRecorder(), open: make(chan struct{})}
			req := httptest.NewRequest(http.MethodGet, "/media/tok", nil)
			done := make(chan struct{})
			go func() { defer close(done); m.writeSpan(w, req, 0, first, int64(len(file))-1) }()

			time.Sleep(100 * time.Millisecond) // what it asks for arrives
			src.mu.Lock()
			asked := src.all - 1 // the first was asked before
			src.mu.Unlock()
			if asked != c.ahead {
				t.Errorf("%d spans asked ahead for a client reading nothing, want %d", asked, c.ahead)
			}
			if all, ofAddr := budget.held("198.51.100.7"); all != int64(c.ahead)*maxProxiedRange || ofAddr != all {
				t.Errorf("the budget holds %d bytes, %d of the address's, want %d", all, ofAddr, int64(c.ahead)*maxProxiedRange)
			}

			close(w.open)
			<-done
			if !bytes.Equal(w.Body.Bytes(), file) {
				t.Fatalf("the response is %d bytes and not the file", w.Body.Len())
			}
			deadline := time.Now().Add(time.Second)
			for all, _ := budget.held("198.51.100.7"); all != 0; all, _ = budget.held("198.51.100.7") {
				if time.Now().After(deadline) {
					t.Fatalf("the budget still holds %d bytes after the response", all)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

// A client that hangs up gives back what its response held asked ahead.
func TestMediaReadAheadIsGivenBackWhenTheClientHangsUp(t *testing.T) {
	file := mediaFile(16)
	var lengths []int64
	src := &slowSpans{device: fakeMediaDevice(file, &lengths), latency: 20 * time.Millisecond}
	budget := newAheadBudget(64<<20, 32<<20)
	m := &mediaFetch{src: src, token: "tok", host: "pit.otc", ahead: budget, addr: "198.51.100.7"}
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/media/tok", nil).WithContext(ctx)
	w := &hangUpAfter{ResponseRecorder: httptest.NewRecorder(), limit: int(2 * maxProxiedRange), cancel: cancel}
	first, _, _, _ := m.fetch(nil, 0, maxProxiedRange)
	m.writeSpan(w, req, 0, first, int64(len(file))-1)
	deadline := time.Now().Add(time.Second)
	for all, _ := budget.held("198.51.100.7"); all != 0; all, _ = budget.held("198.51.100.7") {
		if time.Now().After(deadline) {
			t.Fatalf("the budget still holds %d bytes for a client gone", all)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
