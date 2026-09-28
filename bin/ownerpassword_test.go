// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/alonsovidales/otc/dao"
)

func withStdin(t *testing.T, s string) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.WriteString(s)
	w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old })
}

// The wizard's password is stored on a new device; a device that already
// has one (recovered) keeps it; a short one is refused before the database.
func TestInitOwnerPassword(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := dao.NewWithDB(db)

	mock.ExpectQuery("select count\\(\\*\\) from `vault`").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	// session.New checks again itself before storing.
	mock.ExpectQuery("select count\\(\\*\\) from `vault`").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectExec("insert into `vault`").WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	withStdin(t, "correct horse battery\n")
	if code := initOwnerPassword(d); code != 0 {
		t.Fatalf("new device: exit %d", code)
	}

	mock.ExpectQuery("select count\\(\\*\\) from `vault`").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	withStdin(t, "another password\n")
	if code := initOwnerPassword(d); code != 0 {
		t.Fatalf("recovered device: exit %d", code)
	}

	withStdin(t, "short\n")
	if code := initOwnerPassword(d); code != 2 {
		t.Fatalf("short password: exit %d", code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
