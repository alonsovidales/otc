// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// A cleared password is NULL, as CreateAccount stores "none" - not an
// empty string, which the admin list counted as a password.
func TestClearingAPasswordStoresNull(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	mock.ExpectExec("update `accounts` set `password_hash` = \\?").WithArgs(nil, "acc1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("update `accounts` set `password_hash` = \\?").WithArgs("$2a$12$hash", "acc1").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := d.SetAccountPassword("acc1", ""); err != nil {
		t.Fatal(err)
	}
	if err := d.SetAccountPassword("acc1", "$2a$12$hash"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Against a real MySQL with the bridge schema (OTC_TEST_MYSQL_DSN, as in
// accounts/inactivity_mysql_test.go): the new epoch comes back from the
// update itself, and a cleared password is NULL.
func TestAccountPasswordMySQL(t *testing.T) {
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
	const id = "pw-epoch-test"
	cleanup := func() { db.Exec("delete from `accounts` where `id` = ?", id) }
	cleanup()
	defer cleanup()
	now := time.Now().UTC()
	if _, err := db.Exec("insert into `accounts` (`id`, `email`, `name`, `surname`, `country`, `created`, `last_seen`, `free_until`, `email_verified`) values (?, ?, 'T', 'U', 'NL', ?, ?, ?, 1)",
		id, id+"@example.com", now, now, now); err != nil {
		t.Fatal(err)
	}
	for want := 1; want <= 2; want++ {
		epoch, err := d.SetAccountPasswordEndingSessions(id, "$2a$12$hash")
		if err != nil || epoch != want {
			t.Fatalf("epoch %d, %v; want %d", epoch, err, want)
		}
	}
	if epoch, found, err := d.AccountSessionEpoch(id); err != nil || !found || epoch != 2 {
		t.Fatalf("stored epoch %d %v %v", epoch, found, err)
	}
	if _, err := d.SetAccountPasswordEndingSessions("no-such-account", "$2a$12$hash"); err != sql.ErrNoRows {
		t.Fatalf("an unknown account: %v", err)
	}
	if err := d.SetAccountPassword(id, ""); err != nil {
		t.Fatal(err)
	}
	var hash sql.NullString
	if err := db.QueryRow("select `password_hash` from `accounts` where `id` = ?", id).Scan(&hash); err != nil || hash.Valid {
		t.Fatalf("a cleared password is %q (valid %v), %v", hash.String, hash.Valid, err)
	}
}
