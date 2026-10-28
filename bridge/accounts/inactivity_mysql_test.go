// SPDX-License-Identifier: AGPL-3.0-or-later

package accounts

import (
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/alonsovidales/otc/bridge/dao"
)

// Issue #176, against a real MySQL with the bridge schema (the Lima VM's
// test database): OTC_TEST_MYSQL_DSN=user:pass@tcp(127.0.0.1:3306)/otc?parseTime=true
// - with migration 012 (devices.last_online_at) and without it: run it
// against both. A device used only at home is connected to the bridge but
// never reached through it: with the column its stamp keeps the account,
// without it the live check does (inUseNow).
func TestInactivityPassMySQL(t *testing.T) {
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
	a := &Accounts{dao: dao.NewWithDB(db), tld: "bridge.test"}
	now := time.Now().UTC()
	months := func(m int) time.Time { return now.AddDate(0, -m, 0) }
	ids := []string{"inact-active", "inact-device", "inact-5m", "inact-7m-unwarned", "inact-7m-warned", "inact-home", "inact-home-warned"}
	cleanup := func() {
		for _, id := range ids {
			db.Exec("delete from `devices` where `account_id` = ?", id)
			db.Exec("delete from `accounts` where `id` = ?", id)
		}
	}
	cleanup()
	defer cleanup()
	add := func(id string, lastSeen time.Time, warned *time.Time) {
		if _, err := db.Exec("insert into `accounts` (`id`, `email`, `name`, `surname`, `country`, `created`, `last_seen`, `free_until`, `email_verified`, `inactivity_warned_at`) values (?, ?, 'T', 'U', 'NL', ?, ?, ?, 1, ?)",
			id, id+"@example.com", months(12), lastSeen, now, warned); err != nil {
			t.Fatal(err)
		}
	}
	add("inact-active", now.Add(-time.Hour), nil)
	add("inact-device", months(8), nil) // its device is used
	if _, err := db.Exec("insert into `devices` (`domain`, `owner_uuid`, `secret`, `account_id`, `last_client_at`) values ('inact-device.bridge.test', 'x', 'y', 'inact-device', ?)", now.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	add("inact-5m", months(5).Add(-24*time.Hour), nil)
	add("inact-7m-unwarned", months(7), nil)
	old := now.Add(-40 * 24 * time.Hour)
	add("inact-7m-warned", months(7), &old)
	// Used only at home: no sign-in or relayed client for months, the
	// device connected to the bridge now (its stamp a few minutes old) -
	// one of them warned before this counted, and due for removal.
	add("inact-home", months(8), nil)
	add("inact-home-warned", months(7), &old)
	for _, id := range []string{"inact-home", "inact-home-warned"} {
		if migrated {
			_, err = db.Exec("insert into `devices` (`domain`, `owner_uuid`, `secret`, `account_id`, `last_online_at`) values (?, 'x', 'y', ?, ?)", id+".bridge.test", id, now.Add(-9*time.Minute))
		} else {
			_, err = db.Exec("insert into `devices` (`domain`, `owner_uuid`, `secret`, `account_id`) values (?, 'x', 'y', ?)", id+".bridge.test", id)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !migrated {
		// No stamps to go by: the live connections.
		a.deviceOnline = func(domain string) bool {
			return domain == "inact-home.bridge.test" || domain == "inact-home-warned.bridge.test"
		}
	}

	warnedAt := func(id string) sql.NullTime {
		var warned sql.NullTime
		db.QueryRow("select `inactivity_warned_at` from `accounts` where `id` = ?", id).Scan(&warned)
		return warned
	}
	// The email fails (the SMTP server is away): nobody counts as warned,
	// so the next pass tries again.
	a.inactivityPass(now, func(to, subject, body string) error { return errors.New("smtp away") }, nil)
	for _, id := range []string{"inact-5m", "inact-7m-unwarned"} {
		if warnedAt(id).Valid {
			t.Errorf("%s counts as warned after a failed email", id)
		}
	}

	// A language stored by a bridge carrying one this build doesn't: the
	// warning is English, as is one for an account with none ("").
	db.Exec("update `accounts` set `lang` = 'zz' where `id` = 'inact-5m'")
	sent := map[string]int{}
	send := func(to, subject, body string) error {
		sent[to]++
		if ws, wb := legacyInactivity("T", to, a.tld, now); subject != ws || body != wb {
			t.Errorf("the warning to %s: %q %q, want %q %q", to, subject, body, ws, wb)
		}
		return nil
	}
	a.inactivityPass(now, send, nil)
	if !warnedAt("inact-5m").Valid {
		t.Error("a warning that went out was not recorded")
	}

	exists := func(id string) bool {
		var n int
		db.QueryRow("select count(*) from `accounts` where `id` = ?", id).Scan(&n)
		return n == 1
	}
	for id, want := range map[string]bool{"inact-active": true, "inact-device": true, "inact-5m": true, "inact-7m-unwarned": true, "inact-7m-warned": false, "inact-home": true, "inact-home-warned": true} {
		if exists(id) != want {
			t.Errorf("%s exists = %v, want %v", id, !want, want)
		}
	}
	for id, want := range map[string]int{"inact-active": 0, "inact-device": 0, "inact-5m": 1, "inact-7m-unwarned": 1, "inact-home": 0, "inact-home-warned": 0} {
		if sent[id+"@example.com"] != want {
			t.Errorf("%s got %d warnings, want %d", id, sent[id+"@example.com"], want)
		}
	}
	if warnedAt("inact-home").Valid {
		t.Error("an account whose device is online was warned")
	}
	if warnedAt("inact-home-warned").Valid {
		t.Error("the warning of an account whose device is online was kept")
	}
	// A second pass (the other node, or the next day) warns nobody again.
	a.inactivityPass(now, send, nil)
	if sent["inact-5m@example.com"] != 1 {
		t.Error("a warning went out twice")
	}
	// Used again after the warning: the warning is forgotten.
	db.Exec("update `accounts` set `last_seen` = ? where `id` = 'inact-5m'", now.Add(time.Minute))
	a.inactivityPass(now.Add(2*time.Minute), send, nil)
	var warned sql.NullTime
	db.QueryRow("select `inactivity_warned_at` from `accounts` where `id` = 'inact-5m'").Scan(&warned)
	if warned.Valid {
		t.Error("the warning of an account used again was kept")
	}
}
