// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

func unknownColumn(col string) error {
	return &mysql.MySQLError{Number: 1054, Message: "Unknown column '" + col + "' in 'field list'"}
}

// The language columns read and write as they are, and a database release
// 118's script hasn't reached answers ErrSchemaBehind - never another
// error a caller would fail on.
func TestLanguageSettingsAndAnOlderSchema(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)

	mock.ExpectQuery("select `language`, `last_ui_language` from `settings`").
		WillReturnRows(sqlmock.NewRows([]string{"language", "last_ui_language"}).AddRow("es", "en"))
	if l, ui, err := d.GetLanguageSettings(); err != nil || l != "es" || ui != "en" {
		t.Errorf("got %q %q %v", l, ui, err)
	}
	mock.ExpectExec("update `settings` set `language` = \\?").WithArgs("fr").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := d.SetLanguage("fr"); err != nil {
		t.Error(err)
	}
	mock.ExpectExec("update `settings` set `last_ui_language` = \\?").WithArgs("en").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := d.SetLastUILanguage("en"); err != nil {
		t.Error(err)
	}

	mock.ExpectQuery("select `language`, `last_ui_language` from `settings`").WillReturnError(unknownColumn("language"))
	if l, ui, err := d.GetLanguageSettings(); !errors.Is(err, ErrSchemaBehind) || l != "" || ui != "" {
		t.Errorf("older schema: got %q %q %v", l, ui, err)
	}
	mock.ExpectExec("update `settings` set `language`").WillReturnError(unknownColumn("language"))
	if err := d.SetLanguage("fr"); !errors.Is(err, ErrSchemaBehind) {
		t.Errorf("older schema, SetLanguage: %v", err)
	}
	mock.ExpectExec("update `settings` set `last_ui_language`").WillReturnError(unknownColumn("last_ui_language"))
	if err := d.SetLastUILanguage("en"); !errors.Is(err, ErrSchemaBehind) {
		t.Errorf("older schema, SetLastUILanguage: %v", err)
	}

	// Any other failure is passed on as it is.
	gone := errors.New("connection lost")
	mock.ExpectExec("update `settings` set `language`").WillReturnError(gone)
	if err := d.SetLanguage("fr"); !errors.Is(err, gone) || errors.Is(err, ErrSchemaBehind) {
		t.Errorf("another error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

const updateCount = "select count\\(\\*\\) from `notifications` where `type` = 'Update' and \\(`update_version` = \\? or `title` in \\(\\?, \\?\\)\\)"

// An update alert is added once per version: a row with its
// update_version, or a row from before release 118 with either English
// title naming the version, keeps it from being added again.
func TestAddUpdateNotificationOncePerVersion(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	legacy := []string{"Update 3.0 is available", "Critical update 3.0 - please install it soon"}

	mock.ExpectQuery(updateCount).WithArgs("3.0", legacy[0], legacy[1]).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectExec("insert into `notifications` \\(`uuid`, `dt`, `type`, `actor_name`, `actor_domain`, `title`, `details`, `occurrences`, `update_version`\\) values \\(\\?, now\\(\\), 'Update', 'This device', '', \\?, \\?, 1, \\?\\)").
		WithArgs(sqlmock.AnyArg(), legacy[1], "Fixes. Install it from Settings.", "3.0").
		WillReturnResult(sqlmock.NewResult(0, 1))
	added, err := d.AddUpdateNotification("3.0", legacy[1], "Fixes. Install it from Settings.", legacy)
	if err != nil || !added {
		t.Fatalf("first time: added %v, %v", added, err)
	}

	// Already there, by version or by a legacy title (the count can't tell
	// them apart: either keeps it out).
	mock.ExpectQuery(updateCount).WithArgs("3.0", legacy[0], legacy[1]).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	added, err = d.AddUpdateNotification("3.0", legacy[1], "Fixes. Install it from Settings.", legacy)
	if err != nil || added {
		t.Errorf("again: added %v, %v", added, err)
	}

	// A version too long for the column is cut the same way for the
	// compare and the insert.
	long := "1.2345678901234567890"
	mock.ExpectQuery(updateCount).WithArgs(long[:16], "a", "b").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectExec("insert into `notifications`").WithArgs(sqlmock.AnyArg(), "a", "d", long[:16]).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if added, err := d.AddUpdateNotification(long, "a", "d", []string{"a", "b"}); err != nil || !added {
		t.Errorf("long version: added %v, %v", added, err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// On a database without update_version it de-duplicates by the titles and
// adds the row without it, as before release 118 - never a lost alert.
func TestAddUpdateNotificationOnAnOlderSchema(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	legacy := []string{"Update 3.0 is available", "Critical update 3.0 - please install it soon"}
	legacyInsert := "insert into `notifications` \\(`uuid`, `dt`, `type`, `actor_name`, `actor_domain`, `title`, `details`, `occurrences`\\) values"

	mock.ExpectQuery(updateCount).WillReturnError(unknownColumn("update_version"))
	mock.ExpectQuery("select count\\(\\*\\) from `notifications` where `type` = 'Update' and `title` in \\(\\?, \\?\\)").
		WithArgs(legacy[0], legacy[1]).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectExec(legacyInsert).WithArgs(sqlmock.AnyArg(), legacy[0], "d").WillReturnResult(sqlmock.NewResult(0, 1))
	if added, err := d.AddUpdateNotification("3.0", legacy[0], "d", legacy); err != nil || !added {
		t.Errorf("older schema: added %v, %v", added, err)
	}

	mock.ExpectQuery(updateCount).WillReturnError(unknownColumn("update_version"))
	mock.ExpectQuery("select count\\(\\*\\) from `notifications` where `type` = 'Update' and `title` in").
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	if added, err := d.AddUpdateNotification("3.0", legacy[0], "d", legacy); err != nil || added {
		t.Errorf("older schema, again: added %v, %v", added, err)
	}

	// The column gone between the two statements: the insert is retried
	// without it.
	mock.ExpectQuery(updateCount).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectExec("insert into `notifications`.*`update_version`").WillReturnError(unknownColumn("update_version"))
	mock.ExpectExec(legacyInsert).WithArgs(sqlmock.AnyArg(), legacy[0], "d").WillReturnResult(sqlmock.NewResult(0, 1))
	if added, err := d.AddUpdateNotification("3.0", legacy[0], "d", legacy); err != nil || !added {
		t.Errorf("retried without the column: added %v, %v", added, err)
	}

	// Without legacy titles, its own title stands for them.
	mock.ExpectQuery("select count\\(\\*\\) from `notifications` where `type` = 'Update' and \\(`update_version` = \\? or `title` in \\(\\?\\)\\)").
		WithArgs("3.0", "t").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	if added, err := d.AddUpdateNotification("3.0", "t", "d", nil); err != nil || added {
		t.Errorf("no legacy titles: added %v, %v", added, err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
