// SPDX-License-Identifier: AGPL-3.0-or-later

// Package cluster lets several bridge nodes serve one set of devices
// (issue #144). A device keeps a pool of connections to whichever nodes DNS
// sends it to; a client can land on any node. Redis records which nodes
// hold connections to which device, and a node without one forwards the
// client's whole request to a node that has one, over the private network
// between them (see api.clusterRoute).
//
// Without a [cluster] section there is no cluster: Init returns nil and
// every method on a nil *Cluster answers as a single bridge would.
package cluster

import (
	"context"
	"crypto/subtle"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	"github.com/redis/go-redis/v9"
)

const (
	// Holding is how long a node's claim on a device lasts without being
	// refreshed; Refresh is how often the nodes refresh theirs.
	Holding = 60 * time.Second
	Refresh = 20 * time.Second

	// TokenHeader carries the cluster token on a forwarded request;
	// HopHeader is set by the internal listener, once the token checked
	// out, so handlers know the request came from another node (and trust
	// its X-Forwarded-For). The public listeners strip both.
	TokenHeader = "X-Otc-Cluster-Token"
	HopHeader   = "X-Otc-Cluster-Hop"

	keyNodes    = "otc:nodes"  // node id -> internal address
	keyDevice   = "otc:dev:"   // + domain: node id -> claim expiry (unix)
	keyDrop     = "otc:drop"   // pub/sub: domains whose connections every node closes
	keyAlert    = "otc:alert:" // + domain: the node that sent this outage's offline alert
	cRedisTimer = 2 * time.Second

	// AlertClaim only tidies up: a claim is cleared as soon as any node
	// holds the device again (Hold), which is what lets the next outage
	// alert.
	AlertClaim = 10 * time.Minute
)

// Cluster is this node's view of the others.
type Cluster struct {
	node  string // this node's id, e.g. "bridge1"
	addr  string // this node's internal listener, e.g. "10.10.0.2:8444"
	token string // shared by the nodes; proves a forwarded request is theirs
	rdb   *redis.Client
	now   func() time.Time
}

// Init reads [cluster]: node-id, internal-addr, redis-addr, redis-pass,
// token. Nil when the section isn't there.
func Init() *Cluster {
	if !cfg.HasSection("cluster") {
		return nil
	}
	c := New(
		cfg.GetStr("cluster", "node-id"),
		cfg.GetStr("cluster", "internal-addr"),
		cfg.GetStr("cluster", "token"),
		redis.NewClient(&redis.Options{
			Addr:         cfg.GetStr("cluster", "redis-addr"),
			Password:     cfg.GetStr("cluster", "redis-pass"),
			DialTimeout:  cRedisTimer,
			ReadTimeout:  cRedisTimer,
			WriteTimeout: cRedisTimer,
		}),
	)
	if c.node == "" || c.addr == "" || len(c.token) < 32 {
		log.Fatal("[cluster] needs node-id, internal-addr and a token of 32 characters or more")
	}
	return c
}

// New is Init with its parts given (tests).
func New(node, addr, token string, rdb *redis.Client) *Cluster {
	return &Cluster{node: node, addr: addr, token: token, rdb: rdb, now: time.Now}
}

// Enabled reports whether this bridge is part of a cluster.
func (c *Cluster) Enabled() bool { return c != nil }

// Node is this node's id.
func (c *Cluster) Node() string {
	if c == nil {
		return ""
	}
	return c.node
}

// InternalAddr is where this node's internal listener runs.
func (c *Cluster) InternalAddr() string { return c.addr }

// ValidToken reports whether t is the cluster's token.
func (c *Cluster) ValidToken(t string) bool {
	return c != nil && t != "" && subtle.ConstantTimeCompare([]byte(t), []byte(c.token)) == 1
}

// Token is what this node sends with a forwarded request.
func (c *Cluster) Token() string { return c.token }

func (c *Cluster) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), cRedisTimer)
}

// Announce publishes this node's internal address, so the others can
// forward to it. Called on start and with every refresh.
func (c *Cluster) Announce() error {
	if c == nil {
		return nil
	}
	ctx, cancel := c.ctx()
	defer cancel()
	return c.rdb.HSet(ctx, keyNodes, c.node, c.addr).Err()
}

// Hold records that this node has connections to each of domains, until
// Holding from now.
func (c *Cluster) Hold(domains ...string) error {
	if c == nil || len(domains) == 0 {
		return nil
	}
	until := strconv.FormatInt(c.now().Add(Holding).Unix(), 10)
	ctx, cancel := c.ctx()
	defer cancel()
	pipe := c.rdb.Pipeline()
	for _, d := range domains {
		pipe.HSet(ctx, keyDevice+d, c.node, until)
		// The key outlives every claim in it by a little; a device no node
		// holds any more goes away on its own.
		pipe.Expire(ctx, keyDevice+d, 2*Holding)
		// Held again: the device is back, so its next outage alerts anew.
		pipe.Del(ctx, keyAlert+d)
	}
	_, err := pipe.Exec(ctx)
	return err
}

// Release records that this node no longer has connections to domain.
func (c *Cluster) Release(domain string) error {
	if c == nil {
		return nil
	}
	ctx, cancel := c.ctx()
	defer cancel()
	return c.rdb.HDel(ctx, keyDevice+domain, c.node).Err()
}

// holders is the nodes whose claim on domain hasn't expired.
func (c *Cluster) holders(domain string) ([]string, error) {
	ctx, cancel := c.ctx()
	defer cancel()
	m, err := c.rdb.HGetAll(ctx, keyDevice+domain).Result()
	if err != nil {
		return nil, err
	}
	now := c.now().Unix()
	var out []string
	for node, until := range m {
		if t, err := strconv.ParseInt(until, 10, 64); err == nil && t > now {
			out = append(out, node)
		}
	}
	return out, nil
}

// Online reports whether any node, this one included, holds domain.
func (c *Cluster) Online(domain string) bool {
	if c == nil {
		return false
	}
	nodes, err := c.holders(domain)
	if err != nil {
		log.Error("cluster: could not ask Redis who holds", domain, ":", err)
		return false
	}
	return len(nodes) > 0
}

// HeldElsewhere reports whether another node holds domain.
func (c *Cluster) HeldElsewhere(domain string) bool {
	_, ok := c.Locate(domain)
	return ok
}

// Locate is the internal address of another node holding domain - one at
// random when several do.
func (c *Cluster) Locate(domain string) (string, bool) {
	if c == nil {
		return "", false
	}
	nodes, err := c.holders(domain)
	if err != nil {
		log.Error("cluster: could not ask Redis who holds", domain, ":", err)
		return "", false
	}
	var others []string
	for _, n := range nodes {
		if n != c.node {
			others = append(others, n)
		}
	}
	if len(others) == 0 {
		return "", false
	}
	node := others[rand.IntN(len(others))]
	ctx, cancel := c.ctx()
	defer cancel()
	addr, err := c.rdb.HGet(ctx, keyNodes, node).Result()
	if err != nil || addr == "" {
		log.Error("cluster: node", node, "holds", domain, "but has no address:", err)
		return "", false
	}
	return addr, true
}

// PublishDrop tells every node, this one included, to close its
// connections to domains: released, deleted, or given a new identity.
// A node on a release without SubscribeDrops ignores it.
func (c *Cluster) PublishDrop(domains ...string) error {
	if c == nil || len(domains) == 0 {
		return nil
	}
	ctx, cancel := c.ctx()
	defer cancel()
	pipe := c.rdb.Pipeline()
	for _, d := range domains {
		pipe.Publish(ctx, keyDrop, d)
	}
	_, err := pipe.Exec(ctx)
	return err
}

// SubscribeDrops calls drop with every domain PublishDrop names, from any
// node, for as long as the process runs. go-redis resubscribes after a
// lost connection; what was published meanwhile is lost, which each
// node's periodic check of its devices against the database makes up for.
func (c *Cluster) SubscribeDrops(drop func(domain string)) {
	if c == nil {
		return
	}
	ps := c.rdb.Subscribe(context.Background(), keyDrop)
	go func() {
		for m := range ps.Channel() {
			drop(m.Payload)
		}
	}()
}

// ClaimAlert reports whether this node is the one to send domain's
// offline alert for this outage: when the device was on both nodes, both
// count down and both found it gone. True without a cluster, and when
// Redis can't say - a duplicate alert beats none.
func (c *Cluster) ClaimAlert(domain string) bool {
	if c == nil {
		return true
	}
	ctx, cancel := c.ctx()
	defer cancel()
	ok, err := c.rdb.SetNX(ctx, keyAlert+domain, c.node, AlertClaim).Result()
	if err != nil {
		log.Error("cluster: could not claim the offline alert for", domain, ":", err)
		return true
	}
	return ok
}
