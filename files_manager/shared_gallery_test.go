// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
)

var (
	galleryEnvOnce sync.Once
	galleryStorage string
	galleryPosts   string // [otc] unenc-storage-path
)

// galleryTestEnv points [otc] storage-path at a temporary folder and gives
// a real session (its vault in a mocked database).
func galleryTestEnv(t *testing.T) (string, *session.Session) {
	t.Helper()
	// cfg caches what it read, so every test shares one storage folder,
	// configured once.
	galleryEnvOnce.Do(func() {
		dir, err := os.MkdirTemp("", "otc-gallerytest-")
		if err != nil {
			t.Fatal(err)
		}
		galleryStorage = filepath.Join(dir, "storage") + "/"
		galleryPosts = filepath.Join(dir, "posts") + "/"
		os.MkdirAll(galleryStorage, 0o750)
		os.MkdirAll(galleryPosts, 0o750)
		os.MkdirAll(filepath.Join(dir, "etc"), 0o750)
		os.WriteFile(filepath.Join(dir, "etc", "otc_gallerytest.ini"), []byte("[otc]\nstorage-path="+galleryStorage+"\nunenc-storage-path="+galleryPosts+"\n[tagger]\nmax-images-search=2\n"), 0o600)
		wd, _ := os.Getwd()
		os.Chdir(dir)
		err = cfg.Init("otc", "gallerytest")
		os.Chdir(wd)
		if err != nil {
			t.Fatal(err)
		}
	})
	storage := galleryStorage
	sesDB, sesMock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sesDB.Close() })
	sesMock.ExpectQuery("select count\\(\\*\\) from `vault`").WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(0))
	sesMock.ExpectExec("insert into `vault`").WillReturnResult(sqlmock.NewResult(1, 1))
	ses, err := session.New("owner-uuid", "test-password", true, dao.NewWithDB(sesDB))
	if err != nil {
		t.Fatal(err)
	}
	return storage, ses
}

// libraryFile stores content as the owner's encrypted blob.
func libraryFile(t *testing.T, ses *session.Session, name, mime string, content []byte) *pb.File {
	t.Helper()
	hash := strings.Repeat(string(rune('a'+len(name)%6)), 64)
	w, err := blobstore.Create(blobPath(hash), ses)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(content)
	if err := w.Commit(); err != nil {
		t.Fatal(err)
	}
	return &pb.File{Path: "/photos/" + name, Hash: hash, Mime: mime, Size: int32(len(content)), Created: timestamppb.Now()}
}

func TestSharedGalleryRoundTrip(t *testing.T) {
	storage, ses := galleryTestEnv(t)
	photo := bytes.Repeat([]byte("JPEGDATA"), 100000) // 800 KB, "image/jpeg": shown as is
	img := image.NewRGBA(image.Rect(0, 0, 40, 30))
	img.Set(5, 5, color.RGBA{255, 0, 0, 255})
	var pngBuf bytes.Buffer
	png.Encode(&pngBuf, img)
	// A big portrait photo: its preview is screen-sized, limited by height.
	tall := image.NewRGBA(image.Rect(0, 0, 3000, 4000))
	var tallBuf bytes.Buffer
	png.Encode(&tallBuf, tall)
	files := []*pb.File{
		libraryFile(t, ses, "beach.jpg", "image/jpeg", photo),             // not a real JPEG: no preview, the original is shown
		libraryFile(t, ses, "scan.tif", "image/x-scan", pngBuf.Bytes()),   // not for browsers: gets a JPEG preview
		libraryFile(t, ses, "portrait.png", "image/png", tallBuf.Bytes()), // big: a 1920x2560 preview
	}

	db, mock, _ := sqlmock.New()
	defer db.Close()
	mg := &Manager{dao: dao.NewWithDB(db), sharedLinkTTL: time.Hour}
	var insertedDesc []byte
	mock.ExpectExec("insert into `shared_links`").WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), descCapture{&insertedDesc}, 3, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	job := &galleryJob{state: &pb.SharedGalleryJob{}}
	link, err := mg.buildSharedGallery(ses, files, "Summer 2026", time.Hour, "cala.off-the.cloud", false, job)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	frag := strings.TrimPrefix(link, "https://cala.off-the.cloud/shared#")
	parts := strings.SplitN(frag, ".", 2)
	if frag == link || len(parts) != 2 || !galleryUUID.MatchString(parts[0]) || !gallerySecret.MatchString(parts[1]) {
		t.Fatalf("link %q", link)
	}
	id, secret := parts[0], parts[1]
	if job.state.Done != 3 || job.state.BytesDone != int64(len(photo)+pngBuf.Len()+tallBuf.Len()) {
		t.Errorf("progress %+v", job.state)
	}
	if bytes.Contains(insertedDesc, []byte("Summer")) || bytes.Contains(insertedDesc, []byte(secret)) {
		t.Error("the description went to the database in the clear (or with the secret)")
	}
	if d, err := ses.Decrypt(insertedDesc); err != nil || string(d) != "Summer 2026" {
		t.Errorf("stored description doesn't decrypt to the owner's: %q %v", d, err)
	}
	// Nothing on disk is readable without the link.
	filepath.Walk(filepath.Join(storage, "shared", id), func(p string, info os.FileInfo, err error) error {
		if info != nil && !info.IsDir() {
			raw, _ := os.ReadFile(p)
			if bytes.Contains(raw, []byte("JPEGDATA")) || bytes.Contains(raw, []byte("Summer")) {
				t.Errorf("%s holds plaintext", p)
			}
		}
		return nil
	})

	alive := func() {
		mock.ExpectQuery("select `created`, `expires` from `shared_links`").
			WillReturnRows(sqlmock.NewRows([]string{"created", "expires"}).AddRow(time.Now(), time.Now().Add(time.Hour)))
	}
	alive()
	mock.ExpectExec("update `shared_links` set `opens`").WillReturnResult(sqlmock.NewResult(0, 1))
	g, err := mg.OpenSharedGallery(id, secret)
	if err != nil || g.Description != "Summer 2026" || len(g.Items) != 3 || g.Items[0].Name != "beach.jpg" || g.Items[0].HasPreview || !g.Items[1].HasPreview || !g.Items[2].HasPreview {
		t.Fatalf("open: %+v %v", g, err)
	}

	// The original, in parts, byte for byte.
	var got []byte
	for off := int64(0); ; {
		alive()
		data, total, mime, err := mg.ReadSharedGalleryItem(id, secret, 0, pb.GetSharedGalleryItem_ORIGINAL, off, 300000)
		if err != nil || mime != "image/jpeg" || total != int64(len(photo)) {
			t.Fatalf("read at %d: %v %s %d", off, err, mime, total)
		}
		got = append(got, data...)
		off += int64(len(data))
		if off >= total {
			break
		}
	}
	if !bytes.Equal(got, photo) {
		t.Fatal("the original came back different")
	}
	alive()
	prev, _, mime, err := mg.ReadSharedGalleryItem(id, secret, 1, pb.GetSharedGalleryItem_PREVIEW, 0, 0)
	if err != nil || mime != "image/jpeg" || len(prev) < 3 || prev[0] != 0xFF || prev[1] != 0xD8 {
		t.Fatalf("preview: %v %s", err, mime)
	}

	alive()
	big, _, _, err := mg.ReadSharedGalleryItem(id, secret, 2, pb.GetSharedGalleryItem_PREVIEW, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if cfgImg, _, err := image.DecodeConfig(bytes.NewReader(big)); err != nil || cfgImg.Width != 1920 || cfgImg.Height != 2560 {
		t.Fatalf("portrait preview %dx%d (%v), want 1920x2560", cfgImg.Width, cfgImg.Height, err)
	}

	// The library has no thumbnails for these (not processed yet): the
	// gallery made them from the photos, at the thumbnail width.
	alive()
	th, _, mime, err := mg.ReadSharedGalleryItem(id, secret, 2, pb.GetSharedGalleryItem_THUMBNAIL, 0, 0)
	if err != nil || mime != "image/jpeg" {
		t.Fatalf("made thumbnail: %v %s", err, mime)
	}
	if c, _, err := image.DecodeConfig(bytes.NewReader(th)); err != nil || c.Width != 1000 || c.Height != 1333 {
		t.Fatalf("thumbnail %dx%d (%v), want 1000x1333", c.Width, c.Height, err)
	}
	alive()
	if _, _, _, err := mg.ReadSharedGalleryItem(id, secret, 0, pb.GetSharedGalleryItem_THUMBNAIL, 0, 0); err == nil {
		t.Fatal("a thumbnail for a file that doesn't decode")
	}

	// A wrong secret is the same answer as no gallery.
	alive()
	if _, err := mg.OpenSharedGallery(id, strings.Repeat("0", 64)); err != ErrNoSuchGallery {
		t.Fatalf("wrong secret: %v", err)
	}
	if _, err := mg.OpenSharedGallery("../../etc", secret); err != ErrNoSuchGallery {
		t.Fatalf("a path for a uuid: %v", err)
	}

	// Expired: refused, and its files go.
	mock.ExpectQuery("select `created`, `expires` from `shared_links`").
		WillReturnRows(sqlmock.NewRows([]string{"created", "expires"}).AddRow(time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)))
	mock.ExpectExec("delete from `shared_links`").WillReturnResult(sqlmock.NewResult(0, 1))
	if _, err := mg.OpenSharedGallery(id, secret); err != ErrNoSuchGallery {
		t.Fatalf("expired: %v", err)
	}
	if _, err := os.Stat(filepath.Join(storage, "shared", id)); !os.IsNotExist(err) {
		t.Fatal("an expired gallery's files are still there")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

type descCapture struct{ to *[]byte }

func (d descCapture) Match(v driver.Value) bool {
	b, ok := v.([]byte)
	*d.to = b
	return ok
}

// Low resolution: only a small JPEG of each photo is copied, under its
// name as .jpg, and the gallery says so; the originals never leave.
func TestSharedGalleryLowRes(t *testing.T) {
	storage, ses := galleryTestEnv(t)
	tall := image.NewRGBA(image.Rect(0, 0, 3000, 4000))
	var tallBuf bytes.Buffer
	png.Encode(&tallBuf, tall)
	files := []*pb.File{libraryFile(t, ses, "portrait.png", "image/png", tallBuf.Bytes())}

	db, mock, _ := sqlmock.New()
	defer db.Close()
	mg := &Manager{dao: dao.NewWithDB(db), sharedLinkTTL: time.Hour}
	mock.ExpectExec("insert into `shared_links`").WillReturnResult(sqlmock.NewResult(1, 1))
	link, err := mg.buildSharedGallery(ses, files, "small", time.Hour, "cala.off-the.cloud", true, &galleryJob{state: &pb.SharedGalleryJob{}})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(strings.TrimPrefix(link, "https://cala.off-the.cloud/shared#"), ".", 2)
	id, secret := parts[0], parts[1]
	alive := func() {
		mock.ExpectQuery("select `created`, `expires` from `shared_links`").
			WillReturnRows(sqlmock.NewRows([]string{"created", "expires"}).AddRow(time.Now(), time.Now().Add(time.Hour)))
	}
	alive()
	mock.ExpectExec("update `shared_links` set `opens`").WillReturnResult(sqlmock.NewResult(0, 1))
	g, err := mg.OpenSharedGallery(id, secret)
	if err != nil || !g.LowRes || g.Items[0].Name != "portrait.jpg" || g.Items[0].Mime != "image/jpeg" {
		t.Fatalf("open: %+v %v", g, err)
	}
	alive()
	orig, total, _, err := mg.ReadSharedGalleryItem(id, secret, 0, pb.GetSharedGalleryItem_ORIGINAL, 0, 0)
	if err != nil || total != int64(len(orig)) {
		t.Fatal(err)
	}
	if c, _, err := image.DecodeConfig(bytes.NewReader(orig)); err != nil || c.Width > 1000 {
		t.Fatalf("the shared copy is %dx%d (%v), want a thumbnail", c.Width, c.Height, err)
	}
	// Nothing in the gallery is anywhere near the original's size.
	filepath.Walk(filepath.Join(storage, "shared", id), func(p string, info os.FileInfo, err error) error {
		if info != nil && !info.IsDir() && info.Size() >= int64(tallBuf.Len()) {
			t.Errorf("%s is as big as the original", p)
		}
		return nil
	})
}

// A file that crashes the copy ends the job with an error the owner's
// dialog shows, instead of a job that never finishes, and the partial
// copy doesn't stay on disk without a row to expire it.
func TestSharedGalleryJobFinishesWhenTheCopyPanics(t *testing.T) {
	storage, ses := galleryTestEnv(t)
	f := libraryFile(t, ses, "crash.jpg", "image/jpeg", []byte("not really a jpeg"))
	galleries := func() int {
		entries, _ := os.ReadDir(filepath.Join(storage, "shared"))
		return len(entries)
	}
	before := galleries()

	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = \\?").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).
			AddRow(f.Hash, f.Mime, time.Now(), time.Now(), f.Path, f.Size))
	mg := &Manager{dao: dao.NewWithDB(db), sharedLinkTTL: time.Hour}
	// No session: reading the library blob panics, as a decoder would.
	job, err := mg.StartSharedGallery(nil, &pb.SharedGallerySource{Paths: []string{f.Path}}, "", 1, "cala.off-the.cloud", false)
	if err != nil {
		t.Fatal(err)
	}
	var state *pb.SharedGalleryJob
	for i := 0; i < 500; i++ {
		state, _ = SharedGalleryJobState(job.JobId)
		if state.Finished {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !state.Finished || state.Error == "" || state.Link != "" {
		t.Fatalf("job after a panic: %+v", state)
	}
	if got := galleries(); got != before {
		t.Errorf("%d gallery folders after the crash, want %d: the partial copy was left", got, before)
	}
}

// The archive download is asked before signing in: a gallery's id - or an
// id in upper case - must not reach the expiry check that deletes, while a
// real archive past its expiry still goes at once.
func TestOpenSharedLinkRangeOnlyExpiresArchives(t *testing.T) {
	storage, _ := galleryTestEnv(t)
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mg := &Manager{dao: dao.NewWithDB(db), sharedLinkTTL: 7 * 24 * time.Hour}

	// A gallery shared for 30 days, 8 days ago: not an archive, so not found
	// - and its folder stays.
	gallery := "0b9f3c3e-6a51-4c1e-9d0a-3f6f2b7c8d10"
	os.MkdirAll(galleryDir(gallery), 0o750)
	mock.ExpectQuery("select `created`, `expires` from `shared_links` where `uuid` = \\? and `kind` = 'archive'").
		WithArgs(gallery).
		WillReturnRows(sqlmock.NewRows([]string{"created", "expires"}))
	if _, _, err := mg.OpenSharedLinkRange(gallery, "x", 0, 1); err == nil {
		t.Error("a gallery was served as an archive")
	}
	if _, err := os.Stat(galleryDir(gallery)); err != nil {
		t.Errorf("the gallery's folder went: %v", err)
	}

	// Upper case: refused before the database is asked.
	if _, _, err := mg.OpenSharedLinkRange(strings.ToUpper(gallery), "x", 0, 1); err == nil {
		t.Error("an upper-cased id was accepted")
	}

	// An archive past the device's default expiry is deleted right away.
	archive := "5d2c1b0a-9e8f-4a7b-8c6d-5e4f3a2b1c0d"
	os.WriteFile(filepath.Join(storage, archive), []byte("zip"), 0o600)
	mock.ExpectQuery("select `created`, `expires` from `shared_links` where `uuid` = \\? and `kind` = 'archive'").
		WithArgs(archive).
		WillReturnRows(sqlmock.NewRows([]string{"created", "expires"}).AddRow(time.Now().Add(-8*24*time.Hour), nil))
	mock.ExpectExec("delete from `shared_links` where `uuid` = \\?").WithArgs(archive).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if _, _, err := mg.OpenSharedLinkRange(archive, "x", 0, 1); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("an expired archive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(storage, archive)); !os.IsNotExist(err) {
		t.Errorf("the expired archive is still on disk: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A share archive leaves photos and videos uncompressed and compresses
// the rest, every entry is deflated so a streaming unzipper can read it,
// and every file comes out of it byte for byte.
func TestGetSharedLinkStoresMediaAndDeflatesTheRest(t *testing.T) {
	storage, ses := galleryTestEnv(t)
	photo := bytes.Repeat([]byte("JPEG"), 5000)
	notes := bytes.Repeat([]byte("some notes "), 5000)
	clip := bytes.Repeat([]byte("MP4 "), 40000) // more than one stored block
	readme := bytes.Repeat([]byte("read me "), 5000)
	// Names of different lengths: libraryFile derives the hash from it.
	// Media and text alternate, so each level's writer is used again.
	files := []*pb.File{
		libraryFile(t, ses, "a.jpg", "image/jpeg", photo),
		libraryFile(t, ses, "notes.txt", "text/plain; charset=utf-8", notes),
		libraryFile(t, ses, "clip.mp4", "video/mp4", clip),
		libraryFile(t, ses, "readme", "", readme),
	}
	db, mock, _ := sqlmock.New()
	defer db.Close()
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
		mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` = \\?").WithArgs(f.Path).
			WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).
				AddRow(f.Hash, f.Mime, time.Now(), time.Now(), f.Path, f.Size))
	}
	mock.ExpectExec("insert into `shared_links`").WillReturnResult(sqlmock.NewResult(1, 1))
	mg := &Manager{dao: dao.NewWithDB(db), sharedLinkTTL: time.Hour}
	link, err := mg.GetSharedLink(ses, paths, "cala.off-the.cloud")
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err) // one query per file, not two
	}
	id, secret, _ := strings.Cut(strings.TrimPrefix(link, "https://cala.off-the.cloud/"+CDownloadAttr), "_")
	defer os.Remove(filepath.Join(storage, id))
	raw, err := blobstore.ReadAll(filepath.Join(storage, id), linkKeys{getCipher(secret)})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		compressed bool
		content    []byte
	}{"a.jpg": {false, photo}, "notes.txt": {true, notes}, "clip.mp4": {false, clip}, "readme": {true, readme}}

	// Through the central directory, as Finder or unzip read it.
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != len(want) {
		t.Errorf("%d entries, want %d", len(zr.File), len(want))
	}
	for _, zf := range zr.File {
		w, ok := want[zf.Name]
		if !ok {
			t.Fatalf("unexpected entry %q", zf.Name)
		}
		if zf.Method != zip.Deflate {
			t.Errorf("%s: method %d, want deflate", zf.Name, zf.Method)
		}
		// The content repeats, so any compression would shrink it a lot.
		if compressed := zf.CompressedSize64 < zf.UncompressedSize64/2; compressed != w.compressed {
			t.Errorf("%s: %d bytes stored for %d, compressed %v, want %v", zf.Name, zf.CompressedSize64, zf.UncompressedSize64, compressed, w.compressed)
		}
		rc, err := zf.Open()
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(rc)
		rc.Close()
		if err != nil || !bytes.Equal(got, w.content) {
			t.Errorf("%s: content changed (%v)", zf.Name, err)
		}
	}

	// Front to back, as a streaming reader does.
	streamed := streamZip(t, raw)
	if len(streamed) != len(want) {
		t.Errorf("streamed %d entries, want %d", len(streamed), len(want))
	}
	for name, w := range want {
		if !bytes.Equal(streamed[name], w.content) {
			t.Errorf("%s: streamed content changed", name)
		}
	}
}

// streamZip reads an archive front to back from its local headers only,
// never its central directory, the way java.util.zip.ZipInputStream or
// funzip do. archive/zip writes each file's CRC and sizes after its data,
// so such a reader finds where an entry ends only when the entry is
// deflated: a stored one fails the test.
func streamZip(t *testing.T, raw []byte) map[string][]byte {
	t.Helper()
	r := bytes.NewReader(raw)
	got := map[string][]byte{}
	for {
		var hdr [30]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			t.Fatalf("local header: %v", err)
		}
		switch sig := binary.LittleEndian.Uint32(hdr[0:]); sig {
		case 0x02014b50: // the central directory: no more entries
			return got
		case 0x04034b50:
		default:
			t.Fatalf("signature %#x where an entry should start", sig)
		}
		flags := binary.LittleEndian.Uint16(hdr[6:])
		method := binary.LittleEndian.Uint16(hdr[8:])
		name := make([]byte, binary.LittleEndian.Uint16(hdr[26:]))
		if _, err := io.ReadFull(r, name); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Seek(int64(binary.LittleEndian.Uint16(hdr[28:])), io.SeekCurrent); err != nil {
			t.Fatal(err)
		}
		if flags&0x8 == 0 {
			t.Fatalf("%s: sizes in the local header, not after the data", name)
		}
		if method != zip.Deflate {
			t.Fatalf("%s: method %d with its sizes after its data: a streaming reader can't find its end", name, method)
		}
		// bytes.Reader is an io.ByteReader: flate stops at the end of
		// the stream instead of reading ahead.
		content, err := io.ReadAll(flate.NewReader(r))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var dd [16]byte // signature, CRC-32, 32-bit sizes (small files)
		if _, err := io.ReadFull(r, dd[:]); err != nil {
			t.Fatal(err)
		}
		if binary.LittleEndian.Uint32(dd[0:]) != 0x08074b50 ||
			binary.LittleEndian.Uint32(dd[4:]) != crc32.ChecksumIEEE(content) ||
			binary.LittleEndian.Uint32(dd[12:]) != uint32(len(content)) {
			t.Fatalf("%s: the data descriptor doesn't follow the deflated data", name)
		}
		got[string(name)] = content
	}
}

// The manifest is decrypted once for a page's many requests, but every
// request still checks the link (a wrong secret, an expiry), and a
// deleted gallery leaves nothing behind in memory.
func TestSharedGalleryManifestIsCachedButStillChecked(t *testing.T) {
	_, ses := galleryTestEnv(t)
	img := image.NewRGBA(image.Rect(0, 0, 40, 30))
	var pngBuf bytes.Buffer
	png.Encode(&pngBuf, img)
	files := []*pb.File{libraryFile(t, ses, "cached.png", "image/png", pngBuf.Bytes())}
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mg := &Manager{dao: dao.NewWithDB(db), sharedLinkTTL: time.Hour}
	mock.ExpectExec("insert into `shared_links`").WillReturnResult(sqlmock.NewResult(1, 1))
	link, err := mg.buildSharedGallery(ses, files, "cache", time.Hour, "cala.off-the.cloud", false, &galleryJob{state: &pb.SharedGalleryJob{}})
	if err != nil {
		t.Fatal(err)
	}
	id, secret, _ := strings.Cut(strings.TrimPrefix(link, "https://cala.off-the.cloud/shared#"), ".")
	alive := func() {
		mock.ExpectQuery("select `created`, `expires` from `shared_links`").
			WillReturnRows(sqlmock.NewRows([]string{"created", "expires"}).AddRow(time.Now(), time.Now().Add(time.Hour)))
	}

	alive()
	first, _, _, err := mg.ReadSharedGalleryItem(id, secret, 0, pb.GetSharedGalleryItem_ORIGINAL, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if mg.galleryCache[id] == nil {
		t.Fatal("the manifest wasn't kept")
	}
	alive()
	again, _, _, err := mg.ReadSharedGalleryItem(id, secret, 0, pb.GetSharedGalleryItem_ORIGINAL, 0, 0)
	if err != nil || !bytes.Equal(first, again) {
		t.Fatalf("a cached read differs: %v", err)
	}
	alive()
	if _, _, _, err := mg.ReadSharedGalleryItem(id, strings.Repeat("0", 64), 0, pb.GetSharedGalleryItem_ORIGINAL, 0, 0); err != ErrNoSuchGallery {
		t.Errorf("a wrong secret after a cached read: %v", err)
	}
	// Expired: deleted on this request, cache included.
	mock.ExpectQuery("select `created`, `expires` from `shared_links`").
		WillReturnRows(sqlmock.NewRows([]string{"created", "expires"}).AddRow(time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)))
	mock.ExpectExec("delete from `shared_links`").WillReturnResult(sqlmock.NewResult(0, 1))
	if _, _, _, err := mg.ReadSharedGalleryItem(id, secret, 0, pb.GetSharedGalleryItem_ORIGINAL, 0, 0); err != ErrNoSuchGallery {
		t.Errorf("an expired gallery: %v", err)
	}
	if mg.galleryCache[id] != nil {
		t.Error("a deleted gallery's manifest stayed in memory")
	}
	if _, err := os.Stat(galleryDir(id)); !os.IsNotExist(err) {
		t.Errorf("the expired gallery is still on disk: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// At most cGalleryCacheMax manifests are kept, the least recently used
// going first.
func TestGalleryCacheIsBounded(t *testing.T) {
	mg := &Manager{}
	fi, err := os.Stat(".")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < cGalleryCacheMax+3; i++ {
		mg.cacheGallery(strings.Repeat(string(rune('a'+i)), 4), galleryCacheTag("s"), fi, &galleryManifest{}, linkKeys{})
		time.Sleep(time.Millisecond)
	}
	if len(mg.galleryCache) != cGalleryCacheMax {
		t.Fatalf("%d manifests kept, want %d", len(mg.galleryCache), cGalleryCacheMax)
	}
	if mg.galleryCache["aaaa"] != nil || mg.galleryCache[strings.Repeat(string(rune('a'+cGalleryCacheMax+2)), 4)] == nil {
		t.Error("evicted the wrong manifest")
	}
}

// A photo too large to decode gets no preview, and isn't handed to ffmpeg
// to decode anyway, unbounded (issue #188); the gallery is still made.
func TestGalleryPreviewKeepsTooLargeImagesFromFFmpeg(t *testing.T) {
	_, ses := galleryTestEnv(t)
	calls := 0
	orig := stillFFmpeg
	stillFFmpeg = func([]byte) (image.Image, error) {
		calls++
		return image.NewRGBA(image.Rect(0, 0, 1, 1)), nil
	}
	t.Cleanup(func() { stillFFmpeg = orig })

	mg := &Manager{sharedLinkTTL: time.Hour}
	f := libraryFile(t, ses, "panorama-400mp.png", "image/png", pngHeader(20000, 20000))
	if _, _, err := mg.galleryPreview(ses, f); !errors.Is(err, errImageTooLarge) {
		t.Fatalf("got %v, want errImageTooLarge", err)
	}
	if calls != 0 {
		t.Fatalf("the too-large photo was handed to ffmpeg %d time(s)", calls)
	}
}
