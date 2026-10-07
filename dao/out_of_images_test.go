// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

// Issue #192: a folder inside another kept out adds nothing, and each
// outer folder is one negated underPrefix term - exact range plus an
// escaped binary LIKE.
func TestOutsideFoldersCollapsesNestedFolders(t *testing.T) {
	cond, args := outsideFolders("`f`.`path`", []string{"/Private/Sub/", "/B_1/", "/Private/", "/Private/"})
	if got := strings.Count(cond, "not ("); got != 2 {
		t.Errorf("%d terms in %q, want 2", got, cond)
	}
	want := []any{"/B_1/", "/B_1/", `/B\_1/%`, "/Private/", "/Private/", "/Private/%"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("args %v, want %v", args, want)
	}
	if cond, args := outsideFolders("`path`", nil); cond != "" || args != nil {
		t.Errorf("no folders: %q %v", cond, args)
	}
	if cond, _ := underFolders("`path`", []string{"/A/", "/B/"}); !strings.HasPrefix(cond, "((") || !strings.Contains(cond, ") or (") {
		t.Errorf("underFolders: %q", cond)
	}
}

// Every filter at once: the args follow the ?s left to right - joins
// (tags, people, group), then the path, the folders kept out, and the
// date last.
func TestSearchMediaClausesArgOrderWithKeptOutFolders(t *testing.T) {
	before := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	_, where, _, _, _, _, args := searchMediaClauses("/Photos/", []string{"beach"}, []string{"p1", "p2"}, "g1", true, &before, []string{"/Photos/Private/"})
	want := []any{"beach", "p1", "p2", "g1",
		"/Photos/", "/Photos/", "/Photos/%", "/Photos/%/%",
		"/Photos/Private/", "/Photos/Private/", "/Photos/Private/%",
		before}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("args\n%v\nwant\n%v", args, want)
	}
	if strings.Count(where, "?") != 8 {
		t.Errorf("%d placeholders in the WHERE, want 8: %s", strings.Count(where, "?"), where)
	}
	if !strings.Contains(where, "not (`f`.`path` >= ?") {
		t.Errorf("no exclusion in %s", where)
	}
	// Nothing kept out: the query is what it was.
	_, plain, _, _, _, _, _ := searchMediaClauses("", nil, nil, "", true, nil, nil)
	if plain != " where `f`.`mime` like 'image%'" {
		t.Errorf("without folders: %q", plain)
	}
}

// The collections' count and cover leave out members only folders kept
// out hold: the same condition in both subqueries, so its args twice,
// then the group id.
func TestImageGroupSelectRepeatsTheExclusion(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	mock.ExpectQuery("select `g`.`id`, `g`.`name`, \\(select count.* and not \\(`f`.`path` >= .*\\), \\(select `m`.`hash` .* and not \\(`f`.`path` >= .* order by rand\\(\\) limit 1\\) from `image_groups` as `g` where `g`.`id` = \\?").
		WithArgs("/P/", "/P/", "/P/%", "/P/", "/P/", "/P/%", "g1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "n", "cover"}).AddRow("g1", "Trip", 1, "h1"))
	g, err := d.GetImageGroup("g1", []string{"/P/"})
	if err != nil || g.FileCount != 1 || g.CoverHash != "h1" {
		t.Fatalf("%+v, %v", g, err)
	}
	mock.ExpectQuery("from `image_groups` as `g` order by `g`.`created` desc").WithArgs().
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "n", "cover"}))
	if _, err := d.ListImageGroups(nil); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A database without the tables (a newer binary before release 108's
// script reached it) has no folder kept out and nothing skipped; any
// other error is one.
func TestOutOfImagesTablesMissing(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	noTable := &mysql.MySQLError{Number: 1146, Message: "Table 'otc.out_of_images_folders' doesn't exist"}
	mock.ExpectQuery("select `path` from `out_of_images_folders`").WillReturnError(noTable)
	if folders, err := d.GetOutOfImagesFolders(); err != nil || folders != nil {
		t.Errorf("without the table: %v, %v", folders, err)
	}
	mock.ExpectQuery("select `path` from `out_of_images_folders`").WillReturnError(fmt.Errorf("connection lost"))
	if _, err := d.GetOutOfImagesFolders(); err == nil {
		t.Error("a lost connection read as no folders")
	}
	mock.ExpectQuery("select `hash` from `skipped_analysis`").WillReturnError(noTable)
	if hashes, err := d.SkippedAnalysis(); err != nil || hashes != nil {
		t.Errorf("SkippedAnalysis without the table: %v, %v", hashes, err)
	}
	mock.ExpectExec("delete from `skipped_analysis`").WillReturnError(noTable)
	if err := d.DelSkippedAnalysis("h"); err != nil {
		t.Errorf("DelSkippedAnalysis without the table: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// Hashes go cHashChunk per statement, files and kept versions in each.
func TestVisibleHashesChunks(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	hashes := make([]string, cHashChunk+100)
	for i := range hashes {
		hashes[i] = fmt.Sprintf("%064x", i)
	}
	q := "select `hash` from `files` where `hash` in \\(.*\\) and not \\(.* union select `hash` from `file_versions` where `hash` in \\(.*\\) and not \\("
	mock.ExpectQuery(q).WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow(hashes[0]))
	mock.ExpectQuery(q).WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow(hashes[cHashChunk+1]))
	got, err := d.VisibleHashes(hashes, []string{"/P/"})
	if err != nil || len(got) != 2 || !got[hashes[0]] || !got[hashes[cHashChunk+1]] {
		t.Errorf("%v, %v", got, err)
	}
	// Nothing under no folder: no query at all.
	if got, err := d.HashesWithRowsUnder(hashes, nil); err != nil || len(got) != 0 {
		t.Errorf("under no folder: %v, %v", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// TakeSkippedAnalysis moves only what it found to pending_analysis, in
// one transaction: a failure leaves it skipped, never in neither table.
func TestTakeSkippedAnalysis(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	mock.ExpectBegin()
	mock.ExpectQuery("select `hash` from `skipped_analysis` where `hash` in \\(\\?,\\?\\) for update").WithArgs("a", "b").
		WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow("b"))
	mock.ExpectExec("insert ignore into `pending_analysis` \\(`hash`, `queued`\\) values \\(\\?, \\?\\)$").WithArgs("b", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("delete from `skipped_analysis` where `hash` in \\(\\?\\)").WithArgs("b").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if got, err := d.TakeSkippedAnalysis([]string{"a", "b"}); err != nil || !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("%v, %v", got, err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery("select `hash` from `skipped_analysis`").WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow("b"))
	mock.ExpectExec("insert ignore into `pending_analysis`").WillReturnError(errors.New("disk full"))
	mock.ExpectRollback()
	if got, err := d.TakeSkippedAnalysis([]string{"b"}); err == nil || got != nil {
		t.Errorf("a failed move: %v, %v", got, err)
	}
	if got, err := d.TakeSkippedAnalysis(nil); err != nil || got != nil {
		t.Errorf("nothing to take: %v, %v", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
