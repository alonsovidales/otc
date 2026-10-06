// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/segcrypt"
)

// sparseBlob makes, at path, what a blob of plain bytes of content looks
// like from outside: a segmented file's header and its size on disk.
func sparseBlob(t *testing.T, path string, plain int64, segmented bool) {
	t.Helper()
	head := make([]byte, segcrypt.HeaderSize)
	if segmented {
		copy(head, "OTS1")
	}
	if err := os.WriteFile(path, head, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(path) })
	if err := os.Truncate(path, segcrypt.EncryptedSize(plain)); err != nil {
		t.Fatal(err)
	}
}

func backfillMock(t *testing.T) (*Manager, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Manager{dao: dao.NewWithDB(db)}, mock
}

func expectBackfillPending(mock sqlmock.Sqlmock, wideColumns int) {
	mock.ExpectQuery("select `sizes_backfilled` from `settings`").
		WillReturnRows(sqlmock.NewRows([]string{"sizes_backfilled"}).AddRow(0))
	mock.ExpectQuery("from information_schema.columns where table_schema = database\\(\\) and column_name = 'size'").
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(wideColumns))
}

// Issue #187: a blob larger than MaxInt32 gives its content's size to
// every row of its hash; smaller, missing and unsegmented blobs are left
// alone, as are friends' posts. Hashes are read a page at a time, and the
// pass is recorded once it is done.
func TestBackfillSizesCorrectsWrappedRows(t *testing.T) {
	galleryTestEnv(t)
	big, small, plain, gone := strings.Repeat("a1", 32), strings.Repeat("a2", 32), strings.Repeat("a3", 32), strings.Repeat("a4", 32)
	sparseBlob(t, blobPath(big), 3<<30, true)
	sparseBlob(t, blobPath(small), 1<<20, true)
	sparseBlob(t, blobPath(plain), 3<<30, false) // not segmented: blobstore can't read it either
	post, smallPost := strings.Repeat("b1", 32), strings.Repeat("b2", 32)
	for name, n := range map[string]int64{post: 5 << 30, smallPost: 100} {
		if err := os.WriteFile(filepath.Join(galleryPosts, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(filepath.Join(galleryPosts, name))
		if err := os.Truncate(filepath.Join(galleryPosts, name), n); err != nil {
			t.Fatal(err)
		}
	}

	mg, mock := backfillMock(t)
	expectBackfillPending(mock, 3)
	page := sqlmock.NewRows([]string{"hash"}).AddRow(big)
	for i := 1; i < cSizeBackfillPage; i++ {
		page.AddRow(fmt.Sprintf("c%063d", i)) // no blob
	}
	mock.ExpectQuery("select `hash` from `files` where `hash` > \\? union select `hash` from `file_versions` where `hash` > \\? order by `hash` limit \\?").
		WithArgs("", "", cSizeBackfillPage).WillReturnRows(page)
	mock.ExpectExec("update `files` set `size` = \\? where `hash` = \\? and `size` <> \\?").
		WithArgs(int64(3<<30), big, int64(3<<30)).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec("update `file_versions` set `size` = \\? where `hash` = \\? and `size` <> \\?").
		WithArgs(int64(3<<30), big, int64(3<<30)).WillReturnResult(sqlmock.NewResult(0, 1))
	last := fmt.Sprintf("c%063d", cSizeBackfillPage-1)
	mock.ExpectQuery("select `hash` from `files` where `hash` > \\?").
		WithArgs(last, last, cSizeBackfillPage).
		WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow(small).AddRow(plain).AddRow(gone))
	mock.ExpectQuery("select distinct f.`hash` from `social_publications_files` f join `social_publications` p .* where p.`own_publication` = 1").
		WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow(post).AddRow(smallPost))
	mock.ExpectExec("update `social_publications_files` f join `social_publications` p .* set f.`size` = \\? where f.`hash` = \\? and p.`own_publication` = 1 and f.`size` <> \\?").
		WithArgs(int64(5<<30), post, int64(5<<30)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("update `settings` set `sizes_backfilled` = 1").WillReturnResult(sqlmock.NewResult(0, 1))

	mg.backfillSizes()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
	if !mg.sizesBackfilled.Load() {
		t.Error("a finished backfill was not noted")
	}
}

// Once recorded it never runs again; it notes it is done all the same.
func TestBackfillSizesRunsOnce(t *testing.T) {
	mg, mock := backfillMock(t)
	mock.ExpectQuery("select `sizes_backfilled` from `settings`").
		WillReturnRows(sqlmock.NewRows([]string{"sizes_backfilled"}).AddRow(1))
	mg.backfillSizes()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
	if !mg.sizesBackfilled.Load() {
		t.Error("a backfill recorded as done was not noted")
	}
}

// Before release 93's migration nothing is touched, and a pass that hit
// an error isn't recorded: both run again at the next start.
func TestBackfillSizesWaitsForTheMigrationAndRetriesFailures(t *testing.T) {
	galleryTestEnv(t)
	mg, mock := backfillMock(t)
	expectBackfillPending(mock, 2)
	mg.backfillSizes()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
	if mg.sizesBackfilled.Load() {
		t.Error("columns still int counted as backfilled")
	}

	big := strings.Repeat("d1", 32)
	sparseBlob(t, blobPath(big), 4<<30, true)
	mg, mock = backfillMock(t)
	expectBackfillPending(mock, 3)
	mock.ExpectQuery("select `hash` from `files` where `hash` > \\?").
		WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow(big))
	mock.ExpectExec("update `files` set `size`").WithArgs(int64(4<<30), big, int64(4<<30)).
		WillReturnError(errors.New("lock wait timeout"))
	mock.ExpectQuery("select distinct f.`hash` from `social_publications_files`").
		WillReturnRows(sqlmock.NewRows([]string{"hash"}))
	mg.backfillSizes()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err) // a sizes_backfilled update would be unexpected
	}
	if mg.sizesBackfilled.Load() {
		t.Error("a failed pass counted as done")
	}
}
