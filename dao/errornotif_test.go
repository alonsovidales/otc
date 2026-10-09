// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

const errGroupSelect = "select `uuid`, coalesce\\(length\\(`details`\\), 0\\) from `notifications` where `type` = 'Error'"

// An error joins the open group: its line is appended while the details
// have room, and once they don't it still counts (and makes the row
// unread again) instead of failing on a TEXT that is full.
func TestAddErrorNotificationGroupsAndCapsDetails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)

	mock.ExpectBegin()
	mock.ExpectQuery(errGroupSelect).WillReturnRows(sqlmock.NewRows([]string{"uuid", "n"}).AddRow("g1", 100))
	mock.ExpectExec("update `notifications` set `details` = concat").
		WithArgs(sqlmock.AnyArg(), "g1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := d.AddErrorNotification("upload failed", "disk full"); err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery(errGroupSelect).WillReturnRows(sqlmock.NewRows([]string{"uuid", "n"}).AddRow("g1", errorNotificationDetailsCap-10))
	mock.ExpectExec("update `notifications` set `occurrences` = `occurrences` \\+ 1, `acknowledged` = 0 where `uuid` = \\?").
		WithArgs("g1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := d.AddErrorNotification("upload failed", "disk full"); err != nil {
		t.Fatal(err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// The first error of a window starts a row, its line cut to what the
// column holds.
func TestAddErrorNotificationStartsAGroup(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(errGroupSelect).WillReturnRows(sqlmock.NewRows([]string{"uuid", "n"}))
	mock.ExpectExec("insert into `notifications`").
		WithArgs(sqlmock.AnyArg(), "t", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := NewWithDB(db).AddErrorNotification("t", strings.Repeat("é", errorNotificationDetailsCap)); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestTruncateUTF8(t *testing.T) {
	if got := truncateUTF8("aé", 2); got != "a" {
		t.Errorf("cut inside a character: %q", got)
	}
	if got := truncateUTF8("abc", 5); got != "abc" {
		t.Errorf("a short string changed: %q", got)
	}
}

// A recurring story is one row: started when there is none (or it was
// deleted), then updated in place - title, a line, one more occurrence.
func TestUpsertErrorNotification(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)

	mock.ExpectExec("insert into `notifications`").
		WithArgs(sqlmock.AnyArg(), cStandaloneError, "ran out of memory", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	id, err := d.UpsertErrorNotification("", "ran out of memory", "a hint")
	if err != nil || id == "" {
		t.Fatalf("first: %q, %v", id, err)
	}

	mock.ExpectExec("update `notifications` set `title` = \\?").
		WithArgs("ran out of memory 2 times", sqlmock.AnyArg(), errorNotificationDetailsCap, sqlmock.AnyArg(), id).
		WillReturnResult(sqlmock.NewResult(0, 1))
	got, err := d.UpsertErrorNotification(id, "ran out of memory 2 times", "")
	if err != nil || got != id {
		t.Fatalf("update: %q, %v", got, err)
	}

	// The owner deleted the row: a new one starts.
	mock.ExpectExec("update `notifications` set `title` = \\?").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("insert into `notifications`").WillReturnResult(sqlmock.NewResult(1, 1))
	got, err = d.UpsertErrorNotification(id, "ran out of memory 3 times", "")
	if err != nil || got == "" || got == id {
		t.Fatalf("after a delete: %q, %v", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// Grouping joins only grouped rows: a standalone one (the memory Alert, a
// file set aside) is never the open group.
func TestAddErrorNotificationSkipsStandaloneRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery(errGroupSelect + " and `actor_domain` = '' and `dt` >= now\\(\\) - interval").WillReturnRows(sqlmock.NewRows([]string{"uuid", "n"}))
	mock.ExpectExec("insert into `notifications`").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	if err := NewWithDB(db).AddErrorNotification("x could not be processed", "bad"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
