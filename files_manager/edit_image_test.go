// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"testing"

	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/exifinfo"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// testExif is a little-endian TIFF structure as a camera writes one: IFD0
// with the maker and orientation and a pointer to the Exif IFD (the date
// taken and the dimensions), then IFD1, the embedded thumbnail's.
func testExif(orientation, w, h int) []byte {
	le := binary.LittleEndian
	var b []byte
	put16 := func(v int) { b = le.AppendUint16(b, uint16(v)) }
	put32 := func(v int) { b = le.AppendUint32(b, uint32(v)) }
	entry := func(tag, typ, count, value int) { put16(tag); put16(typ); put32(count); put32(value) }
	short := func(tag, value int) { put16(tag); put16(3); put32(1); put16(value); put16(0) }

	const ifd0 = 8
	const ifd0Size = 2 + 3*12 + 4
	makeAt := ifd0 + ifd0Size // "Test\0"
	exifAt := makeAt + 6
	const exifSize = 2 + 3*12 + 4
	dateAt := exifAt + exifSize // "2021:05:06 07:08:09\0"
	ifd1 := dateAt + 20

	b = append(b, "II*\x00"...)
	put32(ifd0)
	put16(3)
	entry(0x010F, 2, 5, makeAt)
	short(cTagOrientation, orientation)
	entry(cTagExifIFD, 4, 1, exifAt)
	put32(ifd1)
	b = append(b, "Test\x00\x00"...)
	put16(3)
	entry(0x9003, 2, 20, dateAt)
	entry(cTagPixelX, 4, 1, w)
	entry(cTagPixelY, 4, 1, h)
	put32(0)
	b = append(b, "2021:05:06 07:08:09\x00"...)
	put16(1)
	short(0x0103, 6)
	put32(0)
	return b
}

// testICC is the start of an ICC profile: its header, with the signature
// the readers check.
func testICC(n int) []byte {
	icc := make([]byte, n)
	binary.BigEndian.PutUint32(icc, uint32(n))
	copy(icc[36:], "acsp")
	return icc
}

// quadrants is a w x h picture in four colours, top left red, top right
// green, bottom left blue, bottom right white: where each lands says how
// it was turned.
func quadrants(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBA{255, 0, 0, 255}
			switch {
			case x >= w/2 && y < h/2:
				c = color.NRGBA{0, 255, 0, 255}
			case x < w/2 && y >= h/2:
				c = color.NRGBA{0, 0, 255, 255}
			case x >= w/2 && y >= h/2:
				c = color.NRGBA{255, 255, 255, 255}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

// cameraJPEG is quadrants as a camera's JPEG: with testExif (orientation)
// and an ICC profile.
func cameraJPEG(t *testing.T, w, h, orientation int) []byte {
	t.Helper()
	var enc bytes.Buffer
	if err := jpeg.Encode(&enc, quadrants(w, h), &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return withMetadata(enc.Bytes(), testExif(orientation, w, h), iccSegments(testICC(600)))
}

// colourAt names the colour at x, y of img: r, g, b, w (white) or ?.
func colourAt(img image.Image, x, y int) string {
	r, g, b, _ := img.At(img.Bounds().Min.X+x, img.Bounds().Min.Y+y).RGBA()
	hi := func(v uint32) bool { return v > 0xA000 }
	lo := func(v uint32) bool { return v < 0x6000 }
	switch {
	case hi(r) && hi(g) && hi(b):
		return "w"
	case hi(r) && lo(g) && lo(b):
		return "r"
	case lo(r) && hi(g) && lo(b):
		return "g"
	case lo(r) && lo(g) && hi(b):
		return "b"
	}
	return "?"
}

// corners is the colours of img's four quadrants: top left, top right,
// bottom left, bottom right.
func corners(img image.Image) string {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	return colourAt(img, w/4, h/4) + colourAt(img, 3*w/4, h/4) + colourAt(img, w/4, 3*h/4) + colourAt(img, 3*w/4, 3*h/4)
}

// One pass with thenTurned is the photo's own orientation and then the
// turn, for every orientation and turn.
func TestThenTurnedIsTheTwoPasses(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	for i := range img.Pix {
		img.Pix[i] = byte(i * 7)
	}
	for o := 1; o <= 8; o++ {
		for turns := 0; turns < 4; turns++ {
			want := applyOrientation(applyOrientation(img, o), editTurn[turns])
			got := applyOrientation(img, thenTurned(o, turns))
			wb, gb := want.Bounds(), got.Bounds()
			if wb.Dx() != gb.Dx() || wb.Dy() != gb.Dy() {
				t.Fatalf("orientation %d, %d turns: %v, want %v", o, turns, gb, wb)
			}
			for y := 0; y < wb.Dy(); y++ {
				for x := 0; x < wb.Dx(); x++ {
					if want.At(wb.Min.X+x, wb.Min.Y+y) != got.At(gb.Min.X+x, gb.Min.Y+y) {
						t.Fatalf("orientation %d, %d turns: pixel %d,%d differs (one pass as %d)", o, turns, x, y, thenTurned(o, turns))
					}
				}
			}
		}
	}
}

func TestEditRectStaysInsideThePhoto(t *testing.T) {
	for _, c := range []struct {
		crop *pb.EditCrop
		want image.Rectangle
	}{
		{nil, image.Rect(0, 0, 100, 50)},
		{&pb.EditCrop{}, image.Rect(0, 0, 100, 50)},
		{&pb.EditCrop{X: 0.1, Y: 0.2, Width: 0.5, Height: 0.5}, image.Rect(10, 10, 60, 35)},
		{&pb.EditCrop{X: -1, Y: -1, Width: 5, Height: 5}, image.Rect(0, 0, 100, 50)},
		{&pb.EditCrop{X: 1, Y: 1, Width: 0.0001, Height: 0.0001}, image.Rect(99, 49, 100, 50)},
	} {
		if got := editRect(c.crop, 100, 50); got != c.want {
			t.Errorf("%v: %v, want %v", c.crop, got, c.want)
		}
	}
}

func TestEditKindOfGoesByTheBytes(t *testing.T) {
	heic, err := os.ReadFile("testdata/rotate.heic")
	if err != nil {
		t.Fatal(err)
	}
	var pngBuf bytes.Buffer
	png.Encode(&pngBuf, quadrants(4, 4))
	apng := append(bytes.Clone(pngBuf.Bytes()[:33]), append([]byte("\x00\x00\x00\x08acTL"), pngBuf.Bytes()[33:]...)...)
	webp := []byte("RIFF\x10\x00\x00\x00WEBPVP8 \x04\x00\x00\x00\x00\x00\x00\x00")
	animated := []byte("RIFF\x10\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x02ANIM\x00\x00")
	for _, c := range []struct {
		name    string
		content []byte
		want    editKind
	}{
		{"jpeg", cameraJPEG(t, 8, 8, 1), editJPEG},
		{"png", pngBuf.Bytes(), editPNG},
		{"animated png", apng, editNone},
		{"heic", heic, editPhoto},
		{"webp", webp, editPhoto},
		{"animated webp", animated, editNone},
		{"gif", []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00\x00\x00\x00\x00"), editNone},
		{"video", []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"), editNone},
		{"empty", nil, editNone},
	} {
		if got := editKindOf(c.content); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// A camera JPEG turned and cropped: written again, upright, with the
// camera's details and colour profile, its orientation 1, its dimensions
// the edit's and no thumbnail of the photo before it.
func TestRenderEditKeepsTheCameraDetails(t *testing.T) {
	// Stored 80x40 on its side (orientation 6): shown 40x80, red top
	// right, blue bottom left... as applyOrientation turns it.
	src := cameraJPEG(t, 80, 40, 6)
	shown := applyOrientation(quadrants(80, 40), 6)
	r, err := renderEdit(src, editJPEG, 1, &pb.EditCrop{X: 0, Y: 0, Width: 1, Height: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if r.mime != "image/jpeg" || r.ext != ".jpg" || r.fromW != 40 || r.fromH != 80 || r.toW != 80 || r.toH != 20 {
		t.Fatalf("result %s %s from %dx%d to %dx%d", r.mime, r.ext, r.fromW, r.fromH, r.toW, r.toH)
	}
	img, err := jpeg.Decode(bytes.NewReader(r.content))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 80 || b.Dy() != 20 {
		t.Fatalf("decoded %v", b)
	}
	// The shown photo turned once more: its top half is what was its left.
	want := applyOrientation(shown, 6)
	if got, wantTop := colourAt(img, 10, 10)+colourAt(img, 70, 10), colourAt(want, 10, 10)+colourAt(want, 70, 10); got != wantTop {
		t.Errorf("the edit's top row reads %s, want %s", got, wantTop)
	}
	info, err := exifinfo.FromJPEG(r.content)
	if err != nil {
		t.Fatal(err)
	}
	if info.Orientation != 1 || info.CameraMake != "Test" || !info.HasTakenAt || info.TakenClock.Year() != 2021 {
		t.Errorf("EXIF: orientation %d, make %q, taken %v %v", info.Orientation, info.CameraMake, info.HasTakenAt, info.TakenClock)
	}
	exif, icc := jpegMetadata(r.content)
	tv, _ := newTIFF(exif)
	ifd0, _ := tv.u32(4)
	_, next, _ := tv.ifd(ifd0)
	if n, _ := tv.u32(next); n != 0 {
		t.Errorf("the thumbnail of the photo before the edit is still there (IFD1 at %d)", n)
	}
	if info.Width != 80 || info.Height != 20 {
		t.Errorf("EXIF dimensions %dx%d, want 80x20", info.Width, info.Height)
	}
	if len(icc) != 1 || !bytes.Equal(icc[0], iccSegments(testICC(600))[0]) {
		t.Errorf("the colour profile is not the photo's: %d segments", len(icc))
	}
}

// A camera JPEG only turned is turned by its tag: every other byte stays.
func TestRenderEditTurnsAJPEGLosslessly(t *testing.T) {
	src := cameraJPEG(t, 80, 40, 6)
	r, err := renderEdit(src, editJPEG, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.content) != len(src) {
		t.Fatalf("%d bytes, want the photo's %d", len(r.content), len(src))
	}
	diff := 0
	for i := range src {
		if src[i] != r.content[i] {
			diff++
		}
	}
	if diff == 0 || diff > 2 {
		t.Errorf("%d bytes changed, want the orientation's alone", diff)
	}
	info, err := exifinfo.FromJPEG(r.content)
	if err != nil || info.Orientation != 3 {
		t.Fatalf("orientation %v (%v), want 3 (on its side, then a quarter turn)", info, err)
	}
	if r.fromW != 40 || r.fromH != 80 || r.toW != 80 || r.toH != 40 {
		t.Errorf("from %dx%d to %dx%d", r.fromW, r.fromH, r.toW, r.toH)
	}
	// Without an orientation tag it is written again instead.
	var plain bytes.Buffer
	jpeg.Encode(&plain, quadrants(20, 10), nil)
	r, err = renderEdit(plain.Bytes(), editJPEG, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(r.content))
	if err != nil || img.Bounds().Dx() != 10 || img.Bounds().Dy() != 20 {
		t.Fatalf("a JPEG without EXIF turned: %v %v", img.Bounds(), err)
	}
	if got, want := corners(img), corners(applyOrientation(quadrants(20, 10), 6)); got != want {
		t.Errorf("turned: %s, want %s", got, want)
	}
}

// A HEIC is written as an upright JPEG, its own turn (irot) in the pixels.
func TestRenderEditOfAHEICIsAnUprightJPEG(t *testing.T) {
	heic, err := os.ReadFile("testdata/eightbit-grid.heic")
	if err != nil {
		t.Fatal(err)
	}
	upright, err := mgForEditTest().heicToJpeg(heic, 90, 1)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(upright))
	if err != nil {
		t.Fatal(err)
	}
	r, err := renderEdit(heic, editPhoto, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.mime != "image/jpeg" || r.fromW != cfg.Width || r.fromH != cfg.Height || r.toW != cfg.Width || r.toH != cfg.Height {
		t.Fatalf("%s from %dx%d to %dx%d, the photo shown is %dx%d", r.mime, r.fromW, r.fromH, r.toW, r.toH, cfg.Width, cfg.Height)
	}
	if info, err := exifinfo.FromJPEG(r.content); err == nil && info.Orientation != 1 {
		t.Errorf("the edit's EXIF says orientation %d", info.Orientation)
	}
}

// A PNG stays a PNG, every pixel as it was turned.
func TestRenderEditOfAPNGIsAPNG(t *testing.T) {
	var src bytes.Buffer
	png.Encode(&src, quadrants(20, 10))
	r, err := renderEdit(src.Bytes(), editPNG, 3, &pb.EditCrop{X: 0, Y: 0.5, Width: 1, Height: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if r.mime != "image/png" || r.ext != ".png" {
		t.Fatalf("%s %s", r.mime, r.ext)
	}
	img, err := png.Decode(bytes.NewReader(r.content))
	if err != nil {
		t.Fatal(err)
	}
	turned := applyOrientation(quadrants(20, 10), 8)
	want := turned.(interface {
		SubImage(image.Rectangle) image.Image
	}).SubImage(image.Rect(0, 10, 10, 20))
	if img.Bounds().Dx() != 10 || img.Bounds().Dy() != 10 {
		t.Fatalf("%v", img.Bounds())
	}
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			a, b := color.NRGBAModel.Convert(img.At(x, y)), color.NRGBAModel.Convert(want.At(x, y+10))
			if a != b {
				t.Fatalf("pixel %d,%d: %v, want %v", x, y, a, b)
			}
		}
	}
}

// Faces move with the edit: turned, cropped, and gone when the crop cut
// most of one off.
func TestFacesMoveWithTheEdit(t *testing.T) {
	r := &editResult{turns: 1, fromW: 100, fromH: 50, toW: 50, toH: 60, crop: image.Rect(0, 20, 50, 80)}
	// In the 100x50 photo at 10..20 x 5..15: turned clockwise into the
	// 50x100 one at (50-15..50-5) x (10..20) = 35..45 x 10..20, above the
	// crop (which starts at 20): cut off.
	if _, ok := r.moveFace(dao.FaceBox{X: 10, Y: 5, W: 10, H: 10}); ok {
		t.Error("a face the crop cut off is still there")
	}
	// At 40..60 x 10..30: turned into 20..40 x 40..60, inside the crop:
	// 20..40 x 20..40 in the edit.
	if got, ok := r.moveFace(dao.FaceBox{X: 40, Y: 10, W: 20, H: 20}); !ok || got != (dao.FaceBox{X: 20, Y: 20, W: 20, H: 20}) {
		t.Errorf("a face inside the crop: %v %v", got, ok)
	}
	// Half in, half out (turned into 0..10 x 15..25, the crop from 20): a
	// face mostly kept stays, cut to what is left.
	if got, ok := r.moveFace(dao.FaceBox{X: 15, Y: 40, W: 10, H: 10}); !ok || got != (dao.FaceBox{X: 0, Y: 0, W: 10, H: 5}) {
		t.Errorf("a face half kept: %v %v", got, ok)
	}
	// A photo analysed shrunk (over cWorkPixels): its boxes are in the
	// shrunk picture's pixels, before and after.
	big := &editResult{turns: 2, fromW: 10000, fromH: 6000, toW: 10000, toH: 6000, crop: image.Rect(0, 0, 10000, 6000)}
	k := workScale(10000, 6000)
	if k < 2 {
		t.Fatalf("a 60 MP photo is analysed at %d", k)
	}
	if got, ok := big.moveFace(dao.FaceBox{X: 0, Y: 0, W: 100, H: 100}); !ok || got.X != (10000-100*k)/k || got.Y != (6000-100*k)/k || got.W != 100 {
		t.Errorf("a face of a shrunk photo turned over: %v %v (scale %d)", got, ok, k)
	}
}

func TestEditNames(t *testing.T) {
	if renamedNames("/a/b.jpg", "image/jpeg", ".jpg") != nil || renamedNames("/a/b.PNG", "image/png", ".png") != nil {
		t.Error("a name that fits is renamed")
	}
	n := renamedNames("/iphone/IMG_1.HEIC", "image/jpeg", ".jpg")
	if n(0) != "/iphone/IMG_1.JPG" || n(1) != "/iphone/IMG_1 (2).JPG" {
		t.Errorf("a HEIC become a JPEG: %q, %q", n(0), n(1))
	}
	if n := renamedNames("/x/pic.webp", "image/jpeg", ".jpg"); n(0) != "/x/pic.jpg" {
		t.Errorf("a WebP become a JPEG: %q", n(0))
	}
	c := copyNames("/iphone/IMG_1.HEIC", ".jpg", "es")
	if c(0) != "/iphone/IMG_1 (editada).JPG" || c(1) != "/iphone/IMG_1 (editada) 2.JPG" {
		t.Errorf("the copy's names: %q, %q", c(0), c(1))
	}
	if c := copyNames("/a/b.c.png", ".png", "en"); c(0) != "/a/b.c (edited).png" {
		t.Errorf("a name with dots: %q", c(0))
	}
}

func TestHeifICCIsTheColrBoxs(t *testing.T) {
	icc := testICC(200)
	box := binary.BigEndian.AppendUint32(nil, uint32(12+len(icc)))
	box = append(box, "colrprof"...)
	box = append(box, icc...)
	file := append([]byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00heicmif1....ipco"), box...)
	if got := heifICC(file); !bytes.Equal(got, icc) {
		t.Errorf("found %d bytes, want the profile's %d", len(got), len(icc))
	}
	nclx := append([]byte("\x00\x00\x00\x18ftypheic"), "\x00\x00\x00\x13colrnclx\x00\x01\x00\x0d\x00\x06\x80"...)
	if got := heifICC(nclx); got != nil {
		t.Errorf("an nclx colr gave a profile: %d bytes", len(got))
	}
}

func TestICCSegmentsSplitALargeProfile(t *testing.T) {
	icc := testICC(cICCChunk*2 + 10)
	segs := iccSegments(icc)
	if len(segs) != 3 || segs[0][len(cICCHeader)] != 1 || segs[2][len(cICCHeader)] != 3 || segs[1][len(cICCHeader)+1] != 3 {
		t.Fatalf("%d segments", len(segs))
	}
	var joined []byte
	for _, s := range segs {
		joined = append(joined, s[len(cICCHeader)+2:]...)
	}
	if !bytes.Equal(joined, icc) {
		t.Error("the segments don't make the profile")
	}
	// Carried into a JPEG and read back.
	var enc bytes.Buffer
	jpeg.Encode(&enc, quadrants(8, 8), nil)
	out := withMetadata(enc.Bytes(), nil, segs)
	if _, got := jpegMetadata(out); len(got) != 3 {
		t.Errorf("%d segments read back", len(got))
	}
	if _, err := jpeg.Decode(bytes.NewReader(out)); err != nil {
		t.Errorf("the JPEG with them doesn't decode: %v", err)
	}
	// An EXIF too large for its segment is left out, the rest kept.
	out = withMetadata(enc.Bytes(), make([]byte, cMaxSegment), segs[:1])
	if exif, got := jpegMetadata(out); exif != nil || len(got) != 1 {
		t.Errorf("exif %d bytes, %d profile segments", len(exif), len(got))
	}
}

// mgForEditTest is a Manager with nothing started, for heicToJpeg.
func mgForEditTest() *Manager { return &Manager{} }
