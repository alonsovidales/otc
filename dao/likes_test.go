// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

const likeInsert = "insert into `social_publication_likes` \\(`uuid`, `pub_uuid`, `dt`, `friend_domain`\\) select \\?, \\?, \\?, \\? from dual " +
	"where not exists \\(select 1 from `social_publication_likes` where `pub_uuid` = \\? and `friend_domain` = \\?\\)"

// A like is stored and counted once per domain: a second one (already
// liked, or a replayed event hitting a unique key) changes nothing and
// reports nothing new. Any other failure is still an error.
func TestNewLikePublicationOncePerDomain(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	dt := time.Unix(1700000000, 0)

	mock.ExpectBegin()
	mock.ExpectExec(likeInsert).WithArgs("l1", "p1", dt, "x", "p1", "x").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("update `social_publications` set `likes` = `likes` \\+ 1 where `uuid` = \\?").WithArgs("p1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if inserted, err := d.NewLikePublication("l1", "p1", "x", dt); err != nil || !inserted {
		t.Fatalf("first like: %v, %v", inserted, err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(likeInsert).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	if inserted, err := d.NewLikePublication("l2", "p1", "x", dt); err != nil || inserted {
		t.Fatalf("already liked: %v, %v", inserted, err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(likeInsert).WillReturnError(&mysql.MySQLError{Number: 1062})
	mock.ExpectRollback()
	if inserted, err := d.NewLikePublication("l1", "p1", "x", dt); err != nil || inserted {
		t.Fatalf("replayed like: %v, %v", inserted, err)
	}

	fk := &mysql.MySQLError{Number: 1452}
	mock.ExpectBegin()
	mock.ExpectExec(likeInsert).WillReturnError(fk)
	mock.ExpectRollback()
	if inserted, err := d.NewLikePublication("l3", "gone", "x", dt); !errors.Is(err, fk) || inserted {
		t.Fatalf("a missing post must still fail: %v, %v", inserted, err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// Removing a domain's like takes the counter down by every row removed
// (a domain counted twice before like_once), never below zero.
func TestDeleteLikesDecrementByRowsRemoved(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)

	mock.ExpectExec("delete from `social_publication_likes` where `pub_uuid` = \\? and `friend_domain` = \\?").
		WithArgs("p1", "x").WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec("update `social_publications` set `likes` = greatest\\(`likes` - \\?, 0\\) where `uuid` = \\?").
		WithArgs(int64(2), "p1").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := d.DeleteLikePublication("p1", "x"); err != nil {
		t.Fatal(err)
	}

	mock.ExpectExec("delete from `social_publication_comment_likes` where `comment_uuid` = \\? and `friend_domain` = \\?").
		WithArgs("c1", "x").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("update `social_publications_comments` set `likes` = greatest\\(`likes` - \\?, 0\\) where `uuid` = \\?").
		WithArgs(int64(1), "c1").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := d.DeleteLikePublicationComment("c1", "x"); err != nil {
		t.Fatal(err)
	}

	// Nothing to remove: the counter isn't touched.
	mock.ExpectExec("delete from `social_publication_likes`").WillReturnResult(sqlmock.NewResult(0, 0))
	if err := d.DeleteLikePublication("p2", "x"); err != nil {
		t.Fatal(err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
