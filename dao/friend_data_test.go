// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// Issue #174: an event for one friend is served to that friend only.
func TestGetEventsServesTargetedEventsToTheirTargetOnly(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	since := time.Unix(0, 0)
	mock.ExpectQuery("from `events` where `dt` > \\? and \\(`target` is null or `target` = \\?\\) order by `dt` asc limit \\?").
		WithArgs(since, "x.off-the.cloud", int32(10)).
		WillReturnRows(sqlmock.NewRows([]string{"uuid", "dt", "type", "content"}))
	if _, err := NewWithDB(db).GetEvents(since, 10, "x.off-the.cloud"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// Issue #174: purging a friend touches rows of that domain only, in one
// transaction.
func TestPurgeFriendActivityOnlyThatDomain(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	for i := 0; i < 7; i++ {
		mock.ExpectExec(".").WithArgs("x.off-the.cloud").WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()
	if err := NewWithDB(db).PurgeFriendActivity("x.off-the.cloud"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestFriendshipAccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	mock.ExpectQuery("select `status`, `forget_requested` is not null from `social_friendship`").
		WillReturnRows(sqlmock.NewRows([]string{"status", "leaving"}).AddRow("accepted", true))
	if ok, leaving, err := d.FriendshipAccess("x"); err != nil || !ok || !leaving {
		t.Errorf("accepted+leaving: %v %v %v", ok, leaving, err)
	}
	mock.ExpectQuery("select `status`").WillReturnRows(sqlmock.NewRows([]string{"status", "leaving"}))
	if ok, _, err := d.FriendshipAccess("gone"); err != nil || ok {
		t.Errorf("unknown friend allowed: %v %v", ok, err)
	}
}
