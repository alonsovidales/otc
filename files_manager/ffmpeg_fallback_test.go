// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func ffmpegFixture(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("ffmpeg", append(append([]string{"-v", "error", "-y"}, args...), out)...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("this ffmpeg can't make %s: %v %s", name, err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// JPEG 2000 has no Go decoder; ffmpeg reads it.
func TestDecodeWithFFmpegReadsJPEG2000(t *testing.T) {
	requireFFmpeg(t)
	jp2 := ffmpegFixture(t, "photo.jp2", "-f", "lavfi", "-i", "testsrc=size=64x48", "-frames:v", "1")
	if _, _, err := image.Decode(bytes.NewReader(jp2)); err == nil {
		t.Skip("Go decodes JPEG 2000 here; nothing to fall back from")
	}
	img, err := decodeWithFFmpeg(jp2)
	if err != nil {
		t.Fatalf("decodeWithFFmpeg: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 64 || b.Dy() != 48 {
		t.Errorf("got %v, want 64x48", b)
	}
}

// A JPEG cut short ("unexpected EOF" in Go) still gives a picture.
func TestDecodeWithFFmpegReadsATruncatedJPEG(t *testing.T) {
	requireFFmpeg(t)
	var buf bytes.Buffer
	src := image.NewRGBA(image.Rect(0, 0, 256, 256))
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	cut := buf.Bytes()[:buf.Len()*2/3]
	if _, _, err := image.Decode(bytes.NewReader(cut)); err == nil {
		t.Skip("Go decoded the truncated JPEG")
	}
	if _, err := decodeWithFFmpeg(cut); err != nil {
		t.Errorf("a truncated JPEG should still decode through ffmpeg: %v", err)
	}
}

// A clip of a few hundredths of a second (a Live Photo's video) has no
// frame at the sampled seek points; its first frame is used.
func TestExtractVideoFramesFromAVeryShortClip(t *testing.T) {
	requireFFmpeg(t)
	clip := ffmpegFixture(t, "live.mov", "-f", "lavfi", "-i", "testsrc=size=64x48:rate=30", "-t", "0.05", "-c:v", "libx264", "-pix_fmt", "yuv420p")
	frames, err := extractVideoFrames(clip, 4)
	if err != nil || len(frames) == 0 {
		t.Fatalf("extractVideoFrames on a 0.05 s clip: %d frames, %v", len(frames), err)
	}
}

// What checkImageSize refuses never reaches ffmpeg (issue #188), which
// would decode it anyway, unbounded; what Go can't read still does.
func TestDecodeStillKeepsTooLargeImagesFromFFmpeg(t *testing.T) {
	calls := 0
	orig := stillFFmpeg
	stillFFmpeg = func([]byte) (image.Image, error) {
		calls++
		return image.NewRGBA(image.Rect(0, 0, 1, 1)), nil
	}
	t.Cleanup(func() { stillFFmpeg = orig })

	for name, content := range map[string][]byte{
		"a 400 MP PNG header": pngHeader(20000, 20000),
		"a HEIC grid bomb":    heicGrid(t, 200, 200, 100, 100),
	} {
		if _, err := decodeStill(content, name); !errors.Is(err, errImageTooLarge) {
			t.Errorf("%s: got %v, want errImageTooLarge", name, err)
		}
	}
	if calls != 0 {
		t.Fatalf("a too-large image was handed to ffmpeg %d time(s)", calls)
	}

	if img, err := decodeStill([]byte("a format Go has no decoder for"), "odd.bin"); err != nil || img == nil {
		t.Fatalf("an unreadable image should fall back to ffmpeg: %v", err)
	}
	if calls != 1 {
		t.Errorf("ffmpeg was called %d time(s) for an unreadable image, want 1", calls)
	}
}
