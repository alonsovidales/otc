// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// Issue #201: a photo stored at several paths.

const reCopies = "select `hash`, count\\(\\*\\) from `files` where `hash` in \\("

func copiesRows(counts ...any) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"hash", "n"})
	for i := 0; i+1 < len(counts); i += 2 {
		rows.AddRow(counts[i], counts[i+1])
	}
	return rows
}

// The search's rows hold every copy, newest first and, at one instant, by
// path backwards (as the query orders them): each photo comes once, at
// its oldest copy - its path and date, wherever that falls in the order -
// saying how many paths hold it, page after page, in both page kinds.
func TestImageSearchShowsEachPhotoAtItsOldestCopy(t *testing.T) {
	bothPageKinds(t, func(t *testing.T, omit bool) {
		_, ses := galleryTestEnv(t)
		orig := maxImagesSearch
		maxImagesSearch = func() int { return 2 }
		t.Cleanup(func() { maxImagesSearch = orig })
		base := 0x2010
		if omit {
			base += cOmittedBase
		}
		hash := func(i int) string { return fmt.Sprintf("%064x", base+i) }
		for i := 0; i < 3; i++ {
			if err := blobstore.WriteBytes(thumbnailPath(hash(i)), ses, []byte("thumb")); err != nil {
				t.Fatal(err)
			}
		}
		at := func(y int, m time.Month) time.Time { return time.Date(y, m, 1, 12, 0, 0, 0, time.UTC) }
		db, mock, _ := sqlmock.New()
		t.Cleanup(func() { db.Close() })
		mock.MatchExpectationsInOrder(false)
		row := func(rows *sqlmock.Rows, i int, when time.Time, path string) *sqlmock.Rows {
			return rows.AddRow(hash(i), "image/jpeg", when, when, path, 1)
		}
		rows := sqlmock.NewRows(fileCols)
		row(rows, 0, at(2026, 10), "/android/x/Download/a.jpg") // a, downloaded and synced back
		row(rows, 1, at(2025, 3), "/Phone/b.jpg")
		row(rows, 0, at(2019, 6), "/Phone/a.jpg") // a's oldest copy
		row(rows, 2, at(2018, 1), "/Phone/c.jpg")
		row(rows, 2, at(2018, 1), "/Mac/c.jpg") // the same instant: the smaller path
		mock.ExpectQuery("select `f`.`hash`, `f`.`mime`").WillReturnRows(rows)
		mock.ExpectQuery(reCopies).WithArgs(hash(1), hash(0)).WillReturnRows(copiesRows(hash(1), 1, hash(0), 3))
		mock.ExpectQuery(reCopies).WithArgs(hash(2)).WillReturnRows(copiesRows(hash(2), 2))
		mg := keptOut(&Manager{dao: dao.NewWithDB(db), searchTokens: newSearchTokenCache(100)})

		describe := func(files []*pb.File) string {
			var out []string
			for _, f := range files {
				out = append(out, fmt.Sprintf("%s@%s×%d", f.Path, f.Created.AsTime().Format("2006-01"), f.GetCopies()))
				if f.Copies == nil {
					t.Errorf("%s: copies unset", f.Path)
				}
			}
			return strings.Join(out, " ")
		}
		files, tok, err := mg.ImageSearch(ses, "", nil, "", false, nil, "", nil, nil, 0, 0, false, omit)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := describe(files), "/Phone/b.jpg@2025-03×1 /Phone/a.jpg@2019-06×3"; got != want || tok == "" {
			t.Fatalf("first page: %s (token %q), want %s", got, tok, want)
		}
		files, tok, err = mg.ImageSearch(ses, "", nil, tok, false, nil, "", nil, nil, 2, 0, false, omit)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := describe(files), "/Mac/c.jpg@2018-01×2"; got != want || tok != "" {
			t.Fatalf("last page: %s (token %q), want %s", got, tok, want)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
}

// A page whose copies can't be counted is served without them - copies
// unset on every entry, so the apps offer no "every copy" for it - and its
// token moves as usual: the next page goes on after it.
func TestImageSearchPageServedWithoutItsCopies(t *testing.T) {
	bothPageKinds(t, func(t *testing.T, omit bool) {
		st := newSearchTest(t, omit, 0x2100, 3, photoPaths(9))
		got, tok := st.page("", 0)
		st.expect("first page", got, 0, 1, 2)
		st.mock.ExpectQuery(reCopies).WillReturnError(errors.New("connection lost"))
		files, next, err := st.search(tok, 3)
		if err != nil {
			t.Fatalf("a failed count failed the page: %v", err)
		}
		var paths []string
		for _, f := range files {
			if f.Copies != nil {
				t.Errorf("%s: copies %d after a failed count", f.Path, f.GetCopies())
			}
			if !omit && len(f.Content) == 0 {
				t.Errorf("%s: served without its thumbnail", f.Path)
			}
			paths = append(paths, f.Path)
		}
		want := strings.Join([]string{st.paths[3], st.paths[4], st.paths[5]}, " ")
		if got := strings.Join(paths, " "); got != want || next != tok {
			t.Fatalf("second page: [%s] (token %q), want [%s] and the token %q", got, next, want, tok)
		}
		st.last(tok, 6, 6, 7, 8)
	})
}

// expectLastDelFile is DelFile's delete of the last path holding hash, no
// versions: its tags go in the delete's transaction, then (no face, no
// skipped row) its blob and thumbnails.
func expectLastDelFile(mock sqlmock.Sqlmock, path, hash string) {
	mock.ExpectBegin()
	mock.ExpectQuery("select `hash` from `files` where `path` = \\?").WithArgs(path).WillReturnRows(hashRows(hash))
	mock.ExpectQuery("select count\\(\\*\\) from `files` where `hash` = \\?").WithArgs(hash).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	mock.ExpectQuery("select count\\(\\*\\) from `file_versions` where `hash` = \\?").WithArgs(hash).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectExec("delete from `file_tags` where `hash` = \\?").WithArgs(hash).WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec("delete from `files` where `path` = \\?").WithArgs(path).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery("select `hash` from `file_versions` where `path` = \\?").WithArgs(path).WillReturnRows(hashRows())
	mock.ExpectQuery(reReferenced).WithArgs(hash, hash).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectQuery("select `id`, `person_id` from `faces` where `hash` = \\?").WithArgs(hash).WillReturnRows(sqlmock.NewRows([]string{"id", "person_id"}))
	mock.ExpectExec("delete from `skipped_analysis` where `hash` in").WithArgs(hash).WillReturnResult(sqlmock.NewResult(0, 0))
}

// expectCopiesOf is DelEveryCopy reading path's row and the paths holding
// its content, then the upload-only folders.
func expectCopiesOf(mock sqlmock.Sqlmock, path, hash string, paths []string, uploadOnly ...string) {
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = \\?").WithArgs(path).
		WillReturnRows(fileRow(hash, path))
	mock.ExpectQuery("select `path` from `files` where `hash` = \\? order by `path`").WithArgs(hash).WillReturnRows(pathRows(paths...))
	mock.ExpectQuery("select `path` from `upload_only_folders`").WillReturnRows(pathRows(uploadOnly...))
}

// Every path holding the content goes - one kept out of Images too, its
// flag kept - each as DelFile would, the content with the last one; each
// counts as a delete for the search tokens, and what they held is checked
// against the folders kept out once. The hash the client showed (DelFile.
// hash) is the path's content, so the delete goes ahead.
func TestDelEveryCopyDeletesEveryPath(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg, mock, seen := recordingMock(t)
	keptOut(mg, "/Private/")
	mg.searchTokens = newSearchTokenCache(10)
	h := testHash("7")
	onDisk(t, h, true)
	paths := []string{"/Phone/a.jpg", "/Private/a.jpg", "/android/x/Download/a.jpg"}

	expectCopiesOf(mock, "/android/x/Download/a.jpg", h, paths, "/Backups/")
	expectDelFile(mock, paths[0], h, 2)
	expectDelFile(mock, paths[1], h, 1)
	expectLastDelFile(mock, paths[2], h)
	mock.ExpectQuery(reVisible).WillReturnRows(hashRows())
	mock.ExpectQuery(reUnder).WillReturnRows(hashRows())
	expectSentinel(mock)

	if err := mg.DelEveryCopy(ses, "/android/x/Download/a.jpg", h); err != nil {
		t.Fatal(err)
	}
	metUpToSentinel(t, mock)
	for _, p := range []string{blobPath(h), blobPath(h) + "_thumbnail"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s is still there: %v", p, err)
		}
	}
	if _, deletes := mg.searchTokens.begin(); deletes != 3 {
		t.Errorf("%d deletes counted, want 3", deletes)
	}
	if seen.ran("out_of_images_folders") {
		t.Error("deleting the copies touched the flags")
	}
	if got, _ := mg.OutOfImagesFolders(); len(got) != 1 || got[0] != "/Private/" {
		t.Errorf("flags: %v", got)
	}
}

// All or nothing: a copy in an upload-only folder - another path, or the
// one asked for - refuses the whole delete, naming the outermost
// upload-only folder holding it, and nothing is deleted.
func TestDelEveryCopyRefusesWhenACopyIsUploadOnly(t *testing.T) {
	for _, asked := range []string{"/Phone/a.jpg", "/Backups/Phone/a.jpg"} {
		_, ses := galleryTestEnv(t)
		mg, mock, seen := recordingMock(t)
		keptOut(mg)
		h := testHash("8")
		onDisk(t, h, true)
		expectCopiesOf(mock, asked, h, []string{"/Backups/Phone/a.jpg", "/Phone/a.jpg"}, "/Backups/Phone/", "/Backups/", "/Phonebook/")
		expectSentinel(mock)

		err := mg.DelEveryCopy(ses, asked, "")
		var locked *CopyUploadOnlyError
		if !errors.As(err, &locked) || locked.Folder != "/Backups/" {
			t.Fatalf("asked %s: %v, want CopyUploadOnlyError for /Backups/", asked, err)
		}
		metUpToSentinel(t, mock)
		if seen.ran("delete from") {
			t.Errorf("asked %s: something was deleted", asked)
		}
		if _, err := os.Stat(blobPath(h)); err != nil {
			t.Errorf("asked %s: the content went: %v", asked, err)
		}
		if got, want := plainText(err.Error()), "a copy is in the upload-only folder /Backups: no copy was deleted"; got != want {
			t.Errorf("English: %q, want %q", got, want)
		}
		if got := plainText(locked.Localized("es")); !strings.Contains(got, "/Backups") || !strings.Contains(got, "solo subida") {
			t.Errorf("Spanish: %q", got)
		}
	}
}

// plainText is s without the isolation marks a user argument gets.
func plainText(s string) string { return strings.NewReplacer("\u2068", "", "\u2069", "").Replace(s) }

// A copy deleted meanwhile (another request) is skipped, the rest go.
func TestDelEveryCopySkipsACopyGoneMeanwhile(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg, mock, _ := recordingMock(t)
	keptOut(mg)
	h := testHash("6")
	onDisk(t, h, true)
	expectCopiesOf(mock, "/b.jpg", h, []string{"/a.jpg", "/b.jpg"})
	mock.ExpectBegin()
	mock.ExpectQuery("select `hash` from `files` where `path` = \\?").WithArgs("/a.jpg").WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	expectLastDelFile(mock, "/b.jpg", h)
	expectSentinel(mock)

	if err := mg.DelEveryCopy(ses, "/b.jpg", ""); err != nil {
		t.Fatal(err)
	}
	metUpToSentinel(t, mock)
	if _, err := os.Stat(blobPath(h)); !os.IsNotExist(err) {
		t.Errorf("the content is still there: %v", err)
	}
}

// A folder's path is DelPath's, as before: the flag is ignored, and so is
// a hash (a folder has none), and an upload-only folder refuses it with
// the plain ErrUploadOnly.
func TestDelEveryCopyOfAFolderIsDelPath(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg, mock, seen := recordingMock(t)
	keptOut(mg)
	h := testHash("5")
	now := time.Now()
	byPath := "select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = \\?"
	mock.ExpectQuery(byPath).WithArgs("/Backups").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(byPath).WithArgs("/Backups").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` >= \\?").
		WillReturnRows(sqlmock.NewRows(fileCols).AddRow(h, "image/jpeg", now, now, "/Backups/a.jpg", 7))
	mock.ExpectQuery("select `path` from `upload_only_folders`").WillReturnRows(pathRows("/Backups/"))
	expectSentinel(mock)

	if err := mg.DelEveryCopy(ses, "/Backups", testHash("e")); !errors.Is(err, ErrUploadOnly) {
		t.Fatalf("an upload-only folder: %v, want ErrUploadOnly", err)
	}
	metUpToSentinel(t, mock)
	if seen.ran("select `path` from `files` where `hash`") {
		t.Error("a folder's copies were looked up")
	}
}

const reByPath = "select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = \\?"

// checkContentChanged fails the test unless err is a ContentChangedError
// worded in English and Spanish.
func checkContentChanged(t *testing.T, what string, err error) {
	t.Helper()
	var changed *ContentChangedError
	if !errors.As(err, &changed) {
		t.Fatalf("%s: %v, want ContentChangedError", what, err)
	}
	if got, want := err.Error(), "this file has changed since it was shown, so nothing was deleted: refresh and try again"; got != want {
		t.Errorf("%s: English %q, want %q", what, got, want)
	}
	if got := changed.Localized("es"); !strings.Contains(got, "no se ha eliminado nada") {
		t.Errorf("%s: Spanish %q", what, got)
	}
}

// Issue #201: the path holds other content than the client showed (synced
// over, edited): nothing is deleted - not the path, not a copy of either
// content - with or without every_copy, and the copies and upload-only
// folders aren't even looked up.
func TestDelRefusesContentChangedSinceShown(t *testing.T) {
	for _, everyCopy := range []bool{true, false} {
		_, ses := galleryTestEnv(t)
		mg, mock, seen := recordingMock(t)
		keptOut(mg)
		mg.searchTokens = newSearchTokenCache(10)
		shown, now := testHash("3"), testHash("4")
		onDisk(t, shown, true)
		onDisk(t, now, true)
		mock.ExpectQuery(reByPath).WithArgs("/Phone/a.jpg").WillReturnRows(fileRow(now, "/Phone/a.jpg"))
		expectSentinel(mock)

		var err error
		if everyCopy {
			err = mg.DelEveryCopy(ses, "/Phone/a.jpg", shown)
		} else {
			err = mg.DelPath(ses, "/Phone/a.jpg", shown)
		}
		what := fmt.Sprintf("every copy %v", everyCopy)
		checkContentChanged(t, what, err)
		metUpToSentinel(t, mock)
		for _, q := range []string{"delete from", "where `hash` = ? order by `path`", "upload_only_folders"} {
			if seen.ran(q) {
				t.Errorf("%s: ran %q", what, q)
			}
		}
		for _, h := range []string{shown, now} {
			if _, err := os.Stat(blobPath(h)); err != nil {
				t.Errorf("%s: content %s went: %v", what, h[:4], err)
			}
		}
		if _, deletes := mg.searchTokens.begin(); deletes != 0 {
			t.Errorf("%s: %d deletes counted", what, deletes)
		}
	}
}

// A single path whose content is the one shown is deleted as before.
func TestDelPathOfTheContentShownDeletes(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg, mock, _ := recordingMock(t)
	keptOut(mg)
	h := testHash("2")
	onDisk(t, h, true)
	mock.ExpectQuery(reByPath).WithArgs("/Phone/a.jpg").WillReturnRows(fileRow(h, "/Phone/a.jpg"))
	mock.ExpectQuery("select `path` from `upload_only_folders`").WillReturnRows(pathRows())
	expectLastDelFile(mock, "/Phone/a.jpg", h)
	expectSentinel(mock)

	if err := mg.DelPath(ses, "/Phone/a.jpg", h); err != nil {
		t.Fatal(err)
	}
	metUpToSentinel(t, mock)
	if _, err := os.Stat(blobPath(h)); !os.IsNotExist(err) {
		t.Errorf("the content is still there: %v", err)
	}
}

// A folder's path has no content of its own: a hash sent with it is
// ignored, through DelPath or DelEveryCopy, and the folder goes as before.
func TestDelOfAFolderIgnoresTheHash(t *testing.T) {
	for _, everyCopy := range []bool{true, false} {
		_, ses := galleryTestEnv(t)
		mg, mock, _ := recordingMock(t)
		keptOut(mg)
		h := testHash("1")
		onDisk(t, h, true)
		now := time.Now()
		mock.ExpectQuery(reByPath).WithArgs("/Trip").WillReturnError(sql.ErrNoRows)
		if everyCopy {
			mock.ExpectQuery(reByPath).WithArgs("/Trip").WillReturnError(sql.ErrNoRows)
		}
		mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` >= \\?").
			WillReturnRows(sqlmock.NewRows(fileCols).AddRow(h, "image/jpeg", now, now, "/Trip/a.jpg", 7))
		mock.ExpectQuery("select `path` from `upload_only_folders`").WillReturnRows(pathRows())
		expectLastDelFile(mock, "/Trip/a.jpg", h)
		expectSentinel(mock)

		var err error
		if everyCopy {
			err = mg.DelEveryCopy(ses, "/Trip", testHash("e"))
		} else {
			err = mg.DelPath(ses, "/Trip", testHash("e"))
		}
		if err != nil {
			t.Fatalf("every copy %v: %v", everyCopy, err)
		}
		metUpToSentinel(t, mock)
		if _, err := os.Stat(blobPath(h)); !os.IsNotExist(err) {
			t.Errorf("every copy %v: the content is still there: %v", everyCopy, err)
		}
	}
}

func TestOutermostFolder(t *testing.T) {
	folders := []string{"/A/B/", "/A/", "/C/"}
	for path, want := range map[string]string{
		"/A/B/x.jpg": "/A/", "/A/x.jpg": "/A/", "/C/D/x": "/C/", "/Ab/x": "", "/x": "",
	} {
		if got := outermostFolder(path, folders); got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}
}
