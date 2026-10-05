// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"errors"
	"testing"
	"time"

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

// The export (GDPR Art. 15/20) is all or nothing: a sign-in row that fails
// to read, or a count that fails, fails it rather than leaving it short.
func TestExportAccountFailsWhole(t *testing.T) {
	accountCols := []string{"id", "email", "name", "surname", "country", "password_hash", "created", "last_seen", "free_until", "email_verified", "terms_version", "terms_accepted_at"}
	expectAccount := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery("from `accounts` where `id` = \\?").WillReturnRows(sqlmock.NewRows(accountCols).
			AddRow("acc", "a@b.c", "A", "B", "ES", nil, time.Now(), time.Now(), time.Now(), true, nil, nil))
	}
	expectDomain := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery("from `devices` where `account_id` = \\?").WillReturnRows(sqlmock.NewRows([]string{"domain", "created", "disabled", "last_client_at"}).
			AddRow("cala.off-the.cloud", time.Now(), false, nil))
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)

	expectAccount(mock)
	mock.ExpectQuery("select `provider` from `account_logins`").WillReturnRows(sqlmock.NewRows([]string{"provider"}).
		AddRow("google").AddRow("apple").RowError(1, errors.New("connection lost")))
	if out, err := d.ExportAccount("acc"); err == nil {
		t.Errorf("a broken sign-in list exported: %+v", out.SignIns)
	}

	expectAccount(mock)
	mock.ExpectQuery("select `provider` from `account_logins`").WillReturnRows(sqlmock.NewRows([]string{"provider"}).AddRow("google"))
	expectDomain(mock)
	mock.ExpectQuery("from `push_apns_tokens`").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	mock.ExpectQuery("from `push_fcm_tokens`").WillReturnError(errors.New("connection lost"))
	if _, err := d.ExportAccount("acc"); err == nil {
		t.Error("a failed count exported as 0")
	}

	expectAccount(mock)
	mock.ExpectQuery("select `provider` from `account_logins`").WillReturnRows(sqlmock.NewRows([]string{"provider"}).AddRow("google"))
	expectDomain(mock)
	for _, table := range []string{"push_apns_tokens", "push_fcm_tokens", "push_web_subs"} {
		mock.ExpectQuery("from `" + table + "`").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	}
	mock.ExpectQuery("from `device_metrics`").WillReturnRows(sqlmock.NewRows([]string{"r", "i", "o"}).AddRow(5, 10, 20))
	out, err := d.ExportAccount("acc")
	if err != nil || len(out.SignIns) != 1 || len(out.Domains) != 1 || out.Domains[0].PushTokens != 3 || out.Domains[0].BytesOut != 20 {
		t.Fatalf("export: %+v %v", out, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
