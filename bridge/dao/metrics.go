// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"context"
	"errors"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/alonsovidales/otc/log"
)

// Issue #144: every relayed message used to be one INSERT ... ON DUPLICATE
// KEY UPDATE on the same device_metrics row, on the one MySQL primary all
// bridge nodes share - the load test found that, not the relay, capping a
// node at ~10,000 messages a second with mysqld at 550% CPU. The counts are
// now added up here and written once per device and hour every
// cMetricsFlush: the admin panel's hourly figures lag by at most that.
const cMetricsFlush = 10 * time.Second

type metricKey struct {
	domain string
	hour   time.Time
}

type metricCounts struct {
	requests, in, out int64
}

// RecordDeviceActivity counts one relayed request/response for domain.
func (dao *Dao) RecordDeviceActivity(domain string, bytesIn, bytesOut int64) error {
	dao.touchLastClient(domain)
	k := metricKey{domain, time.Now().UTC().Truncate(time.Hour)}
	dao.metricsMu.Lock()
	if dao.metrics == nil {
		dao.metrics = map[metricKey]*metricCounts{}
	}
	c := dao.metrics[k]
	if c == nil {
		c = &metricCounts{}
		dao.metrics[k] = c
	}
	c.requests++
	c.in += bytesIn
	c.out += bytesOut
	dao.metricsMu.Unlock()
	return nil
}

// FlushMetrics writes what RecordDeviceActivity counted since the last
// flush. What surely failed to write is kept for the next one. A pass
// starts no write after cMetricsFlush, and each write gives up after
// cMetricsWriteTimeout: with the primary away the rest waits for the next
// pass rather than hanging - Stop waits for the last pass.
func (dao *Dao) FlushMetrics() { dao.flushMetrics(cMetricsFlush, cMetricsWriteTimeout) }

// cMetricsWriteTimeout is above InnoDB's lock wait (innodb_lock_wait_timeout,
// 50 s by default) and below the DSN's readTimeout. A write held behind a
// lock - PruneOldLogs' daily delete locks every row it scans - then ends as
// it always has: applied, or refused by MySQL and not applied. Only a
// primary that doesn't answer at all cuts a write short.
const cMetricsWriteTimeout = 55 * time.Second

// flushMetrics bounds the pass and each write apart: one under way is
// never cut short because the pass ran out of time. A write that is cut
// short, or loses its connection waiting for the answer, may have been
// applied all the same (the server goes on with a statement it already
// has: behind a metadata lock, a stalled disk), so its counts are dropped
// - a late or lost hour of metrics rather than one counted twice. Counts
// from a write that surely wasn't applied are kept.
func (dao *Dao) flushMetrics(budget, writeTimeout time.Duration) {
	dao.metricsMu.Lock()
	pending := dao.metrics
	dao.metrics = nil
	dao.metricsMu.Unlock()
	deadline := time.Now().Add(budget)
	kept := 0
	for k, c := range pending {
		if time.Now().After(deadline) {
			dao.keepMetric(k, c)
			kept++
			continue
		}
		if unsure, err := dao.writeMetric(k, c, writeTimeout); unsure {
			log.Error("device activity for", k.domain, "may not be recorded,", c.requests, "requests dropped rather than risk counting them twice:", err)
		} else if err != nil {
			log.Error("error recording device activity for", k.domain, ":", err)
			dao.keepMetric(k, c)
		}
	}
	if kept > 0 {
		log.Error("device activity flush out of time:", kept, "counts kept for the next one")
	}
}

// writeMetric adds one device and hour's counts to device_metrics. unsure
// is a failed write that may have been applied: no answer within timeout,
// or the connection lost waiting for one.
func (dao *Dao) writeMetric(k metricKey, c *metricCounts, timeout time.Duration) (unsure bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, err = dao.db.ExecContext(ctx,
		"insert into `device_metrics` (`domain`, `hour_bucket`, `requests`, `bytes_in`, `bytes_out`) values (?, ?, ?, ?, ?) "+
			"on duplicate key update `requests` = `requests` + values(`requests`), `bytes_in` = `bytes_in` + values(`bytes_in`), `bytes_out` = `bytes_out` + values(`bytes_out`)",
		k.domain, k.hour, c.requests, c.in, c.out)
	return err != nil && (ctx.Err() != nil || errors.Is(err, mysql.ErrInvalidConn)), err
}

// keepMetric puts counts that weren't written back, for the next flush.
func (dao *Dao) keepMetric(k metricKey, c *metricCounts) {
	dao.metricsMu.Lock()
	defer dao.metricsMu.Unlock()
	if dao.metrics == nil {
		dao.metrics = map[metricKey]*metricCounts{}
	}
	if cur := dao.metrics[k]; cur != nil {
		cur.requests += c.requests
		cur.in += c.in
		cur.out += c.out
	} else {
		dao.metrics[k] = c
	}
}

func (dao *Dao) startMetricsFlusher() {
	dao.stopMetrics = make(chan struct{})
	dao.metricsDone = make(chan struct{})
	go func() {
		defer close(dao.metricsDone)
		t := time.NewTicker(cMetricsFlush)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				dao.FlushMetrics()
			case <-dao.stopMetrics:
				dao.FlushMetrics()
				return
			}
		}
	}()
}
