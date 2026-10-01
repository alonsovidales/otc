// SPDX-License-Identifier: AGPL-3.0-or-later

// Package limits holds the bridge's request limits (issue #163): bounded
// request bodies and per-key rate limits.
package limits

import (
	"encoding/json"
	"net/http"
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

// Rate is a token bucket per key (an address, a domain): burst requests
// at once, refilled at perSecond. Keys idle long enough to be full again
// are dropped, so the map only holds recent keys.
type Rate struct {
	mu        sync.Mutex
	perSecond float64
	burst     float64
	buckets   map[string]*bucket
	lastSweep time.Time
	now       func() time.Time
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
		return false
	}
	b.tokens--
	return true
}

func (l *Rate) sweep(now time.Time) {
	full := time.Duration(l.burst / l.perSecond * float64(time.Second))
	if now.Sub(l.lastSweep) < full && len(l.buckets) < 100000 {
		return
	}
	l.lastSweep = now
	for k, b := range l.buckets {
		if now.Sub(b.last) >= full {
			delete(l.buckets, k)
		}
	}
}
