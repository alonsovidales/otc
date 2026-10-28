// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// "Not this person": the faces of the person in the photos named go to no
// one (""), in one transaction - a cover among them is cleared, and the
// person, left with none, goes if unnamed.
func TestRemoveFromPersonGivesTheFacesToNoOne(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("select `id` from `faces` where `person_id` = \\? and `hash` in \\(\\?,\\?\\) for update").
		WithArgs("p1", "A", "B").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("f1").AddRow("f2"))
	mock.ExpectExec("update `faces` set `person_id` = \\? where `id` in \\(\\?,\\?\\)").
		WithArgs("", "f1", "f2").WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec("update `people` set `cover_face_id` = null where `cover_face_id` in \\(\\?,\\?\\)").
		WithArgs("f1", "f2").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("delete from `people` where `id` = \\? and `name` = '' and not exists \\(select 1 from `faces` where `faces`.`person_id` = `people`.`id`\\)").
		WithArgs("p1").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	// A hash named twice is asked for once.
	moved, err := NewWithDB(db).RemoveFromPerson("p1", []string{"A", "B", "A"}, "")
	if err != nil || moved != 2 {
		t.Fatalf("RemoveFromPerson = %d, %v; want 2 faces moved", moved, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// "Someone else…": the faces go to the person picked, who is locked first.
func TestRemoveFromPersonMovesTheFacesToAnotherPerson(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("select 1 from `people` where `id` = \\? for update").WithArgs("p2").
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery("select `id` from `faces` where `person_id` = \\? and `hash` in \\(\\?\\) for update").
		WithArgs("p1", "A").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("f1"))
	mock.ExpectExec("update `faces` set `person_id` = \\? where `id` in \\(\\?\\)").
		WithArgs("p2", "f1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("update `people` set `cover_face_id` = null where `cover_face_id` in \\(\\?\\)").
		WithArgs("f1").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("delete from `people` where `id` = \\?").WithArgs("p1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if moved, err := NewWithDB(db).RemoveFromPerson("p1", []string{"A"}, "p2"); err != nil || moved != 1 {
		t.Fatalf("RemoveFromPerson = %d, %v; want 1 face moved", moved, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A person to move to who is gone (deleted or merged meanwhile) moves
// nothing and says so; photos with no face of the person change nothing.
func TestRemoveFromPersonChangesNothingWhenThereIsNothingToDo(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)

	mock.ExpectBegin()
	mock.ExpectQuery("select 1 from `people` where `id` = \\? for update").WithArgs("gone").
		WillReturnRows(sqlmock.NewRows([]string{"1"}))
	mock.ExpectRollback()
	if moved, err := d.RemoveFromPerson("p1", []string{"A"}, "gone"); !errors.Is(err, ErrNoSuchPerson) || moved != 0 {
		t.Errorf("to a person who is gone: %d, %v; want ErrNoSuchPerson", moved, err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery("select `id` from `faces` where `person_id` = \\?").WithArgs("p1", "Z").
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectRollback()
	if moved, err := d.RemoveFromPerson("p1", []string{"Z"}, ""); err != nil || moved != 0 {
		t.Errorf("photos without a face of the person: %d, %v; want 0 and no error", moved, err)
	}

	// No one, to themselves, or no photos: not even a transaction.
	for _, c := range []struct {
		person, to string
		hashes     []string
	}{{"", "p2", []string{"A"}}, {"p1", "p1", []string{"A"}}, {"p1", "", nil}} {
		if moved, err := d.RemoveFromPerson(c.person, c.hashes, c.to); err != nil || moved != 0 {
			t.Errorf("%+v: %d, %v", c, moved, err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// The empty person id is no one: deleting it or merging it into someone
// would delete those faces (and the photos would be analysed again) or
// hand them back to a person.
func TestNoOneIsNeverDeletedOrMerged(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)

	if err := d.DeletePerson(""); err != nil {
		t.Errorf("DeletePerson(\"\"): %v", err)
	}
	if err := d.MergePeople("", []string{"p1"}); err != nil {
		t.Errorf("MergePeople into no one: %v", err)
	}
	if err := d.MergePeople("p1", []string{""}); err != nil {
		t.Errorf("MergePeople of no one: %v", err)
	}
	if faces, err := d.ListPersonFaceEmbeddings(""); err != nil || faces != nil {
		t.Errorf("ListPersonFaceEmbeddings(\"\") = %v, %v", faces, err)
	}
	// None of it reached the database.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}

	mock.ExpectBegin()
	mock.ExpectExec("update `faces` set `person_id` = \\? where `person_id` in \\(\\?\\)").
		WithArgs("p1", "p2").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("delete from `people` where `id` in \\(\\?\\)").WithArgs("p2").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := d.MergePeople("p1", []string{"", "p2"}); err != nil {
		t.Errorf("MergePeople: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// Face matching never reads the faces of no one.
func TestListFaceEmbeddingsLeavesOutNoOne(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("select `id`, `person_id`, `embedding` from `faces` where `person_id` <> ''$").
		WillReturnRows(sqlmock.NewRows([]string{"id", "person_id", "embedding"}))
	if _, err := NewWithDB(db).ListFaceEmbeddings(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
