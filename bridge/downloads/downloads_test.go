// SPDX-License-Identifier: AGPL-3.0-or-later

package downloads

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGitHub answers the releases API from pages, by the page= of the
// address asked (1 when none), and records what was asked.
type fakeGitHub struct {
	mu     sync.Mutex
	pages  map[int]string // body
	links  map[int]string // Link header
	status map[int]int
	asked  []*http.Request
}

func (f *fakeGitHub) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, r)
	page := 1
	fmt.Sscanf(r.URL.Query().Get("page"), "%d", &page)
	status := http.StatusOK
	if s, ok := f.status[page]; ok {
		status = s
	}
	h := http.Header{}
	if l := f.links[page]; l != "" {
		h.Set("Link", l)
	}
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: h, Body: io.NopCloser(strings.NewReader(f.pages[page])), Request: r}, nil
}

// store records what a poll saved.
type store struct {
	mu    sync.Mutex
	saved []map[string]int64
	days  []time.Time
	err   error
}

func (s *store) SaveDownloadCounts(_ context.Context, day time.Time, counts map[string]int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved = append(s.saved, maps.Clone(counts))
	s.days = append(s.days, day)
	return s.err
}

func newPoller(gh *fakeGitHub, st *store) *Poller {
	p := New(st)
	p.Repo = "someone/fork"
	p.Client = &http.Client{Transport: gh, Timeout: time.Second}
	return p
}

const page1 = `[
  {"tag_name": "v136", "assets": [{"name": "web-dist.tar.gz", "download_count": 40}, {"name": "src.tar.gz", "download_count": 31}]},
  {"tag_name": "desktop", "assets": [{"name": "otc-sync-linux-amd64", "download_count": 12}, {"name": "desktop.json", "download_count": 900}]},
  {"tag_name": "image", "assets": [{"name": "off-the-cloud-rpi-lite-arm64.img.xz", "download_count": 77}, {"name": "off-the-cloud-rpi-lite-arm64.img.xz.sha256", "download_count": 20}]},
  {"tag_name": "v1.2-beta", "assets": [{"name": "src.tar.gz", "download_count": 5}]},
  {"tag_name": "notes", "assets": [{"name": "readme.pdf", "download_count": 3}]}
]`

const page2 = `[
  {"tag_name": "v135", "assets": [{"name": "src.tar.gz", "download_count": 29}, {"name": "web-dist.tar.gz", "download_count": 35}]},
  {"tag_name": "v134", "assets": []}
]`

// A poll keeps every file of the image and desktop releases by its name,
// each device release's source archive by the release, nothing else; it
// follows the Link header to the next page and asks as the bridge.
func TestPollFollowsPagesAndKeepsWhatCounts(t *testing.T) {
	gh := &fakeGitHub{
		pages: map[int]string{1: page1, 2: page2},
		links: map[int]string{1: `<https://api.github.com/repositories/1/releases?per_page=100&page=2>; rel="next", <https://api.github.com/repositories/1/releases?per_page=100&page=2>; rel="last"`},
	}
	st := &store{}
	now := time.Date(2026, 10, 24, 23, 30, 0, 0, time.FixedZone("CEST", 2*3600))
	if err := newPoller(gh, st).Poll(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{
		"src.tar.gz@v136":                            31,
		"src.tar.gz@v135":                            29,
		"otc-sync-linux-amd64":                       12,
		"desktop.json":                               900,
		"off-the-cloud-rpi-lite-arm64.img.xz":        77,
		"off-the-cloud-rpi-lite-arm64.img.xz.sha256": 20,
	}
	if len(st.saved) != 1 || !maps.Equal(st.saved[0], want) {
		t.Fatalf("saved %v, want %v", st.saved, want)
	}
	if !st.days[0].Equal(now) || st.days[0].Location() != time.UTC {
		t.Errorf("day %v", st.days[0])
	}
	if len(gh.asked) != 2 {
		t.Fatalf("%d requests", len(gh.asked))
	}
	first := gh.asked[0]
	if first.URL.String() != "https://api.github.com/repos/someone/fork/releases?per_page=100" {
		t.Errorf("asked %s", first.URL)
	}
	if first.Header.Get("Accept") != "application/vnd.github+json" || !strings.Contains(first.Header.Get("User-Agent"), "otc-bridge") || first.Header.Get("Authorization") != "" {
		t.Errorf("headers %v", first.Header)
	}
	if gh.asked[1].URL.Query().Get("page") != "2" {
		t.Errorf("second request %s", gh.asked[1].URL)
	}
}

// At most cMaxPages are read, however many the Link header promises.
func TestPollReadsAtMostFivePages(t *testing.T) {
	gh := &fakeGitHub{pages: map[int]string{}, links: map[int]string{}}
	for i := 1; i <= 9; i++ {
		gh.pages[i] = fmt.Sprintf(`[{"tag_name": "v%d", "assets": [{"name": "src.tar.gz", "download_count": %d}]}]`, i, i)
		gh.links[i] = fmt.Sprintf(`<https://api.github.com/repositories/1/releases?per_page=100&page=%d>; rel="next"`, i+1)
	}
	st := &store{}
	if err := newPoller(gh, st).Poll(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(gh.asked) != cMaxPages || len(st.saved) != 1 || len(st.saved[0]) != cMaxPages {
		t.Fatalf("%d requests, saved %v", len(gh.asked), st.saved)
	}
}

// A poll that fails anywhere - GitHub refusing (its rate limit), an
// answer that isn't the releases, the transport - stores nothing, not
// even the pages read before.
func TestFailedPollStoresNothing(t *testing.T) {
	next := map[int]string{1: `<https://api.github.com/repositories/1/releases?per_page=100&page=2>; rel="next"`}
	cases := map[string]*fakeGitHub{
		"rate limited":    {pages: map[int]string{1: page1, 2: `{"message": "API rate limit exceeded"}`}, links: next, status: map[int]int{2: http.StatusForbidden}},
		"not the release": {pages: map[int]string{1: page1, 2: `{"message": "Not Found"}`}, links: next},
		"cut short":       {pages: map[int]string{1: page1, 2: `[{"tag_name": "v1", "assets": [`}, links: next},
		"nothing to keep": {pages: map[int]string{1: `[{"tag_name": "notes", "assets": []}]`}},
	}
	for name, gh := range cases {
		st := &store{}
		if err := newPoller(gh, st).Poll(context.Background(), time.Now()); err == nil {
			t.Errorf("%s: no error", name)
		}
		if len(st.saved) != 0 {
			t.Errorf("%s: saved %v", name, st.saved)
		}
	}

	st := &store{}
	p := newPoller(nil, st)
	p.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("no route to host") })}
	if err := p.Poll(context.Background(), time.Now()); err == nil || len(st.saved) != 0 {
		t.Errorf("transport failure: %v, saved %v", err, st.saved)
	}
}

// A page longer than MaxPage is an error, not the releases read so far:
// GitHub's answer is cut there, and a poll that couldn't read it all
// stores nothing.
func TestPollPageTooLong(t *testing.T) {
	gh := &fakeGitHub{pages: map[int]string{1: page1}}
	st := &store{}
	p := newPoller(gh, st)
	p.MaxPage = int64(len(page1) / 2)
	if err := p.Poll(context.Background(), time.Now()); err == nil || len(st.saved) != 0 {
		t.Fatalf("a page past MaxPage: %v, saved %v", err, st.saved)
	}
	// The same page within it is read.
	p.MaxPage = int64(len(page1))
	if err := p.Poll(context.Background(), time.Now()); err != nil || len(st.saved) != 1 {
		t.Fatalf("a page within MaxPage: %v, saved %v", err, st.saved)
	}
}

// Stop ends a write under way: the store is given the poll's context.
func TestStopEndsTheWrite(t *testing.T) {
	gh := &fakeGitHub{pages: map[int]string{1: page1}}
	writing := make(chan struct{})
	st := &blockingStore{writing: writing}
	p := New(st)
	p.Client = &http.Client{Transport: gh, Timeout: time.Second}
	p.FirstAfter = time.Millisecond
	p.Start()
	select {
	case <-writing:
	case <-time.After(5 * time.Second):
		t.Fatal("no write in 5 s")
	}
	stopped := make(chan struct{})
	go func() { p.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop waited for the write")
	}
}

// blockingStore's write waits for its context to end.
type blockingStore struct{ writing chan struct{} }

func (s *blockingStore) SaveDownloadCounts(ctx context.Context, _ time.Time, _ map[string]int64) error {
	close(s.writing)
	<-ctx.Done()
	return ctx.Err()
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Only rel="next", and only on GitHub's API.
func TestNextPage(t *testing.T) {
	for link, want := range map[string]string{
		"": "",
		`<https://api.github.com/repositories/1/releases?page=2>; rel="next", <https://api.github.com/repositories/1/releases?page=4>; rel="last"`: "https://api.github.com/repositories/1/releases?page=2",
		`<https://api.github.com/repositories/1/releases?page=1>; rel="prev", <https://api.github.com/repositories/1/releases?page=3>; rel="next"`: "https://api.github.com/repositories/1/releases?page=3",
		`<https://api.github.com/repositories/1/releases?page=1>; rel="first"`:                                                                     "",
		`<https://evil.example/releases?page=2>; rel="next"`:                                                                                       "",
		`<http://api.github.com/repositories/1/releases?page=2>; rel="next"`:                                                                       "",
		`https://api.github.com/repositories/1/releases?page=2; rel="next"`:                                                                        "",
	} {
		if got := nextPage(link); got != want {
			t.Errorf("%q: %q, want %q", link, got, want)
		}
	}
}

// Start polls once after FirstAfter and then every Every; a failed poll
// is only logged; Stop ends it.
func TestStartPollsUntilStopped(t *testing.T) {
	gh := &fakeGitHub{pages: map[int]string{1: page1}}
	st := &store{err: errors.New("primary away")}
	p := newPoller(gh, st)
	p.FirstAfter, p.Every = 10*time.Millisecond, 20*time.Millisecond
	p.Start()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st.mu.Lock()
		n := len(st.saved)
		st.mu.Unlock()
		if n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d polls in 5 s", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	p.Stop()
	st.mu.Lock()
	n := len(st.saved)
	st.mu.Unlock()
	time.Sleep(60 * time.Millisecond)
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.saved) != n {
		t.Errorf("polled after Stop: %d, then %d", n, len(st.saved))
	}
}
