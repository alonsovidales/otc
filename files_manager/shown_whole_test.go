// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/devtext"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// sized is a row of size bytes at path.
func sized(path, mime string, size int64) *pb.File {
	f := &pb.File{Path: path, Mime: mime, Hash: strings.Repeat("ab", 32)}
	dao.SetFileSize(f, size)
	return f
}

// What GetFile sends whole is a third of the content budget, at least
// 64 MB - never a HEIC (sent as its JPEG, bounded by the decode budget) or
// a video (the web composer's trimmer has no other way to read one yet).
func TestOverShownWhole(t *testing.T) {
	mg := &Manager{contentBudget: newMemBudget(1500 << 20)}
	mg.sizesBackfilled.Store(true)
	if got := mg.shownWholeLimit(); got != 500<<20 {
		t.Fatalf("limit %d, want a third of the budget", got)
	}
	if got := (&Manager{contentBudget: newMemBudget(30 << 20)}).shownWholeLimit(); got != cMinShownWhole {
		t.Fatalf("a small budget's limit %d, want %d", got, cMinShownWhole)
	}
	for _, c := range []struct {
		f    *pb.File
		over bool
	}{
		{sized("/scans/a.tif", "image/tiff", 1500<<20), true},
		{sized("/scans/b.tif", "image/tiff", 400<<20), false},
		{sized("/docs/c.pdf", "application/pdf", 900<<20), true},
		{sized("/photos/d.HEIC", "image/heic", 900<<20), false},
		{sized("/videos/e.mov", "video/quicktime", 3<<30), false},
	} {
		if got := mg.overShownWhole(c.f); got != c.over {
			t.Errorf("%s: over %v, want %v", c.f.Path, got, c.over)
		}
	}
}

// A file too large to send whole is refused before anything is read -
// "too_large", the viewers keep its thumbnail - and reserves nothing of
// the budget the other downloads wait on.
func TestGetFileRefusesWhatItCantSendWhole(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mg := &Manager{dao: dao.NewWithDB(db), contentBudget: newMemBudget(30 << 20)}
	mg.sizesBackfilled.Store(true)
	scan := sized("/scans/big.tif", "image/tiff", 200<<20)
	for i := 0; i < 2; i++ {
		mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = ?").
			WithArgs(scan.Path).WillReturnRows(fileRows(scan))
	}
	release := mg.ReserveForDownload(scan.Path, "")
	if used := mg.contentBudget.used; used != 0 {
		t.Fatalf("a refused file holds %d bytes of the budget", used)
	}
	release()
	_, err = mg.GetFile(nil, scan.Path, "")
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v, want ErrTooLarge", err)
	}
	if want := "this file is too large to open here (210 MB): download it instead"; err.Error() != want {
		t.Errorf("English %q, want %q", err.Error(), want)
	}
	if es := devtext.Text("es", err); es == err.Error() || !strings.Contains(es, "210") {
		t.Errorf("Spanish %q", es)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// "More info" reads a stored photo's metadata where it is, through the
// blob, never the photo whole: a JPEG and a HEIC (by their bytes, not
// their names) give their EXIF; a TIFF is read only up to the limit, and
// one whose metadata is past it gives an error, not gigabytes in memory.
func TestReadExifReadsTheBlobWhereTheMetadataIs(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg := &Manager{}
	mg.sizesBackfilled.Store(true)
	jpg, err := os.ReadFile("../exifinfo/testdata/gps_test.jpg")
	if err != nil {
		t.Fatal(err)
	}
	heic, err := os.ReadFile("../exifinfo/testdata/gps_test.heic")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, mime string
		content    []byte
	}{
		{"gps.jpg", "image/jpeg", jpg},
		{"named.HEIC", "image/heic", jpg}, // a JPEG exported under a .HEIC name
		{"gps.heic", "image/heic", heic},  // lower-case: by its bytes
		{"stored.bin", "application/octet-stream", jpg},
	} {
		f := storedBlob(t, ses, c.name, c.mime, c.content)
		ex, err := mg.readExif(ses, f, 64<<20)
		if err != nil || !ex.HasGPS {
			t.Errorf("%s: %+v, %v", c.name, ex, err)
		}
	}
	// A TIFF whose first directory is 2 MB in, read up to 1 MB.
	tiff := make([]byte, 3<<20)
	copy(tiff, "II*\x00")
	binary.LittleEndian.PutUint32(tiff[4:], 2<<20)
	f := storedBlob(t, ses, "scan.tif", "image/tiff", tiff)
	if _, err := mg.readExif(ses, f, 1<<20); err == nil {
		t.Error("a TIFF's metadata past the limit was found")
	}
}

// storedBlob stores content as a blob of its own and returns its row.
func storedBlob(t *testing.T, ses blobstore.Keys, name, mime string, content []byte) *pb.File {
	t.Helper()
	f := &pb.File{Path: "/info/" + name, Mime: mime, Hash: sha256Hex(append([]byte(name), content...))}
	if err := blobstore.WriteBytes(blobPath(f.Hash), ses, content); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(blobPath(f.Hash)) })
	dao.SetFileSize(f, int64(len(content)))
	return f
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// The Files grid's thumbnails: the rows of every path asked for come in
// one query (they were one query a path, up to 48), in the order asked,
// leaving out a path with no row and one that isn't media.
func TestThumbnailsReadTheRowsInOneQuery(t *testing.T) {
	_, ses := galleryTestEnv(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mg := &Manager{dao: dao.NewWithDB(db)}
	photo, clip, doc := sized("/grid/a.jpg", "image/jpeg", 10), sized("/grid/b.mov", "video/quicktime", 10), sized("/grid/c.pdf", "application/pdf", 10)
	photo.Hash, clip.Hash, doc.Hash = sha256Hex([]byte("grid a")), sha256Hex([]byte("grid b")), sha256Hex([]byte("grid c"))
	for _, f := range []*pb.File{photo, clip} {
		if err := blobstore.WriteBytes(blobPath(f.Hash)+cThumbnailSuffix, ses, []byte("thumb of "+f.Path)); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Remove(blobPath(f.Hash) + cThumbnailSuffix) })
	}
	rows := sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"})
	now := time.Now()
	for _, f := range []*pb.File{clip, doc, photo} { // any order
		rows.AddRow(f.Hash, f.Mime, now, now, f.Path, dao.FileSize(f))
	}
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` in \\(\\?, \\?, \\?, \\?\\)").
		WithArgs(photo.Path, "/grid/gone.jpg", doc.Path, clip.Path).WillReturnRows(rows)
	out, again := mg.Thumbnails(ses, []string{photo.Path, "/grid/gone.jpg", doc.Path, clip.Path}, false)
	if again != 0 || len(out) != 2 || out[0].Path != photo.Path || out[1].Path != clip.Path {
		t.Fatalf("got %d entries (%v), ask again from %d", len(out), out, again)
	}
	if string(out[0].Content) != "thumb of /grid/a.jpg" || out[0].GetThumbnailSmall() {
		t.Errorf("first entry %+v", out[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
