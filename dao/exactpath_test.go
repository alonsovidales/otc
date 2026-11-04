// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	pb "github.com/alonsovidales/otc/proto/generated"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// A row = finds for a name with spaces at its end (PAD SPACE) is another
// file: GetFileByPath says there is none.
func TestGetFileByPathIsExact(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	row := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).
			AddRow("abc", "text/plain", now, now, "/Docs/notes.txt", 10)
	}
	mock.ExpectQuery("from `files` where `path` = \\?").WithArgs("/Docs/notes.txt ").WillReturnRows(row())
	mock.ExpectQuery("from `files` where `path` = \\?").WithArgs("/Docs/notes.txt").WillReturnRows(row())
	d := NewWithDB(db)
	if f, err := d.GetFileByPath("/Docs/notes.txt "); !errors.Is(err, sql.ErrNoRows) || f.Hash != "" {
		t.Errorf("the name with a space found %+v, %v", f, err)
	}
	if f, err := d.GetFileByPath("/Docs/notes.txt"); err != nil || f.Hash != "abc" {
		t.Errorf("the row itself: %+v, %v", f, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A delete bound to the content it was asked for leaves a path that holds
// other content now, rolled back, and says what it holds.
func TestDelFileIfHashLeavesOtherContent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery("select `hash` from `files` where `path` = \\? and length\\(`path`\\) = length\\(\\?\\) for update").
		WithArgs("/a.jpg", "/a.jpg").WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow("new"))
	mock.ExpectRollback()
	hash, err := NewWithDB(db).DelFileIfHash("/a.jpg", "old")
	if !errors.Is(err, ErrOtherContent) || hash != "new" {
		t.Fatalf("got %q, %v", hash, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err) // a delete would show up here
	}
}

// pathTestDB is searchTestDB's temporary files table, with the other
// tables a file's delete and versions touch.
func pathTestDB(t *testing.T) *Dao {
	t.Helper()
	db := searchTestDB(t)
	for _, q := range []string{
		"create temporary table `file_versions` (`path` varchar(768) character set utf8mb4 collate utf8mb4_bin not null, " +
			"`hash` varchar(64) not null, `mime` varchar(150) not null, `size` bigint not null, `created` datetime not null, " +
			"`modified` datetime not null, `replaced` datetime not null, key (`path`), key (`hash`)) engine=InnoDB",
		"create temporary table `file_tags` (`hash` varchar(64) not null, `tag` varchar(150) not null, `score` float not null, " +
			"unique key (`hash`, `tag`)) engine=InnoDB",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return NewWithDB(db)
}

// Against MariaDB itself (OTC_TEST_MYSQL_DSN): the path columns compare
// PAD SPACE - a name with a space at its end is the same key as the one
// without - and every lookup, change and delete by path still reaches
// only the same bytes.
func TestPathsAreExactMySQL(t *testing.T) {
	d := pathTestDB(t)
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	file := func(path, hash string) *pb.File {
		f := &pb.File{Path: path, Hash: hash, Mime: "text/plain", Created: timestamppb.New(at), Modified: timestamppb.New(at)}
		SetFileSize(f, 10)
		return f
	}
	const plain, spaced = "/Docs/notes.txt", "/Docs/notes.txt "
	if dup, err := d.StoreNewFile(file(plain, "aaa"), ""); err != nil || dup {
		t.Fatalf("stored %v, %v", dup, err)
	}
	if _, err := d.db.Exec("insert into `file_versions` (`path`, `hash`, `mime`, `size`, `created`, `modified`, `replaced`) values (?, 'v1', 'text/plain', 5, ?, ?, ?)", plain, at, at, at); err != nil {
		t.Fatal(err)
	}
	// The unique key takes the two for one path: the second is refused.
	if dup, err := d.StoreNewFile(file(spaced, "bbb"), ""); err != nil || !dup {
		t.Fatalf("the name with a space at its end: duplicated %v, %v", dup, err)
	}
	if _, err := d.GetFileByPath(spaced); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetFileByPath of the other name: %v", err)
	}
	if _, err := d.DelFileByPathHash(spaced); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("DelFileByPathHash of the other name: %v", err)
	}
	if v, err := d.GetFileVersions(spaced); err != nil || len(v) != 0 {
		t.Errorf("the other name's versions: %v, %v", v, err)
	}
	if h, err := d.DelFileVersions(spaced); err != nil || len(h) != 0 {
		t.Errorf("deleting the other name's versions: %v, %v", h, err)
	}
	if err := d.ReplaceFileKeepingVersion(file(spaced, "ccc"), ""); err != nil {
		t.Errorf("ReplaceFileKeepingVersion of the other name: %v", err)
	}
	if _, err := d.OverrideFile(file(spaced, "ddd"), ""); err == nil {
		t.Error("an override of the other name stored it over the file")
	}
	f, err := d.GetFileByPath(plain)
	if err != nil || f.Hash != "aaa" {
		t.Fatalf("the file was changed: %+v, %v", f, err)
	}
	if v, err := d.GetFileVersions(plain); err != nil || len(v) != 1 {
		t.Errorf("its version: %v, %v", v, err)
	}
	if _, err := d.DelFileIfHash(plain, "zzz"); !errors.Is(err, ErrOtherContent) {
		t.Errorf("a delete bound to other content: %v", err)
	}
	if h, err := d.DelFileIfHash(plain, "aaa"); err != nil || h != "aaa" {
		t.Errorf("its own delete: %q, %v", h, err)
	}
}
