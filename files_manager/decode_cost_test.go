// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The tests decode as a device whose memory isn't known does (1 GB), so
// what they let through doesn't depend on the machine running them.
func init() {
	decodeBudgetBytes = func() int64 { return decodeBudgetFor(0) }
}

// jpegHeader is a JPEG whose frame header claims w x h with components
// sampled as hv (0x22 is 2x2) - enough for DecodeConfig, never decoded.
func jpegHeader(w, h int, progressive bool, hv ...byte) []byte {
	sof := byte(0xc0)
	if progressive {
		sof = 0xc2
	}
	seg := []byte{8, byte(h >> 8), byte(h), byte(w >> 8), byte(w), byte(len(hv))}
	for i, s := range hv {
		seg = append(seg, byte(i+1), s, 0)
	}
	n := len(seg) + 2
	// JFIF's APP0 first: without it DecodeConfig reads on past the frame.
	jfif := []byte{0xff, 0xe0, 0, 16, 'J', 'F', 'I', 'F', 0, 1, 1, 0, 0, 1, 0, 1, 0, 0}
	b := append([]byte{0xff, 0xd8}, jfif...)
	return append(append(b, 0xff, sof, byte(n>>8), byte(n)), seg...)
}

// A JPEG's frame says what its decode takes: a byte a sample (4:2:0 is
// 1.5 a pixel), and a progressive one 4 more a sample for its
// coefficients.
func TestJpegFrame(t *testing.T) {
	for _, c := range []struct {
		name        string
		b           []byte
		samples     float64
		progressive bool
	}{
		{"4:2:0", jpegHeader(64, 48, false, 0x22, 0x11, 0x11), 1.5, false},
		{"4:2:2", jpegHeader(64, 48, false, 0x21, 0x11, 0x11), 2, false},
		{"4:4:4 progressive", jpegHeader(64, 48, true, 0x11, 0x11, 0x11), 3, true},
		{"gray", jpegHeader(64, 48, false, 0x11), 1, false},
	} {
		s, p, ok := jpegFrame(c.b)
		if !ok || s != c.samples || p != c.progressive {
			t.Errorf("%s: got %v samples, progressive %v, ok %v", c.name, s, p, ok)
		}
	}
	// One Go's encoder wrote, with its tables before the frame.
	var b bytes.Buffer
	jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 64, 48)), nil)
	if s, p, ok := jpegFrame(b.Bytes()); !ok || s != 1.5 || p {
		t.Errorf("Go's JPEG: got %v samples, progressive %v, ok %v", s, p, ok)
	}
	if _, _, ok := jpegFrame([]byte("not a jpeg")); ok {
		t.Error("read a frame out of nothing")
	}
	// A segment length under its own two bytes: refused, not a panic.
	for _, n := range []byte{0, 1} {
		if _, _, ok := jpegFrame([]byte{0xff, 0xd8, 0xff, 0xc0, 0, n, 8, 0, 48, 0, 64, 3}); ok {
			t.Errorf("read a frame past a segment of length %d", n)
		}
	}
}

// What a photo may be goes by the device's memory: a 200 MP phone photo
// is processed on a 4 GB Raspberry Pi, a 240 MP pixel-shift camera JPEG
// on an 8 GB one, and the numbers in decodeBudgetBytes' table hold.
func TestDecodeLimitGoesByMemory(t *testing.T) {
	const gib = 1 << 30
	pi4, pi8 := decodeBudgetFor(37*gib/10), decodeBudgetFor(79*gib/10) // MemTotal on a 4 and an 8 GB board
	kinds := []struct {
		name     string
		b        []byte
		pi4, pi8 int64 // megapixels
	}{
		{"JPEG 4:2:0", jpegHeader(64, 48, false, 0x22, 0x11, 0x11), 311, 500},
		{"JPEG 4:2:2", jpegHeader(64, 48, false, 0x21, 0x11, 0x11), 259, 500},
		{"JPEG 4:4:4", jpegHeader(64, 48, false, 0x11, 0x11, 0x11), 194, 419},
		{"progressive JPEG 4:2:0", jpegHeader(64, 48, true, 0x22, 0x11, 0x11), 91, 197},
		{"PNG, 8-bit RGBA", pngHeader(64, 48), 155, 335},
		{"HEIC", nil, 86, 186},
	}
	for _, k := range kinds {
		cost := stillCost{perPixel: cHeicBytesPerPixel, shrunk: true}
		if k.b != nil {
			c, format, err := image.DecodeConfig(bytes.NewReader(k.b))
			if err != nil {
				t.Fatalf("%s: %v", k.name, err)
			}
			cost = stillCost{perPixel: decodeBytesPerPixel(k.b, format, c.ColorModel), shrunk: true}
		}
		if got := cost.maxPixels(pi4) / 1_000_000; got != k.pi4 {
			t.Errorf("%s on 4 GB: %d MP, want %d", k.name, got, k.pi4)
		}
		if got := cost.maxPixels(pi8) / 1_000_000; got != k.pi8 {
			t.Errorf("%s on 8 GB: %d MP, want %d", k.name, got, k.pi8)
		}
	}
	if b := decodeBudgetFor(0); b != 1<<30 {
		t.Errorf("unknown memory: %d, want 1 GB", b)
	}
	if b := decodeBudgetFor(gib); b != 256<<20 {
		t.Errorf("1 GB: %d, want the 256 MB floor", b)
	}
}

// The photos the owner couldn't process: a phone's 200 MP JPEG and a 150
// MP camera's are let through, and refused only above what the device
// can hold - with that limit in the message, not a fixed 120.
func TestLargePhotosAreLetThrough(t *testing.T) {
	for name, b := range map[string][]byte{
		"a 200 MP phone photo (16320x12240, 4:2:0)":    jpegHeader(16320, 12240, false, 0x22, 0x11, 0x11),
		"a 150 MP camera photo (14204x10652, 4:2:2)":   jpegHeader(14204, 10652, false, 0x21, 0x11, 0x11),
		"a 121 MP photo, refused before (12000x10100)": jpegHeader(12000, 10100, false, 0x22, 0x11, 0x11),
	} {
		if err := checkImageSize(b); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// 240 MP of progressive 4:2:0 takes 8.5 bytes a pixel: over the
	// test's 1 GB, which holds 124 MP of it.
	err := checkImageSize(jpegHeader(19008, 12672, true, 0x22, 0x11, 0x11))
	if !errors.Is(err, errImageTooLarge) {
		t.Fatalf("a 240 MP progressive JPEG on 1 GB: %v", err)
	}
	if want := "(over 124 megapixels)"; !strings.Contains(err.Error(), want) {
		t.Errorf("%q doesn't say %s", err, want)
	}
	// Over cMaxImagePixels on any device.
	if err := checkImageSize(jpegHeader(30000, 20000, false, 0x22, 0x11, 0x11)); !errors.Is(err, errImageTooLarge) {
		t.Errorf("a 600 MP JPEG: %v", err)
	}
}

// What decodeImage holds from the budget covers what it allocates -
// measured, the least of three runs (a stray allocation elsewhere in the
// test binary only adds), at sizes where the bytes a pixel, not the
// overhead, are most of it.
func TestDecodeReservationCoversWhatItAllocates(t *testing.T) {
	const w, h = 4000, 3000
	opaque := image.NewRGBA(image.Rect(0, 0, w, h))
	translucent := image.NewNRGBA(image.Rect(0, 0, w, h))
	deep := image.NewNRGBA64(image.Rect(0, 0, w, h))
	gray := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBA{uint8(x), uint8(y), uint8(x ^ y), uint8(x + y)}
			opaque.Set(x, y, color.RGBA{c.R, c.G, c.B, 0xff})
			translucent.SetNRGBA(x, y, c)
			deep.Set(x, y, c)
			gray.SetGray(x, y, color.Gray{uint8(x * y)})
		}
	}
	encoded := map[string][]byte{}
	var b bytes.Buffer
	jpeg.Encode(&b, opaque, &jpeg.Options{Quality: 90})
	encoded["JPEG 4:2:0"] = bytes.Clone(b.Bytes())
	b.Reset()
	jpeg.Encode(&b, gray, nil)
	encoded["gray JPEG"] = bytes.Clone(b.Bytes())
	for name, img := range map[string]image.Image{"PNG RGB": opaque, "PNG RGBA": translucent, "PNG 16-bit": deep} {
		b.Reset()
		png.Encode(&b, img)
		encoded[name] = bytes.Clone(b.Bytes())
	}
	if p := progressiveJPEG(t, opaque); p != nil {
		encoded["progressive JPEG 4:2:0"] = p
	}
	for name, content := range encoded {
		t.Run(name, func(t *testing.T) {
			var alloc uint64
			for run := 0; run < 3; run++ {
				var before, after runtime.MemStats
				runtime.GC()
				runtime.ReadMemStats(&before)
				img, err := decodeImage(content)
				if err != nil {
					t.Fatal(err)
				}
				runtime.ReadMemStats(&after)
				if img.Bounds().Dx() != w {
					t.Fatalf("decoded to %v", img.Bounds())
				}
				if d := after.TotalAlloc - before.TotalAlloc; run == 0 || d < alloc {
					alloc = d
				}
			}
			rw, rh, cost, ok, err := stillSize(content)
			if !ok || err != nil {
				t.Fatalf("stillSize: %v %v", ok, err)
			}
			// The bytes a pixel alone, with a little room: cDecodeOverhead
			// is a margin on top, not what makes this pass.
			perPixel := int64(float64(rw*rh) * cost.perPixelAt(rw, rh))
			t.Logf("allocated %.1f MB, %.1f MB at %.1f bytes a pixel", float64(alloc)/1e6, float64(perPixel)/1e6, cost.perPixelAt(rw, rh))
			if int64(alloc) > perPixel+2<<20 {
				t.Errorf("allocated %d bytes, %d at the bytes a pixel reserved", alloc, perPixel)
			}
		})
	}
}

// progressiveJPEG is img as a progressive 4:2:0 JPEG, made by cjpeg (Go
// only writes baseline ones); nil, the case skipped, without it.
func progressiveJPEG(t *testing.T, img *image.RGBA) []byte {
	t.Helper()
	cjpeg, err := exec.LookPath("cjpeg")
	if err != nil {
		t.Log("cjpeg not on PATH: no progressive JPEG")
		return nil
	}
	b := img.Bounds()
	var ppm bytes.Buffer
	fmt.Fprintf(&ppm, "P6\n%d %d\n255\n", b.Dx(), b.Dy())
	for i := 0; i < len(img.Pix); i += 4 {
		ppm.Write(img.Pix[i : i+3])
	}
	in := filepath.Join(t.TempDir(), "in.ppm")
	if err := os.WriteFile(in, ppm.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(cjpeg, "-progressive", "-sample", "2x2", "-quality", "90", in).Output()
	if err != nil {
		t.Fatalf("cjpeg: %v", err)
	}
	if _, p, ok := jpegFrame(out); !ok || !p {
		t.Fatal("cjpeg didn't write a progressive JPEG")
	}
	return out
}

// A photo over the work size comes back shrunk by a whole factor, and
// the copy takes at most a byte for every pixel of the original - the
// one stillCost adds.
func TestShrinkForWork(t *testing.T) {
	for _, c := range []struct{ w, h, k int }{
		{16320, 12240, 2}, // a 200 MP phone photo
		{19008, 12672, 3}, // a 240 MP pixel shift
		{100000, 600, 2},  // a panorama
		{10000, 5000, 1},  // exactly 50 MP: as it is
	} {
		k := 1
		if int64(c.w)*int64(c.h) > cWorkPixels {
			k = shrinkFactor(c.w, c.h, cWorkPixels)
		}
		if k != c.k {
			t.Errorf("%dx%d: shrunk by %d, want %d", c.w, c.h, k, c.k)
		}
		if dw, dh := (c.w+k-1)/k, (c.h+k-1)/k; int64(dw)*int64(dh) > cWorkPixels {
			t.Errorf("%dx%d: shrunk to %dx%d, over the work size", c.w, c.h, dw, dh)
		}
	}

	const w, h = 3000, 2000
	ycc := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)
	for i := range ycc.Y {
		ycc.Y[i] = uint8(i)
	}
	for _, img := range []image.Image{ycc, image.NewNRGBA(image.Rect(0, 0, w, h))} {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		out := shrinkTo(img, w*h/4)
		runtime.ReadMemStats(&after)
		if b := out.Bounds(); b.Dx() != w/2 || b.Dy() != h/2 {
			t.Fatalf("%T: shrunk to %v", img, b)
		}
		// And a row of sums, in cDecodeOverhead.
		if d := after.TotalAlloc - before.TotalAlloc; d > w*h+1<<20 {
			t.Errorf("%T: shrinking allocated %d bytes, more than one a pixel (%d)", img, d, w*h)
		}
	}
}

// Each pixel of the shrunk copy is the average of the ones it stands for:
// by their alpha, and the same from a YCbCr's samples as from its colors.
func TestShrinkAverages(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 4, 2))
	// Left block: white and black, opaque - grey. Right block: red and a
	// transparent green - red, half as opaque.
	for y := 0; y < 2; y++ {
		src.SetNRGBA(0, y, color.NRGBA{255, 255, 255, 255})
		src.SetNRGBA(1, y, color.NRGBA{0, 0, 0, 255})
		src.SetNRGBA(2, y, color.NRGBA{255, 0, 0, 255})
		src.SetNRGBA(3, y, color.NRGBA{0, 255, 0, 0})
	}
	out := shrinkTo(src, 2).(*image.NRGBA)
	if got, want := out.NRGBAAt(0, 0), (color.NRGBA{128, 128, 128, 255}); got != want {
		t.Errorf("white and black: %v, want %v", got, want)
	}
	if got, want := out.NRGBAAt(1, 0), (color.NRGBA{255, 0, 0, 127}); got != want {
		t.Errorf("red and transparent green: %v, want %v", got, want)
	}

	// Colors well inside RGB: where a conversion clips, averaging the
	// samples and averaging the colors part ways.
	ycc := image.NewYCbCr(image.Rect(0, 0, 64, 48), image.YCbCrSubsampleRatio420)
	for i := range ycc.Y {
		ycc.Y[i] = uint8(60 + i%120)
	}
	for i := range ycc.Cb {
		ycc.Cb[i], ycc.Cr[i] = uint8(110+i%30), uint8(140-i%25)
	}
	fast := shrinkTo(ycc, 64*48/9).(*image.NRGBA)
	slow := shrinkTo(&notYCbCr{ycc}, 64*48/9).(*image.NRGBA)
	if fast.Bounds() != slow.Bounds() {
		t.Fatalf("%v and %v", fast.Bounds(), slow.Bounds())
	}
	for i := range fast.Pix {
		if d := int(fast.Pix[i]) - int(slow.Pix[i]); d < -3 || d > 3 {
			t.Fatalf("byte %d: %d from the samples, %d from the colors", i, fast.Pix[i], slow.Pix[i])
		}
	}
}

// notYCbCr hides a YCbCr's type, for the general path.
type notYCbCr struct{ image.Image }
