// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"os"
	"testing"
	"time"
)

// Against a real MySQL with the bridge schema (OTC_TEST_MYSQL_DSN): a name
// released by an account is held for it for cNameHold - NameHold says it
// is its own, NameHeld that nobody else may take it - one released by no
// account is held from everyone, and a hold past cNameHold is none.
func TestNameHoldMySQL(t *testing.T) {
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
	const mine, nobodys, old = "nh-mine.bridge.test", "nh-nobody.bridge.test", "nh-old.bridge.test"
	cleanup := func() { db.Exec("delete from `released_domains` where `domain` in (?, ?, ?)", mine, nobodys, old) }
	cleanup()
	defer cleanup()
	if err := d.recordRelease(mine, "nh-acc1"); err != nil {
		t.Fatal(err)
	}
	if err := d.recordRelease(nobodys, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("insert into `released_domains` (`domain`, `account_id`, `released_at`) values (?, ?, ?)", old, "nh-acc1", time.Now().Add(-cNameHold-time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		domain, accountID     string
		forOthers, forAccount bool
	}{
		{mine, "nh-acc1", false, true},
		{mine, "nh-acc2", true, false},
		{mine, "", true, false},
		{nobodys, "nh-acc1", true, false},
		{old, "nh-acc1", false, false},
		{old, "nh-acc2", false, false},
		{"nh-never.bridge.test", "nh-acc1", false, false},
	} {
		forOthers, forAccount, err := d.NameHold(c.domain, c.accountID)
		if err != nil || forOthers != c.forOthers || forAccount != c.forAccount {
			t.Errorf("%s for %q: others %v, account %v, %v", c.domain, c.accountID, forOthers, forAccount, err)
		}
		if held, err := d.NameHeld(c.domain, c.accountID); err != nil || held != c.forOthers {
			t.Errorf("NameHeld(%s, %q) = %v, %v; NameHold says %v", c.domain, c.accountID, held, err, c.forOthers)
		}
	}
}
