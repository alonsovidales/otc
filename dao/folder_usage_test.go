// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// usageRows is FolderUsage's result set: child, is a folder, size, is a
// version - as the database sends them.
func usageRows(rows ...[]driver.Value) *sqlmock.Rows {
	r := sqlmock.NewRows([]string{"child", "dir", "size", "version"})
	for _, row := range rows {
		r.AddRow(row...)
	}
	return r
}

func usageText(entries []*pb.FolderUsageEntry) string {
	var out []string
	for _, e := range entries {
		out = append(out, fmt.Sprintf("%s dir=%v files=%d/%d versions=%d", e.Path, e.IsDir, e.FilesSize, e.FileCount, e.VersionsSize))
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

// Issue #195: one statement, both tables by the folder's index range; each
// row is summed into its child - a file and a folder of the same name
// apart, versions apart from the files' bytes and count, sums past 4 GiB
// in int64.
func TestFolderUsageSumsEachChild(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)

	// "/x/" has two slashes: a child is the path up to its third.
	mock.ExpectQuery("select substring_index\\(`path`, '/', \\?\\), `path` <> substring_index\\(`path`, '/', \\?\\), `size`, 0 from `files` where `path` >= \\? .* "+
		"union all select substring_index\\(`path`, '/', \\?\\), `path` <> substring_index\\(`path`, '/', \\?\\), `size`, 1 from `file_versions` where `path` >= \\?").
		WithArgs(3, 3, "/x/", "/x/", "/x/%", 3, 3, "/x/", "/x/", "/x/%").
		WillReturnRows(usageRows(
			[]driver.Value{"/x/a", int64(1), threeGiB, int64(0)},     // /x/a/big.mov
			[]driver.Value{"/x/a", int64(1), threeGiB, int64(0)},     // /x/a/deep/big2.mov
			[]driver.Value{"/x/a", int64(0), int64(10), int64(0)},    // the file /x/a
			[]driver.Value{"/x/a", int64(0), int64(4), int64(1)},     // a version of the file /x/a
			[]driver.Value{"/x/a", int64(1), int64(7), int64(1)},     // a version of /x/a/notes.txt
			[]driver.Value{"/x/old", int64(1), int64(5), int64(1)},   // only versions left there
			[]driver.Value{"/x/b.txt", int64(0), int64(3), int64(0)}, // a file right in /x/
		))

	entries, err := d.FolderUsage("/x/")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"/x/a dir=false files=10/1 versions=4",
		fmt.Sprintf("/x/a dir=true files=%d/2 versions=7", 2*threeGiB),
		"/x/b.txt dir=false files=3/1 versions=0",
		"/x/old dir=true files=0/0 versions=5",
	}, "\n")
	if got := usageText(entries); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// The root's children are the path up to its second slash; an empty
// folder has no entries.
func TestFolderUsageOfTheRoot(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)

	mock.ExpectQuery("from `files` where").WithArgs(2, 2, "/", "/", "/%", 2, 2, "/", "/", "/%").
		WillReturnRows(usageRows(
			// The device's DSN interpolates its parameters, so the answer
			// comes in the text protocol: every value as text, the flags
			// as "0"/"1".
			[]driver.Value{[]byte("/Photos"), []byte("1"), []byte("5000000000"), []byte("0")},
			[]driver.Value{[]byte("/a.txt"), []byte("0"), []byte("12"), []byte("0")},
		))
	entries, err := d.FolderUsage("/")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := usageText(entries), "/Photos dir=true files=5000000000/1 versions=0\n/a.txt dir=false files=12/1 versions=0"; got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}

	mock.ExpectQuery("from `files` where").WillReturnRows(usageRows())
	if entries, err := d.FolderUsage("/nothing/"); err != nil || len(entries) != 0 {
		t.Errorf("an empty folder: %v, %v", entries, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A failed query, row or read is an error, never a partial answer: a
// folder would look smaller than it is.
func TestFolderUsageFailsWhole(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	boom := errors.New("boom")

	mock.ExpectQuery("from `files` where").WillReturnError(boom)
	if entries, err := d.FolderUsage("/x/"); !errors.Is(err, boom) || entries != nil {
		t.Errorf("query: %v, %v", entries, err)
	}

	mock.ExpectQuery("from `files` where").WillReturnRows(usageRows(
		[]driver.Value{"/x/a", int64(0), int64(1), int64(0)},
	).RowError(0, boom))
	if entries, err := d.FolderUsage("/x/"); !errors.Is(err, boom) || entries != nil {
		t.Errorf("a row: %v, %v", entries, err)
	}

	mock.ExpectQuery("from `files` where").WillReturnRows(usageRows(
		[]driver.Value{"/x/a", int64(0), "not a size", int64(0)},
	))
	if entries, err := d.FolderUsage("/x/"); err == nil || entries != nil {
		t.Errorf("a size that doesn't scan: %v, %v", entries, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
