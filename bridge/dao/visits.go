// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/alonsovidales/otc/log"
)

// Anonymous visit counts for the public site (/privacy, "Visit counts"):
// per UTC day, page and source, how many times a page was opened - nothing
// about who opened it. The api package decides what a visit is and what its
// page and source are (api/visits.go); here they are added up in memory and
// written in batches, as the device metrics are (metrics.go): each bridge
// node adds its own counts to the shared rows every cMetricsFlush.

type visitKey struct {
	day, page, source string
}

// The source comes from the request (a Referer's host, a link's ref=), so a
// script making up a new one on every request could otherwise add rows to
// site_visits without limit - and to the Growth tab's "group by source".
// Two bounds keep it in check, and past either the visit is still counted,
// under its page's cVisitOther, so the day's and the page's totals stay
// right and only the source's name is lost:
//
//   - cMaxVisitSourcesPerDay: the sources a node counts by name in a UTC
//     day. A node then adds at most cMaxVisitSourcesPerDay+2 rows per page
//     a day (with direct visits and cVisitOther); the site's real sources
//     are a few dozen.
//   - cMaxVisitKeys: the keys waiting for a flush, those a failed write
//     kept included - with the primary away for days they would otherwise
//     pile up a day's worth at a time.
const (
	cMaxVisitSourcesPerDay = 1000
	cMaxVisitKeys          = 5000
)

// cVisitOther is the source of the visits counted past those bounds. It
// can't be a real source's: the api package's are "ref:" and letters,
// digits, ".", "_" and "-" only.
const cVisitOther = "(other)"

// cVisitsPerWrite is how many keys one statement writes.
const cVisitsPerWrite = 200

// RecordSiteVisit counts one visit to page from source, today (UTC).
func (dao *Dao) RecordSiteVisit(page, source string) {
	dao.recordSiteVisit(time.Now().UTC().Format(time.DateOnly), page, source)
}

func (dao *Dao) recordSiteVisit(day, page, source string) {
	dao.visitsMu.Lock()
	defer dao.visitsMu.Unlock()
	// A direct visit ("") is never folded. The list starts again when the
	// day moves on; a visit from just before midnight that takes the lock
	// just after counts against the new day's list rather than reset it.
	if source != "" {
		if day > dao.visitSourcesDay {
			dao.visitSourcesDay, dao.visitSources = day, map[string]struct{}{}
		}
		if _, seen := dao.visitSources[source]; !seen {
			if len(dao.visitSources) >= cMaxVisitSourcesPerDay {
				source = cVisitOther
				dao.visitsFolded++
			} else {
				dao.visitSources[source] = struct{}{}
			}
		}
	}
	dao.addVisits(visitKey{day, page, source}, 1)
}

// addVisits adds n visits under k, visitsMu held. With cMaxVisitKeys
// waiting, a key not among them is counted under its page's cVisitOther.
// Direct visits and cVisitOther are one key per day and page, so they are
// let through: the map grows past the bound by at most two keys per day
// and page.
func (dao *Dao) addVisits(k visitKey, n int64) {
	if dao.visits == nil {
		dao.visits = map[visitKey]int64{}
	}
	if k.source != "" && k.source != cVisitOther {
		if _, ok := dao.visits[k]; !ok && len(dao.visits) >= cMaxVisitKeys {
			k.source = cVisitOther
			dao.visitsFolded += n
		}
	}
	dao.visits[k] += n
}

// FlushVisits writes what RecordSiteVisit counted since the last flush,
// on FlushMetrics' terms: what surely failed to write is kept for the
// next flush, a write that may have been applied is dropped rather than
// counted twice, a pass starts no write after cMetricsFlush and each write
// gives up after cMetricsWriteTimeout. The flusher runs it in the same
// pass and budget as the metrics (flushPass); Stop waits for the last.
func (dao *Dao) FlushVisits() { dao.flushVisits(cMetricsFlush, cMetricsWriteTimeout) }

func (dao *Dao) flushVisits(budget, writeTimeout time.Duration) {
	dao.visitsMu.Lock()
	pending, folded := dao.visits, dao.visitsFolded
	dao.visits, dao.visitsFolded = nil, 0
	dao.visitsMu.Unlock()
	if folded > 0 {
		log.Error("site visits:", folded, "visits counted under", cVisitOther, "- more than", cMaxVisitSourcesPerDay, "sources today or", cMaxVisitKeys, "keys waiting")
	}
	if len(pending) == 0 {
		return
	}

	// The same order on every node: two nodes' batches take the rows'
	// locks in the same order, so they wait for each other rather than
	// deadlock.
	keys := slices.SortedFunc(maps.Keys(pending), func(a, b visitKey) int {
		return cmp.Or(strings.Compare(a.day, b.day), strings.Compare(a.page, b.page), strings.Compare(a.source, b.source))
	})
	deadline := time.Now().Add(budget)
	var late, kept, lost int
	var keptErr, lostErr error
	for len(keys) > 0 {
		batch := keys[:min(len(keys), cVisitsPerWrite)]
		keys = keys[len(batch):]
		if time.Now().After(deadline) {
			dao.keepVisits(pending, batch)
			late += len(batch)
			continue
		}
		unsure, err := dao.writeVisits(pending, batch, writeTimeout)
		switch {
		case unsure:
			lost += len(batch)
			lostErr = err
		case err != nil:
			dao.keepVisits(pending, batch)
			kept += len(batch)
			keptErr = err
		}
	}
	// One line a pass, not one a key: while the primary is away every
	// pass fails the same way.
	if lost > 0 {
		log.Error("site visits may not be recorded,", lost, "counts dropped rather than risk counting them twice:", lostErr)
	}
	if kept > 0 {
		log.Error("error recording site visits,", kept, "counts kept for the next flush:", keptErr)
	}
	if late > 0 {
		log.Error("site visits flush out of time:", late, "counts kept for the next one")
	}
}

// writeVisits adds keys' counts to site_visits in one statement. unsure
// is a failed write that may have been applied, as writeMetric's.
func (dao *Dao) writeVisits(counts map[visitKey]int64, keys []visitKey, timeout time.Duration) (unsure bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	args := make([]any, 0, 4*len(keys))
	for _, k := range keys {
		args = append(args, k.day, k.page, k.source, counts[k])
	}
	_, err = dao.db.ExecContext(ctx,
		"insert into `site_visits` (`day`, `page`, `source`, `visits`) values "+
			strings.TrimSuffix(strings.Repeat("(?, ?, ?, ?), ", len(keys)), ", ")+
			" on duplicate key update `visits` = `visits` + values(`visits`)",
		args...)
	return err != nil && (ctx.Err() != nil || errors.Is(err, mysql.ErrInvalidConn)), err
}

// keepVisits puts keys' counts that weren't written back, for the next
// flush, within cMaxVisitKeys as new visits are (addVisits).
func (dao *Dao) keepVisits(counts map[visitKey]int64, keys []visitKey) {
	dao.visitsMu.Lock()
	defer dao.visitsMu.Unlock()
	for _, k := range keys {
		dao.addVisits(k, counts[k])
	}
}
