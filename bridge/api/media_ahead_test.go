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
