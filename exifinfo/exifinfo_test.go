package exifinfo

import (
	"os"
	"os/exec"
	"strconv"
	"testing"
)

// testdata/gps_test.{jpg,heic} are real-world sample images with GPS EXIF
// data from github.com/ianare/exif-samples (CC BY-SA 4.0), used here to
// validate against actual camera-written EXIF rather than synthetic bytes.

func TestFromJPEGReadsGPSAndCameraInfo(t *testing.T) {
	content, err := os.ReadFile("testdata/gps_test.jpg")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	info, err := FromJPEG(content)
	if err != nil {
		t.Fatalf("FromJPEG: %v", err)
	}
	if !info.HasGPS {
		t.Fatal("expected HasGPS to be true")
	}
	if got := strconv.FormatFloat(info.Latitude, 'f', 2, 64); got != "43.47" {
		t.Errorf("expected latitude ~43.47, got %s", got)
	}
	if info.CameraModel != "COOLPIX P6000" {
		t.Errorf("expected model 'COOLPIX P6000', got %q", info.CameraModel)
	}
	if !info.HasTakenAt {
		t.Error("expected HasTakenAt to be true")
	}
}

func TestFromHEICReadsGPSAndCameraInfo(t *testing.T) {
	content, err := os.ReadFile("testdata/gps_test.heic")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	info, err := FromHEIC(content)
	if err != nil {
		t.Fatalf("FromHEIC: %v", err)
	}
	if !info.HasGPS {
		t.Fatal("expected HasGPS to be true")
	}
	if info.CameraModel == "" {
		t.Error("expected a non-empty camera model")
	}
	if !info.HasTakenAt {
		t.Error("expected HasTakenAt to be true")
	}
}

func requireFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH, skipping")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH, skipping")
	}
}

func TestFromVideoReadsISO6709Location(t *testing.T) {
	requireFFmpeg(t)

	tmp, err := os.CreateTemp("", "otc-exifinfo-video-*.mp4")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	path := tmp.Name()
	tmp.Close()
	defer os.Remove(path)

	cmd := exec.Command(
		"ffmpeg", "-y", "-f", "lavfi", "-i", "testsrc=duration=1:size=64x64:rate=5",
		"-metadata", "location=+40.6892-074.0445/",
		"-pix_fmt", "yuv420p", path,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generating test video: %v\n%s", err, out)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading generated video: %v", err)
	}

	info, err := FromVideo(content)
	if err != nil {
		t.Fatalf("FromVideo: %v", err)
	}
	if !info.HasGPS {
		t.Fatal("expected HasGPS to be true")
	}
	if got := strconv.FormatFloat(info.Latitude, 'f', 4, 64); got != "40.6892" {
		t.Errorf("expected latitude 40.6892, got %s", got)
	}
	if got := strconv.FormatFloat(info.Longitude, 'f', 4, 64); got != "-74.0445" {
		t.Errorf("expected longitude -74.0445, got %s", got)
	}
}

func TestParseISO6709(t *testing.T) {
	cases := []struct {
		in      string
		wantLat float64
		wantLon float64
		wantOK  bool
	}{
		{"+40.6892-074.0445/", 40.6892, -74.0445, true},
		{"+40.6892-074.0445+015.000/", 40.6892, -74.0445, true},
		{"-33.8688+151.2093/", -33.8688, 151.2093, true},
		{"garbage", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, c := range cases {
		lat, lon, ok := parseISO6709(c.in)
		if ok != c.wantOK {
			t.Errorf("parseISO6709(%q) ok = %v, want %v", c.in, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if lat != c.wantLat || lon != c.wantLon {
			t.Errorf("parseISO6709(%q) = (%v, %v), want (%v, %v)", c.in, lat, lon, c.wantLat, c.wantLon)
		}
	}
}

func TestFormatExposure(t *testing.T) {
	cases := []struct {
		num, den int64
		want     string
	}{
		{1, 300, "1/300s"},
		{1, 60, "1/60s"},
		{59, 10, "5.9s"}, // num > den: a multi-second exposure, not a fraction
		{2, 1, "2.0s"},
		{0, 1, ""},
	}
	for _, c := range cases {
		if got := formatExposure(c.num, c.den); got != c.want {
			t.Errorf("formatExposure(%d, %d) = %q, want %q", c.num, c.den, got, c.want)
		}
	}
}
