// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"os"
	"testing"
	"time"
)

// Against a real MySQL with the bridge schema (OTC_TEST_MYSQL_DSN), in
// ONLY_FULL_GROUP_BY as MySQL 8 runs by default: the admin device list
// pages first and sums only that page's traffic, with the same figures.
func TestListAdminDevicesMySQL(t *testing.T) {
	dsn := os.Getenv("OTC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("OTC_TEST_MYSQL_DSN not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // the sql_mode below is the connection's
	if _, err := db.Exec("set session sql_mode = concat_ws(',', nullif(@@sql_mode, ''), 'ONLY_FULL_GROUP_BY')"); err != nil {
		t.Fatal(err)
	}
	d := NewWithDB(db)
	const acc = "admlist-acc"
	cleanup := func() {
		db.Exec("delete from `device_metrics` where `domain` like 'admlist-%'")
		db.Exec("delete from `devices` where `domain` like 'admlist-%'")
		db.Exec("delete from `accounts` where `id` = ?", acc)
	}
	cleanup()
	defer cleanup()
	now := time.Now().UTC()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("insert into `accounts` (`id`, `email`, `name`, `surname`, `country`, `created`, `last_seen`, `free_until`, `email_verified`) values (?, 'admlist@example.com', 'Ada', 'L', 'NL', ?, ?, ?, 1)", acc, now, now, now)
	for _, dev := range []struct{ domain, account string }{{"admlist-a.test", acc}, {"admlist-b.test", acc}, {"admlist-c.test", ""}} {
		var account any
		if dev.account != "" {
			account = dev.account
		}
		exec("insert into `devices` (`domain`, `owner_uuid`, `secret`, `account_id`, `created`) values (?, 'owner', 'secret', ?, ?)", dev.domain, account, now)
	}
	hour := now.Truncate(time.Hour)
	for _, m := range []struct {
		domain  string
		at      time.Time
		in, out int64
	}{
		{"admlist-a.test", hour, 100, 1},
		{"admlist-a.test", hour.Add(-5 * time.Hour), 10, 0},
		{"admlist-a.test", hour.Add(-10 * 24 * time.Hour), 1000, 0},
		{"admlist-a.test", hour.Add(-40 * 24 * time.Hour), 99999, 0}, // past the 30 days
		{"admlist-b.test", hour.Add(-2 * 24 * time.Hour), 7, 0},
	} {
		exec("insert into `device_metrics` (`domain`, `hour_bucket`, `requests`, `bytes_in`, `bytes_out`) values (?, ?, 1, ?, ?)", m.domain, m.at, m.in, m.out)
	}

	page, total, err := d.ListAdminDevices("", "admlist-", 2, 0)
	if err != nil || total != 3 || len(page) != 2 {
		t.Fatalf("first page: %d of %d, %v", len(page), total, err)
	}
	a, b := page[0], page[1]
	if a.Domain != "admlist-a.test" || a.AccountID != acc || a.AccountEmail != "admlist@example.com" || a.AccountName != "Ada L" ||
		a.BytesHour != 101 || a.BytesDay != 111 || a.BytesMonth != 1111 {
		t.Errorf("a: %+v", a)
	}
	if b.Domain != "admlist-b.test" || b.BytesHour != 0 || b.BytesDay != 0 || b.BytesMonth != 7 {
		t.Errorf("b: %+v", b)
	}
	page, total, err = d.ListAdminDevices("", "admlist-", 2, 2)
	if err != nil || total != 3 || len(page) != 1 || page[0].Domain != "admlist-c.test" || page[0].AccountID != "" || page[0].AccountName != "" || page[0].BytesMonth != 0 {
		t.Fatalf("second page: %+v of %d, %v", page, total, err)
	}
	// An account's own devices, unpaged (the admin's account view).
	page, total, err = d.ListAdminDevices(acc, "", 0, 0)
	if err != nil || total != 2 || len(page) != 2 || page[0].Domain != "admlist-a.test" || page[1].BytesMonth != 7 {
		t.Fatalf("the account's devices: %+v of %d, %v", page, total, err)
	}
}
