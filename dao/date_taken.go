// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"time"
)

// Dates taken: a photo's or video's `files.created` is moved back to the
// date its own metadata says it was taken, when that is clearly earlier
// than the row's date (files_manager/date_taken.go has the rule). Written
// when the device reads the metadata (processing, Reprocess) and once in
// the background for what it held before (settings.dates_taken_cursor and
// dates_taken_backfilled).

// MoveToDateTaken sets `created` to taken on every files row of the content
// hash dated after `after` (the rule's bound: taken plus its margin), and
// says how many rows moved. The condition is checked here, in the same
// statement, so a row stored or changed since the metadata was read is
// judged by its own date, and a date is never moved later. Kept versions
// (file_versions) keep theirs.
func (dao *Dao) MoveToDateTaken(hash string, taken, after time.Time) (moved int64, err error) {
	res, err := dao.db.Exec("update `files` set `created` = ? where `hash` = ? and `created` > ?",
		taken.UTC().Truncate(time.Second), hash, after.UTC().Truncate(time.Second))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DatesTakenBackfill is where the one-off pass over what the device held
// before stands: done, or the last content hash it read (resumed after
// it). ErrSchemaBehind before the release that added the columns, which
// callers read as "not run yet" and skip.
func (dao *Dao) DatesTakenBackfill() (after string, done bool, err error) {
	err = dao.db.QueryRow("select `dates_taken_cursor`, `dates_taken_backfilled` from `settings`").Scan(&after, &done)
	return after, done, schemaBehind(err)
}

// SetDatesTakenCursor records the last content hash the pass read.
func (dao *Dao) SetDatesTakenCursor(after string) error {
	_, err := dao.db.Exec("update `settings` set `dates_taken_cursor` = ?", after)
	return schemaBehind(err)
}

// SetDatesTakenBackfilled records that the pass has read everything.
func (dao *Dao) SetDatesTakenBackfilled() error {
	_, err := dao.db.Exec("update `settings` set `dates_taken_backfilled` = 1")
	return schemaBehind(err)
}
