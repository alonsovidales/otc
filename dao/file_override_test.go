// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	pb "github.com/alonsovidales/otc/proto/generated"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func overrideFile() *pb.File {
	return &pb.File{Path: "/backup/a.txt", Hash: "new", Mime: "text/plain", Size: 3, Created: timestamppb.Now(), Modified: timestamppb.Now()}
}

// The row changes in place - its versions are never touched - and the
// replaced content's tags go when this row was their last use.
func TestOverrideFileReplacesTheRowInPlace(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery("select `hash` from `files` where `path` = \\? for update").WithArgs("/backup/a.txt").
		WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow("old"))
	mock.ExpectQuery("select count\\(\\*\\) from `files` where `hash` = \\? for update").WithArgs("old").
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	mock.ExpectQuery("select count\\(\\*\\) from `file_versions` where `hash` = \\?").WithArgs("old").
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectExec("delete from `file_tags` where `hash` = \\?").WithArgs("old").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec("update `files` set `hash` = \\?, `mime` = \\?, `size` = \\?, `created` = \\?, `modified` = \\?, `cloud_id` = \\? where `path` = \\?").
		WithArgs("new", "text/plain", int32(3), sqlmock.AnyArg(), sqlmock.AnyArg(), sql.NullString{}, "/backup/a.txt").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	old, err := NewWithDB(db).OverrideFile(overrideFile(), "")
	if err != nil || old != "old" {
		t.Fatalf("OverrideFile = %q, %v", old, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err) // a delete from file_versions would show up here
	}
}

// Content still used elsewhere keeps its tags.
func TestOverrideFileKeepsTagsOfContentStillUsed(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery("select `hash` from `files` where `path` = \\? for update").
		WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow("old"))
	mock.ExpectQuery("select count\\(\\*\\) from `files` where `hash` = \\? for update").
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	mock.ExpectQuery("select count\\(\\*\\) from `file_versions` where `hash` = \\?").
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1)) // a kept version of it
	mock.ExpectExec("update `files` set").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if old, err := NewWithDB(db).OverrideFile(overrideFile(), "cloud-1"); err != nil || old != "old" {
		t.Fatalf("OverrideFile = %q, %v", old, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// The row went meanwhile: the file is stored as new.
func TestOverrideFileInsertsWhenTheRowWentMeanwhile(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery("select `hash` from `files` where `path` = \\? for update").
		WillReturnRows(sqlmock.NewRows([]string{"hash"}))
	mock.ExpectExec("insert into `files`").WithArgs("new", "text/plain", sqlmock.AnyArg(), sqlmock.AnyArg(), "/backup/a.txt", int32(3), sql.NullString{String: "c", Valid: true}).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	if old, err := NewWithDB(db).OverrideFile(overrideFile(), "c"); err != nil || old != "" {
		t.Fatalf("OverrideFile = %q, %v", old, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
