package filesmanager

import (
	"os"
	"os/exec"
	"strconv"
	"testing"
)

// requireFFmpeg skips the test when ffmpeg/ffprobe aren't on PATH, rather
// than failing — they're present on the actual devices (see Makefile.pi)
// but not guaranteed on every dev/CI machine.
func requireFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH, skipping")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH, skipping")
	}
}

// makeTestVideo generates a short synthetic test-pattern video with
// ffmpeg's built-in test source — real enough to exercise probing/seeking/
// decoding without needing a checked-in binary fixture.
func makeTestVideo(t *testing.T, seconds int) []byte {
	t.Helper()
	tmp, err := os.CreateTemp("", "otc-video-fixture-*.mp4")
	if err != nil {
		t.Fatalf("creating temp fixture path: %v", err)
	}
	path := tmp.Name()
	tmp.Close()
	defer os.Remove(path)

	cmd := exec.Command(
		"ffmpeg", "-y", "-f", "lavfi",
		"-i", "testsrc=duration="+strconv.Itoa(seconds)+":size=320x240:rate=10",
		"-pix_fmt", "yuv420p", path,
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

func TestExtractVideoFramesSamplesAcrossDuration(t *testing.T) {
	requireFFmpeg(t)
	content := makeTestVideo(t, 6)

	frames, err := extractVideoFrames(content, cVideoSampleFrames)
	if err != nil {
		t.Fatalf("extractVideoFrames: %v", err)
	}
	if len(frames) != cVideoSampleFrames {
		t.Errorf("expected %d frames, got %d", cVideoSampleFrames, len(frames))
	}
	for i, f := range frames {
		b := f.Bounds()
		if b.Dx() != 320 || b.Dy() != 240 {
			t.Errorf("frame %d has unexpected size %dx%d, want 320x240", i, b.Dx(), b.Dy())
		}
	}
}

func TestExtractVideoFramesRejectsGarbageInput(t *testing.T) {
	requireFFmpeg(t)
	if _, err := extractVideoFrames([]byte("not a real video"), cVideoSampleFrames); err == nil {
		t.Error("expected an error for non-video input, got nil")
	}
}

func TestProbeVideoDurationMatchesGeneratedLength(t *testing.T) {
	requireFFmpeg(t)
	content := makeTestVideo(t, 3)

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

	got, err := probeVideoDuration(path)
	if err != nil {
		t.Fatalf("probeVideoDuration: %v", err)
	}
	if got < 2.5 || got > 3.5 {
		t.Errorf("expected duration close to 3s, got %.2fs", got)
	}
}
