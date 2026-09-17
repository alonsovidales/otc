// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
