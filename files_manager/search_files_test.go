// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/protobuf/proto"

	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// A search gives each entry exactly as ListFiles lists it: the lock of an
// upload-only folder on files and folders under it, the versions badge,
// and no hash for a file whose content is missing (issue #141).
func TestSearchFilesFillsEntriesAsListFilesDoes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mg := keptOut(&Manager{dao: dao.NewWithDB(db)})
	missingBlobs.set("h-gone", true)
	defer missingBlobs.set("h-gone", false)
	at := time.Date(2026, 10, 7, 9, 30, 0, 0, time.UTC)
	fileRows := func(paths ...string) *sqlmock.Rows {
		rows := sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"})
		hashes := map[string]string{"/Trip/a.jpg": "h-a", "/Trip/b.jpg": "h-gone", "/Trip/Day1/c.jpg": "h-c"}
		for _, p := range paths {
			rows.AddRow(hashes[p], "image/jpeg", at, at, p, int64(len(p)))
		}
		return rows
	}
	locked := func() {
		mock.ExpectQuery("select `path` from `upload_only_folders`").WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow("/Trip/"))
	}

	// ListFiles("/Trip/").
	mock.ExpectQuery("select distinct\\(SUBSTRING_INDEX").WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow("/Trip/Day1"))
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` >=").
		WillReturnRows(fileRows("/Trip/a.jpg", "/Trip/b.jpg"))
	locked()
	mock.ExpectQuery("select `path`, count\\(\\*\\) from `file_versions` where `path` >=").
		WillReturnRows(sqlmock.NewRows([]string{"path", "n"}).AddRow("/Trip/a.jpg", 2))
	listed, err := mg.ListFiles(nil, "/Trip/", false)
	if err != nil {
		t.Fatal(err)
	}

	// SearchFiles("trip").
	mock.ExpectQuery("select `path` from `files` where `path` like \\?").
		WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow("/Trip/a.jpg").AddRow("/Trip/b.jpg").AddRow("/Trip/Day1/c.jpg"))
	mock.ExpectQuery("from `files` where `path` in").WithArgs("/Trip/a.jpg", "/Trip/b.jpg", "/Trip/Day1/c.jpg").
		WillReturnRows(fileRows("/Trip/a.jpg", "/Trip/b.jpg", "/Trip/Day1/c.jpg"))
	locked()
	mock.ExpectQuery("select `path`, count\\(\\*\\) from `file_versions` where `path` in").
		WithArgs("/Trip/a.jpg", "/Trip/b.jpg", "/Trip/Day1/c.jpg").
		WillReturnRows(sqlmock.NewRows([]string{"path", "n"}).AddRow("/Trip/a.jpg", 2))
	found, err := mg.SearchFiles("trip", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	byPath := map[string]*pb.File{}
	for _, f := range found {
		byPath[f.Path] = f
	}
	for _, l := range listed {
		if f := byPath[l.Path]; f == nil || !proto.Equal(f, l) {
			t.Errorf("%s: listed %v, found %v", l.Path, l, f)
		}
	}
	if len(listed) != 3 {
		t.Fatalf("listed %d entries, want 3", len(listed))
	}
	// And the entries the listing of /Trip/ doesn't have.
	if f := byPath["/Trip"]; f == nil || !f.UploadOnly || f.Mime != "inode/directory" {
		t.Errorf("the locked folder itself: %v", f)
	}
	if f := byPath["/Trip/Day1/c.jpg"]; f == nil || !f.UploadOnly || f.Hash != "h-c" || f.Versions != 0 {
		t.Errorf("a file deeper down: %v", f)
	}
	if f := byPath["/Trip/a.jpg"]; f.Versions != 2 || !f.UploadOnly {
		t.Errorf("versions and lock: %v", f)
	}
	if f := byPath["/Trip/b.jpg"]; f.Hash != "" {
		t.Errorf("a missing blob is listed without its hash: %v", f)
	}
}

// No text: nothing asked of the database, no entries.
func TestSearchFilesEmptyQueryAsksNothing(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mg := keptOut(&Manager{dao: dao.NewWithDB(db)})
	if files, err := mg.SearchFiles(" \t", 0); err != nil || len(files) != 0 {
		t.Errorf("got %v, %v", files, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A failure on the way is an error, never a partial answer.
func TestSearchFilesReturnsErrors(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mg := keptOut(&Manager{dao: dao.NewWithDB(db)})
	boom := errors.New("boom")

	mock.ExpectQuery("select `path` from `files`").WillReturnError(boom)
	if files, err := mg.SearchFiles("x", 0); !errors.Is(err, boom) || files != nil {
		t.Errorf("search: %v, %v", files, err)
	}

	mock.ExpectQuery("select `path` from `files`").WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow("/x/a"))
	mock.ExpectQuery("from `files` where `path` in").WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).
		AddRow("h", "text/plain", time.Now(), time.Now(), "/x/a", int64(1)))
	mock.ExpectQuery("select `path` from `upload_only_folders`").WillReturnError(boom)
	if files, err := mg.SearchFiles("x", 0); !errors.Is(err, boom) || files != nil {
		t.Errorf("upload-only folders: %v, %v", files, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
