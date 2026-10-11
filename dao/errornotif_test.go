// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/alonsovidales/otc/alerttext"
)

// standaloneSelect is UpsertErrorNotification's look at its row.
const standaloneSelect = "select coalesce\\(length\\(`details`\\), 0\\), `msg_key`, `msg_lines` from `notifications` where `uuid` = \\?"

const errGroupSelect = "select `uuid`, coalesce\\(length\\(`details`\\), 0\\), `msg_key`, `msg_lines` from `notifications` where `type` = 'Error'"

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
	mock.ExpectQuery(errGroupSelect).WillReturnRows(sqlmock.NewRows([]string{"uuid", "n", "msg_key", "msg_lines"}).AddRow("g1", 100, nil, nil))
	mock.ExpectExec("update `notifications` set `details` = concat").
		WithArgs(sqlmock.AnyArg(), "g1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := d.AddErrorNotification(alerttext.English("upload failed", "disk full")); err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery(errGroupSelect).WillReturnRows(sqlmock.NewRows([]string{"uuid", "n", "msg_key", "msg_lines"}).AddRow("g1", errorNotificationDetailsCap-10, nil, nil))
	mock.ExpectExec("update `notifications` set `occurrences` = `occurrences` \\+ 1, `acknowledged` = 0 where `uuid` = \\?").
		WithArgs("g1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := d.AddErrorNotification(alerttext.English("upload failed", "disk full")); err != nil {
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
	mock.ExpectQuery(errGroupSelect).WillReturnRows(sqlmock.NewRows([]string{"uuid", "n", "msg_key", "msg_lines"}))
	mock.ExpectExec("insert into `notifications`").
		WithArgs(sqlmock.AnyArg(), "", "t", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := NewWithDB(db).AddErrorNotification(alerttext.English("t", strings.Repeat("é", errorNotificationDetailsCap))); err != nil {
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
	id, err := d.UpsertErrorNotification("", alerttext.English("ran out of memory", "a hint"))
	if err != nil || id == "" {
		t.Fatalf("first: %q, %v", id, err)
	}

	mock.ExpectQuery(standaloneSelect).WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"n", "msg_key", "msg_lines"}).AddRow(30, nil, nil))
	mock.ExpectExec("update `notifications` set `title` = \\?").
		WithArgs("ran out of memory 2 times", sqlmock.AnyArg(), nil, nil, nil, id).
		WillReturnResult(sqlmock.NewResult(0, 1))
	got, err := d.UpsertErrorNotification(id, alerttext.English("ran out of memory 2 times", ""))
	if err != nil || got != id {
		t.Fatalf("update: %q, %v", got, err)
	}

	// The owner deleted the row: a new one starts.
	mock.ExpectQuery(standaloneSelect).WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"n", "msg_key", "msg_lines"}))
	mock.ExpectExec("insert into `notifications`").WillReturnResult(sqlmock.NewResult(1, 1))
	got, err = d.UpsertErrorNotification(id, alerttext.English("ran out of memory 3 times", ""))
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
	mock.ExpectQuery(errGroupSelect + " and `actor_domain` = '' and `dt` >= now\\(\\) - interval").WillReturnRows(sqlmock.NewRows([]string{"uuid", "n", "msg_key", "msg_lines"}))
	mock.ExpectExec("insert into `notifications`").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	if err := NewWithDB(db).AddErrorNotification(alerttext.English("x could not be processed", "bad")); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
