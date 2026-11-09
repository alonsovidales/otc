// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"reflect"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

// Content with no faces, and no one added to it by hand, costs one query
// and no transaction.
func TestDelFacesByHashWithNoFaces(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("select `id`, `person_id` from `faces` where `hash` = \\? union all select '', `b`.`person_id` from `people_by_hand` as `b` where `b`.`hash` = \\?").WithArgs("h1", "h1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "person_id"}))
	gone, err := NewWithDB(db).DelFacesByHash("h1")
	if err != nil || len(gone) != 0 {
		t.Errorf("DelFacesByHash = %v, %v", gone, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// The faces go, and the people added to the content by hand; covers
// pointing at the faces are cleared, only the unnamed people they touched
// who have no face left (nor are added by hand anywhere) are deleted, and
// the faces that went come back with their people.
func TestDelFacesByHashClearsCoversAndEmptyUnnamedPeople(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("select `id`, `person_id` from `faces` where `hash` = \\?").WithArgs("h1", "h1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "person_id"}).AddRow("f1", "p1").AddRow("f2", "p2").AddRow("f3", "p1").AddRow("", "p3"))
	mock.ExpectBegin()
	mock.ExpectExec("update `people` set `cover_face_id` = null, `cover_chosen` = \\(`cover_image` is not null\\) where `cover_face_id` in \\(select `id` from `faces` where `hash` = \\?\\)").
		WithArgs("h1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("delete from `faces` where `hash` = \\?").WithArgs("h1").
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec("delete from `people_by_hand` where `hash` = \\?").WithArgs("h1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("delete from `people` where `id` in \\(\\?,\\?,\\?\\) and `name` = '' and not exists \\(select 1 from `faces` where `faces`.`person_id` = `people`.`id`\\)"+
		" and not exists \\(select 1 from `people_by_hand` as `bh` where `bh`.`person_id` = `people`.`id`\\)").
		WithArgs("p1", "p2", "p3").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	gone, err := NewWithDB(db).DelFacesByHash("h1")
	want := []DeletedFace{{"f1", "p1"}, {"f2", "p2"}, {"f3", "p1"}}
	if err != nil || !reflect.DeepEqual(gone, want) {
		t.Errorf("DelFacesByHash = %v, %v, want %v", gone, err, want)
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
	// The faces' contents and those people were added to by hand.
	mock.ExpectQuery("select `hash` from `faces` as `fc` where not exists \\(select 1 from `files` .*\\) and not exists \\(select 1 from `file_versions` .*\\)" +
		" union select `hash` from `people_by_hand` as `fc` where not exists \\(select 1 from `files` .*\\)").
		WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow("h1").AddRow("h2"))
	hashes, err := NewWithDB(db).OrphanFaceHashes()
	if err != nil || len(hashes) != 2 || hashes[0] != "h1" {
		t.Errorf("OrphanFaceHashes = %v, %v", hashes, err)
	}

	// A database without people_by_hand, nor edits: the faces alone.
	noEdits := &mysql.MySQLError{Number: 1146, Message: "Table 'otc.edited_content' doesn't exist"}
	mock.ExpectQuery("union select `hash` from `people_by_hand`").WillReturnError(noByHandTable)
	mock.ExpectQuery("union select `hash` from \\(select `hash`, `person_id`, `created` from `faces` where 0 = 1\\) as `fc`.*`edited_content`").WillReturnError(noEdits)
	mock.ExpectQuery("union select `hash` from `people_by_hand`").WillReturnError(noByHandTable)
	mock.ExpectQuery("union select `hash` from \\(select `hash`, `person_id`, `created` from `faces` where 0 = 1\\) as `fc` where not exists \\(select 1 from `files` .*`file_versions`.*`fc`.`hash`\\)$").
		WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow("h3"))
	if hashes, err := NewWithDB(db).OrphanFaceHashes(); err != nil || len(hashes) != 1 || hashes[0] != "h3" {
		t.Errorf("without the tables: %v, %v", hashes, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
