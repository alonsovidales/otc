// SPDX-License-Identifier: AGPL-3.0-or-later

package limits

import (
	"net/http"
	"net/http/httptest"
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
