// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/jpeg"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// makeTestVideoSized is makeTestVideo (video_frames_test.go) with an
// explicit resolution, needed here to exercise the downscale-only behavior
// of CompressVideoForSocial's scale filter.
func makeTestVideoSized(t *testing.T, seconds int, width, height int) []byte {
	t.Helper()
	tmp, err := os.CreateTemp("", "otc-video-fixture-*.mp4")
	if err != nil {
		t.Fatalf("creating temp fixture path: %v", err)
	}
	path := tmp.Name()
	tmp.Close()
	defer os.Remove(path)

	size := strconv.Itoa(width) + "x" + strconv.Itoa(height)
	cmd := exec.Command(
		"ffmpeg", "-y", "-f", "lavfi",
		"-i", "testsrc=duration="+strconv.Itoa(seconds)+":size="+size+":rate=10",
		"-f", "lavfi", "-i", "sine=frequency=440:duration="+strconv.Itoa(seconds),
		"-pix_fmt", "yuv420p", "-shortest", path,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generating test video: %v\n%s", err, out)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading generated test video: %v", err)
	}
	return content
}

// probeVideoDimensions shells out to ffprobe directly (rather than reusing
// extractFrameAt/decoding a frame) so this test verifies the *container's*
// actual encoded resolution, not just whatever a JPEG re-encode of one
// frame happens to report.
func probeVideoDimensions(t *testing.T, content []byte) (width, height int) {
	t.Helper()
	tmp, err := os.CreateTemp("", "otc-video-probe-*.mp4")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	path := tmp.Name()
	defer os.Remove(path)
	if _, err := tmp.Write(content); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}
	tmp.Close()

	out, err := exec.Command(
		"ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height",
		"-of", "csv=s=x:p=0",
		path,
	).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	parts := strings.Split(strings.TrimSpace(string(out)), "x")
	if len(parts) != 2 {
		t.Fatalf("unexpected ffprobe output: %q", out)
	}
	w, err := strconv.Atoi(parts[0])
	if err != nil {
		t.Fatalf("parsing width: %v", err)
	}
	h, err := strconv.Atoi(parts[1])
	if err != nil {
		t.Fatalf("parsing height: %v", err)
	}
	return w, h
}

func TestCompressVideoForSocialDownscalesWideVideo(t *testing.T) {
	requireFFmpeg(t)
	content := makeTestVideoSized(t, 2, 1280, 720)

	mg := &Manager{}
	out, err := mg.CompressVideoForSocial(content)
	if err != nil {
		t.Fatalf("CompressVideoForSocial: %v", err)
	}

	w, _ := probeVideoDimensions(t, out)
	if w != cSocialVideoMaxWidth {
		t.Errorf("expected output width %d, got %d", cSocialVideoMaxWidth, w)
	}
}

// A video already narrower than the cap must never be upscaled - the
// filter is "shrink if wider than the cap", not "resize to exactly the
// cap" (issue #60 only asks to shrink oversized videos, never to enlarge a
// smaller one).
func TestCompressVideoForSocialNeverUpscalesNarrowVideo(t *testing.T) {
	requireFFmpeg(t)
	content := makeTestVideoSized(t, 2, 320, 240)

	mg := &Manager{}
	out, err := mg.CompressVideoForSocial(content)
	if err != nil {
		t.Fatalf("CompressVideoForSocial: %v", err)
	}

	w, h := probeVideoDimensions(t, out)
	if w != 320 || h != 240 {
		t.Errorf("expected the original 320x240 to be kept as-is, got %dx%d", w, h)
	}
}

func TestCompressVideoForSocialRejectsGarbageInput(t *testing.T) {
	requireFFmpeg(t)
	mg := &Manager{}
	if _, err := mg.CompressVideoForSocial([]byte("not a real video")); err == nil {
		t.Error("expected an error for non-video input, got nil")
	}
}

// BuildTransientFile must never insert anything into the `files` table (no
// DB dependency in its signature at all) while still computing the exact
// same Hash/Mime/Size metadata UploadFile would - this is the whole point
// of the issue this covers: NewPublication's compressed video-for-social
// copy stopped going through UploadFile so it wouldn't show up in the
// Files section, but it still needs correct file metadata for
// social_publications_files.
func TestBuildTransientFile(t *testing.T) {
	content := []byte("fake mp4 bytes for hashing purposes")
	f := BuildTransientFile(content)

	sum := sha256.Sum256(content)
	wantHash := hex.EncodeToString(sum[:])
	if f.Hash != wantHash {
		t.Errorf("Hash = %q, want %q", f.Hash, wantHash)
	}
	if f.Size != int32(len(content)) {
		t.Errorf("Size = %d, want %d", f.Size, len(content))
	}
	if !bytes.Equal(f.Content, content) {
		t.Error("Content does not match the input bytes")
	}
	if f.Created == nil || f.Modified == nil {
		t.Error("Created/Modified should both be set")
	}
	// Deliberately not asserting Path here — it's left unset, since
	// social_publications_files never stores or reads one (see
	// dao.NewSocialPublication/GetSocialPublicationFiles).
}

func TestGenerateVideoThumbnail(t *testing.T) {
	requireFFmpeg(t)
	content := makeTestVideoSized(t, 2, 1280, 720)

	mg := &Manager{}
	thumb, err := mg.GenerateVideoThumbnail(content, 1000)
	if err != nil {
		t.Fatalf("GenerateVideoThumbnail: %v", err)
	}
	img, err := jpeg.Decode(bytes.NewReader(thumb))
	if err != nil {
		t.Fatalf("decoding thumbnail as JPEG: %v", err)
	}
	if img.Bounds().Dx() <= 0 || img.Bounds().Dy() <= 0 {
		t.Error("expected a non-empty thumbnail image")
	}
}

// A narrow video must still get a thumbnail (unconditionally, not just
// when downscaling was needed) - processMediaContent's own inline version
// of this had exactly this gap once for images (see its own comment on
// that fix), and would have reproduced it for video here too if this
// hadn't encoded unconditionally from the start.
func TestGenerateVideoThumbnailNeverSkipsNarrowVideo(t *testing.T) {
	requireFFmpeg(t)
	content := makeTestVideoSized(t, 2, 320, 240)

	mg := &Manager{}
	thumb, err := mg.GenerateVideoThumbnail(content, 1000)
	if err != nil {
		t.Fatalf("GenerateVideoThumbnail: %v", err)
	}
	if len(thumb) == 0 {
		t.Fatal("expected a thumbnail to be produced for a narrow video too")
	}
	if _, err := jpeg.Decode(bytes.NewReader(thumb)); err != nil {
		t.Fatalf("decoding thumbnail as JPEG: %v", err)
	}
}

// probeEncodedDuration reports the length of an in-memory clip, reusing
// the package's own ffprobe wrapper rather than a second copy of it.
func probeEncodedDuration(t *testing.T, content []byte) float64 {
	t.Helper()
	tmp, err := os.CreateTemp("", "otc-probe-dur-*.mp4")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	path := tmp.Name()
	defer os.Remove(path)
	if _, err := tmp.Write(content); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}
	tmp.Close()

	d, err := probeVideoDuration(path)
	if err != nil {
		t.Fatalf("probeVideoDuration: %v", err)
	}
	return d
}

// Issue #108: the cut has to actually land where it was asked to - a
// keyframe-aligned stream copy would silently return whole seconds more
// than requested, which is exactly what someone trimming to a moment would
// see as the feature not working.
func TestTrimVideoForSocialCutsToTheRequestedRange(t *testing.T) {
	requireFFmpeg(t)
	content := makeTestVideoSized(t, 6, 640, 480)

	mg := &Manager{}
	out, err := mg.TrimVideoForSocial(content, TrimRange{Start: 1.5, End: 3.5}, false)
	if err != nil {
		t.Fatalf("TrimVideoForSocial: %v", err)
	}

	if d := probeEncodedDuration(t, out); d < 1.8 || d > 2.2 {
		t.Errorf("expected a ~2s cut, got %.2fs", d)
	}
	// Trimming alone must not touch the picture - that's what separates it
	// from issue #60's compression.
	if w, h := probeVideoDimensions(t, out); w != 640 || h != 480 {
		t.Errorf("expected the original 640x480 to be kept, got %dx%d", w, h)
	}
}

// An open-ended trim keeps everything after the start, rather than
// producing an empty clip from a zero duration.
func TestTrimVideoForSocialWithNoEndRunsToTheEnd(t *testing.T) {
	requireFFmpeg(t)
	content := makeTestVideoSized(t, 6, 640, 480)

	mg := &Manager{}
	out, err := mg.TrimVideoForSocial(content, TrimRange{Start: 4}, false)
	if err != nil {
		t.Fatalf("TrimVideoForSocial: %v", err)
	}

	if d := probeEncodedDuration(t, out); d < 1.7 || d > 2.3 {
		t.Errorf("expected the remaining ~2s, got %.2fs", d)
	}
}

// Trim and issue #60's downscale in one pass, for an oversized source.
func TestTrimVideoForSocialDownscalesWhenAsked(t *testing.T) {
	requireFFmpeg(t)
	content := makeTestVideoSized(t, 6, 1280, 720)

	mg := &Manager{}
	out, err := mg.TrimVideoForSocial(content, TrimRange{Start: 1, End: 3}, true)
	if err != nil {
		t.Fatalf("TrimVideoForSocial: %v", err)
	}

	if w, _ := probeVideoDimensions(t, out); w != cSocialVideoMaxWidth {
		t.Errorf("expected output width %d, got %d", cSocialVideoMaxWidth, w)
	}
	if d := probeEncodedDuration(t, out); d < 1.8 || d > 2.2 {
		t.Errorf("expected a ~2s cut, got %.2fs", d)
	}
}

// Issue #108: an open-ended trim has to reach ffmpeg as "no -t at all"
// rather than a zero or negative duration, which would produce an empty
// clip instead of one that runs to the end.
func TestTrimRangeDuration(t *testing.T) {
	for _, tc := range []struct {
		name string
		trim TrimRange
		want float64
	}{
		{"a closed range is its own length", TrimRange{Start: 2, End: 5}, 3},
		{"no end means run to the end of the clip", TrimRange{Start: 2}, 0},
		{"an end before the start is not a duration", TrimRange{Start: 5, End: 2}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.trim.Duration(); got != tc.want {
				t.Errorf("TrimRange%+v.Duration() = %v, want %v", tc.trim, got, tc.want)
			}
		})
	}
}

// A video narrower than the thumbnail cap still needs a thumbnail: a
// publication reads one back unconditionally, so "no resize needed" used
// to mean "no thumbnail at all", and posting such a video failed outright
// (see the video branch of UploadFile's background processing).
func TestThumbnailSourceKeepsANarrowFrameInsteadOfSkippingIt(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 480, 270))
	got := thumbnailSource(src, 1000)
	if got == nil {
		t.Fatal("thumbnailSource returned nothing for a frame narrower than the cap")
	}
	if b := got.Bounds(); b.Dx() != 480 || b.Dy() != 270 {
		t.Errorf("got %dx%d, want the original 480x270 kept as-is", b.Dx(), b.Dy())
	}
}

// An iPhone recording is 10-bit HDR (HLG). Re-encoded as it was, it came
// out as H.264 "High 10", which Android phones' hardware decoders refuse -
// so posted videos didn't play there. Whatever the source, a post's video
// must be 8-bit 4:2:0 High profile in standard colour.
func TestCompressVideoForSocialOutputsPlayable8BitFromHDR(t *testing.T) {
	requireFFmpeg(t)
	tmp, err := os.CreateTemp("", "otc-video-hlg-*.mp4")
	if err != nil {
		t.Fatal(err)
	}
	path := tmp.Name()
	tmp.Close()
	defer os.Remove(path)
	gen := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "testsrc=duration=2:size=640x360:rate=10",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-c:v", "libx264", "-pix_fmt", "yuv420p10le",
		"-color_primaries", "bt2020", "-color_trc", "arib-std-b67", "-colorspace", "bt2020nc",
		"-shortest", path)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generating an HLG test video: %v\n%s", err, out)
	}
	content, _ := os.ReadFile(path)

	mg := &Manager{}
	out, err := mg.CompressVideoForSocial(content)
	if err != nil {
		t.Fatalf("CompressVideoForSocial: %v", err)
	}
	outPath := path + "-out.mp4"
	defer os.Remove(outPath)
	_ = os.WriteFile(outPath, out, 0o600)
	probe, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=profile,pix_fmt,color_transfer", "-of", "default=noprint_wrappers=1", outPath).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	got := string(probe)
	for _, want := range []string{"profile=High\n", "pix_fmt=yuv420p\n", "color_transfer=bt709"} {
		if !strings.Contains(got, want) {
			t.Errorf("output stream is not plain 8-bit High/BT.709 (missing %q):\n%s", strings.TrimSpace(want), got)
		}
	}
}

func TestHdrToSDROnlyForHDR(t *testing.T) {
	if hdrToSDR(parseProbeColor("color_transfer=bt709\ncolor_primaries=bt709\ncolor_space=bt709\n")) != nil {
		t.Error("an SDR video must not be tone-mapped")
	}
	if hdrToSDR(parseProbeColor("color_transfer=unknown\n")) != nil {
		t.Error("an untagged video must not be tone-mapped")
	}
	chain := hdrToSDR(parseProbeColor("color_transfer=arib-std-b67\ncolor_primaries=bt2020\ncolor_space=bt2020nc\n"))
	if len(chain) == 0 || !strings.Contains(chain[0], "tin=arib-std-b67") || chain[len(chain)-1] != "format=yuv420p" {
		t.Errorf("HLG chain = %v", chain)
	}
}
