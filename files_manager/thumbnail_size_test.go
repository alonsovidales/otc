// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/exifinfo"
	"github.com/alonsovidales/otc/session"
)

// testJPEG is a w x h JPEG, with an EXIF Orientation tag when orientation
// is not 0.
func testJPEG(t *testing.T, w, h, orientation int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8(x + y), 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	if orientation == 0 {
		return buf.Bytes()
	}
	// APP1 "Exif": a little-endian TIFF header and one IFD entry,
	// Orientation (0x0112), SHORT, 1 value.
	tiff := []byte{'I', 'I', 0x2a, 0, 8, 0, 0, 0, 1, 0, 0x12, 0x01, 3, 0, 1, 0, 0, 0, byte(orientation), 0, 0, 0, 0, 0, 0, 0}
	payload := append([]byte("Exif\x00\x00"), tiff...)
	seg := []byte{0xff, 0xe1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	seg = append(seg, payload...)
	out := append([]byte{0xff, 0xd8}, seg...)
	return append(out, buf.Bytes()[2:]...)
}

func jpegSize(t *testing.T, b []byte) (int, int) {
	t.Helper()
	c, err := jpeg.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("not a JPEG: %v", err)
	}
	return c.Width, c.Height
}

// storedSize is the size of the thumbnail sealed at path.
func storedSize(t *testing.T, ses *session.Session, path string) (int, int) {
	t.Helper()
	b, err := blobstore.ReadAll(path, ses)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return jpegSize(t, b)
}

type wh struct{ w, h int }

// The big thumbnail fits a 1000 px box on its longest side - a portrait
// photo is capped by its height, a long screenshot no longer keeps its
// whole height - and is never scaled up.
func TestFitWithinCapsTheLongestSide(t *testing.T) {
	for _, c := range []struct {
		name     string
		in, want wh
	}{
		{"landscape", wh{4000, 3000}, wh{1000, 750}},
		{"portrait", wh{3000, 4000}, wh{750, 1000}},
		{"square", wh{2048, 2048}, wh{1000, 1000}},
		{"very tall", wh{1170, 25000}, wh{47, 1000}},
		{"very wide", wh{30000, 1000}, wh{1000, 33}},
		{"one pixel wide stays a pixel", wh{1, 100000}, wh{1, 1000}},
		{"smaller than the cap", wh{640, 480}, wh{640, 480}},
		{"exactly the cap", wh{1000, 600}, wh{1000, 600}},
	} {
		if w, h := fitWithin(c.in.w, c.in.h, 1000); w != c.want.w || h != c.want.h {
			t.Errorf("%s: %dx%d gave %dx%d, want %dx%d", c.name, c.in.w, c.in.h, w, h, c.want.w, c.want.h)
		}
	}
	if w, h := fitWithin(4000, 3000, 0); w != 4000 || h != 3000 {
		t.Errorf("no cap scaled to %dx%d", w, h)
	}
}

// The small one: 400 px on the shorter side, the longer at most 800, from
// the big one's size; never scaled up, never cropped.
func TestSmallThumbnailSize(t *testing.T) {
	for _, c := range []struct {
		name     string
		in, want wh
	}{
		{"landscape", wh{1000, 750}, wh{533, 400}},
		{"portrait", wh{750, 1000}, wh{400, 533}},
		{"square", wh{1000, 1000}, wh{400, 400}},
		{"2:1 reaches both bounds", wh{1000, 500}, wh{800, 400}},
		{"panorama: the longer side bounds it", wh{1000, 250}, wh{800, 200}},
		{"very tall", wh{47, 1000}, wh{38, 800}},
		{"smaller than the tile", wh{320, 240}, wh{320, 240}},
		{"short side already 400", wh{600, 400}, wh{600, 400}},
		{"a tiny long strip", wh{10, 2000}, wh{4, 800}},
	} {
		if w, h := smallThumbnailSize(c.in.w, c.in.h); w != c.want.w || h != c.want.h {
			t.Errorf("%s: %dx%d gave %dx%d, want %dx%d", c.name, c.in.w, c.in.h, w, h, c.want.w, c.want.h)
		}
	}
}

func TestTestJPEGCarriesItsOrientation(t *testing.T) {
	ex, err := exifinfo.FromJPEG(testJPEG(t, 64, 32, 6))
	if err != nil || ex == nil || ex.Orientation != 6 {
		t.Fatalf("orientation not read back: %+v, %v", ex, err)
	}
}

// Processing writes both thumbnails from the photo as shown: a landscape
// sensor image tagged "rotate 90" is a portrait, capped by its height.
func TestProcessingWritesBothThumbnailsRotated(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg, mock, _ := recordingMock(t)
	keptOut(mg)
	h := testHash("7")
	file := storedFile(t, ses, h, "/Phone/IMG_1.jpg", "image/jpeg", testJPEG(t, 2000, 1500, 6))
	referenced(mock, h) // dropIfOrphaned
	expectNoAlert(mock)

	if !mg.thumbnailJob(mediaJob{ses: ses, file: file, target: blobPath(h)}) {
		t.Fatal("processing failed")
	}
	noAlert(t, mock)
	if w, hh := storedSize(t, ses, thumbnailPath(h)); w != 750 || hh != 1000 {
		t.Errorf("big thumbnail %dx%d, want 750x1000 (rotated, capped by its height)", w, hh)
	}
	if w, hh := storedSize(t, ses, smallThumbnailPath(h)); w != 400 || hh != 533 {
		t.Errorf("small thumbnail %dx%d, want 400x533", w, hh)
	}
}

// A photo smaller than both still gets both, at its own size.
func TestProcessingNeverScalesUp(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg, mock, _ := recordingMock(t)
	keptOut(mg)
	h := testHash("8")
	file := storedFile(t, ses, h, "/Phone/small.jpg", "image/jpeg", testJPEG(t, 300, 200, 0))
	referenced(mock, h)
	expectNoAlert(mock)
	if !mg.thumbnailJob(mediaJob{ses: ses, file: file, target: blobPath(h)}) {
		t.Fatal("processing failed")
	}
	noAlert(t, mock)
	for _, p := range []string{thumbnailPath(h), smallThumbnailPath(h)} {
		if w, hh := storedSize(t, ses, p); w != 300 || hh != 200 {
			t.Errorf("%s is %dx%d, want 300x200", p, w, hh)
		}
	}
}

// A video's poster gets both too, from the same frame.
func TestProcessingAVideoWritesBothThumbnails(t *testing.T) {
	requireFFmpeg(t)
	_, ses := galleryTestEnv(t)
	mg, mock, _ := recordingMock(t)
	keptOut(mg)
	h := testHash("9")
	content := makeTestVideoSized(t, 1, 720, 1280)
	file := storedFile(t, ses, h, "/Phone/clip.mp4", "video/mp4", content)
	referenced(mock, h)
	expectNoAlert(mock)
	if !mg.processMedia(ses, file, blobPath(h), content, stageThumbnail) {
		t.Fatal("processing failed")
	}
	noAlert(t, mock)
	bw, bh := storedSize(t, ses, thumbnailPath(h))
	if bh != 1000 || bw != 563 {
		t.Errorf("poster %dx%d, want 563x1000", bw, bh)
	}
	sw, sh := smallThumbnailSize(bw, bh)
	if w, hh := storedSize(t, ses, smallThumbnailPath(h)); w != sw || hh != sh || w != 400 {
		t.Errorf("small poster %dx%d, want %dx%d", w, hh, sw, sh)
	}
}

// Issue #192: content kept out of Images is never analysed, but Files
// shows it - it gets both thumbnails.
func TestKeptOutContentGetsBothThumbnails(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg, mock, _ := recordingMock(t)
	keptOut(mg, "/Private/")
	h := testHash("6")
	file := storedFile(t, ses, h, "/Private/a.jpg", "image/jpeg", testJPEG(t, 1200, 900, 0))

	// guardAnalysis: not visible, recorded as skipped.
	mock.ExpectQuery(reVisible).WillReturnRows(hashRows())
	referenced(mock, h)
	mock.ExpectExec("insert ignore into `skipped_analysis`").WillReturnResult(sqlmock.NewResult(0, 1))
	referenced(mock, h) // dropIfOrphaned
	pendingCleared(mock, h)

	mg.processMediaContent(ses, file, blobPath(h), testJPEG(t, 1200, 900, 0))
	allMet(t, mock)
	if w, hh := storedSize(t, ses, thumbnailPath(h)); w != 1000 || hh != 750 {
		t.Errorf("big thumbnail %dx%d", w, hh)
	}
	if w, hh := storedSize(t, ses, smallThumbnailPath(h)); w != 533 || hh != 400 {
		t.Errorf("small thumbnail %dx%d", w, hh)
	}
}

// What removes the content removes both thumbnails (and the marker), under
// the hash's lock: a delete, and processing that finds its file deleted.
func TestBothThumbnailsGoWithTheContent(t *testing.T) {
	_, ses := galleryTestEnv(t)
	for name, remove := range map[string]func(mg *Manager, hash string){
		"removeBlobIfUnused": func(mg *Manager, hash string) {
			if err := mg.removeBlobIfUnused(hash); err != nil {
				t.Fatal(err)
			}
		},
		"dropIfOrphaned": func(mg *Manager, hash string) { mg.dropIfOrphaned(hash) },
	} {
		t.Run(name, func(t *testing.T) {
			mg, mock, _ := recordingMock(t)
			h := testHash("4")
			storedFile(t, ses, h, "/a.jpg", "image/jpeg", []byte("content"))
			for _, p := range []string{thumbnailPath(h), smallThumbnailPath(h), blobPath(h) + cNoThumbnailSuffix} {
				if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			mock.ExpectQuery(reReferenced).WithArgs(h, h).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
			remove(mg, h)
			for _, p := range []string{blobPath(h), thumbnailPath(h), smallThumbnailPath(h), blobPath(h) + cNoThumbnailSuffix} {
				if _, err := os.Stat(p); !os.IsNotExist(err) {
					t.Errorf("%s is still there", p)
				}
			}
		})
	}
}

// Content another file still uses keeps both.
func TestBothThumbnailsStayWhileReferenced(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg, mock, _ := recordingMock(t)
	h := testHash("3")
	storedFile(t, ses, h, "/a.jpg", "image/jpeg", []byte("content"))
	for _, p := range []string{thumbnailPath(h), smallThumbnailPath(h)} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Remove(p) })
	}
	referenced(mock, h)
	if err := mg.removeBlobIfUnused(h); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{thumbnailPath(h), smallThumbnailPath(h)} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s went: %v", p, err)
		}
	}
}

// The test ini has no max-thumbnail-width-px: the cap is the default, as
// for a missing or broken value on a device.
func TestThumbnailMaxSideDefaults(t *testing.T) {
	galleryTestEnv(t)
	if got := ThumbnailMaxSide(); got != cDefaultThumbnailMaxSide {
		t.Errorf("got %d, want %d", got, cDefaultThumbnailMaxSide)
	}
}
