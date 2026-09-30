// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func twoNodes(t *testing.T) (*Cluster, *Cluster, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := func() *redis.Client { return redis.NewClient(&redis.Options{Addr: mr.Addr()}) }
	a := New("bridge1", "10.10.0.2:8444", "t", rdb())
	b := New("bridge2", "10.10.0.3:8444", "t", rdb())
	for _, c := range []*Cluster{a, b} {
		if err := c.Announce(); err != nil {
			t.Fatal(err)
		}
	}
	return a, b, mr
}

// A device one node holds is online everywhere, and the other node is
// told where to send its clients; the holder itself is not sent to itself.
func TestHoldLocateRelease(t *testing.T) {
	a, b, _ := twoNodes(t)
	if a.Online("cala.off-the.cloud") {
		t.Fatal("online before anyone holds it")
	}
	if err := a.Hold("cala.off-the.cloud"); err != nil {
		t.Fatal(err)
	}
	if !b.Online("cala.off-the.cloud") || !a.Online("cala.off-the.cloud") {
		t.Fatal("held by bridge1 but not online")
	}
	if addr, ok := b.Locate("cala.off-the.cloud"); !ok || addr != "10.10.0.2:8444" {
		t.Fatalf("bridge2 locates %q %v, want bridge1's address", addr, ok)
	}
	if _, ok := a.Locate("cala.off-the.cloud"); ok {
		t.Fatal("bridge1 was sent to itself")
	}
	if !b.HeldElsewhere("cala.off-the.cloud") || a.HeldElsewhere("cala.off-the.cloud") {
		t.Fatal("HeldElsewhere wrong")
	}
	if err := a.Release("cala.off-the.cloud"); err != nil {
		t.Fatal(err)
	}
	if b.Online("cala.off-the.cloud") {
		t.Fatal("still online after its only holder released it")
	}
}

// A node that stops refreshing (crashed, cut off) loses its claim after
// Holding, without anyone releasing it.
func TestClaimExpires(t *testing.T) {
	a, b, _ := twoNodes(t)
	if err := a.Hold("pit.off-the.cloud"); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(Holding + time.Second)
	b.now = func() time.Time { return later }
	if b.Online("pit.off-the.cloud") {
		t.Fatal("an expired claim still counts")
	}
}

// Without [cluster] every answer is a single bridge's.
func TestNilCluster(t *testing.T) {
	var c *Cluster
	if c.Enabled() || c.Online("x") || c.HeldElsewhere("x") || c.ValidToken("anything") {
		t.Fatal("a nil cluster claimed something")
	}
	if err := c.Hold("x"); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Locate("x"); ok {
		t.Fatal("a nil cluster located a node")
	}
}
