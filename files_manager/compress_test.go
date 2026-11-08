// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/exifinfo"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// noisyPhoto is a w x h photo as a camera's: a gradient with amp of grain
// (deterministic), which a JPEG encoder can't shrink to nothing.
func noisyPhoto(w, h, amp int) *image.YCbCr {
	img := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)
	seed := uint32(1)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			seed = seed*1664525 + 1013904223
			n := int(seed>>24)%(2*amp+1) - amp
			img.Y[y*img.YStride+x] = uint8(min(255, max(0, (x*255/w+y*255/h)/2+n)))
		}
	}
	for i := range img.Cb {
		img.Cb[i], img.Cr[i] = 110, 140
	}
	return img
}

// noisyJPEG is noisyPhoto as a camera's JPEG at quality, with testExif
// (orientation) and an ICC profile.
func noisyJPEG(t testing.TB, w, h, amp, quality, orientation int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, noisyPhoto(w, h, amp), &jpeg.Options{Quality: quality}); err != nil {
		t.Fatal(err)
	}
	return withMetadata(b.Bytes(), testExif(orientation, w, h), iccSegments(testICC(600)))
}

func choiceText(cs []*pb.CompressChoice) string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Preset)
	}
	return strings.Join(out, ",")
}

// nearSize is whether got is want within a byte in a thousand (float rounding).
func nearSize(got, want int64) bool {
	return math.Abs(float64(got-want)) <= math.Max(2, float64(want)/1000)
}

// Every preset's estimate, as the spec's formula has it: the targets, the
// frame rate's scale, the cap at the source's own rate, the sound, the 1%
// - and what isn't offered: no gain of 15%, an efficient codec at its
// size, no HEVC encoder.
func TestCompressEstimates(t *testing.T) {
	aac := func(rate int64) audioStream { return audioStream{index: 1, codec: "aac", bitRate: rate} }
	video := func(secs float64, w, h int, codec string, fps float64, rate int64, audio ...audioStream) *videoProbe {
		return &videoProbe{duration: secs, width: w, height: h, codec: codec, pixFmt: "yuv420p", fps: fps, bitRate: rate, audio: audio}
	}
	sizeOf := func(v *videoProbe) int64 {
		rate := v.bitRate
		for _, a := range v.audio {
			rate += a.bitRate
		}
		return int64(v.duration * float64(rate) / 8)
	}
	type want struct {
		preset string
		size   int64
	}
	for _, c := range []struct {
		name string
		v    *videoProbe
		size int64 // 0: the video's own rates
		x265 bool
		want []want
	}{
		{"4K HEVC at 30 fps", video(60, 3840, 2160, "hevc", 30, 40_000_000, aac(128_000)), 0, true,
			[]want{{cPresetVideo720, 23_694_600}, {cPresetVideo1080, 46_419_600}}},
		{"4K H.264 at 60 fps", video(60, 3840, 2160, "h264", 60, 50_000_000, aac(192_000)), 0, true,
			[]want{{cPresetVideo720, 46_904_400}, {cPresetVideo1080, 92_354_400}, {cPresetVideoHEVC, 209_766_900}}},
		{"1080p H.264", video(30, 1920, 1080, "h264", 30, 17_000_000, aac(128_000)), 0, true,
			[]want{{cPresetVideo720, 11_847_300}, {cPresetVideoHEVC, 35_897_925}}},
		{"a portrait 1080p H.264", video(30, 1080, 1920, "h264", 29.97, 17_000_000, aac(128_000)), 0, true,
			[]want{{cPresetVideo720, 11_847_300}, {cPresetVideoHEVC, 35_897_925}}},
		{"720p HEVC", video(30, 1280, 720, "hevc", 30, 8_000_000, aac(128_000)), 0, true, nil},
		// A 1080p H.264 already light: 720p would be its own rate (the
		// cap), so no gain; HEVC at its size is.
		{"a light 1080p H.264", video(30, 1920, 1080, "h264", 30, 2_000_000, aac(128_000)), 0, true,
			[]want{{cPresetVideoHEVC, 4_651_050}}},
		// Sound that isn't AAC is re-encoded at 160 kbps.
		{"4K H.264 with PCM sound", video(10, 3840, 2160, "h264", 30, 50_000_000, audioStream{index: 1, codec: "pcm_s16le", bitRate: 1_536_000}), 0, true,
			[]want{{cPresetVideo720, 3_989_500}, {cPresetVideo1080, 7_777_000}, {cPresetVideoHEVC, 34_920_750}}},
		{"no HEVC encoder", video(60, 3840, 2160, "h264", 30, 50_000_000, aac(128_000)), 0, false, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			size := c.size
			if size == 0 {
				size = sizeOf(c.v)
			}
			got := videoChoices(c.v, size, c.x265)
			if len(got) != len(c.want) {
				t.Fatalf("got %s, want %v", choiceText(got), c.want)
			}
			for i, w := range c.want {
				if got[i].Preset != w.preset || !nearSize(got[i].EstimatedSize, w.size) {
					t.Errorf("%d: %s %d, want %s %d", i, got[i].Preset, got[i].EstimatedSize, w.preset, w.size)
				}
			}
		})
	}

	photo := func(kind editKind, w, h int, opaque bool) *photoHeader {
		return &photoHeader{kind: kind, w: w, h: h, opaque: opaque}
	}
	for _, c := range []struct {
		name string
		p    *photoHeader
		size int64
		want []want
	}{
		{"a 48 MP HEIC", photo(editPhoto, 8064, 6048, true), 6_000_000, []want{{cPresetPhoto12MP, 3_840_000}}},
		{"a light 48 MP HEIC", photo(editPhoto, 8064, 6048, true), 4_000_000, nil},
		{"a 12 MP JPEG at 0.57 bytes a pixel", photo(editJPEG, 4032, 3024, true), 7_000_000, []want{{cPresetJPEGResave, 2_926_264}}},
		{"a 12 MP JPEG at 0.25 bytes a pixel", photo(editJPEG, 4032, 3024, true), 3_000_000, nil},
		{"a 24 MP JPEG at 0.42 bytes a pixel", photo(editJPEG, 6000, 4000, true), 10_000_000, []want{{cPresetPhoto12MP, 3_840_000}}},
		{"a 48 MP JPEG at 0.6", photo(editJPEG, 8000, 6000, true), 29_000_000, []want{{cPresetPhoto12MP, 3_840_000}, {cPresetJPEGResave, 11_520_000}}},
		{"a PNG with no alpha", photo(editPNG, 2000, 2000, true), 8_000_000, []want{{cPresetPNGToJPEG, 1_200_000}}},
		{"a PNG with alpha", photo(editPNG, 2000, 2000, false), 8_000_000, nil},
		{"a 20 MP PNG", photo(editPNG, 5000, 4000, true), 30_000_000, []want{{cPresetPhoto12MP, 3_840_000}, {cPresetPNGToJPEG, 6_000_000}}},
		{"a GIF", photo(editNone, 4000, 4000, true), 30_000_000, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := photoChoices(c.p, c.size)
			if len(got) != len(c.want) {
				t.Fatalf("got %s, want %v", choiceText(got), c.want)
			}
			for i, w := range c.want {
				if got[i].Preset != w.preset || !nearSize(got[i].EstimatedSize, w.size) {
					t.Errorf("%d: %s %d, want %s %d", i, got[i].Preset, got[i].EstimatedSize, w.preset, w.size)
				}
			}
		})
	}
}

// What counts as a photo or a video to compress, by type or - a type that
// says nothing - by name; which containers a compressed video goes into.
func TestCompressKinds(t *testing.T) {
	for _, c := range []struct {
		path, mime, kind, format string
	}{
		{"/a/IMG_1.HEIC", "image/heic", "photo", ""},
		{"/a/IMG_1.HEIC", "video/mp4", "photo", ""}, // a HEIC its sniffing took for an MP4
		{"/a/b.png", "image/png", "photo", ""},
		{"/a/c.pdf", "application/pdf", "", ""},
		{"/a/notes.txt", "text/plain", "", ""},
		{"/a/old.JPG", "application/octet-stream", "photo", ""},
		{"/a/old.mov", "application/octet-stream", "video", "mov"},
		{"/a/IMG_1.MOV", "video/quicktime", "video", "mov"},
		{"/a/v.mp4", "video/mp4", "video", "mp4"},
		{"/a/v.m4v", "video/x-m4v", "video", "mp4"},
		{"/a/v.3gp", "video/3gpp", "video", ""}, // HEVC doesn't go into a 3GP
		{"/a/v.mkv", "video/x-matroska", "video", ""},
		{"/a/blob", "application/octet-stream", "", ""},
	} {
		f := &pb.File{Path: c.path, Mime: c.mime}
		if got := compressKind(f); got != c.kind {
			t.Errorf("%s (%s): %q, want %q", c.path, c.mime, got, c.kind)
		}
		if got := compressFormatOf(f); c.kind == "video" && got != c.format {
			t.Errorf("%s (%s): format %q, want %q", c.path, c.mime, got, c.format)
		}
	}
}

// Why nothing is offered, in the order the apps are told.
func TestCompressRefusals(t *testing.T) {
	galleryTestEnv(t)
	mg := &Manager{}
	held := sha256Hex([]byte("compress refusals: on the disk"))
	if err := os.WriteFile(blobPath(held), []byte("sealed"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(blobPath(held)) })
	gone := sha256Hex([]byte("compress refusals: missing"))
	photo := &pb.File{Path: "/phone/a.jpg", Mime: "image/jpeg", Hash: held}
	facts := func(change func(*compressFacts)) *compressFacts {
		f := &compressFacts{paths: map[string][]string{held: {"/phone/a.jpg", "/backup/a.jpg"}}, versions: map[string]bool{}, folders: []string{"/locked/"}, supported: true}
		if change != nil {
			change(f)
		}
		return f
	}
	for _, c := range []struct {
		name  string
		file  *pb.File
		shown string
		facts *compressFacts
		code  string
	}{
		{"offered", photo, held, facts(nil), ""},
		{"nothing shown", photo, "", facts(nil), ""},
		{"no row at the path", nil, held, facts(nil), cReasonChanged},
		{"other content", photo, sha256Hex([]byte("other")), facts(nil), cReasonChanged},
		{"a PDF", &pb.File{Path: "/docs/a.pdf", Mime: "application/pdf", Hash: held}, held, facts(nil), cReasonNotMedia},
		{"a copy in an upload-only folder", photo, held, facts(func(f *compressFacts) { f.paths[held] = append(f.paths[held], "/locked/a.jpg") }), cReasonUploadOnly},
		{"an older version", photo, held, facts(func(f *compressFacts) { f.versions[held] = true }), cReasonKeptVersion},
		{"no content on the disk", &pb.File{Path: "/phone/b.jpg", Mime: "image/jpeg", Hash: gone}, gone, facts(nil), cReasonMissing},
		{"a database from before release 177", photo, held, facts(func(f *compressFacts) { f.supported = false }), cReasonNothing},
		// The first reason wins: changed before not a photo.
		{"changed and not media", &pb.File{Path: "/docs/a.pdf", Mime: "application/pdf", Hash: held}, gone, facts(nil), cReasonChanged},
	} {
		r := mg.refusal(c.file, c.shown, c.facts)
		switch {
		case c.code == "" && r != nil:
			t.Errorf("%s: refused %s", c.name, r.code)
		case c.code != "" && (r == nil || r.code != c.code):
			t.Errorf("%s: %v, want %s", c.name, r, c.code)
		case r != nil && r.msg.Render("en") == "":
			t.Errorf("%s: no words", c.name)
		}
	}
	if got := refusedFor(cReasonChanged).msg.Render("en"); got != "this file has changed since it was shown, so nothing was deleted: refresh and try again" {
		t.Errorf("changed reads %q", got)
	}
}

// Largest files' token: "<size>:<hash>"; anything else starts the list
// again rather than failing; the page's size is 1-200, 0 is 50.
func TestLargestToken(t *testing.T) {
	h := sha256Hex([]byte("largest"))
	for _, c := range []struct {
		token string
		want  dao.LargestAfter
	}{
		{"", dao.LargestAfter{First: true}},
		{"123:" + h, dao.LargestAfter{Size: 123, Hash: h}},
		{"0:" + h, dao.LargestAfter{Size: 0, Hash: h}},
		{"9223372036854775807:x", dao.LargestAfter{Size: math.MaxInt64, Hash: "x"}},
		{"12:", dao.LargestAfter{Size: 12, Hash: ""}},
		{"12", dao.LargestAfter{First: true}},
		{"-1:" + h, dao.LargestAfter{First: true}},
		{"abc:" + h, dao.LargestAfter{First: true}},
		{"99999999999999999999:" + h, dao.LargestAfter{First: true}},
		{"1.5:" + h, dao.LargestAfter{First: true}},
	} {
		if got := parseLargestToken(c.token); got != c.want {
			t.Errorf("%q: %+v, want %+v", c.token, got, c.want)
		}
	}
	if got := largestToken(dao.LargestAfter{Size: 4096, Hash: h}); got != "4096:"+h || parseLargestToken(got) != (dao.LargestAfter{Size: 4096, Hash: h}) {
		t.Errorf("a token made: %q", got)
	}
	for limit, want := range map[int32]int{0: 50, -3: 50, 1: 1, 75: 75, 200: 200, 201: 200, 5000: 200} {
		if got := largestLimit(limit); got != want {
			t.Errorf("limit %d: %d, want %d", limit, got, want)
		}
	}
}

// ffprobe's answer for an iPhone's video: the picture (not the cover
// picture an M4V may carry first), its rate, colours and frame rate; the
// sound ffmpeg knows, not the spatial audio it has no codec for. A
// picture with no rate of its own gets the container's less the sound's.
func TestParseVideoProbe(t *testing.T) {
	out := []byte(`{"streams": [
	  {"index": 0, "codec_name": "mjpeg", "codec_type": "video", "width": 320, "height": 240, "disposition": {"attached_pic": 1}},
	  {"index": 1, "codec_name": "hevc", "codec_type": "video", "width": 3840, "height": 2160, "pix_fmt": "yuv420p10le",
	   "color_space": "bt2020nc", "color_transfer": "arib-std-b67", "color_primaries": "bt2020",
	   "r_frame_rate": "30/1", "avg_frame_rate": "30000/1001", "bit_rate": "45000000", "disposition": {"attached_pic": 0}},
	  {"index": 2, "codec_name": "aac", "codec_type": "audio", "bit_rate": "180000", "disposition": {"attached_pic": 0}},
	  {"index": 3, "codec_type": "audio", "disposition": {"attached_pic": 0}},
	  {"index": 4, "codec_name": "bin_data", "codec_type": "data", "disposition": {"attached_pic": 0}}
	], "format": {"duration": "45.123", "bit_rate": "45300000"}}`)
	v, err := parseVideoProbe(out, 0)
	if err != nil {
		t.Fatal(err)
	}
	if v.index != 1 || v.width != 3840 || v.height != 2160 || v.codec != "hevc" || !v.tenBit() || v.bitRate != 45_000_000 || v.duration != 45.123 {
		t.Errorf("picture: %+v", v)
	}
	if math.Abs(v.fps-29.97) > 0.01 {
		t.Errorf("fps %v", v.fps)
	}
	if v.color != (videoColor{Transfer: "arib-std-b67", Primaries: "bt2020", Matrix: "bt2020nc"}) {
		t.Errorf("colours %+v", v.color)
	}
	if len(v.audio) != 1 || v.audio[0].index != 2 || !v.audio[0].copied() || v.audio[0].rate() != 180_000 {
		t.Errorf("sound %+v", v.audio)
	}

	noRate := []byte(`{"streams": [{"index": 0, "codec_name": "h264", "codec_type": "video", "width": 1920, "height": 1080, "pix_fmt": "yuv420p", "avg_frame_rate": "0/0", "r_frame_rate": "25/1"},
	  {"index": 1, "codec_name": "aac", "codec_type": "audio", "bit_rate": "128000"}], "format": {"duration": "10", "bit_rate": "8128000"}}`)
	if v, err := parseVideoProbe(noRate, 0); err != nil || v.bitRate != 8_000_000 || v.fps != 25 || v.tenBit() {
		t.Errorf("no stream rate: %+v %v", v, err)
	}
	noFormatRate := []byte(`{"streams": [{"index": 0, "codec_name": "h264", "codec_type": "video", "width": 1920, "height": 1080}], "format": {"duration": "10"}}`)
	if v, err := parseVideoProbe(noFormatRate, 10_000_000); err != nil || v.bitRate != 8_000_000 {
		t.Errorf("no rate at all: %+v %v", v, err)
	}
	for _, bad := range []string{`{}`, `{"format": {"duration": "N/A"}}`, `{"streams": [{"index": 0, "codec_type": "audio", "codec_name": "aac"}], "format": {"duration": "3"}}`, `not json`} {
		if _, err := parseVideoProbe([]byte(bad), 100); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}

// ffmpeg's command line: the picture's stream and the sound's, sized by
// its shorter side, HEVC at the preset's rate, 10 bits kept with the
// colours, AAC copied and any other sound re-encoded, the tags kept.
func TestCompressVideoArgs(t *testing.T) {
	v := &videoProbe{duration: 45, index: 1, width: 3840, height: 2160, codec: "hevc", pixFmt: "yuv420p10le", fps: 30, bitRate: 45_000_000,
		color: videoColor{Transfer: "arib-std-b67", Primaries: "bt2020", Matrix: "bt2020nc"},
		audio: []audioStream{{index: 2, codec: "aac", bitRate: 180_000}, {index: 4, codec: "alac"}}}
	args := strings.Join(compressVideoArgs("http://127.0.0.1:1/media/t", "/tmp/.compress-1", "mov", v, cPresetVideo1080), " ")
	for _, want := range []string{
		"-progress pipe:1", "-i http://127.0.0.1:1/media/t", "-map 0:1 -map 0:2 -map 0:4",
		"-vf scale=w='if(lte(iw,ih),1080,-2)':h='if(lte(iw,ih),-2,1080)'",
		"-c:v libx265 -preset faster -b:v 6000k -maxrate 9000k -bufsize 12000k -pix_fmt yuv420p10le",
		"-tag:v hvc1", "-color_primaries bt2020 -color_trc arib-std-b67 -colorspace bt2020nc",
		"-c:a:0 copy -c:a:1 aac -b:a:1 160k", "-map_metadata 0 -movflags +faststart+use_metadata_tags -f mov /tmp/.compress-1",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("no %q in\n%s", want, args)
		}
	}
	h264 := &videoProbe{duration: 10, index: 0, width: 1920, height: 1080, codec: "h264", pixFmt: "yuvj420p", fps: 30, bitRate: 16_000_000}
	args = strings.Join(compressVideoArgs("in", "out", "mp4", h264, cPresetVideoHEVC), " ")
	if strings.Contains(args, "-vf") || !strings.Contains(args, "-b:v 8800k") || !strings.Contains(args, "-pix_fmt yuv420p ") || strings.Contains(args, "-color_") || strings.Contains(args, "-c:a") {
		t.Errorf("HEVC at its size: %s", args)
	}
	if got := compressTimeout(45); got != cTranscodeTimeout {
		t.Errorf("45 s: %v", got)
	}
	if got := compressTimeout(3600); got.Hours() != 40 {
		t.Errorf("an hour: %v", got)
	}
}

// pngOf is img as a PNG, with extra chunks before its pixels.
func pngOf(t *testing.T, img image.Image, extra ...[]byte) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	out := b.Bytes()
	if len(extra) == 0 {
		return out
	}
	// After the IHDR (8 + 25 bytes).
	var with []byte
	with = append(with, out[:33]...)
	for _, c := range extra {
		with = append(with, c...)
	}
	return append(with, out[33:]...)
}

// pngChunk is a PNG chunk of kind with data.
func pngChunk(kind string, data []byte) []byte {
	var b []byte
	b = binary.BigEndian.AppendUint32(b, uint32(len(data)))
	b = append(b, kind...)
	b = append(b, data...)
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(append([]byte(kind), data...)))
}

// A photo's header: a JPEG's size, a PNG's and whether it can have a
// transparent pixel, a HEIC's (grid canvas), a WebP's alpha flag; what
// the device doesn't compress.
func TestReadPhotoHeader(t *testing.T) {
	jpg := noisyJPEG(t, 120, 80, 4, 90, 6)
	if p, ok := readPhotoHeader(jpg); !ok || p.kind != editJPEG || p.w != 120 || p.h != 80 || !p.opaque {
		t.Errorf("JPEG: %+v %v", p, ok)
	}
	if _, ok := readPhotoHeader(jpg[:200]); ok {
		t.Error("a JPEG's first bytes read as complete")
	}
	rgb := image.NewRGBA(image.Rect(0, 0, 30, 20))
	for i := range rgb.Pix {
		rgb.Pix[i] = 0xff
	}
	// Go writes an opaque RGBA as RGB, colour type 2.
	if p, ok := readPhotoHeader(pngOf(t, rgb)); !ok || p.kind != editPNG || p.w != 30 || p.h != 20 || !p.opaque {
		t.Errorf("an RGB PNG: %+v %v", p, ok)
	}
	gray := image.NewGray(image.Rect(0, 0, 30, 20))
	if p, ok := readPhotoHeader(pngOf(t, gray)); !ok || p.kind != editPNG || !p.opaque {
		t.Errorf("a grey PNG: %+v %v", p, ok)
	}
	if p, ok := readPhotoHeader(pngOf(t, gray, pngChunk("tRNS", []byte{0, 0}))); !ok || p.opaque {
		t.Errorf("a grey PNG with a colour key: %+v %v", p, ok)
	}
	alpha := image.NewNRGBA(image.Rect(0, 0, 30, 20))
	alpha.SetNRGBA(3, 3, color.NRGBA{1, 2, 3, 100})
	if p, ok := readPhotoHeader(pngOf(t, alpha)); !ok || p.opaque || decodesOpaque(pngOf(t, alpha)) {
		t.Errorf("a PNG with a transparent pixel: %+v %v", p, ok)
	}
	if !decodesOpaque(pngOf(t, rgb)) {
		t.Error("an opaque RGBA PNG doesn't decode opaque")
	}
	heic, err := os.ReadFile("testdata/eightbit-grid.heic")
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(heic))
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := readPhotoHeader(heic); !ok || p.kind != editPhoto || p.w != cfg.Width || p.h != cfg.Height || !p.opaque {
		t.Errorf("HEIC: %+v %v, want %dx%d", p, ok, cfg.Width, cfg.Height)
	}
	webp := func(chunk string, flags byte) []byte {
		b := make([]byte, 30)
		copy(b, "RIFF")
		copy(b[8:], "WEBPVP8X")
		copy(b[12:], chunk)
		b[20], b[24] = flags, flags
		return b
	}
	if !webpAlpha(webp("VP8X", 0x10)) || webpAlpha(webp("VP8X", 0x08)) || !webpAlpha(webp("VP8L", 0x10)) || webpAlpha(webp("VP8 ", 0x10)) {
		t.Error("WebP's alpha flag")
	}
	for name, b := range map[string][]byte{"a GIF": []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;"), "nothing": nil, "a PDF": []byte("%PDF-1.7\n%..........")} {
		if p, _ := readPhotoHeader(b); p.kind != editNone || len(photoChoices(p, 1<<30)) != 0 {
			t.Errorf("%s: %+v", name, p)
		}
	}
}

// A PNG's EXIF and colour profile go into its JPEG.
func TestPNGMetadata(t *testing.T) {
	var z bytes.Buffer
	icc := testICC(700)
	zw := zlib.NewWriter(&z)
	zw.Write(icc)
	zw.Close()
	exif := testExif(1, 30, 20)
	b := pngOf(t, image.NewGray(image.Rect(0, 0, 30, 20)), pngChunk("iCCP", append([]byte("Display P3\x00\x00"), z.Bytes()...)), pngChunk("eXIf", exif))
	gotExif, gotICC := pngMetadata(b)
	if !bytes.Equal(gotExif, exif) {
		t.Error("the EXIF wasn't read")
	}
	if !slices.EqualFunc(gotICC, iccSegments(icc), bytes.Equal) {
		t.Errorf("the profile: %d segments", len(gotICC))
	}
	if e, i := pngMetadata(pngOf(t, image.NewGray(image.Rect(0, 0, 3, 2)))); e != nil || i != nil {
		t.Error("metadata in a PNG with none")
	}
}

// photo_12mp: a 48 MP photo halved by the box (no second pass), a 20 MP
// one by Catmull-Rom, both to about 12 MP; turned upright, the camera's
// details and profile kept with the new size; faces scaled with it.
// jpeg_resave and png_to_jpeg keep the pixels and their orientation.
func TestRenderCompress(t *testing.T) {
	big := noisyJPEG(t, 8000, 6000, 4, 92, 1)
	r, err := renderCompress(big, cPresetPhoto12MP)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(r.content))
	if err != nil || cfg.Width != 4000 || cfg.Height != 3000 {
		t.Fatalf("48 MP: %v %v", cfg, err)
	}
	if box, _ := r.move(dao.FaceBox{X: 1000, Y: 2000, W: 400, H: 600}); box != (dao.FaceBox{X: 500, Y: 1000, W: 200, H: 300}) {
		t.Errorf("a face moved to %+v", box)
	}
	if len(r.content) >= len(big) {
		t.Errorf("%d bytes from %d", len(r.content), len(big))
	}

	turned := noisyJPEG(t, 5000, 4000, 4, 92, 6)
	r, err = renderCompress(turned, cPresetPhoto12MP)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(r.content))
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	if px := b.Dx() * b.Dy(); b.Dx() >= b.Dy() || math.Abs(float64(px)-cCompressPixels) > cCompressPixels*0.02 {
		t.Errorf("20 MP turned: %v (%d px)", b, px)
	}
	info, err := exifinfo.FromJPEG(r.content)
	if err != nil {
		t.Fatal(err)
	}
	if info.Orientation != 1 || int(info.Width) != b.Dx() || int(info.Height) != b.Dy() || info.CameraMake != "Test" {
		t.Errorf("EXIF: orientation %d, %dx%d, make %q", info.Orientation, info.Width, info.Height, info.CameraMake)
	}
	if _, icc := jpegMetadata(r.content); len(icc) != 1 {
		t.Errorf("%d profile segments", len(icc))
	}

	heavy := noisyJPEG(t, 1200, 900, 40, 98, 6)
	r, err = renderCompress(heavy, cPresetJPEGResave)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ = jpeg.DecodeConfig(bytes.NewReader(r.content))
	info, _ = exifinfo.FromJPEG(r.content)
	if cfg.Width != 1200 || cfg.Height != 900 || info.Orientation != 6 || len(r.content) >= len(heavy) {
		t.Errorf("resaved: %v, orientation %d, %d bytes from %d", cfg, info.Orientation, len(r.content), len(heavy))
	}
	if box, _ := r.move(dao.FaceBox{X: 10, Y: 20, W: 30, H: 40}); box != (dao.FaceBox{X: 10, Y: 20, W: 30, H: 40}) {
		t.Errorf("a face moved to %+v", box)
	}

	rgb := image.NewRGBA(image.Rect(0, 0, 640, 480))
	for i := range rgb.Pix {
		rgb.Pix[i] = uint8(i * 7)
		if i%4 == 3 {
			rgb.Pix[i] = 0xff
		}
	}
	r, err = renderCompress(pngOf(t, rgb), cPresetPNGToJPEG)
	if err != nil {
		t.Fatal(err)
	}
	if cfg, err := jpeg.DecodeConfig(bytes.NewReader(r.content)); err != nil || cfg.Width != 640 || cfg.Height != 480 {
		t.Errorf("a PNG as JPEG: %v %v", cfg, err)
	}
	if _, err := renderCompress(pngOf(t, rgb), "photo_8k"); err == nil {
		t.Error("an unknown preset")
	}
}

// The shrink to about 12 MP: by the largest whole factor that keeps it at
// least that large, then by Catmull-Rom only when that isn't close enough
// (here at a hundredth of the pixels, the sides a tenth).
func TestShrinkToAbout(t *testing.T) {
	for _, c := range []struct {
		w, h, wantW, wantH int
	}{
		{800, 600, 400, 300},   // 48 MP: halved, exactly 12 MP
		{400, 300, 400, 300},   // 12 MP: as it is
		{500, 400, 387, 310},   // 20 MP: Catmull-Rom alone
		{1632, 1224, 400, 300}, // 200 MP: by 4, then the rest
		{1200, 900, 400, 300},  // 108 MP: by 3, exactly
	} {
		got := shrinkToAbout(image.NewYCbCr(image.Rect(0, 0, c.w, c.h), image.YCbCrSubsampleRatio420), cCompressPixels/100).Bounds()
		if got.Dx() != c.wantW || got.Dy() != c.wantH {
			t.Errorf("%dx%d: %dx%d, want %dx%d", c.w, c.h, got.Dx(), got.Dy(), c.wantW, c.wantH)
		}
	}
}
