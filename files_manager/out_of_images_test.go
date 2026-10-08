// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"

	"github.com/alonsovidales/otc/dao"
	imagestagger "github.com/alonsovidales/otc/images_tagger"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// keptOut seeds mg's memory of the folders kept out of Images (issue
// #192) - none for a test that isn't about them, so it asks the database
// nothing about them.
func keptOut(mg *Manager, folders ...string) *Manager {
	if folders == nil {
		folders = []string{}
	}
	mg.outOfImagesCache.Store(&folders)
	return mg
}

// sqlSeen records every statement a mocked database was sent while an
// expectation was still waiting - expected or not - so a test can say a
// statement never ran: sqlmock only reports expectations left unmet, and
// a call it didn't expect is only an error to the code, which may just
// log it. Tests end their expectations with expectSentinel, so every
// statement is matched against something.
type sqlSeen struct {
	mu  sync.Mutex
	all []string
}

func (s *sqlSeen) Match(expectedSQL, actualSQL string) error {
	s.mu.Lock()
	s.all = append(s.all, actualSQL)
	s.mu.Unlock()
	return sqlmock.QueryMatcherRegexp.Match(expectedSQL, actualSQL)
}

func (s *sqlSeen) ran(fragment string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, q := range s.all {
		if strings.Contains(q, fragment) {
			return true
		}
	}
	return false
}

const cSentinel = "^sentinel: nothing runs this$"

func recordingMock(t *testing.T) (*Manager, sqlmock.Sqlmock, *sqlSeen) {
	t.Helper()
	seen := &sqlSeen{}
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(seen))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Manager{dao: dao.NewWithDB(db)}, mock, seen
}

// expectSentinel closes a test's expectations: see sqlSeen.
func expectSentinel(mock sqlmock.Sqlmock) { mock.ExpectExec(cSentinel) }

// metUpToSentinel is ExpectationsWereMet for a test ended by
// expectSentinel: in order, every expectation before it ran.
func metUpToSentinel(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()
	err := mock.ExpectationsWereMet()
	if err == nil || !strings.Contains(err.Error(), "sentinel") {
		t.Errorf("expected statements didn't all run: %v", err)
	}
}

func hashRows(hashes ...string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"hash"})
	for _, h := range hashes {
		rows.AddRow(h)
	}
	return rows
}

func pathRows(paths ...string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"path"})
	for _, p := range paths {
		rows.AddRow(p)
	}
	return rows
}

var fileCols = []string{"hash", "mime", "created", "modified", "path", "size"}

func fileRow(hash, path string) *sqlmock.Rows {
	now := time.Now()
	return sqlmock.NewRows(fileCols).AddRow(hash, "image/jpeg", now, now, path, 7)
}

const (
	reVisible     = "select `hash` from `files` where `hash` in \\([?,]+\\) and not \\(.* union select `hash` from `file_versions`"
	reVisibleAll  = "select `hash` from `files` where `hash` in \\([?,]+\\) union select `hash` from `file_versions`"
	reUnder       = "select `hash` from `files` where `hash` in \\([?,]+\\) and \\(\\(.* union select `hash` from `file_versions`"
	reMediaUnder  = "select `hash` from `files` where `path` >= \\?.* union select `hash` from `file_versions` where `path` >= "
	reByHash      = "select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `hash` = \\?"
	reReferenced  = "select \\(select count.* from `files` where `hash` = .* from `file_versions` where `hash`"
	reLoadFolders = "select `path` from `out_of_images_folders`"
)

// expectTake is TakeSkippedAnalysis finding found, moved to
// pending_analysis.
func expectTake(mock sqlmock.Sqlmock, found ...string) {
	mock.ExpectBegin()
	mock.ExpectQuery("select `hash` from `skipped_analysis` where `hash` in .* for update").WillReturnRows(hashRows(found...))
	if len(found) > 0 {
		mock.ExpectExec("insert ignore into `pending_analysis`").WillReturnResult(sqlmock.NewResult(0, int64(len(found))))
		mock.ExpectExec("delete from `skipped_analysis` where `hash` in").WillReturnResult(sqlmock.NewResult(0, int64(len(found))))
	}
	mock.ExpectCommit()
}

// expectDropped is dropAnalysisLocked for kept, whose content has no
// faces.
func expectDropped(mock sqlmock.Sqlmock, kept ...string) {
	args := []driver.Value{}
	for _, h := range kept {
		args = append(args, h)
	}
	mock.ExpectExec("insert ignore into `skipped_analysis`").WillReturnResult(sqlmock.NewResult(0, int64(len(kept))))
	mock.ExpectExec("delete from `file_tags` where `hash` in").WithArgs(args...).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("select `id`, `person_id`, `hash` from `faces` where `hash` in").WithArgs(args...).
		WillReturnRows(sqlmock.NewRows([]string{"id", "person_id", "hash"}))
}

// expectQueued is enqueueAnalysis of hash, its pending row already
// written by expectTake.
func expectQueued(mock sqlmock.Sqlmock, hash, path string) {
	mock.ExpectQuery(reByHash).WithArgs(hash).WillReturnRows(fileRow(hash, path))
}

// idleLanes gives mg lanes no worker takes from, to look at what was
// queued.
func idleLanes(mg *Manager) *mediaLanes {
	mg.lanes = &mediaLanes{fast: newLane(0, nil, nil), analysis: newLane(0, nil, nil)}
	mg.lanesOnce.Do(func() {})
	return mg.lanes
}

func testHash(c string) string { return strings.Repeat(c, 64) }

// onDisk writes a blob (and its thumbnail) for hash, gone at the end.
func onDisk(t *testing.T, hash string, thumbnail bool) {
	t.Helper()
	if err := os.WriteFile(blobPath(hash), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(blobPath(hash)) })
	if thumbnail {
		if err := os.WriteFile(blobPath(hash)+"_thumbnail", []byte("thumb"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Remove(blobPath(hash) + "_thumbnail") })
	}
}

// Showing a folder inside another one kept out is refused - the outer one
// still covers it - naming the nearest flagged folder above, and nothing
// is written; a folder with no flag and none above is a no-op.
func TestSetOutOfImagesRefusesToShowInsideAKeptOutFolder(t *testing.T) {
	mg, mock, _ := recordingMock(t)
	expectSentinel(mock)

	keptOut(mg, "/Photos/")
	err := mg.SetOutOfImages(nil, "/Photos/Trip", false)
	var bp *OutOfImagesByParentError
	if !errors.As(err, &bp) || bp.Parent != "/Photos/" || bp.OwnFlag {
		t.Fatalf("showing inside a kept-out folder: %v, want OutOfImagesByParentError for /Photos/", err)
	}
	if want := "Trip is inside /Photos, which is kept out of Images - show /Photos in Images to show Trip"; err.Error() != want {
		t.Errorf("message %q, want %q", err.Error(), want)
	}

	keptOut(mg, "/Photos/", "/Photos/Trip/")
	if err := mg.SetOutOfImages(nil, "/Photos/Trip/", false); !errors.As(err, &bp) || !bp.OwnFlag ||
		!strings.Contains(err.Error(), "show /Photos in Images first, then Trip") {
		t.Errorf("a flagged folder inside a kept-out one: %v, want refused with both steps", err)
	}

	keptOut(mg, "/Photos/", "/Photos/Trip/")
	if err := mg.SetOutOfImages(nil, "/Photos/Trip/Day1", false); !errors.As(err, &bp) || bp.Parent != "/Photos/Trip/" {
		t.Errorf("nested: %v, want /Photos/Trip/ named", err)
	}

	// Neither flagged nor covered ("/Photos/" doesn't cover "/Photoshop/").
	keptOut(mg, "/Photos/")
	if err := mg.SetOutOfImages(nil, "/Photoshop", false); err != nil {
		t.Errorf("showing a folder shown already: %v", err)
	}
	metUpToSentinel(t, mock)
}

func TestSetOutOfImagesRejectsBadPaths(t *testing.T) {
	mg := keptOut(&Manager{})
	for _, p := range []string{"", "/", "//", "Photos", "Photos/", "/a/../b", "/a/./b", "/a//b", "/a/\x00b", "/" + strings.Repeat("x", cMaxFolderPath)} {
		if err := mg.SetOutOfImages(nil, p, true); err == nil {
			t.Errorf("%q was accepted", p)
		}
	}
	for p, want := range map[string]string{"/Photos": "/Photos/", "/Photos/": "/Photos/", "/a b/Café (2020)": "/a b/Café (2020)/"} {
		if got, err := outOfImagesFolder(p); err != nil || got != want {
			t.Errorf("%q: %q, %v; want %q", p, got, err, want)
		}
	}
}

func TestUnderFolders(t *testing.T) {
	folders := []string{"/kim/", "/Private/Trip/"}
	cases := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{"/kim/a.jpg", false, true},
		{"/kimono/a.jpg", false, false},
		{"/kim", true, true},  // the folder itself, without its slash
		{"/kim/", true, true}, // and with it
		{"/kimono", true, false},
		{"/kim", false, false}, // a file named like the folder
		{"/private/Trip/a.jpg", false, false},
		{"/Private/Trip/Day1", true, true},
		{"/Private", true, false},
	}
	for _, c := range cases {
		if got := underFolders(c.path, c.isDir, folders); got != c.want {
			t.Errorf("underFolders(%q, %v) = %v", c.path, c.isDir, got)
		}
	}
}

func TestNearestFolderAbove(t *testing.T) {
	folders := []string{"/A/", "/A/B/", "/A/B/C/", "/Ab/"}
	for folder, want := range map[string]string{
		"/A/B/C/D/": "/A/B/C/",
		"/A/B/C/":   "/A/B/",
		"/A/B/":     "/A/",
		"/A/":       "",
		"/Ab/x/":    "/Ab/",
		"/Abc/":     "",
	} {
		if got := nearestFolderAbove(folder, folders); got != want {
			t.Errorf("nearestFolderAbove(%q) = %q, want %q", folder, got, want)
		}
	}
}

// A token holds a whole search's results: once a folder is kept out, a
// client still scrolling one would be handed its photos. Its rows not
// served yet lose the folder's; it goes on from where it is.
func TestSetOutOfImagesFiltersSearchTokens(t *testing.T) {
	mg, mock, _ := recordingMock(t)
	keptOut(mg)
	mg.searchTokens = newSearchTokenCache(1000)
	untouched := cursorOfPaths(0, "/Phone/x.jpg")
	mg.searchTokens.store("tok", cursorOfPaths(1, "/Private/0.jpg", "/Private/a.jpg", "/Phone/b.jpg", "/Private/Sub/c.jpg", "/Privateer/d.jpg"), time.Now(), 0)
	mg.searchTokens.store("other", untouched, time.Now(), 0)

	mock.ExpectExec("insert ignore into `out_of_images_folders`").WithArgs("/Private/").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(reLoadFolders).WillReturnRows(pathRows("/Private/"))
	mock.ExpectQuery(reMediaUnder).WillReturnRows(hashRows())
	expectSentinel(mock)
	if err := mg.SetOutOfImages(nil, "/Private", true); err != nil {
		t.Fatal(err)
	}
	if cur, ok := mg.searchTokens.load("tok"); !ok || restPaths(cur) != "/Phone/b.jpg,/Privateer/d.jpg" {
		t.Errorf("token after the flag: %v %q", ok, restPaths(cur))
	}
	if cur, _ := mg.searchTokens.load("other"); cur != untouched {
		t.Error("a token the flag doesn't touch was copied")
	}
	if mg.searchTokens.rows != 3 {
		t.Errorf("%d rows counted, want 3", mg.searchTokens.rows)
	}
	if got, _ := mg.OutOfImagesFolders(); len(got) != 1 || got[0] != "/Private/" {
		t.Errorf("folders in memory: %v", got)
	}
	metUpToSentinel(t, mock)
}

// The flagged folders can't be read back after the flag: every token goes.
func TestSetOutOfImagesDropsSearchTokensWhenFoldersUnknown(t *testing.T) {
	mg, mock, _ := recordingMock(t)
	keptOut(mg)
	mg.searchTokens = newSearchTokenCache(1000)
	mg.searchTokens.store("tok", cursorOfPaths(0, "/Phone/b.jpg"), time.Now(), 0)
	mock.ExpectExec("insert ignore into `out_of_images_folders`").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(reLoadFolders).WillReturnError(errors.New("gone away"))
	if err := mg.SetOutOfImages(nil, "/Private", true); err == nil {
		t.Fatal("no error")
	}
	if _, ok := mg.searchTokens.load("tok"); ok {
		t.Error("a search token outlived a flag whose folders are unknown")
	}
}

// fakeTagger is a Tagger whose Tags runs during a test's analysis.
type fakeTagger struct {
	calls  int
	during func()
}

func (f *fakeTagger) Tags(context.Context, image.Image, imagestagger.RAMOptions) ([]imagestagger.RAMTag, error) {
	f.calls++
	if f.during != nil {
		f.during()
	}
	return []imagestagger.RAMTag{{Name: "beach", Score: 0.9}}, nil
}

func jpegBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = 0x80
	}
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func readyTagger(mg *Manager, tg *fakeTagger) {
	mg.tagger = tg
	mg.taggerReady = make(chan struct{})
	close(mg.taggerReady)
}

// A photo uploaded under a folder kept out still gets its thumbnail, but
// no tags, no place tags and no face search: its analysis is recorded as
// skipped and its pending row cleared.
func TestKeptOutContentGetsAThumbnailButNoAnalysis(t *testing.T) {
	_, ses := galleryTestEnv(t)
	content := jpegBytes(t)
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	t.Cleanup(func() { os.Remove(blobPath(hash) + "_thumbnail") })

	mg, mock, seen := recordingMock(t)
	keptOut(mg, "/Private/")
	tg := &fakeTagger{}
	readyTagger(mg, tg)
	det := &countingDetector{}
	mg.faceRecognizer = det

	mock.ExpectQuery(reVisible).WillReturnRows(hashRows()) // only /Private/ holds it
	mock.ExpectQuery(reReferenced).WithArgs(hash, hash).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	mock.ExpectExec("insert ignore into `skipped_analysis`").WithArgs(hash, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(reReferenced).WithArgs(hash, hash).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1)) // dropIfOrphaned
	mock.ExpectExec("delete from `pending_analysis`").WithArgs(hash).WillReturnResult(sqlmock.NewResult(0, 1))
	expectSentinel(mock)

	file := &pb.File{Hash: hash, Mime: "image/jpeg", Path: "/Private/a.jpg"}
	mg.processMediaContent(ses, file, blobPath(hash), content)

	if !mg.hasThumbnail(hash) {
		t.Error("no thumbnail: Files shows it")
	}
	if tg.calls != 0 || det.calls != 0 {
		t.Errorf("analysed: %d tagging and %d face passes", tg.calls, det.calls)
	}
	for _, q := range []string{"file_tags", "image_tagging_enabled", "face_recognition_enabled"} {
		if seen.ran(q) {
			t.Errorf("%s was asked for or written", q)
		}
	}
	metUpToSentinel(t, mock)

	// The analysis lane's job for it: nothing read, nothing to do.
	mg, mock, _ = recordingMock(t)
	keptOut(mg, "/Private/")
	mock.ExpectQuery(reVisible).WillReturnRows(hashRows())
	mock.ExpectQuery(reReferenced).WithArgs(hash, hash).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	mock.ExpectExec("insert ignore into `skipped_analysis`").WillReturnResult(sqlmock.NewResult(0, 0))
	expectSentinel(mock)
	if !mg.processStoredStages(ses, &pb.File{Hash: hash, Mime: "image/jpeg", Path: "/Private/a.jpg"}, "/nonexistent", stageAnalysis) {
		t.Error("a skipped analysis reported a failure")
	}
	metUpToSentinel(t, mock)
}

// The same content held outside the folders kept out too is analysed, and
// forgotten as skipped.
func TestContentShownElsewhereIsAnalysed(t *testing.T) {
	mg, mock, _ := recordingMock(t)
	keptOut(mg, "/Private/")
	h := testHash("a")
	mock.ExpectQuery(reVisible).WillReturnRows(hashRows(h)) // /Phone/ holds it too
	mock.ExpectExec("delete from `skipped_analysis` where `hash` in").WithArgs(h).WillReturnResult(sqlmock.NewResult(0, 0))
	expectSentinel(mock)
	if got := mg.guardAnalysis(h, stageAll); got != stageAll {
		t.Errorf("stages %v, want everything", got)
	}
	// With no folder kept out nothing is asked at all.
	keptOut(mg)
	if got := mg.guardAnalysis(h, stageAnalysis); got != stageAnalysis {
		t.Errorf("no folders: stages %v", got)
	}
	metUpToSentinel(t, mock)
}

// Keeping a folder out deletes the tags and faces of what only it holds,
// and records it as skipped, but not of content shown elsewhere; the
// faces leave the matching set.
func TestKeepingOutDropsAnalysisOfContentOnlyThere(t *testing.T) {
	mg, mock, seen := recordingMock(t)
	keptOut(mg)
	only, both := testHash("a"), testHash("b")
	mg.faceRefs = refsOf(map[string][][]float32{"p1": {unit(0, nil), unit(1, nil)}, "p2": {unit(2, nil)}})
	gone := mg.faceRefs["p1"].refs[0].id

	mock.ExpectExec("insert ignore into `out_of_images_folders`").WithArgs("/Private/").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(reLoadFolders).WillReturnRows(pathRows("/Private/"))
	mock.ExpectQuery(reMediaUnder).WithArgs("/Private/", "/Private/", "/Private/%", "/Private/", "/Private/", "/Private/%").
		WillReturnRows(hashRows(only, both))
	mock.ExpectQuery(reVisible).WillReturnRows(hashRows(both))
	mock.ExpectExec("insert ignore into `skipped_analysis`").WithArgs(only, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("delete from `file_tags` where `hash` in").WithArgs(only).WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectQuery("select `id`, `person_id`, `hash` from `faces` where `hash` in").WithArgs(only).
		WillReturnRows(sqlmock.NewRows([]string{"id", "person_id", "hash"}).AddRow(gone, "p1", only))
	mock.ExpectBegin()
	mock.ExpectExec("update `people` set `cover_face_id` = null where `cover_face_id` in").WithArgs(only).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("delete from `faces` where `hash` in").WithArgs(only).WillReturnResult(sqlmock.NewResult(0, 1))
	// Unnamed and left with no face: goes; a named one stays (the query's
	// `name` = '').
	mock.ExpectExec("delete from `people` where `id` in \\(\\?\\) and `name` = ''").WithArgs("p1").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	expectSentinel(mock)

	if err := mg.SetOutOfImages(nil, "/Private", true); err != nil {
		t.Fatal(err)
	}
	metUpToSentinel(t, mock)
	if seen.ran("delete from `file_tags` where `hash` in (?,?)") {
		t.Error("the tags of content shown elsewhere went too")
	}
	for _, r := range mg.faceRefs["p1"].refs {
		if r.id == gone {
			t.Error("a deleted face is still a reference")
		}
	}
	if !mg.faceRefsStale["p1"] || mg.faceRefsStale["p2"] {
		t.Errorf("stale people: %v, want p1 only", mg.faceRefsStale)
	}
}

// Showing a folder again hands what it skipped back to the analysis:
// pending rows, the analysis lane with a thumbnail, the fast lane without.
// A subfolder with its own flag stays out.
func TestShowingInImagesQueuesSkippedContent(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg, mock, _ := recordingMock(t)
	keptOut(mg, "/Private/", "/Private/Sub/")
	lanes := idleLanes(mg)
	thumbed, bare, sub := testHash("a"), testHash("b"), testHash("c")
	onDisk(t, thumbed, true)

	mock.ExpectExec("delete from `out_of_images_folders` where `path` = \\?").WithArgs("/Private/").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(reLoadFolders).WillReturnRows(pathRows("/Private/Sub/"))
	mock.ExpectQuery(reMediaUnder).WillReturnRows(hashRows(thumbed, bare, sub))
	mock.ExpectQuery(reVisible).WithArgs(thumbed, bare, sub, "/Private/Sub/", "/Private/Sub/", "/Private/Sub/%", thumbed, bare, sub, "/Private/Sub/", "/Private/Sub/", "/Private/Sub/%").
		WillReturnRows(hashRows(thumbed, bare))
	expectTake(mock, thumbed, bare)
	expectQueued(mock, thumbed, "/Private/a.jpg")
	expectQueued(mock, bare, "/Private/b.jpg")
	expectSentinel(mock)

	if err := mg.SetOutOfImages(ses, "/Private/", false); err != nil {
		t.Fatal(err)
	}
	metUpToSentinel(t, mock)
	if len(lanes.analysis.jobs) != 1 || lanes.analysis.jobs[0].file.Hash != thumbed {
		t.Errorf("analysis lane: %+v", lanes.analysis.jobs)
	}
	if len(lanes.fast.jobs) != 1 || lanes.fast.jobs[0].file.Hash != bare {
		t.Errorf("fast lane: %+v", lanes.fast.jobs)
	}
}

// Showing a folder again moves its skipped content to pending_analysis in
// one transaction before anything is queued: when the move fails nothing
// is queued and the content stays recorded as skipped (the transaction is
// rolled back), for the next try or the next start - never in neither.
func TestShowingInImagesKeepsSkippedWhenTheMoveFails(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg, mock, seen := recordingMock(t)
	keptOut(mg, "/Private/")
	lanes := idleLanes(mg)
	h := testHash("4")

	mock.ExpectExec("delete from `out_of_images_folders` where `path` = \\?").WithArgs("/Private/").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(reLoadFolders).WillReturnRows(pathRows())
	mock.ExpectQuery(reMediaUnder).WillReturnRows(hashRows(h))
	mock.ExpectQuery(reVisibleAll).WillReturnRows(hashRows(h))
	mock.ExpectBegin()
	mock.ExpectQuery("select `hash` from `skipped_analysis` where `hash` in .* for update").WillReturnRows(hashRows(h))
	mock.ExpectExec("insert ignore into `pending_analysis`").WillReturnError(errors.New("lock wait timeout"))
	mock.ExpectRollback()
	expectSentinel(mock)

	if err := mg.SetOutOfImages(ses, "/Private", false); err == nil {
		t.Fatal("no error")
	}
	metUpToSentinel(t, mock)
	if seen.ran("delete from `skipped_analysis`") {
		t.Error("the skipped row went without its pending row")
	}
	if len(lanes.analysis.jobs)+len(lanes.fast.jobs) != 0 {
		t.Error("queued without a pending row")
	}
}

// Content handed back whose file isn't a photo or video has nothing to
// analyse: its pending row goes, and nothing is queued.
func TestHandBackOfNonMediaClearsItsPendingRow(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg, mock, _ := recordingMock(t)
	lanes := idleLanes(mg)
	h := testHash("7")
	now := time.Now()
	expectTake(mock, h)
	mock.ExpectQuery(reByHash).WithArgs(h).WillReturnRows(sqlmock.NewRows(fileCols).AddRow(h, "text/plain", now, now, "/Private/notes.txt", 7))
	mock.ExpectExec("delete from `pending_analysis` where `hash` = \\?").WithArgs(h).WillReturnResult(sqlmock.NewResult(0, 1))
	expectSentinel(mock)
	if err := mg.handBackLocked(ses, []string{h}); err != nil {
		t.Fatal(err)
	}
	metUpToSentinel(t, mock)
	if len(lanes.analysis.jobs)+len(lanes.fast.jobs) != 0 {
		t.Error("a file with nothing to analyse was queued")
	}
}

// expectDelFile is DelFile's delete of path holding hash (others: how
// many other files hold it), with no versions, its content still used.
func expectDelFile(mock sqlmock.Sqlmock, path, hash string, others int) {
	mock.ExpectBegin()
	mock.ExpectQuery("select `hash` from `files` where `path` = \\?").WithArgs(path).WillReturnRows(hashRows(hash))
	mock.ExpectQuery("select count\\(\\*\\) from `files` where `hash` = \\?").WithArgs(hash).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(others + 1))
	mock.ExpectQuery("select count\\(\\*\\) from `file_versions` where `hash` = \\?").WithArgs(hash).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectExec("delete from `files` where `path` = \\?").WithArgs(path).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery("select `hash` from `file_versions` where `path` = \\?").WithArgs(path).WillReturnRows(hashRows())
	mock.ExpectQuery(reReferenced).WithArgs(hash, hash).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(others))
}

// A move out of a folder kept out (a link at the new path, then a delete
// of the old one) analyses the content once, and drops nothing.
func TestMoveOutOfKeptOutFolder(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg, mock, seen := recordingMock(t)
	keptOut(mg, "/Private/")
	lanes := idleLanes(mg)
	h := testHash("d")
	onDisk(t, h, true)

	mock.ExpectQuery(reByHash).WithArgs(h).WillReturnRows(fileRow(h, "/Private/x.jpg"))
	mock.ExpectExec("insert into `files`").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(reVisible).WillReturnRows(hashRows(h))
	mock.ExpectQuery(reUnder).WillReturnRows(hashRows(h))
	expectTake(mock, h)
	expectQueued(mock, h, "/Private/x.jpg")
	expectDelFile(mock, "/Private/x.jpg", h, 1)
	mock.ExpectQuery(reVisible).WillReturnRows(hashRows(h))
	mock.ExpectQuery(reUnder).WillReturnRows(hashRows())
	expectTake(mock) // already handed back
	expectSentinel(mock)

	if _, err := mg.LinkFile(ses, "/Photos/x.jpg", h, false, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := mg.DelFile(ses, "/Private/x.jpg"); err != nil {
		t.Fatal(err)
	}
	metUpToSentinel(t, mock)
	if len(lanes.analysis.jobs) != 1 || len(lanes.fast.jobs) != 0 {
		t.Errorf("queued %d analyses and %d thumbnails, want one analysis", len(lanes.analysis.jobs), len(lanes.fast.jobs))
	}
	if seen.ran("delete from `file_tags` where `hash` in") || seen.ran("insert ignore into `skipped_analysis`") {
		t.Error("content moved out lost its analysis")
	}
}

// A move into a folder kept out: once the shown path goes, the content is
// kept out, and its tags and faces go.
func TestMoveIntoKeptOutFolder(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg, mock, _ := recordingMock(t)
	keptOut(mg, "/Private/")
	lanes := idleLanes(mg)
	h := testHash("e")
	onDisk(t, h, true)

	mock.ExpectQuery(reByHash).WithArgs(h).WillReturnRows(fileRow(h, "/Photos/x.jpg"))
	mock.ExpectExec("insert into `files`").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(reVisible).WillReturnRows(hashRows(h)) // /Photos/x.jpg still there
	mock.ExpectQuery(reUnder).WillReturnRows(hashRows(h))
	expectTake(mock)
	expectDelFile(mock, "/Photos/x.jpg", h, 1)
	mock.ExpectQuery(reVisible).WillReturnRows(hashRows())
	mock.ExpectQuery(reUnder).WillReturnRows(hashRows(h))
	expectDropped(mock, h)
	expectSentinel(mock)

	if _, err := mg.LinkFile(ses, "/Private/x.jpg", h, false, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := mg.DelFile(ses, "/Photos/x.jpg"); err != nil {
		t.Fatal(err)
	}
	metUpToSentinel(t, mock)
	if len(lanes.analysis.jobs)+len(lanes.fast.jobs) != 0 {
		t.Error("content moved into a folder kept out was queued")
	}
}

// Known, processed content uploaded to a shown path skips processing as
// before - and still goes back to the analysis when it was kept out, for
// UploadFile and the chunked upload alike.
func TestKnownContentUploadedToShownPathIsAnalysed(t *testing.T) {
	_, ses := galleryTestEnv(t)
	content := []byte("\xff\xd8\xff\xe0 known content, kept out until now")
	sum := sha256.Sum256(content)
	h := hex.EncodeToString(sum[:])
	onDisk(t, h, true)

	expect := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(reReferenced).WithArgs(h, h).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1)) // contentKnown
		mock.ExpectExec("insert into `files`").WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectQuery(reVisible).WillReturnRows(hashRows(h))
		mock.ExpectQuery(reUnder).WillReturnRows(hashRows(h))
		expectTake(mock, h)
		expectQueued(mock, h, "/Private/k.jpg")
		expectSentinel(mock)
	}

	mg, mock, _ := recordingMock(t)
	keptOut(mg, "/Private/")
	lanes := idleLanes(mg)
	expect(mock)
	if _, err := mg.UploadFile(ses, "/Photos/k.jpg", content, false, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	metUpToSentinel(t, mock)
	if len(lanes.analysis.jobs) != 1 || len(lanes.fast.jobs) != 0 {
		t.Errorf("UploadFile: %d analyses, %d thumbnails queued", len(lanes.analysis.jobs), len(lanes.fast.jobs))
	}

	mg, mock, _ = recordingMock(t)
	keptOut(mg, "/Private/")
	lanes = idleLanes(mg)
	expect(mock)
	id, err := mg.BeginUpload(ses, "/Photos/k2.jpg", int64(len(content)), false, nil, nil, "", t)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mg.UploadChunk(ses, id, 0, content); err != nil {
		t.Fatal(err)
	}
	if _, err := mg.FinishUpload(ses, id, h); err != nil {
		t.Fatal(err)
	}
	metUpToSentinel(t, mock)
	if len(lanes.analysis.jobs) != 1 {
		t.Errorf("FinishUpload: %d analyses queued", len(lanes.analysis.jobs))
	}
}

// Overriding the shown copy of content whose only other path is kept out
// keeps that content out: its tags and faces go.
func TestOverrideLeavingContentKeptOut(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg, mock, _ := recordingMock(t)
	keptOut(mg, "/Private/")
	newHash, oldHash := testHash("1"), testHash("2")
	onDisk(t, newHash, true)
	now := time.Now()

	mock.ExpectQuery(reByHash).WithArgs(newHash).WillReturnRows(fileRow(newHash, "/elsewhere/n.jpg"))
	mock.ExpectExec("insert into `files`").WillReturnError(errDuplicate())
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = \\?").WithArgs("/Photos/a.jpg").
		WillReturnRows(sqlmock.NewRows(fileCols).AddRow(oldHash, "image/jpeg", now, now, "/Photos/a.jpg", 5))
	mock.ExpectQuery("select `path` from `upload_only_folders`").WillReturnRows(pathRows())
	mock.ExpectBegin()
	mock.ExpectQuery("select `hash` from `files` where `path` = \\? for update").WillReturnRows(hashRows(oldHash))
	mock.ExpectQuery("select count\\(\\*\\) from `files` where `hash` = \\? for update").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(2))
	mock.ExpectQuery("select count\\(\\*\\) from `file_versions` where `hash` = \\?").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectExec("update `files` set").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(reReferenced).WithArgs(oldHash, oldHash).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1)) // /Private/a.jpg
	mock.ExpectQuery(reVisible).WillReturnRows(hashRows(newHash))
	mock.ExpectQuery(reUnder).WillReturnRows(hashRows(oldHash))
	expectDropped(mock, oldHash)
	expectTake(mock)
	expectSentinel(mock)

	if _, err := mg.LinkFile(ses, "/Photos/a.jpg", newHash, true, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	metUpToSentinel(t, mock)
}

// Kept versions count on both sides: the visibility queries read
// file_versions as well as files, so a version outside the folders kept
// out keeps its content shown, and one inside counts as kept out (the
// SQL itself is checked against MySQL in dao).
func TestVersionsCountForVisibility(t *testing.T) {
	mg, mock, seen := recordingMock(t)
	keptOut(mg, "/Private/")
	h := testHash("f")
	mock.ExpectQuery(reVisible).WillReturnRows(hashRows(h)) // a version outside
	mock.ExpectQuery(reUnder).WillReturnRows(hashRows(h))
	expectSentinel(mock)
	mg.reconcileAnalysis(nil, []string{h})
	metUpToSentinel(t, mock)
	if seen.ran("skipped_analysis") {
		t.Error("content a version shows was kept out")
	}

	mg, mock, _ = recordingMock(t)
	keptOut(mg, "/Private/")
	mock.ExpectQuery(reVisible).WillReturnRows(hashRows())
	mock.ExpectQuery(reUnder).WillReturnRows(hashRows(h)) // a version inside
	expectDropped(mock, h)
	expectSentinel(mock)
	mg.reconcileAnalysis(nil, []string{h})
	metUpToSentinel(t, mock)
}

// A folder kept out while its only photo is being analysed: what the
// analysis wrote after the cleanup goes again once it returns.
func TestFlagDuringAnalysisDropsWhatItWrote(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg, mock, _ := recordingMock(t)
	keptOut(mg)
	h := testHash("7")
	tg := &fakeTagger{during: func() {
		if err := mg.SetOutOfImages(ses, "/Private", true); err != nil {
			t.Errorf("keeping the folder out: %v", err)
		}
	}}
	readyTagger(mg, tg)

	mock.ExpectQuery("select `image_tagging_enabled` from `settings`").WillReturnRows(sqlmock.NewRows([]string{"e"}).AddRow(true))
	// The flag, while the tagger runs.
	mock.ExpectExec("insert ignore into `out_of_images_folders`").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(reLoadFolders).WillReturnRows(pathRows("/Private/"))
	mock.ExpectQuery(reMediaUnder).WillReturnRows(hashRows(h))
	mock.ExpectQuery(reVisible).WillReturnRows(hashRows())
	expectDropped(mock, h)
	// The analysis writes its tags after the cleanup...
	mock.ExpectExec("insert into `file_tags`").WillReturnResult(sqlmock.NewResult(1, 1))
	// ...and the check once it is done drops them.
	mock.ExpectQuery(reVisible).WillReturnRows(hashRows())
	mock.ExpectQuery(reUnder).WillReturnRows(hashRows(h))
	expectDropped(mock, h)
	mock.ExpectQuery(reReferenced).WithArgs(h, h).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1)) // dropIfOrphaned
	expectSentinel(mock)

	file := &pb.File{Hash: h, Mime: "image/jpeg", Path: "/Private/p.jpg"}
	mg.processMedia(ses, file, blobPath(h), jpegBytes(t), stageAnalysis)
	if tg.calls != 1 {
		t.Fatalf("%d tagging passes", tg.calls)
	}
	metUpToSentinel(t, mock)
}

// The guard's decide-and-record and an unflag are one after the other,
// never interleaved, and either order ends with the content analysed: an
// unflag after the guard skipped it finds it recorded and queues it.
func TestShowDuringSkipDecisionIsNotLost(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg, mock, _ := recordingMock(t)
	keptOut(mg, "/Private/")
	lanes := idleLanes(mg)
	h := testHash("8")
	onDisk(t, h, true)

	// The guard waits for whoever holds the decision.
	mg.outOfImagesMu.Lock()
	mock.ExpectQuery(reVisible).WillReturnRows(hashRows())
	mock.ExpectQuery(reReferenced).WithArgs(h, h).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	mock.ExpectExec("insert ignore into `skipped_analysis`").WithArgs(h, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	// Then the unflag finds what the guard recorded.
	mock.ExpectExec("delete from `out_of_images_folders` where `path` = \\?").WithArgs("/Private/").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(reLoadFolders).WillReturnRows(pathRows())
	mock.ExpectQuery(reMediaUnder).WillReturnRows(hashRows(h))
	mock.ExpectQuery(reVisibleAll).WillReturnRows(hashRows(h))
	expectTake(mock, h)
	expectQueued(mock, h, "/Private/p.jpg")
	expectSentinel(mock)

	decided := make(chan mediaStages)
	go func() { decided <- mg.guardAnalysis(h, stageAnalysis) }()
	select {
	case <-decided:
		t.Fatal("the guard decided while the decision was held")
	case <-time.After(50 * time.Millisecond):
	}
	mg.outOfImagesMu.Unlock()
	if got := <-decided; got != 0 {
		t.Fatalf("stages %v, want the analysis skipped", got)
	}
	if err := mg.SetOutOfImages(ses, "/Private", false); err != nil {
		t.Fatal(err)
	}
	metUpToSentinel(t, mock)
	if len(lanes.analysis.jobs) != 1 {
		t.Errorf("%d analyses queued, want the skipped one", len(lanes.analysis.jobs))
	}
}

// Deleting a folder clears its flags, and those under it; deleting its
// files one by one (a sync client) keeps them.
func TestDelPathClearsFlagsOfDeletedFolder(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg, mock, _ := recordingMock(t)
	keptOut(mg, "/Trip/", "/Trip/Day1/", "/Other/")
	h := testHash("9")
	now := time.Now()

	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = \\?").WithArgs("/Trip").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` >= \\?").
		WillReturnRows(sqlmock.NewRows(fileCols).AddRow(h, "image/jpeg", now, now, "/Trip/Day1/a.jpg", 7))
	mock.ExpectQuery("select `path` from `upload_only_folders`").WillReturnRows(pathRows())
	expectDelFile(mock, "/Trip/Day1/a.jpg", h, 1)
	mock.ExpectExec("delete from `out_of_images_folders` where `path` >= \\?").WithArgs("/Trip/", "/Trip/", "/Trip/%").WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectQuery(reLoadFolders).WillReturnRows(pathRows("/Other/"))
	mock.ExpectQuery(reVisible).WillReturnRows(hashRows(h)) // a copy elsewhere
	mock.ExpectQuery(reUnder).WillReturnRows(hashRows())
	expectTake(mock)
	expectSentinel(mock)

	if err := mg.DelPath(ses, "/Trip"); err != nil {
		t.Fatal(err)
	}
	metUpToSentinel(t, mock)
	if got, _ := mg.OutOfImagesFolders(); len(got) != 1 || got[0] != "/Other/" {
		t.Errorf("flags left: %v, want /Other/ only", got)
	}
}

// A file row can share a flagged folder's path without the slash
// ("/Docs/Private" next to "/Docs/Private/"): deleting the file through
// DelPath leaves the folder's flag alone.
func TestDelPathOfFileKeepsSameNamedFolderFlag(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg, mock, seen := recordingMock(t)
	keptOut(mg, "/Docs/Private/")
	h := testHash("9")
	now := time.Now()
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = \\?").WithArgs("/Docs/Private").
		WillReturnRows(sqlmock.NewRows(fileCols).AddRow(h, "application/octet-stream", now, now, "/Docs/Private", 7))
	mock.ExpectQuery("select `path` from `upload_only_folders`").WillReturnRows(pathRows())
	expectDelFile(mock, "/Docs/Private", h, 1)
	mock.ExpectQuery(reVisible).WillReturnRows(hashRows(h)) // a copy elsewhere
	mock.ExpectQuery(reUnder).WillReturnRows(hashRows())
	expectTake(mock)
	expectSentinel(mock)
	if err := mg.DelPath(ses, "/Docs/Private"); err != nil {
		t.Fatal(err)
	}
	metUpToSentinel(t, mock)
	if seen.ran("out_of_images_folders") {
		t.Error("deleting a file touched the flags")
	}
	if got, _ := mg.OutOfImagesFolders(); len(got) != 1 || got[0] != "/Docs/Private/" {
		t.Errorf("flags: %v", got)
	}
}

func TestDelFileKeepsFlag(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg, mock, seen := recordingMock(t)
	keptOut(mg, "/Trip/")
	h := testHash("5")
	expectDelFile(mock, "/Trip/a.jpg", h, 1)
	mock.ExpectQuery(reVisible).WillReturnRows(hashRows(h))
	mock.ExpectQuery(reUnder).WillReturnRows(hashRows())
	expectTake(mock)
	expectSentinel(mock)
	if err := mg.DelFile(ses, "/Trip/a.jpg"); err != nil {
		t.Fatal(err)
	}
	metUpToSentinel(t, mock)
	if seen.ran("out_of_images_folders") {
		t.Error("deleting a file touched the flags")
	}
	if got, _ := mg.OutOfImagesFolders(); len(got) != 1 {
		t.Errorf("flags: %v", got)
	}
}

// At the first sign-in: a cleanup a crash cut short is finished, skipped
// content shown again goes to pending_analysis (ResumePendingAnalysis
// queues it next), and skipped rows of content that is gone are dropped.
func TestReconcileOutOfImagesAtSignIn(t *testing.T) {
	mg, mock, _ := recordingMock(t)
	keptOut(mg, "/Private/", "/Private/Sub/")
	still, shownNow, deleted, both := testHash("a"), testHash("c"), testHash("d"), testHash("b")

	// Only the outermost folder is walked.
	mock.ExpectQuery(reMediaUnder).WithArgs("/Private/", "/Private/", "/Private/%", "/Private/", "/Private/", "/Private/%").
		WillReturnRows(hashRows(still, both))
	mock.ExpectQuery(reVisible).WillReturnRows(hashRows(both))
	expectDropped(mock, still)
	mock.ExpectQuery("select `hash` from `skipped_analysis`").WillReturnRows(hashRows(still, shownNow, deleted))
	mock.ExpectQuery(reVisible).WillReturnRows(hashRows(shownNow))
	mock.ExpectQuery(reUnder).WillReturnRows(hashRows(still))
	mock.ExpectExec("delete from `skipped_analysis` where `hash` in").WithArgs(deleted).WillReturnResult(sqlmock.NewResult(0, 1))
	expectTake(mock, shownNow)
	expectSentinel(mock)

	mg.ReconcileOutOfImages(faceTestSession(t))
	metUpToSentinel(t, mock)
}

// Shared galleries: from a collection, its members kept out of Images are
// left out; from a folder, the folders kept out below it are, but not the
// folder itself when it is kept out (or inside one); from a selection of
// files, nothing is.
func TestSharedGallerySourcesLeaveOutKeptOutFolders(t *testing.T) {
	mg, mock, seen := recordingMock(t)
	keptOut(mg, "/Private/", "/Album/Private/")
	now := time.Now()
	rows := func() *sqlmock.Rows {
		return sqlmock.NewRows(fileCols).AddRow(testHash("a"), "image/jpeg", now, now, "/Album/a.jpg", 7)
	}
	outside := func(folders ...string) []driver.Value {
		var args []driver.Value
		for _, f := range folders {
			args = append(args, f, f, f+"%")
		}
		return args
	}

	mock.ExpectQuery("select `f`.`hash`.* join `image_group_files`").
		WithArgs(append([]driver.Value{"g1"}, outside("/Album/Private/", "/Private/")...)...).WillReturnRows(rows())
	mock.ExpectQuery("select `f`.`hash`.* from `files` as `f` where").
		WithArgs(append([]driver.Value{"/Album/", "/Album/", "/Album/%", "/Album/%/%"}, outside("/Album/Private/", "/Private/")...)...).WillReturnRows(rows())
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` >= ").WillReturnRows(rows())
	mock.ExpectQuery("select `f`.`hash`.* from `files` as `f` where").
		WithArgs(append([]driver.Value{"/Private/Trip/", "/Private/Trip/", "/Private/Trip/%", "/Private/Trip/%/%"}, outside("/Album/Private/")...)...).WillReturnRows(rows())
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` >= ").WillReturnRows(rows())
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = \\?").WithArgs("/Private/x.jpg").
		WillReturnRows(sqlmock.NewRows(fileCols).AddRow(testHash("b"), "image/jpeg", now, now, "/Private/x.jpg", 7))
	expectSentinel(mock)

	for _, src := range []*pb.SharedGallerySource{
		{GroupId: "g1"},
		{Directory: "/Album"},
		{Directory: "/Private/Trip/"},
		{Paths: []string{"/Private/x.jpg"}},
	} {
		if files, _, err := mg.sharedGallerySource(src); err != nil || len(files) != 1 {
			t.Errorf("%v: %d files, %v", src, len(files), err)
		}
	}
	metUpToSentinel(t, mock)
	if seen.ran(reLoadFolders) {
		t.Error("the folders were read again rather than from memory")
	}

	if got := foldersNotCovering("/Private/Trip", []string{"/Private/", "/Private/Trip/", "/Private/Trip/Day1/", "/Other/"}); strings.Join(got, ",") != "/Private/Trip/Day1/,/Other/" {
		t.Errorf("foldersNotCovering: %v", got)
	}
}

// What Images searches is never what is kept out, and a search fails
// rather than show it when the folders can't be read.
func TestImageSearchFailsClosed(t *testing.T) {
	mg, mock, _ := recordingMock(t)
	mg.searchTokens = newSearchTokenCache(10)
	mock.ExpectQuery(reLoadFolders).WillReturnError(errors.New("connection lost"))
	expectSentinel(mock)
	if _, _, err := mg.ImageSearch(nil, "", nil, "", false, nil, "", nil, 0, 0, false); err == nil {
		t.Error("a search ran without knowing what is kept out")
	}
	metUpToSentinel(t, mock)
}

// The listing says which entries are, or are inside, a folder kept out.
func TestAnnotateListingMarksKeptOutEntries(t *testing.T) {
	files := []*pb.File{
		{Path: "/Private", Mime: "inode/directory"},
		{Path: "/Private/a.jpg", Mime: "image/jpeg", Hash: testHash("a")},
		{Path: "/Privateer", Mime: "inode/directory"},
		{Path: "/Photos/b.jpg", Mime: "image/jpeg", Hash: testHash("b")},
	}
	annotateListing(files, nil, []string{"/Private/"}, nil)
	for i, want := range []bool{true, true, false, false} {
		if files[i].OutOfImages != want {
			t.Errorf("%s: out_of_images %v", files[i].Path, files[i].OutOfImages)
		}
		if files[i].UploadOnly {
			t.Errorf("%s: upload only", files[i].Path)
		}
	}
}

// errDuplicate is MySQL's duplicate key error, as StoreNewFile sees it.
func errDuplicate() error { return &mysql.MySQLError{Number: 1062} }
