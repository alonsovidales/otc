// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alonsovidales/otc/log"
)

// Issue #206: Images' month counts (the date scrubber, ReqPhotoDateBuckets)
// without a scan of the library. SearchMediaDateBuckets groups every row by
// its content to find each photo's oldest copy at every Images opening:
// 225 ms on 200k photos in the Lima VM, a second or more on a Pi. With no
// filter they now come from counts kept as the library changes:
//
//   - photo_months is, per month, how many photos have their oldest copy
//     in it (images: among their image rows; media: images and videos).
//   - photo_month_hashes is each content's two months, what it adds to
//     photo_months: what a change takes away from there.
//   - settings.photo_months_for is the folders kept out of Images the
//     counts leave out (outermost, sorted, as JSON); NULL: not built. A
//     search with other folders kept out, or none built, is counted the
//     old way, and the counts are built again in the background.
//
// Every write to `files` here (StoreNewFile, DelFileByPathHash, OverrideFile,
// ReplaceFileKeepingVersion, MoveToDateTaken) marks the contents it touched
// (markMonths, in memory: no statement more); before the counts are read,
// and every minute, each marked content's months are worked out again from
// its rows and photo_months moved by the difference from what
// photo_month_hashes says (ReconcileMonths). So the counts are right
// whatever the writes did, in any order, and however many there were -
// a content worked out twice changes nothing the second time. The marks
// live only in this process: settings.photo_months_run is the process
// that keeps the counts (LeaseMonths, at start); one that finds another's
// there - a run that died with marks not worked out - throws the counts
// away (built again), and a clean stop (Stop) works its marks out and
// gives the lease back. Only this process writes `files` (a release's
// script that changes rows must set photo_months_for to NULL).
// Filtered counts (people, a collection, an area, tags) read the filter's
// photos from its smallest table instead (DatedMedia's way), not the
// library.

// monthsState is the counts' work in this process.
type monthsState struct {
	// mu: one rebuild or reconcile at a time.
	mu sync.Mutex
	// marksMu guards dirty: the contents written since they were last
	// worked out.
	marksMu sync.Mutex
	dirty   map[string]struct{}
	// run is this process's lease (LeaseMonths); "" without one: the
	// counts are never read nor kept.
	run string
	// rebuilding: a rebuild is under way in the background (reads count
	// the old way meanwhile, never waiting for it).
	rebuilding atomic.Bool
}

// cMonthsChunk is how many marked contents a reconcile works out at once,
// and cMonthsInline how many a read works out before answering (more and
// it answers the old way, the rest worked out in the background).
const (
	cMonthsChunk  = 500
	cMonthsInline = 2000
)

// markMonths notes contents whose rows just changed.
func (dao *Dao) markMonths(hashes ...string) {
	m := &dao.months
	m.marksMu.Lock()
	defer m.marksMu.Unlock()
	if m.dirty == nil {
		m.dirty = map[string]struct{}{}
	}
	for _, h := range hashes {
		if h != "" {
			m.dirty[h] = struct{}{}
		}
	}
}

// takeMarks is the marked contents, no longer marked.
func (dao *Dao) takeMarks() []string {
	m := &dao.months
	m.marksMu.Lock()
	defer m.marksMu.Unlock()
	out := make([]string, 0, len(m.dirty))
	for h := range m.dirty {
		out = append(out, h)
	}
	m.dirty = nil
	return out
}

func (dao *Dao) marked() int {
	m := &dao.months
	m.marksMu.Lock()
	defer m.marksMu.Unlock()
	return len(m.dirty)
}

// LeaseMonths makes this process the one that keeps the month counts, at
// its start, before it writes anything: counts another process kept are
// thrown away unless it gave them back (a clean stop). Without the
// columns (release 168's script hasn't run) this process never keeps
// them, and reads count the old way.
func (dao *Dao) LeaseMonths() error {
	run := make([]byte, 16)
	if _, err := rand.Read(run); err != nil {
		return err
	}
	id := hex.EncodeToString(run)
	_, err := dao.db.Exec("update `settings` set `photo_months_for` = if(`photo_months_run` is null, `photo_months_for`, null), `photo_months_run` = ?", id)
	if err != nil {
		return schemaBehind(err)
	}
	dao.months.mu.Lock()
	dao.months.run = id
	dao.months.mu.Unlock()
	return nil
}

// releaseMonths works the marks out and gives the lease back: Stop.
func (dao *Dao) releaseMonths() {
	m := &dao.months
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.run == "" {
		return
	}
	if err := dao.reconcileLocked(dao.takeMarks()); err != nil {
		log.Error("month counts: the last changes couldn't be counted, built again at the next start:", err)
		return
	}
	if _, err := dao.db.Exec("update `settings` set `photo_months_run` = null where `photo_months_run` = ?", m.run); err != nil {
		log.Error("month counts: the lease couldn't be given back:", err)
	}
	m.run = ""
}

// monthsFor is excluded as photo_months_for keeps it.
func monthsFor(excluded []string) string {
	folders := outermostFolders(excluded)
	if folders == nil {
		folders = []string{}
	}
	b, _ := json.Marshal(folders)
	return string(b)
}

// monthOf is t's month as photo_months keeps it: year * 12 + month - 1.
func monthOf(t time.Time) int { return t.Year()*12 + int(t.Month()) - 1 }

// PhotoMonths is SearchMediaDateBucketsWithin's answer for b's search (its
// filters, ImagesOnly, Excluded): with no filter from the kept counts when
// they are built for b.Excluded (worked out first), else the old way while
// they are built in the background; with filters, from the filter's photos.
func (dao *Dao) PhotoMonths(b *MediaBrowse) ([]DateBucket, error) {
	if b.Filtered() {
		return dao.filteredMonths(b)
	}
	if buckets, ok := dao.keptMonths(b.ImagesOnly, b.Excluded); ok {
		return buckets, nil
	}
	return dao.SearchMediaDateBucketsWithin(nil, nil, nil, "", b.ImagesOnly, b.Excluded)
}

// keptMonths is the counts kept, ok false when they can't answer now - a
// read that fails too (logged): the library is counted instead.
func (dao *Dao) keptMonths(imagesOnly bool, excluded []string) ([]DateBucket, bool) {
	m := &dao.months
	if m.rebuilding.Load() {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.run == "" {
		return nil, false
	}
	var builtFor sql.NullString
	if err := dao.db.QueryRow("select `photo_months_for` from `settings`").Scan(&builtFor); err != nil {
		if schemaBehindOrNil(err) != nil {
			log.Error("month counts: couldn't be read, the library is counted instead:", err)
		}
		return nil, false
	}
	if !builtFor.Valid || builtFor.String != monthsFor(excluded) {
		dao.rebuildMonthsLater()
		return nil, false
	}
	if dao.marked() > cMonthsInline {
		dao.reconcileLater()
		return nil, false
	}
	if err := dao.reconcileLocked(dao.takeMarks()); err != nil {
		log.Error("month counts: changes couldn't be counted, the library is counted instead:", err)
		return nil, false
	}
	col := "`media`"
	if imagesOnly {
		col = "`images`"
	}
	rows, err := dao.db.Query("select `month`, " + col + " from `photo_months` where " + col + " > 0 order by `month` desc")
	if err != nil {
		log.Error("month counts: couldn't be read, the library is counted instead:", err)
		return nil, false
	}
	defer rows.Close()
	var buckets []DateBucket
	for rows.Next() {
		var month, n int
		if err := rows.Scan(&month, &n); err != nil {
			log.Error("month counts: couldn't be read, the library is counted instead:", err)
			return nil, false
		}
		buckets = append(buckets, DateBucket{Month: fmt.Sprintf("%04d-%02d", month/12, month%12+1), Count: n})
	}
	if err := rows.Err(); err != nil {
		log.Error("month counts: couldn't be read, the library is counted instead:", err)
		return nil, false
	}
	return buckets, true
}

// schemaBehindOrNil is nil for a database without the counts' columns:
// they are counted the old way there.
func schemaBehindOrNil(err error) error {
	if isUnknownColumn(err) || isNoSuchTable(err) {
		return nil
	}
	return err
}

// ReconcileMonths works the marked contents out now (the minute's pass):
// nothing without the lease or counts built.
func (dao *Dao) ReconcileMonths() error {
	m := &dao.months
	if m.rebuilding.Load() {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.run == "" {
		return nil
	}
	return dao.reconcileLocked(dao.takeMarks())
}

func (dao *Dao) reconcileLater() {
	go func() {
		if err := dao.ReconcileMonths(); err != nil {
			log.Error("month counts: changes couldn't be counted:", err)
		}
	}()
}

// reconcileLocked works hashes out: each one's months now against what
// photo_month_hashes holds, photo_months moved by the difference, both in
// one transaction a chunk. A chunk that fails is marked again. m.mu held.
func (dao *Dao) reconcileLocked(hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}
	var builtFor sql.NullString
	if err := dao.db.QueryRow("select `photo_months_for` from `settings`").Scan(&builtFor); err != nil {
		dao.markMonths(hashes...)
		return err
	}
	if !builtFor.Valid {
		return nil // built again from the rows: nothing to work out
	}
	var excluded []string
	if err := json.Unmarshal([]byte(builtFor.String), &excluded); err != nil {
		return err
	}
	slices.Sort(hashes)
	for start := 0; start < len(hashes); start += cMonthsChunk {
		chunk := hashes[start:min(start+cMonthsChunk, len(hashes))]
		if err := dao.reconcileChunk(chunk, excluded); err != nil {
			dao.markMonths(hashes[start:]...)
			return err
		}
	}
	return nil
}

// hashMonths is a content's two months (photo_month_hashes), -1 for none.
type hashMonths struct{ images, media int }

func (dao *Dao) reconcileChunk(hashes, excluded []string) error {
	now, err := dao.monthsOfHashes(hashes, excluded)
	if err != nil {
		return err
	}
	tx, err := dao.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ph, args := inPlaceholders(hashes)
	rows, err := tx.Query("select `hash`, coalesce(`images`, -1), coalesce(`media`, -1) from `photo_month_hashes` where `hash` in ("+ph+") for update", args...)
	if err != nil {
		return err
	}
	was := map[string]hashMonths{}
	for rows.Next() {
		var h string
		var hm hashMonths
		if err := rows.Scan(&h, &hm.images, &hm.media); err != nil {
			rows.Close()
			return err
		}
		was[h] = hm
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	delta := map[int][2]int{}
	move := func(month, kind, by int) {
		if month >= 0 {
			d := delta[month]
			d[kind] += by
			delta[month] = d
		}
	}
	var gone []string
	var upsert [][3]any
	none := hashMonths{-1, -1}
	for _, h := range hashes {
		before, ok := was[h]
		if !ok {
			before = none
		}
		after, ok := now[h]
		if !ok {
			after = none
		}
		if before == after {
			continue
		}
		move(before.images, 0, -1)
		move(after.images, 0, 1)
		move(before.media, 1, -1)
		move(after.media, 1, 1)
		if after == none {
			gone = append(gone, h)
		} else {
			upsert = append(upsert, [3]any{h, nullMonth(after.images), nullMonth(after.media)})
		}
	}
	if len(gone) > 0 {
		ph, args := inPlaceholders(gone)
		if _, err := tx.Exec("delete from `photo_month_hashes` where `hash` in ("+ph+")", args...); err != nil {
			return err
		}
	}
	if len(upsert) > 0 {
		var vals []string
		var args []any
		for _, u := range upsert {
			vals = append(vals, "(?, ?, ?)")
			args = append(args, u[0], u[1], u[2])
		}
		if _, err := tx.Exec("insert into `photo_month_hashes` (`hash`, `images`, `media`) values "+strings.Join(vals, ", ")+
			" on duplicate key update `images` = values(`images`), `media` = values(`media`)", args...); err != nil {
			return err
		}
	}
	if err := addMonths(tx, delta); err != nil {
		return err
	}
	return tx.Commit()
}

func nullMonth(m int) any {
	if m < 0 {
		return nil
	}
	return m
}

// addMonths adds delta (per month: images, media) to photo_months.
func addMonths(tx *sql.Tx, delta map[int][2]int) error {
	months := make([]int, 0, len(delta))
	for m, d := range delta {
		if d != [2]int{} {
			months = append(months, m)
		}
	}
	if len(months) == 0 {
		return nil
	}
	slices.Sort(months)
	var vals []string
	var args []any
	for _, m := range months {
		vals = append(vals, "(?, ?, ?)")
		args = append(args, m, delta[m][0], delta[m][1])
	}
	_, err := tx.Exec("insert into `photo_months` (`month`, `images`, `media`) values "+strings.Join(vals, ", ")+
		" on duplicate key update `images` = `images` + values(`images`), `media` = `media` + values(`media`)", args...)
	return err
}

// monthsOfHashes is each of hashes' two months now, from its rows outside
// excluded: its oldest image row's, its oldest image or video row's.
func (dao *Dao) monthsOfHashes(hashes, excluded []string) (map[string]hashMonths, error) {
	ph, args := inPlaceholders(hashes)
	where := "`hash` in (" + ph + ")"
	if cond, condArgs := outsideFolders("`path`", excluded); cond != "" {
		where += " and " + cond
		args = append(args, condArgs...)
	}
	return scanMonths(dao.db, "select `hash`, "+monthsCols+" from `files` where "+where+" group by `hash`", args...)
}

// monthsCols is a content's two oldest dates, as monthsOfHashes and the
// rebuild read them.
const monthsCols = "min(case when `mime` like 'image%' then `created` end), min(case when `mime` like 'image%' or `mime` like 'video%' then `created` end)"

// querier is a *sql.DB or a *sql.Tx.
type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

func scanMonths(q querier, query string, args ...any) (map[string]hashMonths, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]hashMonths{}
	var hash sql.RawBytes
	var images, media sql.NullTime
	for rows.Next() {
		if err := rows.Scan(&hash, &images, &media); err != nil {
			return nil, err
		}
		hm := hashMonths{-1, -1}
		if images.Valid {
			hm.images = monthOf(images.Time)
		}
		if media.Valid {
			hm.media = monthOf(media.Time)
		}
		if hm != (hashMonths{-1, -1}) {
			out[string(hash)] = hm
		}
	}
	return out, rows.Err()
}

// rebuildMonthsLater builds the counts again in the background, once at a
// time.
func (dao *Dao) rebuildMonthsLater() {
	m := &dao.months
	if !m.rebuilding.CompareAndSwap(false, true) {
		return
	}
	go func() {
		started := time.Now()
		err := dao.RebuildMonths()
		m.rebuilding.Store(false)
		if err != nil {
			log.Error("month counts: couldn't be built, the library is counted instead:", err)
			return
		}
		log.Info("month counts: built in", time.Since(started).Round(time.Millisecond))
	}()
}

// RebuildMonths builds the counts from the library's rows, for the folders
// kept out of Images now. The marks taken first are in the rows it reads;
// those made meanwhile are worked out after it.
func (dao *Dao) RebuildMonths() error {
	m := &dao.months
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.run == "" {
		return nil
	}
	dao.takeMarks()
	excluded, err := dao.GetOutOfImagesFolders()
	if err != nil {
		return err
	}
	tx, err := dao.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// The lease is checked and the counts marked unbuilt first, so a
	// failure half way leaves nothing taken for counts.
	var run sql.NullString
	if err := tx.QueryRow("select `photo_months_run` from `settings` for update").Scan(&run); err != nil {
		return err
	}
	if run.String != m.run {
		return fmt.Errorf("the month counts' lease is another process's") // i18n-ignore: logged
	}
	if _, err := tx.Exec("update `settings` set `photo_months_for` = null"); err != nil {
		return err
	}
	for _, q := range []string{"delete from `photo_months`", "delete from `photo_month_hashes`"} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	where, args := "", []any(nil)
	if cond, condArgs := outsideFolders("`path`", excluded); cond != "" {
		where, args = " where "+cond, condArgs
	}
	all, err := scanMonths(tx, "select `hash`, "+monthsCols+" from `files`"+where+" group by `hash`", args...)
	if err != nil {
		return err
	}
	delta := map[int][2]int{}
	var vals []string
	var vargs []any
	flush := func() error {
		if len(vals) == 0 {
			return nil
		}
		_, err := tx.Exec("insert into `photo_month_hashes` (`hash`, `images`, `media`) values "+strings.Join(vals, ", "), vargs...)
		vals, vargs = vals[:0], vargs[:0]
		return err
	}
	for h, hm := range all {
		if hm.images >= 0 {
			d := delta[hm.images]
			d[0]++
			delta[hm.images] = d
		}
		if hm.media >= 0 {
			d := delta[hm.media]
			d[1]++
			delta[hm.media] = d
		}
		vals = append(vals, "(?, ?, ?)")
		vargs = append(vargs, h, nullMonth(hm.images), nullMonth(hm.media))
		if len(vals) == 1000 {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	if err := addMonths(tx, delta); err != nil {
		return err
	}
	if _, err := tx.Exec("update `settings` set `photo_months_for` = ? where `photo_months_run` = ?", monthsFor(excluded), m.run); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return dao.reconcileLocked(dao.takeMarks())
}

// filteredMonths is a filtered search's month counts (ImagesOnly and the
// rating aside, as SearchMediaDateBucketsWithin counts them): each photo
// once, in its oldest copy's month, read from the filter that keeps the
// fewest (DatedMedia's rows).
func (dao *Dao) filteredMonths(b *MediaBrowse) ([]DateBucket, error) {
	c := *b
	c.Path, c.Rating, c.Before, c.NewerThan = "", nil, nil, nil
	if !c.Filtered() {
		return dao.PhotoMonths(&c)
	}
	_, driver, err := dao.estimateMedia(&c.MediaFilters)
	if err != nil {
		return nil, err
	}
	list, err := dao.datedRows(&c, driver)
	if err != nil {
		return nil, err
	}
	counts := map[int]int{}
	for _, h := range list {
		counts[monthOf(time.Unix(int64(h.Rank[0]), 0).UTC())]++
	}
	months := make([]int, 0, len(counts))
	for m := range counts {
		months = append(months, m)
	}
	slices.Sort(months)
	slices.Reverse(months)
	buckets := make([]DateBucket, len(months))
	for i, m := range months {
		buckets[i] = DateBucket{Month: fmt.Sprintf("%04d-%02d", m/12, m%12+1), Count: counts[m]}
	}
	return buckets, nil
}
