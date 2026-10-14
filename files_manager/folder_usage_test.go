// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
)

func usageRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"child", "dir", "size", "version"})
}

// Issue #195: the folder is answered with its trailing slash whatever
// way it was asked ("" is the root), the entries largest first - files and
// versions together - then by path, a folder before a file of the same
// name, and the totals are the entries' sums.
func TestFolderUsageOrdersAndTotals(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mg := NewWithDAO(dao.NewWithDB(db))

	mock.ExpectQuery("from `files` where").WithArgs(3, 3, "/Docs/", "/Docs/", "/Docs/%", 3, 3, "/Docs/", "/Docs/", "/Docs/%").
		WillReturnRows(usageRows().
			AddRow("/Docs/small.txt", 0, int64(5), 0).
			AddRow("/Docs/Old", 1, int64(60), 1). // only versions: 60
			AddRow("/Docs/b", 0, int64(40), 0).   // a file b and a folder b, 40 each
			AddRow("/Docs/b", 1, int64(40), 0).
			AddRow("/Docs/a", 1, int64(40), 0).
			AddRow("/Docs/Work", 1, int64(50), 0).
			AddRow("/Docs/Work", 1, int64(25), 0).
			AddRow("/Docs/Work", 1, int64(10), 1). // Work: 75 + 10
			AddRow("/Docs/huge.iso", 0, int64(5)<<30, 0))

	usage, err := mg.FolderUsage("/Docs") // without the slash
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range usage.Entries {
		got = append(got, fmt.Sprintf("%s:%v:%d/%d+%d", e.Path, e.IsDir, e.FilesSize, e.FileCount, e.VersionsSize))
	}
	want := []string{
		fmt.Sprintf("/Docs/huge.iso:false:%d/1+0", int64(5)<<30),
		"/Docs/Work:true:75/2+10",
		"/Docs/Old:true:0/0+60",
		"/Docs/a:true:40/1+0",
		"/Docs/b:true:40/1+0",
		"/Docs/b:false:40/1+0",
		"/Docs/small.txt:false:5/1+0",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("entries\n got %q\nwant %q", got, want)
	}
	if usage.Path != "/Docs/" || usage.FilesSize != int64(5)<<30+75+40+40+40+5 || usage.FileCount != 7 || usage.VersionsSize != 70 {
		t.Errorf("totals: path %q, %d bytes in %d files, %d in versions", usage.Path, usage.FilesSize, usage.FileCount, usage.VersionsSize)
	}

	mock.ExpectQuery("from `files` where").WithArgs(2, 2, "/", "/", "/%", 2, 2, "/", "/", "/%").WillReturnRows(usageRows())
	usage, err = mg.FolderUsage("")
	if err != nil || usage.Path != "/" || len(usage.Entries) != 0 || usage.FilesSize != 0 || usage.FileCount != 0 || usage.VersionsSize != 0 {
		t.Errorf("an empty root: %+v, %v", usage, err)
	}

	mock.ExpectQuery("from `files` where").WithArgs(2, 2, "/", "/", "/%", 2, 2, "/", "/", "/%").WillReturnRows(usageRows())
	if usage, err = mg.FolderUsage("/"); err != nil || usage.Path != "/" {
		t.Errorf("the root: %+v, %v", usage, err)
	}

	boom := errors.New("boom")
	mock.ExpectQuery("from `files` where").WillReturnError(boom)
	if usage, err := mg.FolderUsage("/Docs/"); !errors.Is(err, boom) || usage != nil {
		t.Errorf("a database error: %+v, %v", usage, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// The order never depends on what order the rows came in.
func TestSortFolderUsageIsTotal(t *testing.T) {
	mk := func() []*pb.FolderUsageEntry {
		return []*pb.FolderUsageEntry{
			{Path: "/z", FilesSize: 1},
			{Path: "/a", IsDir: true, VersionsSize: 1},
			{Path: "/a", FilesSize: 1},
			{Path: "/m", IsDir: true, FilesSize: 3},
			{Path: "/é", FilesSize: 1}, // after every ASCII name, byte by byte
			{Path: "/B", FilesSize: 1}, // upper case before lower case
		}
	}
	text := func(es []*pb.FolderUsageEntry) string {
		var out []string
		for _, e := range es {
			out = append(out, fmt.Sprintf("%s:%v", e.Path, e.IsDir))
		}
		return strings.Join(out, " ")
	}
	want := "/m:true /B:false /a:true /a:false /z:false /é:false"
	for shift := range 6 {
		es := mk()
		es = append(es[shift:], es[:shift]...)
		sortFolderUsage(es)
		if got := text(es); got != want {
			t.Errorf("rotated by %d: %s, want %s", shift, got, want)
		}
	}
}
