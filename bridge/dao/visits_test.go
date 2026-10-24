// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

const cVisitsInsert = "insert into `site_visits` \\(`day`, `page`, `source`, `visits`\\) values .* on duplicate key update `visits` = `visits` \\+ values\\(`visits`\\)"

// Visits are added up in memory and written once per key in a flush, in
// one statement, in the same order on every node; a write that fails is
// kept and added to what comes next.
func TestSiteVisitsAreBatched(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	for i := 0; i < 3; i++ {
		d.recordSiteVisit("2026-10-24", "/", "reddit.com")
	}
	d.recordSiteVisit("2026-10-24", "/es/", "")

	mock.ExpectExec(cVisitsInsert).
		WithArgs("2026-10-24", "/", "reddit.com", 3, "2026-10-24", "/es/", "", 1).
		WillReturnError(errors.New("primary away"))
	d.FlushVisits()

	d.recordSiteVisit("2026-10-24", "/", "reddit.com")
	mock.ExpectExec(cVisitsInsert).
		WithArgs("2026-10-24", "/", "reddit.com", 4, "2026-10-24", "/es/", "", 1).
		WillReturnResult(sqlmock.NewResult(0, 2))
	d.FlushVisits()

	d.FlushVisits() // nothing left: no write
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A write that may have been applied (the connection lost waiting for the
// answer) is dropped, not retried: its visits would be counted twice. One
// MySQL refused (a deadlock with the other node) was not applied, and is.
func TestSiteVisitsWriteMaybeAppliedIsDropped(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)

	d.recordSiteVisit("2026-10-24", "/", "")
	mock.ExpectExec(cVisitsInsert).WillReturnError(mysql.ErrInvalidConn)
	d.FlushVisits()
	d.FlushVisits() // nothing kept: no write

	d.recordSiteVisit("2026-10-24", "/", "")
	mock.ExpectExec(cVisitsInsert).WillReturnError(&mysql.MySQLError{Number: 1213, Message: "Deadlock found when trying to get lock; try restarting transaction"})
	d.FlushVisits()
	mock.ExpectExec(cVisitsInsert).WithArgs("2026-10-24", "/", "", 1).WillReturnResult(sqlmock.NewResult(0, 1))
	d.FlushVisits()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Many keys go in statements of cVisitsPerWrite; a pass out of time
// starts no more of them and keeps what it didn't write.
func TestSiteVisitsFlushIsBounded(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	for i := 0; i < cVisitsPerWrite+1; i++ {
		d.recordSiteVisit("2026-10-24", "/", fmt.Sprintf("site%03d.example", i))
	}

	// Slower than the pass, well within a write's own limit: the second
	// statement is never started.
	mock.ExpectExec(cVisitsInsert).WillDelayFor(200 * time.Millisecond).WillReturnResult(sqlmock.NewResult(0, cVisitsPerWrite))
	d.flushVisits(50*time.Millisecond, 5*time.Second)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	d.visitsMu.Lock()
	kept := len(d.visits)
	d.visitsMu.Unlock()
	if kept != 1 {
		t.Fatalf("%d counts kept, want only the one never written", kept)
	}

	// The next pass writes it.
	mock.ExpectExec(cVisitsInsert).WithArgs("2026-10-24", "/", fmt.Sprintf("site%03d.example", cVisitsPerWrite), 1).WillReturnResult(sqlmock.NewResult(0, 1))
	d.FlushVisits()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Made-up sources can't add rows without limit: past
// cMaxVisitSourcesPerDay a node counts a new source's visits under
// cVisitOther, while the sources already seen that day, and direct
// visits, keep their own; the next day starts a new list.
func TestSiteVisitSourcesPerDayAreBounded(t *testing.T) {
	d := NewWithDB(nil)
	for i := 0; i < cMaxVisitSourcesPerDay; i++ {
		d.recordSiteVisit("2026-10-24", "/", fmt.Sprintf("s%d.example", i))
	}
	d.recordSiteVisit("2026-10-24", "/", "one-too-many.example")
	d.recordSiteVisit("2026-10-24", "/es/", "another.example")
	d.recordSiteVisit("2026-10-24", "/es/", "s0.example")
	d.recordSiteVisit("2026-10-24", "/", "")
	want := map[visitKey]int64{
		{"2026-10-24", "/", cVisitOther}:     1,
		{"2026-10-24", "/es/", cVisitOther}:  1,
		{"2026-10-24", "/es/", "s0.example"}: 1,
		{"2026-10-24", "/", ""}:              1,
	}
	for k, n := range want {
		if d.visits[k] != n {
			t.Errorf("%+v: %d, want %d", k, d.visits[k], n)
		}
	}
	if len(d.visits) != cMaxVisitSourcesPerDay+4 || d.visitsFolded != 2 {
		t.Fatalf("%d keys, %d folded", len(d.visits), d.visitsFolded)
	}

	// A visit counted just before midnight doesn't reset the new day's list.
	d.recordSiteVisit("2026-10-25", "/", "new-day.example")
	d.recordSiteVisit("2026-10-24", "/", "late.example")
	if d.visits[visitKey{"2026-10-25", "/", "new-day.example"}] != 1 || d.visitSourcesDay != "2026-10-25" || len(d.visitSources) != 2 {
		t.Fatalf("the next day: %d sources on %s", len(d.visitSources), d.visitSourcesDay)
	}
}

// The waiting counts are bounded too: past cMaxVisitKeys a new key is
// counted under its page's cVisitOther, the ones already there under their
// own; direct visits are never folded.
func TestSiteVisitsKeysAreBounded(t *testing.T) {
	d := NewWithDB(nil)
	// Pages, not sources, so cMaxVisitSourcesPerDay doesn't come first.
	for i := 0; i < cMaxVisitKeys; i++ {
		d.recordSiteVisit("2026-10-24", fmt.Sprintf("/p%d", i), "reddit.com")
	}
	d.recordSiteVisit("2026-10-24", "/", "reddit.com")
	d.recordSiteVisit("2026-10-24", "/", "lobste.rs")
	d.recordSiteVisit("2026-10-24", "/", "")
	d.recordSiteVisit("2026-10-24", "/p0", "reddit.com")
	if len(d.visits) != cMaxVisitKeys+2 || d.visitsFolded != 2 {
		t.Fatalf("%d keys, %d folded", len(d.visits), d.visitsFolded)
	}
	for k, n := range map[visitKey]int64{
		{"2026-10-24", "/", cVisitOther}:    2,
		{"2026-10-24", "/", ""}:             1,
		{"2026-10-24", "/p0", "reddit.com"}: 2,
	} {
		if d.visits[k] != n {
			t.Errorf("%+v: %d, want %d", k, d.visits[k], n)
		}
	}
}

// Counts a failed write kept stay within cMaxVisitKeys as well: with the
// primary away for days, each day's sources would otherwise pile up on the
// last ones'. Nothing is lost on the way.
func TestKeptSiteVisitsAreBounded(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	pages := []string{"/", "/es/", "/fr/"}
	var recorded int64
	for day := 1; day <= 5; day++ {
		for _, page := range pages {
			for i := 0; i < cMaxVisitSourcesPerDay; i++ {
				d.recordSiteVisit(fmt.Sprintf("2026-10-%02d", day), page, fmt.Sprintf("s%d.example", i))
				recorded++
			}
		}
		for i := 0; i < (len(d.visits)+cVisitsPerWrite-1)/cVisitsPerWrite; i++ {
			mock.ExpectExec(cVisitsInsert).WillReturnError(errors.New("primary away"))
		}
		d.flushVisits(time.Minute, time.Minute)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		// At most a cVisitOther per day and page past the bound.
		if n := len(d.visits); n > cMaxVisitKeys+day*len(pages) {
			t.Fatalf("day %d: %d keys waiting", day, n)
		}
	}
	var waiting int64
	for _, n := range d.visits {
		waiting += n
	}
	if waiting != recorded {
		t.Fatalf("%d visits waiting, %d recorded", waiting, recorded)
	}

	// keepVisits on its own: a full map takes no new key.
	d = NewWithDB(nil)
	for i := 0; i < cMaxVisitKeys; i++ {
		d.recordSiteVisit("2026-10-24", fmt.Sprintf("/p%d", i), "")
	}
	kept := map[visitKey]int64{{"2026-10-24", "/", "a.example"}: 2, {"2026-10-24", "/", "b.example"}: 3}
	d.keepVisits(kept, []visitKey{{"2026-10-24", "/", "a.example"}, {"2026-10-24", "/", "b.example"}})
	if len(d.visits) != cMaxVisitKeys+1 || d.visits[visitKey{"2026-10-24", "/", cVisitOther}] != 5 || d.visitsFolded != 5 {
		t.Fatalf("%d keys, %d under %s, %d folded", len(d.visits), d.visits[visitKey{"2026-10-24", "/", cVisitOther}], cVisitOther, d.visitsFolded)
	}
}

// The visits and the metrics share a pass's budget: a primary that stops
// answering costs the pass one write's timeout, and what the pass had no
// time for waits for the next.
func TestFlushPassSharesItsBudget(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	mock.ExpectExec("update `devices` set `last_client_at`").WillReturnResult(sqlmock.NewResult(0, 1))
	d.RecordDeviceActivity("a.off-the.cloud", 1, 2)
	d.recordSiteVisit("2026-10-24", "/", "")

	// The visits go first, and their write outlasts the pass.
	mock.ExpectExec(cVisitsInsert).WillDelayFor(time.Minute).WillReturnResult(sqlmock.NewResult(0, 1))
	start := time.Now()
	d.flushPass(50*time.Millisecond, 200*time.Millisecond)
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("a stalled pass took %v", took)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	d.metricsMu.Lock()
	defer d.metricsMu.Unlock()
	if len(d.metrics) != 1 {
		t.Fatalf("%d metrics kept, want the one the pass had no time for", len(d.metrics))
	}
}

// A poll's totals are one REPLACE of the day's rows, by name.
func TestSaveDownloadCounts(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	day := time.Date(2026, 10, 24, 23, 30, 0, 0, time.FixedZone("CEST", 2*3600)) // 21:30 UTC
	mock.ExpectExec("replace into `download_counts` \\(`day`, `asset`, `downloads`\\) values \\(\\?, \\?, \\?\\), \\(\\?, \\?, \\?\\)$").
		WithArgs("2026-10-24", "off-the-cloud-rpi-lite-arm64.img.xz", 42, "2026-10-24", "src.tar.gz@v136", 7).
		WillReturnResult(sqlmock.NewResult(0, 2))
	if err := d.SaveDownloadCounts(context.Background(), day, map[string]int64{"src.tar.gz@v136": 7, "off-the-cloud-rpi-lite-arm64.img.xz": 42}); err != nil {
		t.Fatal(err)
	}
	if err := d.SaveDownloadCounts(context.Background(), day, nil); err != nil { // nothing: no write
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
