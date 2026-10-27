// SPDX-License-Identifier: AGPL-3.0-or-later

// Package limits holds the bridge's request limits (issue #163): bounded
// request bodies and per-key rate limits.
package limits

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// BcryptCost is the cost for every new password hash on the bridge (issue
// #163: bcrypt's default, 10, is low for a public login). Existing hashes
// keep their own cost; account passwords are rehashed at the next sign-in.
const BcryptCost = 12

// BodyReadTimeout is how long a request body may take to arrive. The
// servers only bound the headers (ReadHeaderTimeout): a whole-request
// ReadTimeout would also cut the websockets the cluster router proxies.
const BodyReadTimeout = 15 * time.Second

// MaxJSONBody bounds every JSON request body: the largest legitimate one,
// the contact form, is a few KB.
const MaxJSONBody = 64 << 10

// DecodeJSON reads r's JSON body into v: at most limit bytes, within
// BodyReadTimeout. Anything longer is an error, not a truncation.
func DecodeJSON(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(BodyReadTimeout))
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v)
}

// ParseForm is r.ParseForm with a form body bounded as DecodeJSON bounds
// a JSON one: at most limit bytes, within BodyReadTimeout. A request
// without a body (a GET) has only its URL to parse, and no deadline set.
func ParseForm(w http.ResponseWriter, r *http.Request, limit int64) error {
	if r.Body != nil && r.Body != http.NoBody {
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(BodyReadTimeout))
		r.Body = http.MaxBytesReader(w, r.Body, limit)
	}
	return r.ParseForm()
}

// RetryAfter sets a 429's Retry-After header to d, in whole seconds
// rounded up and never 0: asked again that late, the request gets
// through (unless more requests used up the limit meanwhile).
func RetryAfter(w http.ResponseWriter, d time.Duration) {
	w.Header().Set("Retry-After", strconv.FormatInt(max(1, int64(math.Ceil(d.Seconds()))), 10))
}

// WriteIdleTimeout is how long a proxied response may wait on a client
// that has stopped reading. The servers have no WriteTimeout either (it
// would cut the websockets), so without one a client that never reads
// holds the response, and the buffer behind it, forever.
const WriteIdleTimeout = 30 * time.Second

// writeChunk is how much WriteAll writes under one write deadline.
const writeChunk = 32 << 10

// WriteAll writes b to w a chunk at a time, renewing the write deadline
// before each: a slow client that keeps reading is never cut, one that
// stops for stall is. Where w has no deadlines (a test recorder) it just
// writes.
func WriteAll(w http.ResponseWriter, b []byte, stall time.Duration) error {
	rc := http.NewResponseController(w)
	for len(b) > 0 {
		n := min(len(b), writeChunk)
		_ = rc.SetWriteDeadline(time.Now().Add(stall))
		if _, err := w.Write(b[:n]); err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

// cSweepKeys is the map size that sweeps a Rate before its time-based
// sweep is due. Past it the trigger is twice what the last sweep left, so
// a map that keeps growing is scanned in O(1) per Allow (amortised), not
// on every call.
//
// The map has no hard cap on purpose: refusing a key it doesn't hold yet
// would turn a flood of distinct addresses into a lockout of everyone new
// (an hour long for the sign-up and email limiters), and evicting one
// would hand a key a fresh bucket. Its size stays bounded by the request
// rate over one refill window.
const cSweepKeys = 100000

// Rate is a token bucket per key (an address, a domain): burst requests
// at once, refilled at perSecond. Keys idle long enough to be full again
// are dropped, so the map only holds recent keys.
type Rate struct {
	mu        sync.Mutex
	perSecond float64
	burst     float64
	buckets   map[string]*bucket
	lastSweep time.Time
	// kept is the map's size after the last sweep.
	kept int
	// sweeps counts full scans (tests).
	sweeps int
	now    func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func NewRate(perSecond, burst float64) *Rate {
	return &Rate{perSecond: perSecond, burst: burst, buckets: map[string]*bucket{}, now: time.Now}
}

// Allow takes a token for key, reporting false when there is none.
func (l *Rate) Allow(key string) bool {
	ok, _ := l.Take(key)
	return ok
}

// Take is Allow that also says, when there is no token, how long until
// key has one again (a 429's Retry-After).
func (l *Rate) Take(key string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.perSecond)
	b.last = now
	if b.tokens < 1 {
		return false, time.Duration((1 - b.tokens) / l.perSecond * float64(time.Second))
	}
	b.tokens--
	return true, 0
}

// sweep drops the keys idle for long enough to be full again (exactly
// like an absent key), once per refill window or when the map has doubled
// since the last sweep.
func (l *Rate) sweep(now time.Time) {
	full := time.Duration(l.burst / l.perSecond * float64(time.Second))
	if now.Sub(l.lastSweep) < full && len(l.buckets) < max(cSweepKeys, 2*l.kept) {
		return
	}
	l.lastSweep = now
	l.sweeps++
	for k, b := range l.buckets {
		if now.Sub(b.last) >= full {
			delete(l.buckets, k)
		}
	}
	l.kept = len(l.buckets)
}
