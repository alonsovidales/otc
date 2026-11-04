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

// Against a real MySQL with the bridge schema (OTC_TEST_MYSQL_DSN): a
// verification or reset link is spent in the transaction that stores what
// it is for. A reset sets the password, proves the email, moves the
// session epoch on and takes the account's other reset links with it,
// leaving its verification links; a used or expired link does nothing;
// and a link whose account is gone stays where it was, nothing written.
func TestEmailLinksSpentWithTheirWriteMySQL(t *testing.T) {
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
	const acc, gone = "email-link-write-test", "email-link-write-gone"
	cleanup := func() {
		db.Exec("delete from `account_email_tokens` where `account_id` in (?, ?)", acc, gone)
		db.Exec("delete from `accounts` where `id` = ?", acc)
	}
	cleanup()
	defer cleanup()
	now := time.Now().UTC().Truncate(time.Second)
	if err := d.CreateAccount(&Account{ID: acc, Email: "email-link-write@example.com", PasswordHash: "$2a$12$old", Created: now, LastSeen: now, FreeUntil: now}); err != nil {
		t.Fatal(err)
	}
	link := func(hash, account, purpose string, expires time.Time) {
		t.Helper()
		if _, err := db.Exec("insert into `account_email_tokens` (`token_hash`, `account_id`, `purpose`, `created`, `expires`) values (?, ?, ?, ?, ?)",
			hash, account, purpose, now, expires); err != nil {
			t.Fatal(err)
		}
	}
	links := func(account string) (n int) {
		t.Helper()
		if err := db.QueryRow("select count(*) from `account_email_tokens` where `account_id` = ?", account).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	state := func() (hash string, verified bool, epoch int) {
		t.Helper()
		if err := db.QueryRow("select `password_hash`, `email_verified`, `session_epoch` from `accounts` where `id` = ?", acc).Scan(&hash, &verified, &epoch); err != nil {
			t.Fatal(err)
		}
		return
	}
	link("reset-1", acc, "reset", now.Add(time.Hour))
	link("reset-2", acc, "reset", now.Add(time.Hour))
	link("verify-1", acc, "verify", now.Add(time.Hour))
	link("reset-old", acc, "reset", now.Add(-time.Minute))

	if id, ok, err := d.EmailTokenAccount("reset-1", "reset"); err != nil || !ok || id != acc {
		t.Fatalf("looking at a live link: %q %v %v", id, ok, err)
	}
	if _, ok, err := d.EmailTokenAccount("reset-old", "reset"); err != nil || ok {
		t.Fatalf("looking at an expired link: %v %v", ok, err)
	}
	if _, ok, err := d.EmailTokenAccount("verify-1", "reset"); err != nil || ok {
		t.Fatalf("a verification link taken for a reset: %v %v", ok, err)
	}
	if links(acc) != 4 {
		t.Fatal("looking at a link used it")
	}

	id, epoch, ok, err := d.ResetPasswordWithToken("reset-1", "reset", "$2a$12$new")
	if err != nil || !ok || id != acc || epoch != 1 {
		t.Fatalf("a reset: %q %d %v %v", id, epoch, ok, err)
	}
	if hash, verified, e := state(); hash != "$2a$12$new" || !verified || e != 1 {
		t.Fatalf("after the reset: %q %v %d", hash, verified, e)
	}
	var left []string
	rows, err := db.Query("select `token_hash` from `account_email_tokens` where `account_id` = ? order by `token_hash`", acc)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var h string
		rows.Scan(&h)
		left = append(left, h)
	}
	rows.Close()
	if len(left) != 1 || left[0] != "verify-1" {
		t.Fatalf("links left after the reset: %v, want only the verification link", left)
	}
	if _, _, ok, err := d.ResetPasswordWithToken("reset-2", "reset", "$2a$12$again"); err != nil || ok {
		t.Fatalf("another reset link of the account after the reset: %v %v", ok, err)
	}
	if _, _, ok, err := d.ResetPasswordWithToken("reset-old", "reset", "$2a$12$again"); err != nil || ok {
		t.Fatalf("an expired reset link: %v %v", ok, err)
	}
	if hash, _, _ := state(); hash != "$2a$12$new" {
		t.Fatalf("a refused reset changed the password: %q", hash)
	}

	db.Exec("update `accounts` set `email_verified` = 0 where `id` = ?", acc)
	if id, ok, err := d.VerifyEmailWithToken("verify-1", "verify"); err != nil || !ok || id != acc {
		t.Fatalf("a verification: %q %v %v", id, ok, err)
	}
	if _, verified, _ := state(); !verified || links(acc) != 0 {
		t.Fatalf("after the verification: verified %v, %d links", verified, links(acc))
	}
	if _, ok, err := d.VerifyEmailWithToken("verify-1", "verify"); err != nil || ok {
		t.Fatalf("a used verification link: %v %v", ok, err)
	}

	// A link whose account is gone: nothing done, and nothing spent.
	link("gone-reset", gone, "reset", now.Add(time.Hour))
	link("gone-verify", gone, "verify", now.Add(time.Hour))
	if _, _, ok, err := d.ResetPasswordWithToken("gone-reset", "reset", "$2a$12$x"); err != nil || ok {
		t.Fatalf("a reset link without its account: %v %v", ok, err)
	}
	if _, ok, err := d.VerifyEmailWithToken("gone-verify", "verify"); err != nil || ok {
		t.Fatalf("a verification link without its account: %v %v", ok, err)
	}
	if links(gone) != 2 {
		t.Fatalf("%d links left of the account that is gone, want both", links(gone))
	}
}
