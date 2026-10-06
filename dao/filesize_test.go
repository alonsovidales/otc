// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	pb "github.com/alonsovidales/otc/proto/generated"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	threeGiB = int64(3) << 30
	// What size carries for 3 GiB: the int64 wrapped to int32.
	threeGiBWrapped = int32(-1 << 30)
)

// Issue #187: size64 holds the size, and size keeps what it carried
// before, the size wrapped to int32 (the apps in the stores read it).
func TestFileSizeFillsBothFields(t *testing.T) {
	f := &pb.File{}
	SetFileSize(f, threeGiB)
	if f.Size64 != threeGiB || f.Size != threeGiBWrapped {
		t.Fatalf("3 GiB: size64 %d, size %d; want %d and the wrapped %d", f.Size64, f.Size, threeGiB, threeGiBWrapped)
	}
	if FileSize(f) != threeGiB {
		t.Errorf("FileSize = %d, want %d", FileSize(f), threeGiB)
	}
	SetFileSize(f, 1234)
	if f.Size64 != 1234 || f.Size != 1234 {
		t.Errorf("1234: size64 %d, size %d", f.Size64, f.Size)
	}
	// A device before release 93 sends only size.
	if n := FileSize(&pb.File{Size: 70}); n != 70 {
		t.Errorf("FileSize of an old device's file = %d, want 70", n)
	}
}

// A BIGINT above MaxInt32 doesn't scan into an int32 (the whole listing
// failed), so every reader scans an int64 and fills both fields.
func TestFileReadersCarryA3GiBSize(t *testing.T) {
	now := time.Now()
	fileRow := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).
			AddRow("h", "video/mp4", now, now, "/v.mp4", threeGiB)
	}
	versionRow := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"hash", "mime", "size", "created", "replaced"}).AddRow("h", "video/mp4", threeGiB, now, now)
	}
	cases := []struct {
		name  string
		query string
		rows  func() *sqlmock.Rows
		read  func(d *Dao) ([]*pb.File, error)
	}{
		{"GetFileByPath", "from `files` where `path` = \\?", fileRow, func(d *Dao) ([]*pb.File, error) {
			f, err := d.GetFileByPath("/v.mp4")
			return []*pb.File{f}, err
		}},
		{"GetFileByHash", "from `files` where `hash` = \\?", fileRow, func(d *Dao) ([]*pb.File, error) {
			f, err := d.GetFileByHash("h")
			return []*pb.File{f}, err
		}},
		{"GetFilesByPath", "from `files` where", fileRow, func(d *Dao) ([]*pb.File, error) {
			return d.GetFilesByPath("/", true, false)
		}},
		{"SearchMedia", "from `files` as `f`", fileRow, func(d *Dao) ([]*pb.File, error) {
			return d.SearchMedia("", nil, nil, "", false, nil)
		}},
		{"ListMediaForReprocess", "from `files`.*group by `hash`", fileRow, func(d *Dao) ([]*pb.File, error) {
			return d.ListMediaForReprocess("", 10)
		}},
		{"GetFileVersions", "from `file_versions` where `path` = \\? order by", versionRow, func(d *Dao) ([]*pb.File, error) {
			return d.GetFileVersions("/v.mp4")
		}},
		{"GetFileVersion", "from `file_versions` where `path` = \\? and `hash` = \\?", versionRow, func(d *Dao) ([]*pb.File, error) {
			f, err := d.GetFileVersion("/v.mp4", "h")
			return []*pb.File{f}, err
		}},
		{"GetSocialPublicationFiles", "from `social_publications_files` where `uuid` = \\?", func() *sqlmock.Rows {
			return sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "size"}).AddRow("h", "video/mp4", now, now, threeGiB)
		}, func(d *Dao) ([]*pb.File, error) {
			return d.GetSocialPublicationFiles("p1")
		}},
		{"GetSocialPublicationsFiles", "from `social_publications_files` where `uuid` in", func() *sqlmock.Rows {
			return sqlmock.NewRows([]string{"uuid", "hash", "mime", "created", "modified", "size"}).AddRow("p1", "h", "video/mp4", now, now, threeGiB)
		}, func(d *Dao) ([]*pb.File, error) {
			m, err := d.GetSocialPublicationsFiles([]string{"p1"})
			return m["p1"], err
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectQuery(c.query).WillReturnRows(c.rows())
			files, err := c.read(NewWithDB(db))
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 1 || files[0].Size64 != threeGiB || files[0].Size != threeGiBWrapped {
				t.Fatalf("got %+v, want size64 %d and size %d", files, threeGiB, threeGiBWrapped)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Error(err)
			}
		})
	}
}

// Rows are written with the real size, never the wrapped int32.
func TestFileWritersStoreTheInt64Size(t *testing.T) {
	f := &pb.File{Hash: strings.Repeat("a", 64), Mime: "video/mp4", Path: "/v.mp4", Created: timestamppb.Now(), Modified: timestamppb.Now()}
	SetFileSize(f, threeGiB)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)

	mock.ExpectExec("insert into `files`").
		WithArgs(f.Hash, f.Mime, sqlmock.AnyArg(), sqlmock.AnyArg(), f.Path, threeGiB, sql.NullString{}).
		WillReturnResult(sqlmock.NewResult(1, 1))
	if _, err := d.StoreNewFile(f, ""); err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectExec("insert into `file_versions`").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("update `files` set `hash` = \\?, `mime` = \\?, `size` = \\?").
		WithArgs(f.Hash, f.Mime, threeGiB, sqlmock.AnyArg(), sqlmock.AnyArg(), sql.NullString{}, f.Path).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := d.ReplaceFileKeepingVersion(f, ""); err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery("select `hash` from `files` where `path` = \\? for update").WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("insert into `files`").
		WithArgs(f.Hash, f.Mime, sqlmock.AnyArg(), sqlmock.AnyArg(), f.Path, threeGiB, sql.NullString{}).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	if _, err := d.OverrideFile(f, ""); err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectExec("insert into `social_publications`").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("insert into `social_publications_files`").
		WithArgs(0, "p1", f.Hash, f.Mime, sqlmock.AnyArg(), sqlmock.AnyArg(), threeGiB).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	if err := d.NewSocialPublication("p1", "", "me", true, []*pb.File{f}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// The backfill's queries: the column check counts all three tables, and
// a hash's size is set on its files and kept versions.
func TestSizeBackfillQueries(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)

	for _, n := range []int{3, 2} {
		mock.ExpectQuery("table_name in \\('files', 'file_versions', 'social_publications_files'\\) and data_type = 'bigint'").
			WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(n))
		if wide, err := d.SizeColumnsWide(); err != nil || wide != (n == 3) {
			t.Errorf("%d BIGINT columns: wide %v, %v", n, wide, err)
		}
	}

	mock.ExpectExec("update `files` set `size` = \\? where `hash` = \\? and `size` <> \\?").
		WithArgs(threeGiB, "h", threeGiB).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec("update `file_versions` set `size` = \\? where `hash` = \\? and `size` <> \\?").
		WithArgs(threeGiB, "h", threeGiB).WillReturnResult(sqlmock.NewResult(0, 1))
	if n, err := d.SetSizeOfHash("h", threeGiB); err != nil || n != 3 {
		t.Errorf("SetSizeOfHash = %d, %v; want 3 rows", n, err)
	}

	mock.ExpectQuery("select `sizes_backfilled` from `settings`").
		WillReturnRows(sqlmock.NewRows([]string{"sizes_backfilled"}).AddRow(1))
	if done, err := d.SizesBackfilled(); err != nil || !done {
		t.Errorf("SizesBackfilled = %v, %v", done, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
