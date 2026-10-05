// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"os"
	"testing"
	"time"
)

// Against a real MySQL with the bridge schema (OTC_TEST_MYSQL_DSN): a new
// link sits next to the old one until it is sent, then the older one goes;
// the time AddEmailToken returns is the one stored, so the throttle and
// the strict "older than" see the same value.
func TestEmailTokensMySQL(t *testing.T) {
	dsn := os.Getenv("OTC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("OTC_TEST_MYSQL_DSN not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	const acc = "email-token-test"
	cleanup := func() { db.Exec("delete from `account_email_tokens` where `account_id` = ?", acc) }
	cleanup()
	defer cleanup()
	count := func() int {
		var n int
		if err := db.QueryRow("select count(*) from `account_email_tokens` where `account_id` = ?", acc).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	old := time.Now().UTC().Add(-3 * time.Minute).Truncate(time.Second)
	if _, err := db.Exec("insert into `account_email_tokens` (`token_hash`, `account_id`, `purpose`, `created`, `expires`) values (?, ?, 'verify', ?, ?)",
		"old-hash", acc, old, old.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	created, err := d.AddEmailToken("new-hash", acc, "verify", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if last, err := d.LastEmailTokenSent(acc, "verify"); err != nil || !last.Equal(created) {
		t.Fatalf("stored %v, returned %v (%v)", last, created, err)
	}
	if count() != 2 {
		t.Fatal("the old link went before the new one was sent")
	}
	if err := d.KeepNewestEmailToken("new-hash", acc, "verify", created); err != nil {
		t.Fatal(err)
	}
	var left string
	if err := db.QueryRow("select `token_hash` from `account_email_tokens` where `account_id` = ?", acc).Scan(&left); err != nil || left != "new-hash" {
		t.Fatalf("left %q, %v", left, err)
	}
	if err := d.DropEmailToken("new-hash"); err != nil || count() != 0 {
		t.Fatalf("drop: %v, %d left", err, count())
	}
}
