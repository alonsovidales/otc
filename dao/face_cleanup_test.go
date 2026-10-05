// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// Content with no faces costs one query and no transaction.
func TestDelFacesByHashWithNoFaces(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("select distinct `person_id` from `faces` where `hash` = \\?").WithArgs("h1").
		WillReturnRows(sqlmock.NewRows([]string{"person_id"}))
	n, err := NewWithDB(db).DelFacesByHash("h1")
	if err != nil || n != 0 {
		t.Errorf("DelFacesByHash = %d, %v", n, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// The faces go, covers pointing at them are cleared, and only the
// unnamed people they touched who have no face left are deleted.
func TestDelFacesByHashClearsCoversAndEmptyUnnamedPeople(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("select distinct `person_id` from `faces` where `hash` = \\?").WithArgs("h1").
		WillReturnRows(sqlmock.NewRows([]string{"person_id"}).AddRow("p1").AddRow("p2"))
	mock.ExpectBegin()
	mock.ExpectExec("update `people` set `cover_face_id` = null where `cover_face_id` in \\(select `id` from `faces` where `hash` = \\?\\)").
		WithArgs("h1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("delete from `faces` where `hash` = \\?").WithArgs("h1").
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec("delete from `people` where `id` in \\(\\?,\\?\\) and `name` = '' and not exists \\(select 1 from `faces` where `faces`.`person_id` = `people`.`id`\\)").
		WithArgs("p1", "p2").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	n, err := NewWithDB(db).DelFacesByHash("h1")
	if err != nil || n != 3 {
		t.Errorf("DelFacesByHash = %d, %v, want 3 faces", n, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestOrphanFaceHashes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("select distinct `hash` from `faces` as `fc` where not exists \\(select 1 from `files` .*\\) and not exists \\(select 1 from `file_versions` .*\\)").
		WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow("h1").AddRow("h2"))
	hashes, err := NewWithDB(db).OrphanFaceHashes()
	if err != nil || len(hashes) != 2 || hashes[0] != "h1" {
		t.Errorf("OrphanFaceHashes = %v, %v", hashes, err)
	}
}
