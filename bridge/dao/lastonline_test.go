// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

const cStampQuery = "update `devices` set `last_online_at` = \\? where `domain` in \\(%s\\) and \\(`last_online_at` is null or `last_online_at` < \\?\\)"

// expectStamp expects one last_online_at write of domains at at.
func expectStamp(mock sqlmock.Sqlmock, at time.Time, domains ...string) *sqlmock.ExpectedExec {
	marks := "\\?"
	args := []driver.Value{at.UTC().Truncate(time.Second)}
	for i, d := range domains {
		if i > 0 {
			marks += ", \\?"
		}
		args = append(args, d)
	}
	args = append(args, at.UTC().Truncate(time.Second))
	return mock.ExpectExec(fmt.Sprintf(cStampQuery, marks)).WithArgs(args...)
}

// A connected device is stamped as it connects and then at most every
// cLastOnlineEvery, several in one statement; one whose last connection
// closed is stamped then, at most once a minute.
func TestTouchLastOnlineThrottled(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	t0 := time.Date(2026, 10, 28, 12, 0, 0, 500, time.UTC)

	expectStamp(mock, t0, "a.test", "b.test").WillReturnResult(sqlmock.NewResult(0, 2))
	d.touchLastOnline([]string{"a.test", "b.test", "a.test"}, cLastOnlineEvery, t0) // a listed twice: written once

	at := t0.Add(5 * time.Minute)
	expectStamp(mock, at, "c.test").WillReturnResult(sqlmock.NewResult(0, 1))
	d.touchLastOnline([]string{"a.test", "b.test", "c.test"}, cLastOnlineEvery, at) // a and b: not yet

	// a's last connection closes: online until now.
	expectStamp(mock, at, "a.test").WillReturnResult(sqlmock.NewResult(0, 1))
	d.touchLastOnline([]string{"a.test"}, cLastOnlineLeft, at)
	// It comes back and drops again within the minute: nothing more.
	d.touchLastOnline([]string{"a.test"}, cLastOnlineLeft, at.Add(30*time.Second))

	at = t0.Add(cLastOnlineEvery)
	expectStamp(mock, at, "b.test").WillReturnResult(sqlmock.NewResult(0, 1))
	d.touchLastOnline([]string{"a.test", "b.test", "c.test"}, cLastOnlineEvery, at)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A write that fails is tried again at the next call, not a whole
// cLastOnlineEvery later; one that worked stays throttled.
func TestTouchLastOnlineRetriesAFailedWrite(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	t0 := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

	expectStamp(mock, t0, "a.test").WillReturnResult(sqlmock.NewResult(0, 1))
	d.touchLastOnline([]string{"a.test"}, cLastOnlineEvery, t0)

	at := t0.Add(cLastOnlineEvery)
	expectStamp(mock, at, "a.test", "b.test").WillReturnError(errors.New("connection refused"))
	d.touchLastOnline([]string{"a.test", "b.test"}, cLastOnlineEvery, at)

	next := at.Add(time.Minute)
	expectStamp(mock, next, "a.test", "b.test").WillReturnResult(sqlmock.NewResult(0, 2))
	d.touchLastOnline([]string{"a.test", "b.test"}, cLastOnlineEvery, next)
	d.touchLastOnline([]string{"a.test", "b.test"}, cLastOnlineEvery, next.Add(time.Minute))

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Many devices go in statements of at most cOwnersChunk.
func TestTouchLastOnlineChunks(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	now := time.Now()
	var domains []string
	for i := 0; i < cOwnersChunk+3; i++ {
		domains = append(domains, fmt.Sprintf("d%d.test", i))
	}
	expectStamp(mock, now, domains[:cOwnersChunk]...).WillReturnResult(sqlmock.NewResult(0, cOwnersChunk))
	expectStamp(mock, now, domains[cOwnersChunk:]...).WillReturnResult(sqlmock.NewResult(0, 3))
	d.touchLastOnline(domains, cLastOnlineEvery, now)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// On a database from before migration 012 nothing is stamped and nothing
// fails; once the column is known missing nothing is even sent, until the
// recheck finds it there.
func TestTouchLastOnlineWithoutTheColumn(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	t0 := time.Now()

	expectStamp(mock, t0, "a.test").WillReturnError(unknownColumn("last_online_at"))
	d.touchLastOnline([]string{"a.test"}, cLastOnlineEvery, t0)
	d.touchLastOnline([]string{"b.test"}, cLastOnlineEvery, t0) // known missing: no query
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	d.lastOnlineCol.missingSince.Store(time.Now().Add(-cColumnRecheck - time.Second).UnixNano())
	at := t0.Add(cLastOnlineEvery)
	expectStamp(mock, at, "a.test", "b.test").WillReturnResult(sqlmock.NewResult(0, 2))
	d.touchLastOnline([]string{"a.test", "b.test"}, cLastOnlineEvery, at)
	if d.lastOnlineCol.missingSince.Load() != 0 {
		t.Error("the column is still taken for missing")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A device connected to the bridge is use: the inactivity list leaves out
// an account with one connected since the date (the date three times),
// and the warnings of accounts with a device connected since are cleared.
// Without the column, both go on as before it.
func TestInactivityCountsDevicesOnline(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	before := time.Date(2026, 5, 28, 0, 0, 0, 0, time.UTC)
	cols := []string{"id", "email", "name", "inactivity_warned_at", "lang"}

	mock.ExpectQuery("from `accounts` a where a.`last_seen` < \\? and not exists \\(select 1 from `devices` d where d.`account_id` = a.`id` and \\(d.`last_client_at` >= \\? or d.`last_online_at` >= \\?\\)\\)").
		WithArgs(before, before, before).WillReturnRows(sqlmock.NewRows(cols).AddRow("acc1", "a@b.c", "A", nil, "en"))
	if out, err := d.InactiveAccounts(before); err != nil || len(out) != 1 || out[0].ID != "acc1" {
		t.Fatalf("with the column: %+v %v", out, err)
	}
	mock.ExpectExec("update `accounts` a set a.`inactivity_warned_at` = null where .*d.`last_client_at` > a.`inactivity_warned_at` or d.`last_online_at` > a.`inactivity_warned_at`").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := d.ClearUsedInactivityWarnings(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	// Migration 012 not run: the queries from before it.
	d = NewWithDB(db)
	mock.ExpectQuery("d.`last_online_at` >= \\?").WillReturnError(unknownColumn("d.last_online_at"))
	mock.ExpectQuery("from `accounts` a where a.`last_seen` < \\? and not exists \\(select 1 from `devices` d where d.`account_id` = a.`id` and d.`last_client_at` >= \\?\\)$").
		WithArgs(before, before).WillReturnRows(sqlmock.NewRows(cols).AddRow("acc1", "a@b.c", "A", nil, "en"))
	if out, err := d.InactiveAccounts(before); err != nil || len(out) != 1 || out[0].Lang != "en" {
		t.Fatalf("without the column: %+v %v", out, err)
	}
	// Known missing: straight to the old statement.
	mock.ExpectExec("update `accounts` a set a.`inactivity_warned_at` = null where .*d.`last_client_at` > a.`inactivity_warned_at`\\)\\)$").
		WillReturnResult(sqlmock.NewResult(0, 0))
	if err := d.ClearUsedInactivityWarnings(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	// Neither migration 010 nor 012: whichever column MySQL names first,
	// the query without both answers.
	for _, first := range []string{"a.lang", "d.last_online_at"} {
		d = NewWithDB(db)
		mock.ExpectQuery("a.`lang` from `accounts` a .*d.`last_online_at`").WillReturnError(unknownColumn(first))
		if first == "a.lang" {
			mock.ExpectQuery("a.`inactivity_warned_at` from `accounts` a .*d.`last_online_at`").WillReturnError(unknownColumn("d.last_online_at"))
		} else {
			mock.ExpectQuery("a.`lang` from `accounts` a .*d.`last_client_at` >= \\?\\)$").WillReturnError(unknownColumn("a.lang"))
		}
		mock.ExpectQuery("a.`inactivity_warned_at` from `accounts` a .*d.`last_client_at` >= \\?\\)$").
			WillReturnRows(sqlmock.NewRows(cols[:4]).AddRow("acc1", "a@b.c", "A", nil))
		if out, err := d.InactiveAccounts(before); err != nil || len(out) != 1 || out[0].Lang != "" {
			t.Fatalf("without both columns (%s first): %+v %v", first, out, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(first, err)
		}
	}
}

// The admin list and the export carry last_online_at, and still answer on
// a database without it.
func TestAdminListAndExportWithoutTheOnlineColumn(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	online := time.Date(2026, 10, 28, 11, 50, 0, 0, time.UTC)
	devCols := []string{"domain", "owner_uuid", "account_id", "email", "name", "disabled", "created", "last_client_at", "last_online_at", "h", "d", "m"}

	mock.ExpectQuery("select count\\(\\*\\) from `devices`").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	mock.ExpectQuery("d.`last_client_at`, d.`last_online_at` from `devices` d").
		WillReturnRows(sqlmock.NewRows(devCols).AddRow("pit.otc", "o", "acc", "a@b.c", "A", false, nil, nil, online, 1, 2, 3))
	devs, total, err := d.ListAdminDevices("", "", 25, 0)
	if err != nil || total != 1 || len(devs) != 1 || devs[0].LastOnline == nil || !devs[0].LastOnline.Equal(online) || devs[0].LastClient != nil {
		t.Fatalf("with the column: %+v %d %v", devs, total, err)
	}

	mock.ExpectQuery("select count\\(\\*\\) from `devices`").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	mock.ExpectQuery("d.`last_online_at` from `devices` d").WillReturnError(unknownColumn("d.last_online_at"))
	mock.ExpectQuery("d.`last_client_at`, null as `last_online_at` from `devices` d").
		WillReturnRows(sqlmock.NewRows(devCols).AddRow("pit.otc", "o", "acc", "a@b.c", "A", false, nil, nil, nil, 1, 2, 3))
	devs, _, err = d.ListAdminDevices("", "", 25, 0)
	if err != nil || len(devs) != 1 || devs[0].LastOnline != nil {
		t.Fatalf("without the column: %+v %v", devs, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	accountCols := []string{"id", "email", "name", "surname", "country", "password_hash", "created", "last_seen", "free_until", "email_verified", "terms_version", "terms_accepted_at", "lang"}
	export := func(withColumn bool) *AccountExport {
		t.Helper()
		mock.ExpectQuery("from `accounts` where `id` = \\?").WillReturnRows(sqlmock.NewRows(accountCols).
			AddRow("acc", "a@b.c", "A", "B", "ES", nil, time.Now(), time.Now(), time.Now(), true, nil, nil, "en"))
		mock.ExpectQuery("select `provider` from `account_logins`").WillReturnRows(sqlmock.NewRows([]string{"provider"}))
		domCols := []string{"domain", "created", "disabled", "last_client_at", "last_online_at"}
		if withColumn {
			mock.ExpectQuery("select `domain`, `created`, `disabled`, `last_client_at`, `last_online_at` from `devices`").
				WillReturnRows(sqlmock.NewRows(domCols).AddRow("pit.otc", nil, false, nil, online))
		} else {
			mock.ExpectQuery("select `domain`, `created`, `disabled`, `last_client_at`, null from `devices`").
				WillReturnRows(sqlmock.NewRows(domCols).AddRow("pit.otc", nil, false, nil, nil))
		}
		for _, table := range []string{"push_apns_tokens", "push_fcm_tokens", "push_web_subs"} {
			mock.ExpectQuery("from `" + table + "`").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
		}
		mock.ExpectQuery("select `language` from `push_registrations`").WillReturnRows(sqlmock.NewRows([]string{"language"}))
		mock.ExpectQuery("from `device_metrics`").WillReturnRows(sqlmock.NewRows([]string{"r", "i", "o"}).AddRow(0, 0, 0))
		out, err := NewWithDB(db).ExportAccount("acc")
		if err != nil || len(out.Domains) != 1 {
			t.Fatalf("export: %+v %v", out, err)
		}
		return out
	}
	if out := export(true); out.Domains[0].LastOnlineAt == nil || !out.Domains[0].LastOnlineAt.Equal(online) {
		t.Errorf("export: last online %v", out.Domains[0].LastOnlineAt)
	}
	// Without the column: the first query fails on it, the second has none.
	mock.ExpectQuery("from `accounts` where `id` = \\?").WillReturnRows(sqlmock.NewRows(accountCols).
		AddRow("acc", "a@b.c", "A", "B", "ES", nil, time.Now(), time.Now(), time.Now(), true, nil, nil, "en"))
	mock.ExpectQuery("select `provider` from `account_logins`").WillReturnRows(sqlmock.NewRows([]string{"provider"}))
	mock.ExpectQuery("`last_online_at` from `devices`").WillReturnError(unknownColumn("last_online_at"))
	mock.ExpectQuery("select `domain`, `created`, `disabled`, `last_client_at`, null from `devices`").
		WillReturnRows(sqlmock.NewRows([]string{"domain", "created", "disabled", "last_client_at", "null"}).AddRow("pit.otc", nil, false, nil, nil))
	for _, table := range []string{"push_apns_tokens", "push_fcm_tokens", "push_web_subs"} {
		mock.ExpectQuery("from `" + table + "`").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	}
	mock.ExpectQuery("select `language` from `push_registrations`").WillReturnRows(sqlmock.NewRows([]string{"language"}))
	mock.ExpectQuery("from `device_metrics`").WillReturnRows(sqlmock.NewRows([]string{"r", "i", "o"}).AddRow(0, 0, 0))
	if out, err := NewWithDB(db).ExportAccount("acc"); err != nil || len(out.Domains) != 1 || out.Domains[0].LastOnlineAt != nil {
		t.Fatalf("export without the column: %+v %v", out, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
