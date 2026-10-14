// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"fmt"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// Issue #201: rows in SearchMedia's order - newest first, then by path
// backwards - keep the last of each hash, its oldest copy, in their order;
// a search with no copies is returned as it is.
func TestOldestCopiesKeepsEachPhotosLastRow(t *testing.T) {
	paths := func(files []*pb.File) string {
		var out []string
		for _, f := range files {
			out = append(out, f.Path)
		}
		return strings.Join(out, ",")
	}
	in := []*pb.File{
		{Hash: "a", Path: "/new/a"}, {Hash: "b", Path: "/b"}, {Hash: "a", Path: "/old/a"},
		{Hash: "c", Path: "/z/c"}, {Hash: "c", Path: "/y/c"}, {Hash: "d", Path: "/d"}, {Hash: "a", Path: "/older/a"},
	}
	if got, want := paths(oldestCopies(in)), "/b,/y/c,/d,/older/a"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	plain := []*pb.File{{Hash: "x", Path: "/x"}, {Hash: "y", Path: "/y"}}
	if got := oldestCopies(plain); len(got) != 2 || &got[0] != &plain[0] {
		t.Error("rows without copies weren't returned as they were")
	}
	if got := oldestCopies(nil); len(got) != 0 {
		t.Errorf("nothing gave %v", got)
	}
}

// CountCopies counts `files` rows only (paths, not versions), cHashChunk
// hashes per statement; PathsOfHash lists a content's paths by path.
func TestCountCopiesAndPathsOfHash(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	hashes := make([]string, cHashChunk+2)
	for i := range hashes {
		hashes[i] = fmt.Sprintf("%064x", i)
	}
	mock.ExpectQuery("^select `hash`, count\\(\\*\\) from `files` where `hash` in \\([?,]+\\) group by `hash`$").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "n"}).AddRow(hashes[0], 3).AddRow(hashes[7], 1))
	mock.ExpectQuery("^select `hash`, count\\(\\*\\) from `files` where `hash` in \\(\\?,\\?\\) group by `hash`$").
		WithArgs(hashes[cHashChunk], hashes[cHashChunk+1]).
		WillReturnRows(sqlmock.NewRows([]string{"hash", "n"}).AddRow(hashes[cHashChunk+1], 2))
	counts, err := d.CountCopies(hashes)
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 3 || counts[hashes[0]] != 3 || counts[hashes[7]] != 1 || counts[hashes[cHashChunk+1]] != 2 {
		t.Errorf("counts %v", counts)
	}
	if counts, err := d.CountCopies(nil); err != nil || len(counts) != 0 {
		t.Errorf("no hashes: %v, %v", counts, err)
	}

	mock.ExpectQuery("^select `path` from `files` where `hash` = \\? order by `path`$").WithArgs("h").
		WillReturnRows(sqlmock.NewRows([]string{"path"}).AddRow("/a").AddRow("/b"))
	if paths, err := d.PathsOfHash("h"); err != nil || strings.Join(paths, ",") != "/a,/b" {
		t.Errorf("paths %v, %v", paths, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
