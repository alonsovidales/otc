// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// A stored contact message's id names it in the email to the project and
// in the shell's commands; a driver that can't say leaves 0, the message
// stored all the same.
func TestNewContactRequestReturnsItsID(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	mock.ExpectExec("insert into `contact_requests`").WithArgs("A", "a@b.example", "help", "hi").WillReturnResult(sqlmock.NewResult(42, 1))
	mock.ExpectExec("insert into `contact_requests`").WillReturnResult(sqlmock.NewErrorResult(errors.New("no id")))
	mock.ExpectExec("insert into `contact_requests`").WillReturnError(errors.New("driver: bad connection"))
	if id, err := d.NewContactRequest("A", "a@b.example", "help", "hi"); id != 42 || err != nil {
		t.Errorf("%d %v", id, err)
	}
	if id, err := d.NewContactRequest("A", "a@b.example", "help", "hi"); id != 0 || err != nil {
		t.Errorf("without an id: %d %v", id, err)
	}
	if _, err := d.NewContactRequest("A", "a@b.example", "help", "hi"); err == nil {
		t.Error("a failed insert")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestContactRequestByID(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	at := time.Date(2026, 11, 13, 18, 4, 5, 0, time.UTC)
	cols := []string{"id", "name", "email", "reason", "message", "created", "is_read"}
	mock.ExpectQuery("from `contact_requests` where `id` = \\?").WithArgs(5).
		WillReturnRows(sqlmock.NewRows(cols).AddRow(5, "A", "a@b.example", "help", "hi", at, true))
	mock.ExpectQuery("from `contact_requests` where `id` = \\?").WithArgs(6).WillReturnRows(sqlmock.NewRows(cols))
	mock.ExpectQuery("from `contact_requests` where `id` = \\?").WithArgs(7).WillReturnError(errors.New("driver: bad connection"))
	c, err := d.ContactRequest(5)
	if err != nil || c == nil || *c != (ContactRequest{Id: 5, Name: "A", Email: "a@b.example", Reason: "help", Message: "hi", Created: at, IsRead: true}) {
		t.Errorf("%+v %v", c, err)
	}
	if c, err := d.ContactRequest(6); c != nil || err != nil {
		t.Errorf("an unknown id: %+v %v", c, err)
	}
	if c, err := d.ContactRequest(7); c != nil || err == nil {
		t.Errorf("a failed read: %+v %v", c, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
