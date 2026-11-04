// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

// unknownTable is what MySQL answers a statement naming a table the
// database lacks (unknownColumn is language_test.go's).
func unknownTable(n string) error {
	return &mysql.MySQLError{Number: 1146, Message: "Table 'otc." + n + "' doesn't exist"}
}

// Without a server: each step of the move to hashed secrets is found from
// what MySQL refuses, and the same work is done without what is missing -
// after migration 014 (no devices.secret) by the hash alone, before 013
// (no secret_hash) by the plaintext alone - and not asked about again for a
// while.
func TestSecretStepsFoundFromTheSchema(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef0123456789abcdef"

	// After 014.
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	mock.ExpectQuery("select `owner_uuid`, `secret` from `devices`").WillReturnError(unknownColumn("secret"))
	mock.ExpectQuery("select `owner_uuid`, `secret_hash` from `devices` where `domain` = \\?").WithArgs("a.test").
		WillReturnRows(sqlmock.NewRows([]string{"owner_uuid", "secret_hash"}).AddRow("owner", SecretHash(secret)))
	if defined, ok, err := d.IsValidDevice("owner", "a.test", secret); !defined || !ok || err != nil {
		t.Fatalf("checked by its hash: %v %v %v", defined, ok, err)
	}
	// Known now: straight to the hash.
	mock.ExpectQuery("select `owner_uuid`, `secret_hash` from `devices`").
		WillReturnRows(sqlmock.NewRows([]string{"owner_uuid", "secret_hash"}).AddRow("owner", SecretHash(secret)))
	if _, ok, _ := d.IsValidDevice("owner", "a.test", "wrong"); ok {
		t.Fatal("a wrong secret matched the hash")
	}
	mock.ExpectExec("insert into `devices` \\(`owner_uuid`, `domain`, `secret_hash`\\) values \\(\\?, \\?, \\?\\)").
		WithArgs("owner", "b.test", SecretHash(secret)).WillReturnResult(sqlmock.NewResult(1, 1))
	if err := d.RegistreDevice("owner", "b.test", secret); err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec("update `devices` set `secret_hash` = \\? where `owner_uuid` = \\? and `domain` = \\? and `secret_hash` = \\?").
		WithArgs(SecretHash("new-secret"), "owner", "b.test", SecretHash(secret)).WillReturnResult(sqlmock.NewResult(0, 1))
	if ok, err := d.RotateSecret("owner", "b.test", secret, "new-secret"); !ok || err != nil {
		t.Fatalf("rotated by the hashes: %v %v", ok, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	// Before 013.
	db2, mock2, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	d2 := NewWithDB(db2)
	mock2.ExpectExec("insert into `devices` \\(`owner_uuid`, `domain`, `account_id`, `created`, `secret`, `secret_hash`\\)").WillReturnError(unknownColumn("secret_hash"))
	mock2.ExpectExec("insert into `devices` \\(`owner_uuid`, `domain`, `account_id`, `created`, `secret`\\) values").
		WithArgs("owner", "c.test", "acc", sqlmock.AnyArg(), secret).WillReturnResult(sqlmock.NewResult(1, 1))
	if err := d2.RegisterAccountDevice("acc", "owner", "c.test", secret); err != nil {
		t.Fatal(err)
	}
	mock2.ExpectExec("update `devices` set `owner_uuid` = \\?, `secret` = \\? where `domain` = \\? and `account_id` = \\?").
		WithArgs("owner-2", secret, "c.test", "acc").WillReturnResult(sqlmock.NewResult(0, 1))
	if ok, err := d2.ReplaceDeviceIdentity("acc", "c.test", "owner-2", secret); !ok || err != nil {
		t.Fatalf("replaced with the plaintext: %v %v", ok, err)
	}
	// Codes before 013: only account_tokens, every one able to move a name.
	mock2.ExpectBegin()
	mock2.ExpectExec("insert into `account_setup_codes`").WillReturnError(unknownTable("account_setup_codes"))
	mock2.ExpectExec("insert into `account_tokens`").WithArgs("ABCD2345", "acc", "setup", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	mock2.ExpectCommit()
	if err := d2.SaveSetupCode("ABCD2345", SecretHash("ABCD2345"), "acc", false, time.Now(), time.Minute); err != nil {
		t.Fatal(err)
	}
	mock2.ExpectQuery("select `account_id` from `account_tokens` where `token` = \\?").WithArgs("ABCD2345", "setup", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow("acc"))
	if sc, found, err := d2.SetupCodeFor("ABCD2345", SecretHash("ABCD2345"), time.Now()); !found || err != nil || sc.AccountID != "acc" || !sc.OwnerConfirmed {
		t.Fatalf("a code before 013: %+v %v %v", sc, found, err)
	}
	if err := mock2.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	// Neither table: no code is made.
	db3, mock3, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db3.Close()
	d3 := NewWithDB(db3)
	mock3.ExpectBegin()
	mock3.ExpectExec("insert into `account_setup_codes`").WillReturnError(unknownTable("account_setup_codes"))
	mock3.ExpectExec("insert into `account_tokens`").WillReturnError(unknownTable("account_tokens"))
	mock3.ExpectRollback()
	if err := d3.SaveSetupCode("ABCD2345", SecretHash("ABCD2345"), "acc", true, time.Now(), time.Minute); err != ErrNoSetupCodeTable {
		t.Fatalf("no table: %v", err)
	}
	if err := mock3.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// SecretHash is the SHA-256 the migrations' SHA2(secret, 256) writes.
func TestSecretHash(t *testing.T) {
	if got := SecretHash("0123456789abcdef0123456789abcdef0123456789abcdef"); got != "34c26e154bab5ff544f29f8747a691d0fedd3b8a51655f27d0f585b1b2753970" {
		t.Errorf("SecretHash = %s", got)
	}
}
