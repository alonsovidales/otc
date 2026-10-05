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

// Issue #144: a device's connections are spread over the nodes, so a
// domain released (or given a new identity) through one node must stop
// relaying on the others too.
func TestDropDomainsReachesTheOtherNode(t *testing.T) {
	mr := miniredis.RunT(t)
	node := func(id string) *Manager {
		mg := &Manager{bridges: map[string]*bridgePool{}}
		mg.SetCluster(cluster.New(id, "10.10.0.2:8444", "t", redis.NewClient(&redis.Options{Addr: mr.Addr()})))
		return mg
	}
	a, b := node("bridge1"), node("bridge2")
	defer stopOfflineTimer(b, "cala.off-the.cloud")
	_, died := pooledRelay(t, b, "cala.off-the.cloud", "owner", true)
	waitFor(t, func() bool { return mr.PubSubNumSub("otc:drop")["otc:drop"] == 2 }, "subscribed")

	a.DropDomains([]string{"cala.off-the.cloud"})
	waitDead(t, died, "the other node's relay")
}

// A domain given a new identity stops relaying for the old one on every
// node, but the new device's connections stay: it registers as soon as it
// is told, on any node, possibly before that node gets the message.
func TestDropReplacedIdentityKeepsTheNewDevice(t *testing.T) {
	mr := miniredis.RunT(t)
	node := func(id string) *Manager {
		mg := &Manager{bridges: map[string]*bridgePool{}}
		mg.SetCluster(cluster.New(id, "10.10.0.2:8444", "t", redis.NewClient(&redis.Options{Addr: mr.Addr()})))
		return mg
	}
	const domain = "cala.off-the.cloud"
	a, b := node("bridge1"), node("bridge2")
	defer stopOfflineTimer(a, domain)
	defer stopOfflineTimer(b, domain)
	_, oldA := pooledRelay(t, a, domain, "old-owner", true)
	_, newA := pooledRelay(t, a, domain, "new-owner", true)
	_, oldB := pooledRelay(t, b, domain, "old-owner", false)
	_, newB := pooledRelay(t, b, domain, "new-owner", true)
	waitFor(t, func() bool { return mr.PubSubNumSub("otc:drop")["otc:drop"] == 2 }, "subscribed")

	a.DropReplacedIdentity(domain, "new-owner")
	waitDead(t, oldA, "this node's relay of the replaced identity")
	waitDead(t, oldB, "the other node's relay of the replaced identity")
	select {
	case <-newA:
		t.Fatal("this node closed the new device's relay")
	case <-newB:
		t.Fatal("the other node closed the new device's relay")
	case <-time.After(200 * time.Millisecond):
	}
	var none *Manager
	none.DropReplacedIdentity(domain, "new-owner") // no panic
}
