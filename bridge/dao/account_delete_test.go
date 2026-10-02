// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// Issue #182: deleting an account removes every row kept for it, in one
// transaction, then holds its names and forgets their push and metrics.
func TestDeleteAccountRemovesEverything(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	d := NewWithDB(db)

	mock.ExpectQuery("select `domain` from `devices` where `account_id` = ?").WithArgs("acc").
		WillReturnRows(sqlmock.NewRows([]string{"domain"}).AddRow("cala.off-the.cloud"))
	mock.ExpectBegin()
	for _, table := range []string{"devices", "account_logins", "account_tokens", "app_signin_codes", "account_email_tokens", "accounts"} {
		mock.ExpectExec("delete from `" + table + "`").WithArgs("acc").WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()
	mock.ExpectExec("insert into `released_domains`").WillReturnResult(sqlmock.NewResult(0, 1))
	for _, table := range []string{"push_registrations", "push_apns_tokens", "push_fcm_tokens", "push_web_subs", "device_metrics"} {
		mock.ExpectExec("delete from `" + table + "`").WithArgs("cala.off-the.cloud").WillReturnResult(sqlmock.NewResult(0, 0))
	}

	domains, err := d.DeleteAccount("acc")
	if err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if len(domains) != 1 || domains[0] != "cala.off-the.cloud" {
		t.Fatalf("released %v", domains)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A device can only give back its own name.
func TestReleaseDeviceDomainChecksTheSecret(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	d := NewWithDB(db)

	mock.ExpectQuery("select `owner_uuid`, `secret` from `devices`").WithArgs("cala.off-the.cloud").
		WillReturnRows(sqlmock.NewRows([]string{"owner_uuid", "secret"}).AddRow("owner", "right"))
	ok, err := d.ReleaseDeviceDomain("owner", "cala.off-the.cloud", "wrong")
	if err != nil || ok {
		t.Fatalf("released with the wrong secret: ok=%v err=%v", ok, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
