// SPDX-License-Identifier: AGPL-3.0-or-later

package limits

import (
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
	now = now.Add(2 * time.Hour)
	l.Allow("late")
	if len(l.buckets) != 1 {
		t.Fatalf("%d keys kept after the window, want 1", len(l.buckets))
	}
}

// A full map refuses new keys and still decides known ones.
func TestRateRefusesNewKeysWhenFull(t *testing.T) {
	defer func(n int) { maxKeys = n }(maxKeys)
	maxKeys = 3
	now := time.Unix(1000, 0)
	l := NewRate(1, 2)
	l.now = func() time.Time { return now }
	for _, k := range []string{"a", "b", "c"} {
		if !l.Allow(k) {
			t.Fatalf("%s refused below the cap", k)
		}
	}
	if l.Allow("d") {
		t.Fatal("a new key was let in past the cap")
	}
	if !l.Allow("a") || l.Allow("a") {
		t.Fatal("a known key was not decided by its own bucket")
	}
	now = now.Add(time.Minute)
	if !l.Allow("d") {
		t.Fatal("no room after the idle keys were swept")
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
