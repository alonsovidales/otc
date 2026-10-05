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
	a := &Accounts{dao: dao.NewWithDB(db), tld: "bridge.test"}
	now := time.Now().UTC()
	months := func(m int) time.Time { return now.AddDate(0, -m, 0) }
	ids := []string{"inact-active", "inact-device", "inact-5m", "inact-7m-unwarned", "inact-7m-warned"}
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

	sent := map[string]int{}
	send := func(to, subject, body string) error { sent[to]++; return nil }
	a.inactivityPass(now, send, nil)
	if !warnedAt("inact-5m").Valid {
		t.Error("a warning that went out was not recorded")
	}

	exists := func(id string) bool {
		var n int
		db.QueryRow("select count(*) from `accounts` where `id` = ?", id).Scan(&n)
		return n == 1
	}
	for id, want := range map[string]bool{"inact-active": true, "inact-device": true, "inact-5m": true, "inact-7m-unwarned": true, "inact-7m-warned": false} {
		if exists(id) != want {
			t.Errorf("%s exists = %v, want %v", id, !want, want)
		}
	}
	for id, want := range map[string]int{"inact-active": 0, "inact-device": 0, "inact-5m": 1, "inact-7m-unwarned": 1} {
		if sent[id+"@example.com"] != want {
			t.Errorf("%s got %d warnings, want %d", id, sent[id+"@example.com"], want)
		}
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
