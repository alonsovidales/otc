// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/alonsovidales/otc/bridge/cluster"
)

// Issue #144: a device's first connection to this node claims it in
// Redis, and its last one going away releases it - so the other nodes
// know where to send its clients, and stop doing so.
func TestClusterClaimFollowsConnections(t *testing.T) {
	mr := miniredis.RunT(t)
	c := cluster.New("bridge1", "10.10.0.2:8444", "t", redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	other := cluster.New("bridge2", "10.10.0.3:8444", "t", redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	mg := &Manager{bridges: map[string]*bridgePool{}}
	mg.SetCluster(c)

	pool := &bridgePool{lock: new(sync.Mutex)}
	mg.bridgesMu.Lock()
	mg.bridges["cala.off-the.cloud"] = pool
	mg.bridgesMu.Unlock()

	pool.lock.Lock()
	mg.onDeviceConnectionRegistered("cala.off-the.cloud", pool)
	pool.lock.Unlock()
	waitFor(t, func() bool { return other.HeldElsewhere("cala.off-the.cloud") }, "claimed")
	if !mg.IsOnline("cala.off-the.cloud") {
		t.Fatal("not online")
	}

	mg.onDeviceConnectionDied("cala.off-the.cloud", nil)
	waitFor(t, func() bool { return !other.Online("cala.off-the.cloud") }, "released")
	pool.lock.Lock()
	if pool.offlineTimer != nil {
		pool.offlineTimer.Stop()
	}
	pool.lock.Unlock()
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("never %s", what)
}
