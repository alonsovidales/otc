// SPDX-License-Identifier: AGPL-3.0-or-later

package fleet

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestReadJoinsAgentsAndBridges(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()

	// bridge1: an agent and its bridge process.
	hs := healthy("bridge1", "bridge")
	hs.Time = t0
	b, _ := json.Marshal(hs)
	rdb.Set(ctx, KeyHostPrefix+"bridge1", b, SnapshotTTL)
	rdb.HSet(ctx, KeyKnownHosts, "bridge1", t0.Unix())
	r := NewReporter(rdb, "bridge1", "", func() BridgeStats { return BridgeStats{Devices: 2, DeviceConns: 9, Idle: 7, Clients: 3} })
	r.now = func() time.Time { return t0 }
	r.errors = func() uint64 { return 4 }
	if err := r.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	// bridge2: its bridge reports, its agent was never installed.
	r2 := NewReporter(rdb, "bridge2", "", nil)
	r2.now = r.now
	if err := r2.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	// redis: reported an hour ago; its snapshot has expired since.
	rdb.HSet(ctx, KeyKnownHosts, "redis", t0.Add(-time.Hour).Unix())
	// junk: a snapshot that doesn't decode counts as missing.
	rdb.Set(ctx, KeyHostPrefix+"junk", "{not json", 0)
	rdb.HSet(ctx, KeyKnownHosts, "junk", t0.Unix())

	v, err := Read(ctx, rdb, t0.Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, h := range v.Hosts {
		names = append(names, h.Name+":"+string(h.State))
	}
	if got := strings.Join(names, ","); got != "bridge1:good,bridge2:bad,junk:bad,redis:bad" {
		t.Fatal(got)
	}
	b1 := v.Hosts[0]
	if b1.Bridge == nil || b1.Bridge.Clients != 3 || b1.Bridge.Devices != 2 || b1.Bridge.Errors15m != 4 || b1.Host == nil {
		t.Fatalf("%+v", b1)
	}
	if c := check(t, &v.Hosts[1], "report"); !strings.Contains(c.Detail, "never reported") {
		t.Fatalf("%+v", c)
	}
	if c := check(t, &v.Hosts[3], "report"); !strings.Contains(c.Detail, "no report since") || !v.Hosts[3].Stale {
		t.Fatalf("%+v", c)
	}
}

func TestReadFailsWithoutRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1, DialTimeout: 200 * time.Millisecond})
	mr.Close()
	if _, err := Read(context.Background(), rdb, t0); err == nil {
		t.Fatal("Redis down must be an error, not an empty fleet")
	}
}

func TestRecentErrors(t *testing.T) {
	r := NewReporter(nil, "bridge1", "", nil)
	n := uint64(0)
	r.errors = func() uint64 { return n }
	start := t0
	r.started = start
	at := func(min int, count uint64) int {
		n = count
		return r.recentErrors(start.Add(time.Duration(min) * time.Minute))
	}
	if got := at(0, 5); got != 5 {
		t.Fatal(got)
	}
	if got := at(10, 8); got != 8 {
		t.Fatal("within the first 15 minutes everything counts:", got)
	}
	if got := at(16, 20); got != 15 {
		t.Fatal("from the sample of minute 0 (5) to 20:", got)
	}
	// The newest sample at least 15 minutes old is minute 10's (8).
	if got := at(30, 21); got != 13 {
		t.Fatal(got)
	}
}
