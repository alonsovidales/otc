// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"testing"

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
