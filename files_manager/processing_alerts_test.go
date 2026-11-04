// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"database/sql/driver"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gabriel-vasile/mimetype"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
)

// Samples of what the owner's alert named: a Windows cursor (sniffed as
// image/x-icon, like an .ico) and a DjVu scan.
var (
	cursorBytes = append([]byte{0, 0, 2, 0, 1, 0, 32, 32, 0, 0, 1, 0, 1, 0, 0x30, 0, 0, 0, 22, 0, 0, 0}, bytes.Repeat([]byte{0x11}, 48)...)
	djvuBytes   = append([]byte("AT&TFORM\x00\x00\x00\x30DJVUINFO\x00\x00\x00\x0a"), bytes.Repeat([]byte{0}, 40)...)
	heicJunk    = append([]byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic"), bytes.Repeat([]byte{0x42}, 64)...)
	// A HEIC whose major brand mimetype doesn't list as one (MiHE, from
	// some Apple exports): it sniffs as video/mp4.
	heicOddBrand = append([]byte("\x00\x00\x00\x18ftypMiHE\x00\x00\x00\x00MiHEheic"), bytes.Repeat([]byte{0x42}, 64)...)
)

// corruptJPEG is a JPEG cut to its first bytes: a real photo format that
// fails to decode.
func corruptJPEG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 64, 64)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()[:40]
}

// noFFmpeg makes decodeStill's ffmpeg fallback fail (ffmpeg would read a
// truncated JPEG), counting the calls.
func noFFmpeg(t *testing.T) *int {
	t.Helper()
	calls := 0
	orig := stillFFmpeg
	stillFFmpeg = func([]byte) (image.Image, error) {
		calls++
		return nil, errors.New("ffmpeg: invalid data")
	}
	t.Cleanup(func() { stillFFmpeg = orig })
	return &calls
}

// expectAlert is AddErrorNotification raising "<name> <what>" as a new
// Alerts row (with its keys: msg_key, msg_args, msg_lines).
func expectAlert(mock sqlmock.Sqlmock, title string) {
	mock.ExpectBegin()
	mock.ExpectQuery("select `uuid`, coalesce\\(length\\(`details`\\), 0\\), `msg_key`, `msg_lines` from `notifications`").
		WillReturnRows(sqlmock.NewRows([]string{"uuid", "size", "msg_key", "msg_lines"}))
	mock.ExpectExec("insert into `notifications`").WithArgs(sqlmock.AnyArg(), "", title, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
}

// expectNoAlert ends a test's expectations with the start of an alert,
// matched out of order - an alert comes before processMedia's deferred
// statements - so noAlert can check it is the one thing that didn't
// happen.
func expectNoAlert(mock sqlmock.Sqlmock) {
	mock.MatchExpectationsInOrder(false)
	mock.ExpectBegin()
}

func noAlert(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()
	err := mock.ExpectationsWereMet()
	if err == nil {
		t.Fatal("an alert was raised")
	}
	if !strings.Contains(err.Error(), "ExpectedBegin") {
		t.Errorf("expected statements didn't all run: %v", err)
	}
}

func allMet(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expected statements didn't all run: %v", err)
	}
}

// storedFile writes content as hash's encrypted blob, gone (with any
// thumbnail) at the end of the test.
func storedFile(t *testing.T, ses *session.Session, hash, path, mime string, content []byte) *pb.File {
	t.Helper()
	w, err := blobstore.Create(blobPath(hash), ses)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(content)
	if err := w.Commit(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Remove(blobPath(hash))
		removeThumbnails(blobPath(hash))
	})
	return &pb.File{Hash: hash, Path: path, Mime: mime}
}

func referenced(mock sqlmock.Sqlmock, hash string) {
	mock.ExpectQuery(reReferenced).WithArgs(hash, hash).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
}

func pendingCleared(mock sqlmock.Sqlmock, hash string) {
	mock.ExpectExec("delete from `pending_analysis`").WithArgs(hash).WillReturnResult(sqlmock.NewResult(0, 1))
}

// Which failures are about a format the device previews: by a ".HEIC"
// name, the row's MIME or the content.
func TestPreviewExpected(t *testing.T) {
	jpg := corruptJPEG(t)
	for _, c := range []struct {
		name    string
		path    string
		mime    string
		content []byte
		want    bool
	}{
		{"a cursor", "/D/zoomin.cur", "image/x-icon", cursorBytes, false},
		{"a DjVu scan", "/D/book.djvu", "image/vnd.djvu", djvuBytes, false},
		{"an SVG", "/D/logo.svg", "image/svg+xml; charset=utf-8", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), false},
		{"an AVIF", "/D/x.avif", "image/avif", nil, false},
		{"a truncated JPEG", "/D/a.jpg", "image/jpeg", jpg, true},
		{"a JPEG damaged past recognition", "/D/a.jpg", "image/jpeg", []byte("garbage"), true},
		{"a JPEG under an odd MIME", "/D/a.jpg", "image/x-whatever", jpg, true},
		{"a HEIC stored as octet-stream", "/D/IMG_1.HEIC", "application/octet-stream", heicJunk, true},
		{"a zero-filled .HEIC", "/D/IMG_2.HEIC", "application/octet-stream", make([]byte, 256), true},
		{"a .HEIC with no content to sniff", "/D/IMG_3.HEIC", "application/octet-stream", nil, true},
		{"a .HEIC that sniffs as a video", "/D/IMG_4.HEIC", "video/mp4", heicOddBrand, true},
		{"a .heic in lower case", "/D/img_5.heic", "application/octet-stream", nil, true},
		{"a Photoshop file (ffmpeg's)", "/D/x.psd", "image/vnd.adobe.photoshop", nil, true},
		{"a JPEG 2000 (ffmpeg's)", "/D/x.jp2", "image/jp2", nil, true},
		{"a DNG (TIFF)", "/D/x.dng", "image/tiff", nil, true},
		{"a BMP alias", "/D/x.bmp", "image/x-ms-bmp", nil, true},
	} {
		if got := previewExpected(&pb.File{Path: c.path, Mime: c.mime}, c.content); got != c.want {
			t.Errorf("%s: previewExpected = %v, want %v", c.name, got, c.want)
		}
	}
	if got := mimetype.Detect(cursorBytes).String(); got != "image/x-icon" {
		t.Errorf("the cursor sample sniffs as %s", got)
	}
	if got := mimetype.Detect(djvuBytes).String(); got != "image/vnd.djvu" {
		t.Errorf("the DjVu sample sniffs as %s", got)
	}
	// The .HEIC cases above count by their name alone: their bytes sniff
	// as nothing previewed.
	for _, b := range [][]byte{heicOddBrand, make([]byte, 256)} {
		if got := mimetype.Detect(b).String(); previewedMimes[baseMime(got)] {
			t.Errorf("a .HEIC sample sniffs as %s, previewed by content", got)
		}
	}
}

// What no decoder reads is not media: never queued or read. A cursor
// is: ffmpeg reads an .ico, which sniffs the same.
func TestNeverPreviewedIsNotMedia(t *testing.T) {
	if isMedia(&pb.File{Mime: "image/vnd.djvu", Path: "/Documents/book.djvu"}) {
		t.Error("a DjVu document is processed as media")
	}
	if !isMedia(&pb.File{Mime: "image/x-icon", Path: "/Documents/zoomin.cur"}) {
		t.Error("an icon is no longer processed")
	}
}

// A DjVu isn't copied out for ffmpeg to refuse; an icon still goes to it.
func TestDecodeStillSkipsFFmpegForWhatNoFFmpegReads(t *testing.T) {
	calls := noFFmpeg(t)
	if _, err := decodeStill(djvuBytes, "book.djvu"); err == nil {
		t.Fatal("a DjVu decoded")
	}
	if *calls != 0 {
		t.Errorf("ffmpeg was run on a DjVu")
	}
	if _, err := decodeStill(cursorBytes, "zoomin.cur"); err == nil {
		t.Fatal("the cursor decoded")
	}
	if *calls != 1 {
		t.Errorf("ffmpeg ran %d time(s) for an icon, want 1", *calls)
	}
}

// The owner's report: a cursor in a normal folder that neither decoder
// reads gets no thumbnail, no analysis and no alert.
func TestUnpreviewableFormatIsSkippedQuietly(t *testing.T) {
	_, ses := galleryTestEnv(t)
	noFFmpeg(t)
	mg, mock, _ := recordingMock(t)
	keptOut(mg)
	h := testHash("1")
	file := storedFile(t, ses, h, "/Documents/zoomin.cur", "image/x-icon", cursorBytes)

	referenced(mock, h) // dropIfOrphaned
	pendingCleared(mock, h)
	expectNoAlert(mock)

	if mg.thumbnailOf(ses, file) {
		t.Error("an undecodable file went on to the analysis")
	}
	if mg.hasThumbnail(h) {
		t.Error("a thumbnail was written")
	}
	noAlert(t, mock)
}

// A format ffmpeg does read keeps its thumbnail.
func TestFormatFFmpegReadsKeepsItsThumbnail(t *testing.T) {
	_, ses := galleryTestEnv(t)
	orig := stillFFmpeg
	stillFFmpeg = func([]byte) (image.Image, error) { return image.NewRGBA(image.Rect(0, 0, 32, 32)), nil }
	t.Cleanup(func() { stillFFmpeg = orig })
	mg, mock, _ := recordingMock(t)
	keptOut(mg)
	h := testHash("2")
	file := storedFile(t, ses, h, "/Documents/app.ico", "image/x-icon", cursorBytes)

	referenced(mock, h)
	expectNoAlert(mock)

	if !mg.thumbnailOf(ses, file) {
		t.Error("a decoded icon reported a failure")
	}
	if !mg.hasThumbnail(h) {
		t.Error("no thumbnail for an icon ffmpeg reads")
	}
	noAlert(t, mock)
}

// A real photo format that fails to decode in a normal folder is still
// the owner's to hear about - and a folder kept out elsewhere doesn't
// change that.
func TestCorruptJPEGInANormalFolderStillAlerts(t *testing.T) {
	_, ses := galleryTestEnv(t)
	noFFmpeg(t)
	content := corruptJPEG(t)

	for _, flagged := range []bool{false, true} {
		mg, mock, _ := recordingMock(t)
		h := testHash("3")
		file := storedFile(t, ses, h, "/Phone/broken.jpg", "image/jpeg", content)
		if flagged {
			keptOut(mg, "/Private/")
			mock.ExpectQuery(reVisible).WillReturnRows(hashRows(h)) // /Phone/ shows it
		} else {
			keptOut(mg)
		}
		expectAlert(mock, "broken.jpg could not be processed")
		referenced(mock, h)
		pendingCleared(mock, h)

		if mg.thumbnailOf(ses, file) {
			t.Error("a corrupt JPEG went on to the analysis")
		}
		allMet(t, mock)
	}
}

// Nothing kept out of Images raises a processing alert: the fast lane.
func TestCorruptJPEGKeptOutIsNotAlerted(t *testing.T) {
	_, ses := galleryTestEnv(t)
	noFFmpeg(t)
	mg, mock, _ := recordingMock(t)
	keptOut(mg, "/Documents/")
	h := testHash("4")
	file := storedFile(t, ses, h, "/Documents/broken.jpg", "image/jpeg", corruptJPEG(t))

	mock.ExpectQuery(reVisible).WillReturnRows(hashRows())
	mock.ExpectQuery(reUnder).WillReturnRows(hashRows(h))
	referenced(mock, h)
	pendingCleared(mock, h)
	expectNoAlert(mock)

	if mg.thumbnailOf(ses, file) {
		t.Error("a corrupt JPEG went on to the analysis")
	}
	noAlert(t, mock)
}

// The reprocess and backfill path (processMediaContent): the analysis is
// guarded off, and the failed thumbnail isn't alerted either.
func TestKeptOutFailureInReprocessIsNotAlerted(t *testing.T) {
	_, ses := galleryTestEnv(t)
	noFFmpeg(t)
	mg, mock, _ := recordingMock(t)
	keptOut(mg, "/Documents/")
	h := testHash("5")
	file := &pb.File{Hash: h, Path: "/Documents/broken.jpg", Mime: "image/jpeg"}

	// guardAnalysis
	mock.ExpectQuery(reVisible).WillReturnRows(hashRows())
	referenced(mock, h)
	mock.ExpectExec("insert ignore into `skipped_analysis`").WillReturnResult(sqlmock.NewResult(0, 0))
	// the failed decode
	mock.ExpectQuery(reVisible).WillReturnRows(hashRows())
	mock.ExpectQuery(reUnder).WillReturnRows(hashRows(h))
	referenced(mock, h)
	expectNoAlert(mock)

	mg.processMediaContent(ses, file, blobPath(h), corruptJPEG(t), nil)
	noAlert(t, mock)
}

// A HEIC that can't be converted: alerted in a normal folder, logged when
// kept out.
func TestHEICConversionFailure(t *testing.T) {
	_, ses := galleryTestEnv(t)
	h := testHash("6")

	mg, mock, _ := recordingMock(t)
	keptOut(mg)
	expectAlert(mock, "IMG_1.HEIC could not be converted from HEIC")
	referenced(mock, h)
	if mg.processMedia(ses, &pb.File{Hash: h, Path: "/Phone/IMG_1.HEIC", Mime: "image/heic"}, blobPath(h), heicJunk, nil, stageThumbnail) {
		t.Error("a broken HEIC reported success")
	}
	allMet(t, mock)

	mg, mock, _ = recordingMock(t)
	keptOut(mg, "/Private/")
	mock.ExpectQuery(reVisible).WillReturnRows(hashRows())
	mock.ExpectQuery(reUnder).WillReturnRows(hashRows(h))
	referenced(mock, h)
	expectNoAlert(mock)
	if mg.processMedia(ses, &pb.File{Hash: h, Path: "/Private/IMG_1.HEIC", Mime: "image/heic"}, blobPath(h), heicJunk, nil, stageThumbnail) {
		t.Error("a broken HEIC reported success")
	}
	noAlert(t, mock)
}

// A video kept out that can't be read is logged too.
func TestKeptOutVideoFailureIsNotAlerted(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg, mock, _ := recordingMock(t)
	keptOut(mg, "/Private/")
	h := testHash("7")
	mock.ExpectQuery(reVisible).WillReturnRows(hashRows())
	mock.ExpectQuery(reUnder).WillReturnRows(hashRows(h))
	referenced(mock, h)
	expectNoAlert(mock)
	if mg.processMedia(ses, &pb.File{Hash: h, Path: "/Private/clip.mp4", Mime: "video/mp4"}, blobPath(h), []byte("not a video"), nil, stageThumbnail) {
		t.Error("an unreadable video reported success")
	}
	noAlert(t, mock)
}

// A crash processing content kept out is logged, not alerted; elsewhere
// it still is.
func TestPanicOnKeptOutContentIsNotAlerted(t *testing.T) {
	h := testHash("8")
	mg, mock, _ := recordingMock(t)
	keptOut(mg, "/Private/")
	mock.ExpectQuery(reVisible).WillReturnRows(hashRows())
	mock.ExpectQuery(reUnder).WillReturnRows(hashRows(h))
	expectNoAlert(mock)
	mg.safelyOn("processing", &pb.File{Hash: h, Path: "/Private/x.jpg"}, func() { panic("decoder bug") })
	noAlert(t, mock)

	mg, mock, _ = recordingMock(t)
	keptOut(mg, "/Private/")
	mock.ExpectQuery(reVisible).WillReturnRows(hashRows(h))
	expectAlert(mock, "x.jpg could not be processed (it crashed the processing)")
	mg.safelyOn("processing", &pb.File{Hash: h, Path: "/Phone/x.jpg"}, func() { panic("decoder bug") })
	allMet(t, mock)
}

// When whether it is kept out can't be told, the alert goes out as before.
func TestKeptOutUnknownStillAlerts(t *testing.T) {
	h := testHash("9")
	mg, mock, _ := recordingMock(t)
	keptOut(mg, "/Private/")
	mock.ExpectQuery(reVisible).WillReturnError(errors.New("gone away"))
	expectAlert(mock, "x.jpg could not be processed")
	mg.processingAlert(i18n.DevAlertFileNotProcessed, &pb.File{Hash: h, Path: "/Private/x.jpg"}, errors.New("bad"))
	allMet(t, mock)
}

// A ".HEIC" goes through processMedia's image branch by its name, whatever
// its bytes sniff as - a zero-filled export (stored as
// application/octet-stream) or a HEIC whose major brand mimetype doesn't
// list (video/mp4). One that can't be converted is still the owner's to
// hear about.
func TestHEICByNameStillAlerts(t *testing.T) {
	_, ses := galleryTestEnv(t)
	h := testHash("b")
	for _, c := range []struct {
		mime    string
		content []byte
	}{
		{"application/octet-stream", make([]byte, 256)},
		{"video/mp4", heicOddBrand},
	} {
		mg, mock, _ := recordingMock(t)
		keptOut(mg)
		expectAlert(mock, "IMG_9.HEIC could not be converted from HEIC")
		referenced(mock, h)
		if mg.processMedia(ses, &pb.File{Hash: h, Path: "/Phone/IMG_9.HEIC", Mime: c.mime}, blobPath(h), c.content, nil, stageThumbnail) {
			t.Errorf("%s: a broken HEIC reported success", c.mime)
		}
		allMet(t, mock)
	}
}

// Issue #156: at level=info the log never names a file path. A failure on
// content kept out of Images is logged by its hash at Info; its path only
// at Debug. Run in a child process: the logger is global.
func TestKeptOutFailureLogNamesNoPathAtInfo(t *testing.T) {
	const secret = "/Private/secret-name.jpg"
	h := testHash("c")
	if logPath := os.Getenv("OTC_TEST_KEPT_OUT_LOG"); logPath != "" {
		level := log.INFO
		if os.Getenv("OTC_TEST_KEPT_OUT_LEVEL") == "debug" {
			level = log.DEBUG
		}
		log.SetLogger(level, logPath, 100)
		mg, mock, _ := recordingMock(t)
		keptOut(mg, "/Private/")
		for range 2 {
			mock.ExpectQuery(reVisible).WillReturnRows(hashRows())
			mock.ExpectQuery(reUnder).WillReturnRows(hashRows(h))
		}
		expectNoAlert(mock)
		file := &pb.File{Hash: h, Path: secret, Mime: "image/jpeg"}
		mg.processingAlert(i18n.DevAlertFileNotProcessed, file, errors.New("bad"))
		mg.safelyOn("processing", file, func() { panic("decoder bug") })
		if err := mock.ExpectationsWereMet(); err == nil || !strings.Contains(err.Error(), "ExpectedBegin") {
			os.Exit(3) // alerted, or the kept-out check didn't run
		}
		os.Exit(0)
	}

	for _, level := range []string{"info", "debug"} {
		logPath := filepath.Join(t.TempDir(), "otc.log")
		cmd := exec.Command(os.Args[0], "-test.run=^TestKeptOutFailureLogNamesNoPathAtInfo$")
		cmd.Env = append(os.Environ(), "OTC_TEST_KEPT_OUT_LOG="+logPath, "OTC_TEST_KEPT_OUT_LEVEL="+level)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", level, err, out)
		}
		b, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		got := string(b)
		if n := strings.Count(got, "content "+h+" dev.alert.file."); n != 2 {
			t.Errorf("%s: %d line(s) name the hash, want 2:\n%s", level, n, got)
		}
		named := strings.Contains(got, "secret-name")
		if level == "info" && named {
			t.Errorf("at level=info the log names the kept-out file:\n%s", got)
		}
		if level == "debug" && !named {
			t.Errorf("at level=debug nothing maps the hash to its path:\n%s", got)
		}
	}
}

// What nothing decodes isn't resumed after a restart: a pending row an
// older build queued, or skipped content shown again (dao's cMediaRows
// doesn't leave it out), is cleared, never read. Media still goes back to
// the lanes.
func TestResumePendingAnalysisDropsWhatIsNotMedia(t *testing.T) {
	galleryTestEnv(t)
	mg, mock, _ := recordingMock(t)
	lanes := idleLanes(mg)
	djvu, photo := testHash("d"), testHash("e")
	now := time.Now()

	mock.ExpectQuery(rePendingPage).WillReturnRows(pendingRows(djvu, photo))
	// Their files in one query (issue #206), not one a hash.
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `hash` in \\(\\?,\\?\\) order by `hash`, `path`").
		WithArgs(djvu, photo).
		WillReturnRows(sqlmock.NewRows(fileCols).
			AddRow(djvu, "image/vnd.djvu", now, now, "/Documents/book.djvu", 40<<20).
			AddRow(photo, "image/jpeg", now, now, "/Phone/a.jpg", 7))
	pendingCleared(mock, djvu)

	mg.ResumePendingAnalysis(nil)
	allMet(t, mock)
	if len(lanes.fast.jobs) != 1 || lanes.fast.jobs[0].hash != photo || len(lanes.analysis.jobs) != 0 {
		t.Errorf("queued: fast %d, analysis %d - want only the photo", len(lanes.fast.jobs), len(lanes.analysis.jobs))
	}
}

// A job is only its content (issue #206): the row it processes is read
// when it runs - the smallest path that is media - and content whose rows
// went meanwhile, or none of them media, is passed over, its analysis no
// longer pending; a row that can't be read is left pending.
func TestJobReadsItsFileWhenItRuns(t *testing.T) {
	galleryTestEnv(t)
	mg, mock, seen := recordingMock(t)
	h := testHash("9")
	now := time.Now()
	reRows := "select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `hash` = \\? order by `path`"
	mock.ExpectQuery(reRows).WithArgs(h).WillReturnRows(sqlmock.NewRows(fileCols).
		AddRow(h, "application/octet-stream", now, now, "/a/copy.bin", 7).
		AddRow(h, "image/jpeg", now, now, "/b/photo.jpg", 7))
	if f, ok := mg.jobFile(h); !ok || f.Path != "/b/photo.jpg" {
		t.Fatalf("the job's file: %v %v", f, ok)
	}
	mock.ExpectQuery(reRows).WithArgs(h).WillReturnRows(sqlmock.NewRows(fileCols))
	pendingCleared(mock, h)
	if mg.thumbnailJob(mediaJob{hash: h}) {
		t.Error("a job of content deleted meanwhile went on to the analysis")
	}
	mock.ExpectQuery(reRows).WithArgs(h).WillReturnError(errors.New("connection lost"))
	expectSentinel(mock)
	if _, ok := mg.jobFile(h); ok {
		t.Error("a row that couldn't be read was processed")
	}
	metUpToSentinel(t, mock)
	if strings.Count(strings.Join(seen.all, "\n"), "delete from `pending_analysis`") != 1 {
		t.Error("a row that couldn't be read cleared its analysis")
	}
}

// rePendingPage is ResumePendingAnalysis' first page of pending_analysis.
const rePendingPage = "select `hash`, `queued` from `pending_analysis` order by `queued`, `hash` limit 500"

// pendingRows is pending_analysis' rows for hashes, queued in that order.
func pendingRows(hashes ...string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"hash", "queued"})
	at := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	for i, h := range hashes {
		rows.AddRow(h, at.Add(time.Duration(i)*time.Second))
	}
	return rows
}

// pending_analysis is read a page at a time (issue #206), in the order it
// was queued, each page's files in one query, and only hashes go into the
// lanes: 501 pending photos are two pages, the second after the first's
// last row.
func TestResumePendingAnalysisPagesThroughItsRows(t *testing.T) {
	galleryTestEnv(t)
	mg, mock, _ := recordingMock(t)
	lanes := idleLanes(mg)
	var hashes []string
	for i := 0; i < cResumeChunk+1; i++ {
		hashes = append(hashes, fmt.Sprintf("%064x", 0x5060+i))
	}
	now := time.Now()
	files := func(hs []string) *sqlmock.Rows {
		rows := sqlmock.NewRows(fileCols)
		for i, h := range hs {
			rows.AddRow(h, "image/jpeg", now, now, fmt.Sprintf("/Phone/%d.jpg", i), 7)
		}
		return rows
	}
	first := pendingRows(hashes[:cResumeChunk]...)
	mock.ExpectQuery(rePendingPage).WillReturnRows(first)
	mock.ExpectQuery("from `files` where `hash` in").WillReturnRows(files(hashes[:cResumeChunk]))
	last := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC).Add(time.Duration(cResumeChunk-1) * time.Second)
	mock.ExpectQuery("select `hash`, `queued` from `pending_analysis` where `queued` > \\? or \\(`queued` = \\? and `hash` > \\?\\) order by `queued`, `hash` limit 500").
		WithArgs(last, last, hashes[cResumeChunk-1]).
		WillReturnRows(pendingRows(hashes[cResumeChunk]))
	mock.ExpectQuery("from `files` where `hash` in").WithArgs(hashes[cResumeChunk]).WillReturnRows(files(hashes[cResumeChunk:]))
	mg.ResumePendingAnalysis(nil)
	allMet(t, mock)
	if len(lanes.fast.jobs) != len(hashes) || lanes.fast.jobs[cResumeChunk].hash != hashes[cResumeChunk] {
		t.Fatalf("queued %d of %d", len(lanes.fast.jobs), len(hashes))
	}
}

// Files that can't be read when the lanes are refilled stay pending for the
// next start - neither queued nor taken for gone - and a hash whose file is
// gone is cleared, as before.
func TestResumePendingAnalysisKeepsWhatItCannotRead(t *testing.T) {
	galleryTestEnv(t)
	mg, mock, seen := recordingMock(t)
	lanes := idleLanes(mg)
	a, b := testHash("a"), testHash("b")

	mock.ExpectQuery(rePendingPage).WillReturnRows(pendingRows(a, b))
	mock.ExpectQuery("from `files` where `hash` in").WithArgs(a, b).WillReturnError(errors.New("connection lost"))
	expectSentinel(mock)
	mg.ResumePendingAnalysis(nil)
	metUpToSentinel(t, mock)
	if seen.ran("delete from `pending_analysis`") || len(lanes.fast.jobs)+len(lanes.analysis.jobs) != 0 {
		t.Errorf("a read that failed cleared or queued: fast %d, analysis %d", len(lanes.fast.jobs), len(lanes.analysis.jobs))
	}

	mg, mock, _ = recordingMock(t)
	lanes = idleLanes(mg)
	mock.ExpectQuery(rePendingPage).WillReturnRows(pendingRows(a, b))
	mock.ExpectQuery("from `files` where `hash` in").WithArgs(a, b).WillReturnRows(fileRow(b, "/Phone/b.jpg"))
	pendingCleared(mock, a)
	mg.ResumePendingAnalysis(nil)
	allMet(t, mock)
	if len(lanes.fast.jobs) != 1 || lanes.fast.jobs[0].hash != b {
		t.Errorf("queued: fast %d, analysis %d - want only b", len(lanes.fast.jobs), len(lanes.analysis.jobs))
	}
}

// containing matches a text argument that holds the words.
type containing string

func (c containing) Match(v driver.Value) bool {
	s, ok := v.(string)
	return ok && strings.Contains(s, string(c))
}

// Processing reads content whole within the content budget, and none
// larger than a decode may hold: the lanes and Reprocess (and the
// thumbnail backfill, through it) alert it as too large to process,
// reading nothing of it, and give the budget back once done.
func TestProcessingReadsWithinTheBudget(t *testing.T) {
	_, ses := galleryTestEnv(t)
	prev := processedWholeLimit
	processedWholeLimit = func() int64 { return 1 << 20 }
	t.Cleanup(func() { processedWholeLimit = prev })

	h := testHash("8")
	file := storedFile(t, ses, h, "/Scans/huge.tif", "image/tiff", make([]byte, 2<<20))
	for _, lane := range []string{"thumbnail lane", "reprocess"} {
		mg, mock, _ := recordingMock(t)
		mg.contentBudget = newMemBudget(64 << 20)
		keptOut(mg)
		mock.ExpectBegin()
		mock.ExpectQuery("select `uuid`, coalesce\\(length\\(`details`\\), 0\\), `msg_key`, `msg_lines` from `notifications`").
			WillReturnRows(sqlmock.NewRows([]string{"uuid", "size", "msg_key", "msg_lines"}))
		mock.ExpectExec("insert into `notifications`").
			WithArgs(sqlmock.AnyArg(), "", "huge.tif could not be processed", containing("the file is 2.1 MB, more than this device can process at once (at most 1.0 MB)"), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectCommit()
		if lane == "reprocess" {
			mg.reprocessOneFile(ses, file, filepath.Dir(blobPath(h)))
		} else {
			pendingCleared(mock, h)
			if mg.thumbnailOf(ses, file) {
				t.Errorf("%s: content too large to process went on to the analysis", lane)
			}
		}
		allMet(t, mock)
		if used := mg.contentBudget.used; used != 0 {
			t.Errorf("%s: %d bytes of the budget held after", lane, used)
		}
	}

	// A shared gallery's preview is read whole as GetFile sends a photo:
	// nothing over what one reply may hold, refused before it is read.
	big := sized("/Scans/big.tif", "image/tiff", 100<<20)
	gallery := &Manager{contentBudget: newMemBudget(30 << 20)}
	gallery.sizesBackfilled.Store(true)
	if _, _, err := gallery.galleryPreview(ses, big); !errors.Is(err, errTooLargeToProcess) {
		t.Errorf("a gallery preview of a 100 MB scan: %v", err)
	}

	// What fits is read, holding its size until it is decoded.
	mg := &Manager{contentBudget: newMemBudget(64 << 20)}
	small := storedFile(t, ses, testHash("9"), "/Scans/small.tif", "image/tiff", make([]byte, 512<<10))
	content, release, err := mg.readForProcessing(ses, blobPath(small.Hash))
	if err != nil || len(content) != 512<<10 {
		t.Fatalf("read %d bytes, %v", len(content), err)
	}
	if used := mg.contentBudget.used; used != 512<<10 {
		t.Errorf("held %d bytes while processing", used)
	}
	release()
	if used := mg.contentBudget.used; used != 0 {
		t.Errorf("held %d bytes after", used)
	}
}

// Once decoded, the content's hold on the content budget is given back:
// the thumbnails, a wait for the tagger still loading, the tagging, faces
// and the rest ran holding it, and downloads that didn't fit next to a
// large image waited for its whole analysis.
func TestProcessingGivesTheBudgetBackOnceDecoded(t *testing.T) {
	_, ses := galleryTestEnv(t)
	photo := storedFile(t, ses, testHash("a"), "/Photos/p.jpg", "image/jpeg", jpegBytes(t))
	for _, run := range []string{"analysis lane", "reprocess"} {
		mg, _, _ := recordingMock(t) // every statement refused: logged
		mg.contentBudget = newMemBudget(64 << 20)
		keptOut(mg)
		held := int64(-1)
		tg := &fakeTagger{during: func() {
			mg.contentBudget.mu.Lock()
			held = mg.contentBudget.used
			mg.contentBudget.mu.Unlock()
		}}
		readyTagger(mg, tg)
		if run == "reprocess" {
			mg.reprocessOneFile(ses, photo, filepath.Dir(blobPath(photo.Hash)))
		} else {
			mg.processStoredStages(ses, photo, blobPath(photo.Hash), stageAnalysis)
		}
		if tg.calls != 1 {
			t.Fatalf("%s: %d tagging passes", run, tg.calls)
		}
		if held != 0 {
			t.Errorf("%s: %d bytes of the content budget held while tagging", run, held)
		}
		if used := mg.contentBudget.used; used != 0 {
			t.Errorf("%s: %d bytes held after", run, used)
		}
	}
}
