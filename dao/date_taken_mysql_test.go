// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"errors"
	"testing"
	"time"
)

// Dates taken, against a real MySQL/MariaDB (OTC_TEST_MYSQL_DSN, as
// search_files_mysql_test.go): temporary tables shaped as db.sql's.

// createdOf is every files row's date by path.
func createdOf(tb testing.TB, d *Dao) map[string]time.Time {
	tb.Helper()
	rows, err := d.db.Query("select `path`, `created` from `files`")
	if err != nil {
		tb.Fatal(err)
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var p string
		var c time.Time
		if err := rows.Scan(&p, &c); err != nil {
			tb.Fatal(err)
		}
		out[p] = c
	}
	return out
}

// Only the content's rows dated more than the margin after the date taken
// move, to that date at the second; a row a day later, an earlier row, the
// other content and the kept versions keep theirs; a second run moves
// nothing.
func TestMoveToDateTakenMySQL(t *testing.T) {
	db, d := outOfImagesTestDB(t)
	taken := time.Date(2014, 7, 19, 21, 5, 33, 400_000_000, time.UTC)
	margin := 26 * time.Hour
	copied := time.Date(2018, 3, 2, 10, 0, 0, 0, time.UTC)
	insertMediaRows(t, db, "files",
		mediaRow{"A", "image/jpeg", "/Drive/2014/a.jpg", copied},
		mediaRow{"A", "image/jpeg", "/Backup/a.jpg", copied.AddDate(1, 0, 0)},
		mediaRow{"A", "image/jpeg", "/Phone/a.jpg", taken.Add(margin).Truncate(time.Second)},                    // at the margin: stays
		mediaRow{"A", "image/jpeg", "/Phone/a-edge.jpg", taken.Add(margin + time.Second).Truncate(time.Second)}, // a second past it: moves
		mediaRow{"A", "image/jpeg", "/Old/a.jpg", time.Date(2012, 1, 1, 9, 0, 0, 0, time.UTC)},                  // older: never later
		mediaRow{"B", "image/jpeg", "/Drive/2014/b.jpg", copied},
	)
	insertMediaRows(t, db, "file_versions", mediaRow{"A", "image/jpeg", "/Kept/a.jpg", copied})

	moved, err := d.MoveToDateTaken("A", taken, taken.Add(margin))
	if err != nil {
		t.Fatal(err)
	}
	if moved != 3 {
		t.Errorf("moved %d rows, want 3", moved)
	}
	at := taken.Truncate(time.Second)
	got := createdOf(t, d)
	for path, want := range map[string]time.Time{
		"/Drive/2014/a.jpg": at,
		"/Backup/a.jpg":     at,
		"/Phone/a-edge.jpg": at,
		"/Phone/a.jpg":      taken.Add(margin).Truncate(time.Second),
		"/Old/a.jpg":        time.Date(2012, 1, 1, 9, 0, 0, 0, time.UTC),
		"/Drive/2014/b.jpg": copied,
	} {
		if !got[path].Equal(want) {
			t.Errorf("%s: %v, want %v", path, got[path], want)
		}
	}
	var kept time.Time
	if err := db.QueryRow("select `created` from `file_versions`").Scan(&kept); err != nil || !kept.Equal(copied) {
		t.Errorf("the kept version: %v, %v", kept, err)
	}
	if moved, err := d.MoveToDateTaken("A", taken, taken.Add(margin)); err != nil || moved != 0 {
		t.Errorf("again: %d, %v", moved, err)
	}
}

// The pass's place: a database before the columns is "not run yet"; with
// them it starts at nothing, keeps its cursor and says when it is done.
func TestDatesTakenBackfillMySQL(t *testing.T) {
	db, d := outOfImagesTestDB(t)
	if _, err := db.Exec("create temporary table `settings` (`places_enabled` tinyint(1) not null default 0) engine=InnoDB"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("insert into `settings` () values ()"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.DatesTakenBackfill(); !errors.Is(err, ErrSchemaBehind) {
		t.Errorf("before the columns: %v", err)
	}
	if err := d.SetDatesTakenCursor("ab"); !errors.Is(err, ErrSchemaBehind) {
		t.Errorf("cursor before the columns: %v", err)
	}
	if err := d.SetDatesTakenBackfilled(); !errors.Is(err, ErrSchemaBehind) {
		t.Errorf("done before the columns: %v", err)
	}
	// As db.sql and release 152's script add them.
	if _, err := db.Exec("alter table `settings` add column if not exists `dates_taken_cursor` varchar(64) not null default '', " +
		"add column if not exists `dates_taken_backfilled` tinyint(1) not null default 0"); err != nil {
		t.Fatal(err)
	}
	if after, done, err := d.DatesTakenBackfill(); err != nil || after != "" || done {
		t.Errorf("new: %q %v %v", after, done, err)
	}
	if err := d.SetDatesTakenCursor("9f86d081"); err != nil {
		t.Fatal(err)
	}
	if after, done, err := d.DatesTakenBackfill(); err != nil || after != "9f86d081" || done {
		t.Errorf("part way: %q %v %v", after, done, err)
	}
	if err := d.SetDatesTakenBackfilled(); err != nil {
		t.Fatal(err)
	}
	if after, done, err := d.DatesTakenBackfill(); err != nil || after != "9f86d081" || !done {
		t.Errorf("done: %q %v %v", after, done, err)
	}
}
