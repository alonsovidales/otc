// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	pb "github.com/alonsovidales/otc/proto/generated"
)

func TestStatusToPb(t *testing.T) {
	d := &Dao{}

	cases := map[string]pb.FriendShipStatus{
		"pending":  pb.FriendShipStatus_Pending,
		"accepted": pb.FriendShipStatus_Accepted,
		"blocked":  pb.FriendShipStatus_Blocked,
		"unknown":  pb.FriendShipStatus_Pending, // zero value fallback
	}

	for in, want := range cases {
		if got := d.statusToPb(in); got != want {
			t.Errorf("statusToPb(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestPbToStatus(t *testing.T) {
	d := &Dao{}

	cases := map[pb.FriendShipStatus]string{
		pb.FriendShipStatus_Pending:  "pending",
		pb.FriendShipStatus_Accepted: "accepted",
		pb.FriendShipStatus_Blocked:  "blocked",
	}

	for in, want := range cases {
		if got := d.pbToStatus(in); got != want {
			t.Errorf("pbToStatus(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestStatusToPbAndPbToStatusRoundTrip(t *testing.T) {
	d := &Dao{}

	for _, s := range []string{"pending", "accepted", "blocked"} {
		if got := d.pbToStatus(d.statusToPb(s)); got != s {
			t.Errorf("round trip for %q produced %q", s, got)
		}
	}
}

// Batch-deleting several files at once now fires their DelFile calls
// concurrently (see websocket.handleConnection on the device side), so two
// deletes sharing a hash can run their ref-count check at the same time —
// without a lock on the count query, both could see "still >1 other
// reference", both skip cleaning up file_tags, and both delete their own
// files row, leaving file_tags referencing a hash no files row has left:
// exactly the "Error 1451 ... foreign key constraint fails" a real batch
// delete hit. sqlmock can't exercise real row locking (that's MySQL's job,
// not something a scripted mock does), but it does confirm the count query
// text itself asks for it, and that both ref-count branches still commit
// the right statements either way.
func TestDelFileByPathLocksRefCountQuery(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	d := NewWithDB(db)

	const hash = "abc123"

	t.Run("last reference cleans up file_tags too", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectQuery("select `hash` from `files` where `path` = \\?").
			WithArgs("/a.jpg").
			WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow(hash))
		mock.ExpectQuery("select count\\(\\*\\) from `files` where `hash` = \\? for update").
			WithArgs(hash).
			WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(1))
		mock.ExpectExec("delete from `file_tags` where `hash` = \\?").
			WithArgs(hash).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("delete from `files` where `path` = \\?").
			WithArgs("/a.jpg").
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()

		if err := d.DelFileByPath("/a.jpg"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations (a fix that drops the FOR UPDATE lock, or skips deleting file_tags, would show up here): %v", err)
		}
	})

	t.Run("another reference leaves file_tags alone", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectQuery("select `hash` from `files` where `path` = \\?").
			WithArgs("/b.jpg").
			WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow(hash))
		mock.ExpectQuery("select count\\(\\*\\) from `files` where `hash` = \\? for update").
			WithArgs(hash).
			WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(2))
		mock.ExpectExec("delete from `files` where `path` = \\?").
			WithArgs("/b.jpg").
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()

		if err := d.DelFileByPath("/b.jpg"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations (a fix that deletes file_tags even though another path still references the hash would show up here): %v", err)
		}
	})
}
