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
	mock.ExpectQuery("from `events` where `dt` > \\? and `dt` < now\\(\\) - interval 1 second and \\(`target` is null or `target` = \\?\\) order by `dt` asc limit \\?").
		WithArgs(since, "x.off-the.cloud", int32(10)).
		WillReturnRows(sqlmock.NewRows([]string{"uuid", "dt", "type", "content"}))
	if _, err := NewWithDB(db).GetEvents(since, 10, "x.off-the.cloud"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A full page never ends part-way through a second: the rest of its last
// second's events come with it (once each), so the requester's next
// "dt > cursor" skips nothing. A short page has nothing more to add.
func TestGetEventsCompletesThePageLastSecond(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	t0 := time.Date(2026, 10, 5, 12, 5, 2, 0, time.UTC)
	t1 := t0.Add(time.Second)
	cols := []string{"uuid", "dt", "type", "content"}
	mock.ExpectQuery("from `events` where `dt` > \\?").WithArgs(t0.Add(-time.Hour), "x", int32(3)).
		WillReturnRows(sqlmock.NewRows(cols).AddRow("a", t0, "comment", "{}").AddRow("b", t1, "like_event", "{}").AddRow("c", t1, "like_event", "{}"))
	mock.ExpectQuery("from `events` where `dt` = \\? and \\(`target` is null or `target` = \\?\\)$").WithArgs(t1, "x").
		WillReturnRows(sqlmock.NewRows(cols).AddRow("b", t1, "like_event", "{}").AddRow("c", t1, "like_event", "{}").AddRow("d", t1, "like_event", "{}"))
	events, err := NewWithDB(db).GetEvents(t0.Add(-time.Hour), 3, "x")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range events {
		got = append(got, e.Uuid)
	}
	if len(got) != 4 || got[0] != "a" || got[3] != "d" {
		t.Fatalf("got %v, want a b c d", got)
	}

	mock.ExpectQuery("from `events` where `dt` > \\?").
		WillReturnRows(sqlmock.NewRows(cols).AddRow("a", t0, "comment", "{}"))
	if events, err := NewWithDB(db).GetEvents(t0.Add(-time.Hour), 3, "x"); err != nil || len(events) != 1 {
		t.Fatalf("short page: %v, %v", events, err)
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
