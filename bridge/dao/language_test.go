// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

// unknownColumn is what MySQL answers a statement naming a column the
// table lacks.
func unknownColumn(col string) error {
	return &mysql.MySQLError{Number: 1054, Message: fmt.Sprintf("Unknown column '%s' in 'field list'", col)}
}

var (
	testAccountCols     = []string{"id", "email", "name", "surname", "country", "password_hash", "created", "last_seen", "free_until", "email_verified", "terms_version", "terms_accepted_at"}
	testAccountColsLang = append(append([]string{}, testAccountCols...), "lang")
)

func TestIsUnknownColumn(t *testing.T) {
	for _, c := range []struct {
		err    error
		column string
		want   bool
	}{
		{unknownColumn("lang"), "lang", true},
		{unknownColumn("a.lang"), "lang", true},
		{unknownColumn("language"), "language", true},
		{unknownColumn("language"), "lang", false},
		{unknownColumn("lang"), "language", false},
		{unknownColumn("terms_version"), "lang", false},
		{&mysql.MySQLError{Number: 1146, Message: "Table 'otc.lang' doesn't exist"}, "lang", false},
		{fmt.Errorf("query: %w", unknownColumn("lang")), "lang", true},
		{errors.New("Error 1054 (42S22): Unknown column 'lang' in 'field list'"), "lang", true},
		{errors.New("connection refused"), "lang", false},
		{nil, "lang", false},
	} {
		if got := isUnknownColumn(c.err, c.column); got != c.want {
			t.Errorf("isUnknownColumn(%v, %q) = %v", c.err, c.column, got)
		}
	}
}

// A bridge started before migration 010 reads accounts without their
// language - sign-in must not fail over it - and stops asking for the
// column until the recheck, when it finds it again once it is there.
func TestAccountReadsWithoutTheLangColumn(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	row := []driver.Value{"acc1", "a@b.c", "A", "B", "ES", nil, time.Now(), time.Now(), time.Now(), true, nil, nil}

	mock.ExpectQuery("select .*`terms_accepted_at`, `lang` from `accounts` where `id` = \\?").WillReturnError(unknownColumn("lang"))
	mock.ExpectQuery("select .*`terms_accepted_at` from `accounts` where `id` = \\?").WithArgs("acc1").
		WillReturnRows(sqlmock.NewRows(testAccountCols).AddRow(row...))
	// Known missing: straight to the query without it.
	mock.ExpectQuery("select .*`terms_accepted_at` from `accounts` where `email` = \\?").WithArgs("a@b.c").
		WillReturnRows(sqlmock.NewRows(testAccountCols).AddRow(row...))

	for _, get := range []func() (*Account, error){
		func() (*Account, error) { return d.GetAccount("acc1") },
		func() (*Account, error) { return d.GetAccountByEmail("a@b.c") },
	} {
		acc, err := get()
		if err != nil || acc == nil || acc.ID != "acc1" || acc.Lang != "" {
			t.Fatalf("without the column: %+v %v", acc, err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	// The migration ran meanwhile: past the recheck the column is used
	// again, and from then on.
	d.accountLang.missingSince.Store(time.Now().Add(-cColumnRecheck - time.Second).UnixNano())
	for i := 0; i < 2; i++ {
		mock.ExpectQuery("select .*`terms_accepted_at`, `lang` from `accounts` where `id` = \\?").
			WillReturnRows(sqlmock.NewRows(testAccountColsLang).AddRow(append(row, "en")...))
		if acc, err := d.GetAccount("acc1"); err != nil || acc.Lang != "en" {
			t.Fatalf("after the migration: %+v %v", acc, err)
		}
	}
	if d.accountLang.missingSince.Load() != 0 {
		t.Error("the column is still taken for missing")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Only "unknown column" for the language column itself means it is
// missing: any other failure is the query's own, never retried without
// the column, and nothing is remembered.
func TestAccountReadOtherErrorsStayErrors(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)

	mock.ExpectQuery("`lang` from `accounts`").WillReturnError(unknownColumn("terms_version"))
	mock.ExpectQuery("`lang` from `accounts`").WillReturnError(errors.New("connection refused"))
	for i := 0; i < 2; i++ {
		if _, err := d.GetAccount("acc1"); err == nil {
			t.Fatalf("attempt %d: no error", i)
		}
	}
	if d.accountLang.missingSince.Load() != 0 {
		t.Error("an unrelated error marked the column missing")
	}
	// An account nobody has proves the column is there just as well.
	mock.ExpectQuery("`lang` from `accounts`").WillReturnRows(sqlmock.NewRows(testAccountColsLang))
	if acc, err := d.GetAccount("nobody"); acc != nil || err != nil {
		t.Fatalf("no such account: %+v %v", acc, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A new account keeps its language, and is created without it on a
// database that has no column for it; with none to keep, the insert is
// the one from before the column.
func TestCreateAccountLang(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	now := time.Now()
	acc := Account{ID: "acc1", Email: "a@b.c", Created: now, LastSeen: now, FreeUntil: now, Lang: "en"}
	any12 := make([]driver.Value, 12)
	for i := range any12 {
		any12[i] = sqlmock.AnyArg()
	}

	mock.ExpectExec("insert into `accounts` \\(.*`terms_accepted_at`, `lang`\\) values").
		WithArgs(append(append([]driver.Value{}, any12...), "en")...).WillReturnResult(sqlmock.NewResult(1, 1))
	if err := d.CreateAccount(&acc); err != nil || acc.Lang != "en" {
		t.Fatal(acc.Lang, err)
	}

	mock.ExpectExec("insert into `accounts` \\(.*`terms_accepted_at`, `lang`\\) values").WillReturnError(unknownColumn("lang"))
	mock.ExpectExec("insert into `accounts` \\(.*`terms_accepted_at`\\) values").WithArgs(any12...).WillReturnResult(sqlmock.NewResult(1, 1))
	d2 := NewWithDB(db)
	if err := d2.CreateAccount(&acc); err != nil {
		t.Fatalf("without the column: %v", err)
	}
	if acc.Lang != "" {
		t.Error("an account created without its language still says it has one")
	}

	mock.ExpectExec("insert into `accounts` \\(.*`terms_accepted_at`\\) values").WithArgs(any12...).WillReturnResult(sqlmock.NewResult(1, 1))
	if err := NewWithDB(db).CreateAccount(&acc); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// The owner's change: refused, not dropped, when there is no column.
func TestSetAccountLang(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)

	mock.ExpectExec("update `accounts` set `lang` = \\? where `id` = \\?").WithArgs("en", "acc1").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := d.SetAccountLang("acc1", "en"); err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec("update `accounts` set `lang`").WillReturnError(unknownColumn("lang"))
	if err := d.SetAccountLang("acc1", ""); err != ErrNoLanguageColumn {
		t.Fatalf("without the column: %v", err)
	}
	// Known missing: nothing is sent.
	if err := d.SetAccountLang("acc1", "en"); err != ErrNoLanguageColumn {
		t.Fatalf("known missing: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A device's push registrations are stored whole on a database without
// push_registrations.language, in the same transaction.
func TestSetPushRegistrationsWithoutTheLanguageColumn(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)

	expect := func(tryLang bool) {
		mock.ExpectBegin()
		if tryLang {
			mock.ExpectExec("insert into `push_registrations` \\(`domain`, `vapid_public_key`, `vapid_private_key`, `language`\\)").
				WithArgs("pit.otc", "pub", "priv", "en").WillReturnError(unknownColumn("language"))
		}
		mock.ExpectExec("insert into `push_registrations` \\(`domain`, `vapid_public_key`, `vapid_private_key`\\) values \\(\\?, \\?, \\?\\) ").
			WithArgs("pit.otc", "pub", "priv").WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectExec("delete from `push_apns_tokens`").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec("insert into `push_apns_tokens`").WithArgs("pit.otc", "tok").WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectExec("delete from `push_fcm_tokens`").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec("delete from `push_web_subs`").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectCommit()
	}
	expect(true)
	if err := d.SetPushRegistrations("pit.otc", "pub", "priv", "en", []string{"tok"}, nil, nil); err != nil {
		t.Fatalf("without the column: %v", err)
	}
	// Known missing: the statement without it, straight away.
	expect(false)
	if err := d.SetPushRegistrations("pit.otc", "pub", "priv", "en", []string{"tok"}, nil, nil); err != nil {
		t.Fatalf("known missing: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPushLanguageForDomain(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)

	q := "select `language` from `push_registrations` where `domain` = \\?"
	mock.ExpectQuery(q).WithArgs("pit.otc").WillReturnRows(sqlmock.NewRows([]string{"language"}).AddRow("en"))
	mock.ExpectQuery(q).WithArgs("new.otc").WillReturnRows(sqlmock.NewRows([]string{"language"}))
	mock.ExpectQuery(q).WithArgs("pit.otc").WillReturnError(errors.New("connection refused"))
	mock.ExpectQuery(q).WithArgs("pit.otc").WillReturnError(unknownColumn("language"))
	for _, c := range []struct {
		domain, want string
		fails        bool
	}{
		{"pit.otc", "en", false},
		{"new.otc", "", false}, // no registrations yet
		{"pit.otc", "", true},
		{"pit.otc", "", false}, // no column: not known
		{"pit.otc", "", false}, // known missing: no query
	} {
		got, err := d.PushLanguageForDomain(c.domain)
		if got != c.want || (err != nil) != c.fails {
			t.Errorf("%s: %q %v", c.domain, got, err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// The inactivity pass (whose emails will be in the account's language)
// goes on without the column too.
func TestInactiveAccountsLang(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)

	mock.ExpectQuery("select a.`id`, a.`email`, a.`name`, a.`inactivity_warned_at`, a.`lang` from `accounts` a").
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "name", "inactivity_warned_at", "lang"}).AddRow("acc1", "a@b.c", "A", nil, "en"))
	if out, err := d.InactiveAccounts(time.Now()); err != nil || len(out) != 1 || out[0].Lang != "en" {
		t.Fatalf("with the column: %+v %v", out, err)
	}
	mock.ExpectQuery("a.`lang` from `accounts` a").WillReturnError(unknownColumn("a.lang"))
	mock.ExpectQuery("select a.`id`, a.`email`, a.`name`, a.`inactivity_warned_at` from `accounts` a").
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "name", "inactivity_warned_at"}).AddRow("acc1", "a@b.c", "A", nil))
	if out, err := d.InactiveAccounts(time.Now()); err != nil || len(out) != 1 || out[0].ID != "acc1" || out[0].Lang != "" {
		t.Fatalf("without the column: %+v %v", out, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
