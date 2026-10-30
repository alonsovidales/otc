// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/exifinfo"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"github.com/go-sql-driver/mysql"
)

func at(y int, mo time.Month, d, h, mi, s int) time.Time {
	return time.Date(y, mo, d, h, mi, s, 0, time.UTC)
}

// taken is a date taken as exifinfo reads one: the instant, and the clock
// it was read from (with offset its zone: +2 h means the clock showed two
// hours more).
func taken(instant time.Time, offset time.Duration) *exifinfo.Info {
	return &exifinfo.Info{HasTakenAt: true, TakenAt: instant, TakenClock: instant.Add(offset)}
}

// The rule: a row moves to the date taken T only when T < C - 26 h, never
// later; T must be from 1990 to a day from now and not a clock reset.
func TestCreatedFromTaken(t *testing.T) {
	now := at(2026, 10, 30, 12, 0, 0)
	copied := at(2018, 3, 2, 10, 0, 0) // when the copy was made
	for _, c := range []struct {
		name    string
		ex      *exifinfo.Info
		created time.Time
		moves   bool
		want    time.Time
	}{
		{"no metadata", nil, copied, false, copied},
		{"no date taken", &exifinfo.Info{CameraModel: "X"}, copied, false, copied},
		{"a 2014 photo copied in 2018", taken(at(2014, 7, 19, 19, 5, 33), 2*time.Hour), copied, true, at(2014, 7, 19, 19, 5, 33)},
		{"a clock read as UTC", taken(at(2014, 7, 19, 21, 5, 33), 0), copied, true, at(2014, 7, 19, 21, 5, 33)},
		{"an offset west of UTC", taken(at(2015, 1, 3, 4, 0, 0), -8*time.Hour), copied, true, at(2015, 1, 3, 4, 0, 0)},
		{"a fraction of a second is dropped", taken(at(2014, 7, 19, 19, 5, 33).Add(700*time.Millisecond), 0), copied, true, at(2014, 7, 19, 19, 5, 33)},
		{"a phone's exact time, its clock 14 h ahead read as UTC",
			taken(at(2020, 6, 1, 22, 0, 0), 0), at(2020, 6, 1, 8, 0, 0), false, at(2020, 6, 1, 8, 0, 0)},
		{"a phone's exact time, its clock 12 h behind read as UTC",
			taken(at(2020, 6, 1, 8, 0, 0), 0), at(2020, 6, 1, 20, 0, 0), false, at(2020, 6, 1, 20, 0, 0)},
		{"exactly 26 h before: stays", taken(at(2020, 6, 1, 8, 0, 0), 0), at(2020, 6, 2, 10, 0, 0), false, at(2020, 6, 2, 10, 0, 0)},
		{"a second more than 26 h: moves", taken(at(2020, 6, 1, 8, 0, 0), 0), at(2020, 6, 2, 10, 0, 1), true, at(2020, 6, 1, 8, 0, 0)},
		{"never later", taken(at(2019, 1, 1, 12, 0, 0), 0), at(2014, 5, 5, 5, 5, 5), false, at(2014, 5, 5, 5, 5, 5)},
		{"before 1990", taken(at(1989, 12, 31, 23, 59, 59), 0), copied, false, copied},
		{"1990 itself", taken(at(1990, 1, 1, 0, 0, 1), 0), copied, true, at(1990, 1, 1, 0, 0, 1)},
		{"1970", taken(at(1970, 1, 1, 0, 0, 0), 0), copied, false, copied},
		{"1980", taken(at(1980, 1, 1, 0, 0, 0), 0), copied, false, copied},
		{"QuickTime's 1904", taken(at(1904, 1, 1, 0, 0, 0), 0), copied, false, copied},
		{"a reset clock, 2000-01-01 00:00:00", taken(at(2000, 1, 1, 0, 0, 0), 0), copied, false, copied},
		{"a reset clock read with an offset", taken(at(1999, 12, 31, 23, 0, 0), time.Hour), copied, false, copied},
		{"a reset clock, 2005-01-01 00:00:00", taken(at(2005, 1, 1, 0, 0, 0), 0), copied, false, copied},
		{"a second after a reset is a date", taken(at(2005, 1, 1, 0, 0, 1), 0), copied, true, at(2005, 1, 1, 0, 0, 1)},
		{"tomorrow", taken(now.Add(23*time.Hour), 0), now.Add(72 * time.Hour), true, now.Add(23 * time.Hour)},
		{"over a day from now", taken(now.Add(24*time.Hour), 0), now.Add(96 * time.Hour), false, now.Add(96 * time.Hour)},
		{"no clock: the instant's", &exifinfo.Info{HasTakenAt: true, TakenAt: at(2014, 7, 19, 19, 5, 33)}, copied, true, at(2014, 7, 19, 19, 5, 33)},
		{"no clock, an instant at a reset", &exifinfo.Info{HasTakenAt: true, TakenAt: at(2000, 1, 1, 0, 0, 0)}, copied, false, copied},
	} {
		got, moves := createdFromTaken(c.ex, c.created, now)
		if moves != c.moves || !got.Equal(c.want) {
			t.Errorf("%s: %v %v, want %v %v", c.name, got, moves, c.want, c.moves)
		}
	}
}

func expectMove(mock sqlmock.Sqlmock, hash string, t time.Time, moved int64) {
	mock.ExpectExec("update `files` set `created` = \\? where `hash` = \\? and `created` > \\?").
		WithArgs(t, hash, t.Add(cTakenMargin)).WillReturnResult(sqlmock.NewResult(0, moved))
}

// Applying it: one statement for the content's rows, the condition in it;
// the answers kept by date are dropped only when a row moved, and nothing
// is asked for a date the rule doesn't trust.
func TestMoveToDateTaken(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mg := &Manager{dao: dao.NewWithDB(db)}
	hash := fmt.Sprintf("%064x", 0xda7e)
	when := at(2014, 7, 19, 19, 5, 33)
	expectMove(mock, hash, when, 2)
	expectMove(mock, hash, when, 0)
	mem, places := mg.memories.gen.Load(), mg.places.gen.Load()
	if moved, err := mg.moveToDateTaken(hash, taken(when, 0)); err != nil || moved != 2 {
		t.Fatalf("moved %d, %v", moved, err)
	}
	if mg.memories.gen.Load() == mem || mg.places.gen.Load() == places {
		t.Error("dates moved without dropping what was kept by date")
	}
	mem, places = mg.memories.gen.Load(), mg.places.gen.Load()
	if moved, err := mg.moveToDateTaken(hash, taken(when, 0)); err != nil || moved != 0 {
		t.Fatalf("again: %d, %v", moved, err)
	}
	mg.storeDateTaken(hash, taken(at(1970, 1, 1, 0, 0, 0), 0))
	mg.storeDateTaken(hash, nil)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
	if mg.memories.gen.Load() != mem || mg.places.gen.Load() != places {
		t.Error("nothing moved, yet the answers kept were dropped")
	}
}

// Processing applies it from the metadata it reads, before anything else
// (a photo: its EXIF clock, here a Nikon's read as UTC).
func TestProcessingGivesThePhotoItsDateTaken(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg, mock, _ := recordingMock(t)
	keptOut(mg)
	gps, err := os.ReadFile("../exifinfo/testdata/gps_test.jpg")
	if err != nil {
		t.Fatal(err)
	}
	h := testHash("d")
	file := storedFile(t, ses, h, "/Drive/2008/tuscany.jpg", "image/jpeg", gps)
	// Declared first: noAlert's sentinel is then the only one left.
	expectMove(mock, h, at(2008, 10, 22, 16, 28, 39), 1)
	referenced(mock, h)
	expectNoAlert(mock)
	mem := mg.memories.gen.Load()
	if !mg.processMedia(ses, file, blobPath(h), gps, stageThumbnail) {
		t.Fatal("processing failed")
	}
	noAlert(t, mock)
	if mg.memories.gen.Load() == mem {
		t.Error("On this day not told")
	}

	// A HEIC named in lower case, which processing doesn't read as one:
	// its date is read from its boxes (an iPhone's, with its offset).
	heic, err := os.ReadFile("../exifinfo/testdata/gps_test.heic")
	if err != nil {
		t.Fatal(err)
	}
	mg, mock, _ = recordingMock(t)
	keptOut(mg)
	h = testHash("e")
	file = storedFile(t, ses, h, "/Drive/2021/img_0001.heic", "image/heic", heic)
	expectMove(mock, h, at(2021, 4, 11, 20, 47, 53), 1)
	mock.MatchExpectationsInOrder(false)
	mem = mg.memories.gen.Load()
	mg.processMedia(ses, file, blobPath(h), heic, stageThumbnail)
	if mg.memories.gen.Load() == mem {
		t.Error("a lower-case .heic's date not read")
	}
}

// noUploadsLately forgets an earlier test's upload, which would hold the
// backfill (waitForQuiet) for a minute.
func noUploadsLately() { lastUploadActivity.Store(0) }

// datesBackfillState is the settings row the pass reads: its cursor and
// whether it is done.
func datesBackfillState(mock sqlmock.Sqlmock, after string, done int) {
	mock.ExpectQuery("select `dates_taken_cursor`, `dates_taken_backfilled` from `settings`").
		WillReturnRows(sqlmock.NewRows([]string{"c", "d"}).AddRow(after, done))
}

func mediaPage(mock sqlmock.Sqlmock, after string, files ...*pb.File) {
	rows := sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"})
	when := at(2018, 3, 2, 10, 0, 0)
	for _, f := range files {
		rows.AddRow(f.Hash, f.Mime, when, when, f.Path, 10)
	}
	mock.ExpectQuery("select `hash`, min\\(`mime`\\), min\\(`created`\\)").WithArgs(after, cDatesBackfillPage).WillReturnRows(rows)
}

// testVideoAt writes an MP4 whose creation_time is when, and makes it the
// stream processing reads stored videos from.
func testVideoAt(t *testing.T, mg *Manager, when string) {
	t.Helper()
	requireFFmpeg(t)
	path := filepath.Join(t.TempDir(), "clip.mp4")
	if out, err := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "testsrc=duration=1:size=64x64:rate=5",
		"-metadata", "creation_time="+when, "-pix_fmt", "yuv420p", path).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	mg.SetVideoSource(func(*session.Session, *pb.File) (string, func(), error) { return path, func() {}, nil })
}

// The backfill reads every photo's and video's metadata once - a photo up
// to its EXIF, a video by ffprobe - and moves the rows of what is a later
// copy; content without a date, a blob that is gone and what isn't media
// are passed by; at the end it records that it is done.
func TestBackfillDatesTaken(t *testing.T) {
	_, ses := galleryTestEnv(t)
	noUploadsLately()
	gps, err := os.ReadFile("../exifinfo/testdata/gps_test.jpg")
	if err != nil {
		t.Fatal(err)
	}
	var plain bytes.Buffer
	jpeg.Encode(&plain, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil)
	h := func(i int) string { return fmt.Sprintf("%064x", 0xda70+i) }
	photo, noDate, gone, video, djvu := h(1), h(2), h(3), h(4), h(5)
	for hash, content := range map[string][]byte{photo: gps, noDate: plain.Bytes(), video: []byte("streamed")} {
		if err := blobstore.WriteBytes(blobPath(hash), ses, content); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, hash := range []string{photo, noDate, gone, video, djvu} {
			os.Remove(blobPath(hash))
		}
	})

	db, mock, _ := sqlmock.New()
	t.Cleanup(func() { db.Close() })
	mg := &Manager{dao: dao.NewWithDB(db)}
	testVideoAt(t, mg, "2014-07-19T19:05:33.000000Z")
	datesBackfillState(mock, "", 0)
	mediaPage(mock, "",
		&pb.File{Hash: photo, Mime: "image/jpeg", Path: "/Drive/a.jpg"},
		&pb.File{Hash: noDate, Mime: "image/jpeg", Path: "/Drive/b.jpg"},
		&pb.File{Hash: gone, Mime: "image/jpeg", Path: "/Drive/c.jpg"},
		&pb.File{Hash: video, Mime: "video/mp4", Path: "/Drive/d.mp4"},
		&pb.File{Hash: djvu, Mime: "image/vnd.djvu", Path: "/Drive/e.djvu"},
	)
	expectMove(mock, photo, at(2008, 10, 22, 16, 28, 39), 2)
	expectMove(mock, video, at(2014, 7, 19, 19, 5, 33), 1)
	mock.ExpectExec("update `settings` set `dates_taken_backfilled` = 1").WillReturnResult(sqlmock.NewResult(0, 1))

	mem := mg.memories.gen.Load()
	mg.BackfillDatesTaken(ses)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
	if mg.memories.gen.Load() == mem {
		t.Error("dates moved without telling On this day")
	}
}

// It goes on from its place, saves it after each page, and stops - its
// place saved - for a full reprocess; done, or before the release's
// columns, it reads nothing.
func TestBackfillDatesTakenResumes(t *testing.T) {
	_, ses := galleryTestEnv(t)
	noUploadsLately()

	// A full page of content whose blobs are gone, then an empty one.
	db, mock, _ := sqlmock.New()
	t.Cleanup(func() { db.Close() })
	mg := &Manager{dao: dao.NewWithDB(db)}
	var page []*pb.File
	for i := range cDatesBackfillPage {
		page = append(page, &pb.File{Hash: fmt.Sprintf("%064x", 0xda7e00+i), Mime: "image/jpeg", Path: "/x.jpg"})
	}
	last := page[len(page)-1].Hash
	datesBackfillState(mock, "0f", 0)
	mediaPage(mock, "0f", page...)
	mock.ExpectExec("update `settings` set `dates_taken_cursor` = \\?").WithArgs(last).WillReturnResult(sqlmock.NewResult(0, 1))
	mediaPage(mock, last)
	mock.ExpectExec("update `settings` set `dates_taken_backfilled` = 1").WillReturnResult(sqlmock.NewResult(0, 1))
	mg.BackfillDatesTaken(ses)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}

	// A reprocess under way: its place is kept, nothing is done.
	db2, mock2, _ := sqlmock.New()
	t.Cleanup(func() { db2.Close() })
	mg = &Manager{dao: dao.NewWithDB(db2)}
	mg.reprocessing = true
	datesBackfillState(mock2, "0f", 0)
	mediaPage(mock2, "0f", page[0])
	mock2.ExpectExec("update `settings` set `dates_taken_cursor` = \\?").WithArgs("0f").WillReturnResult(sqlmock.NewResult(0, 1))
	mg.BackfillDatesTaken(ses)
	if err := mock2.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}

	for _, c := range []struct {
		name string
		rows *sqlmock.Rows
		err  error
	}{
		{"done", sqlmock.NewRows([]string{"c", "d"}).AddRow("ff", 1), nil},
		{"no column", nil, &mysql.MySQLError{Number: 1054, Message: "Unknown column 'dates_taken_cursor'"}},
	} {
		db, mock, _ := sqlmock.New()
		q := mock.ExpectQuery("select `dates_taken_cursor`")
		if c.err != nil {
			q.WillReturnError(c.err)
		} else {
			q.WillReturnRows(c.rows)
		}
		(&Manager{dao: dao.NewWithDB(db)}).BackfillDatesTaken(ses)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		db.Close()
	}
}

// Places' reading shares the metadata reader: a blob gone is a read to try
// again (and says it is gone), a video with no stream too.
func TestReadStoredMetadata(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg := &Manager{}
	_, err := mg.readStoredMetadata(ses, &pb.File{Hash: fmt.Sprintf("%064x", 0xda7f), Mime: "image/jpeg"})
	if !errors.Is(err, errMetadataUnread) || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a blob gone: %v", err)
	}
	if _, err := mg.readStoredMetadata(ses, &pb.File{Hash: "x", Mime: "video/mp4"}); !errors.Is(err, errMetadataUnread) {
		t.Errorf("a video with no stream: %v", err)
	}
}
