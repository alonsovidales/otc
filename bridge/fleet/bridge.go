// SPDX-License-Identifier: AGPL-3.0-or-later

package fleet

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/alonsovidales/otc/log"
	"github.com/redis/go-redis/v9"
)

// BridgeStats is what the websocket manager knows about its connections
// (websocket.Manager.FleetStats).
type BridgeStats struct {
	Devices, DeviceConns, Idle, Clients int
}

// Reporter writes this bridge node's BridgeSnapshot every interval.
type Reporter struct {
	rdb      *redis.Client
	node     string
	certFile string
	stats    func() BridgeStats
	errors   func() uint64
	started  time.Time
	interval time.Duration
	now      func() time.Time

	// errSamples: the error count at each report of the last 15 minutes,
	// oldest first.
	errSamples []errSample
	failing    bool
}

type errSample struct {
	at time.Time
	n  uint64
}

// NewReporter builds node's reporter. stats may be nil (counts of 0).
func NewReporter(rdb *redis.Client, node, certFile string, stats func() BridgeStats) *Reporter {
	return &Reporter{
		rdb: rdb, node: node, certFile: certFile, stats: stats,
		errors: log.ErrorCount, started: time.Now(), interval: DefaultInterval, now: time.Now,
	}
}

// Start reports now and then every interval, for as long as the process
// runs. Nil-safe: a bridge without a cluster has no Redis and no Fleet.
func (r *Reporter) Start() {
	if r == nil || r.rdb == nil {
		return
	}
	go func() {
		t := time.NewTicker(r.interval)
		defer t.Stop()
		for {
			r.report()
			<-t.C
		}
	}()
}

func (r *Reporter) report() {
	err := r.Publish(context.Background())
	switch {
	case err != nil && !r.failing:
		log.Error("fleet: could not publish this node's report (logged again once it works):", err)
		r.failing = true
	case err == nil && r.failing:
		log.Info("fleet: publishing this node's report again")
		r.failing = false
	}
}

// Snapshot is the node's state now.
func (r *Reporter) Snapshot() *BridgeSnapshot {
	now := r.now()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	s := &BridgeSnapshot{
		Node:       r.node,
		Time:       now.UTC(),
		Interval:   int(r.interval / time.Second),
		Version:    Version,
		Started:    r.started.UTC(),
		Goroutines: runtime.NumGoroutine(),
		FDs:        openFDs(),
		MaxFDs:     maxFDs(),
		HeapBytes:  ms.HeapAlloc,
		SysBytes:   ms.Sys,
	}
	if r.stats != nil {
		st := r.stats()
		s.Devices, s.DeviceConns, s.Idle, s.Clients = st.Devices, st.DeviceConns, st.Idle, st.Clients
	}
	s.Errors15m = r.recentErrors(now)
	if r.certFile != "" {
		if na, err := certNotAfter(r.certFile); err != nil {
			s.CertErr = err.Error()
		} else {
			s.CertNotAfter = na.UTC()
		}
	}
	return s
}

// recentErrors records the error count now and returns how many were
// logged over the last 15 minutes (or since the start).
func (r *Reporter) recentErrors(now time.Time) int {
	n := r.errors()
	r.errSamples = append(r.errSamples, errSample{now, n})
	// Keep the newest sample at least 15 minutes old, and those after it.
	for len(r.errSamples) > 1 && now.Sub(r.errSamples[1].at) >= 15*time.Minute {
		r.errSamples = r.errSamples[1:]
	}
	if now.Sub(r.errSamples[0].at) >= 15*time.Minute {
		return int(n - r.errSamples[0].n)
	}
	// Up for less than 15 minutes: every error since the start.
	return int(n)
}

// Publish writes the snapshot with its TTL and records when this node
// last reported.
func (r *Reporter) Publish(ctx context.Context) error {
	s := r.Snapshot()
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	pipe := r.rdb.Pipeline()
	pipe.Set(ctx, KeyBridgePrefix+r.node, b, SnapshotTTL)
	pipe.HSet(ctx, KeyKnownBridges, r.node, strconv.FormatInt(s.Time.Unix(), 10))
	_, err = pipe.Exec(ctx)
	return err
}

func certNotAfter(path string) (time.Time, error) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 256<<10))
	if err != nil {
		return time.Time{}, err
	}
	c, err := FirstCert(b)
	if err != nil {
		return time.Time{}, err
	}
	return c.NotAfter, nil
}

// openFDs counts /proc/self/fd; -1 where there is no /proc.
func openFDs() int {
	d, err := os.Open("/proc/self/fd")
	if err != nil {
		return -1
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return -1
	}
	return len(names) - 1 // the directory's own descriptor
}

func maxFDs() int {
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl); err != nil {
		return -1
	}
	if rl.Cur > 1<<31 {
		return 1 << 31
	}
	return int(rl.Cur)
}
