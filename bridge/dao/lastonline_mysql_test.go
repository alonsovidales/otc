// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"os"
	"testing"
	"time"
)

// Against a real MySQL with the bridge schema (OTC_TEST_MYSQL_DSN), with
// migration 012 and without it: run it against both. With the column a
// device connected to the bridge is stamped, never back in time, and
// counts as use - its account leaves the inactivity list and a warning
// given before it connected is cleared - and the admin list and the
// export show the stamp. Without it stamping does nothing, and every
// query answers as it did before the column.
func TestLastOnlineMySQL(t *testing.T) {
	dsn := os.Getenv("OTC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("OTC_TEST_MYSQL_DSN not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var cols int
	if err := db.QueryRow("select count(*) from information_schema.columns where table_schema = database() and table_name = 'devices' and column_name = 'last_online_at'").Scan(&cols); err != nil {
		t.Fatal(err)
	}
	migrated := cols == 1
	t.Logf("migration 012 run: %v", migrated)

	const acc, email, domain = "lastonline-acc", "lastonline@example.com", "lastonline.example"
	cleanup := func() {
		db.Exec("delete from `devices` where `domain` = ?", domain)
		db.Exec("delete from `accounts` where `id` = ?", acc)
	}
	cleanup()
	defer cleanup()
	now := time.Now().UTC().Truncate(time.Second)
	warned := now.Add(-40 * 24 * time.Hour)
	if _, err := db.Exec("insert into `accounts` (`id`, `email`, `name`, `surname`, `country`, `created`, `last_seen`, `free_until`, `email_verified`, `inactivity_warned_at`) values (?, ?, 'T', 'U', 'NL', ?, ?, ?, 1, ?)",
		acc, email, now.AddDate(-1, 0, 0), now.AddDate(0, -8, 0), now, warned); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("insert into `devices` (`domain`, `owner_uuid`, `secret`, `account_id`, `created`) values (?, 'owner', 'secret', ?, ?)", domain, acc, now.AddDate(-1, 0, 0)); err != nil {
		t.Fatal(err)
	}
	inactive := func() bool {
		t.Helper()
		list, err := NewWithDB(db).InactiveAccounts(now.AddDate(0, -5, 0))
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range list {
			if a.ID == acc {
				return true
			}
		}
		return false
	}
	stamp := func() (at sql.NullTime) {
		t.Helper()
		if migrated {
			if err := db.QueryRow("select `last_online_at` from `devices` where `domain` = ?", domain).Scan(&at); err != nil {
				t.Fatal(err)
			}
		}
		return at
	}
	if !inactive() {
		t.Fatal("the account is not inactive before its device connects")
	}

	d := NewWithDB(db)
	d.TouchLastOnline([]string{domain})
	got := stamp()
	if migrated && (!got.Valid || got.Time.Before(now.Add(-time.Minute)) || got.Time.After(now.Add(time.Minute))) {
		t.Fatalf("stamped %v, want about %v", got, now)
	}
	// A later write of an earlier time (another node, a slow write) leaves it.
	NewWithDB(db).touchLastOnline([]string{domain}, cLastOnlineEvery, now.Add(-time.Hour))
	if again := stamp(); again != got {
		t.Errorf("an earlier time moved the stamp from %v to %v", got, again)
	}

	if err := d.ClearUsedInactivityWarnings(); err != nil {
		t.Fatal(err)
	}
	var w sql.NullTime
	if err := db.QueryRow("select `inactivity_warned_at` from `accounts` where `id` = ?", acc).Scan(&w); err != nil {
		t.Fatal(err)
	}
	if w.Valid == migrated {
		t.Errorf("warning kept = %v after the device connected (column there: %v)", w.Valid, migrated)
	}
	if inactive() == migrated {
		t.Errorf("inactive = %v with the device connected (column there: %v)", !migrated, migrated)
	}

	devs, total, err := NewWithDB(db).ListAdminDevices(acc, "", 0, 0)
	if err != nil || total != 1 || len(devs) != 1 {
		t.Fatalf("admin list: %+v %d %v", devs, total, err)
	}
	if (devs[0].LastOnline != nil) != migrated || (migrated && !devs[0].LastOnline.Equal(got.Time)) {
		t.Errorf("admin list: last online %v, want %v", devs[0].LastOnline, got)
	}
	out, err := NewWithDB(db).ExportAccount(acc)
	if err != nil || len(out.Domains) != 1 {
		t.Fatalf("export: %+v %v", out, err)
	}
	if (out.Domains[0].LastOnlineAt != nil) != migrated || (migrated && !out.Domains[0].LastOnlineAt.Equal(got.Time)) {
		t.Errorf("export: last online %v, want %v", out.Domains[0].LastOnlineAt, got)
	}
}
