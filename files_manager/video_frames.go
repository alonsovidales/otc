package filesmanager

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/alonsovidales/otc/images_tagger"
	"github.com/alonsovidales/otc/log"
)

// cVideoSampleFrames is how many frames get pulled out of a video for
// tagging (find videos by content, same as photos). Spread across the
// video rather than just grabbing the first frame, since a single frame
// can easily miss most of what the video is actually about; a handful of
// samples costs proportionally more tagging time but catches far more.
const cVideoSampleFrames = 4

// extractVideoFrames decodes videoContent (an in-memory video file, any
// container ffmpeg understands) and returns up to n frames sampled evenly
// across its duration, skipping the very first/last instants where players
// often show a black or title frame. Requires ffmpeg/ffprobe on PATH.
func extractVideoFrames(videoContent []byte, n int) ([]image.Image, error) {
	tmp, err := os.CreateTemp("", "otc-video-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp file for video: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(videoContent); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("writing temp video: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("closing temp video: %w", err)
	}

	duration, err := probeVideoDuration(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("probing video duration: %w", err)
	}
	if duration <= 0 {
		return nil, errors.New("video has no usable duration")
	}

	frames := make([]image.Image, 0, n)
	for i := 1; i <= n; i++ {
		// n+1 slots so the first/last samples land a little inside the
		// video rather than exactly on its edges.
		at := duration * float64(i) / float64(n+1)
		img, err := extractFrameAt(tmpPath, at)
		if err != nil {
			log.Error("error extracting video frame at", at, "s:", err)
			continue
		}
		frames = append(frames, img)
	}
	if len(frames) == 0 {
		return nil, errors.New("could not extract any frames from video")
	}
	return frames, nil
}

func probeVideoDuration(path string) (float64, error) {
	out, err := exec.Command(
		"ffprobe", "-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	).Output()
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
}

func extractFrameAt(path string, seconds float64) (image.Image, error) {
	// -ss before -i seeks first (fast, keyframe-ish) rather than decoding
	// from the start — plenty accurate for "roughly evenly spaced samples".
	cmd := exec.Command(
		"ffmpeg", "-ss", fmt.Sprintf("%.3f", seconds), "-i", path,
		"-frames:v", "1", "-f", "image2pipe", "-vcodec", "mjpeg", "-",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %w: %s", err, stderr.String())
	}
	img, _, err := image.Decode(&stdout)
	return img, err
}

// tagVideoFrames runs the tagger over every frame and merges the results,
// keeping each tag's highest score across frames — a tag that's real in
// even one sampled frame (e.g. a face that's only on screen briefly) should
// still surface, rather than being averaged away by frames that don't show
// it at all.
func tagVideoFrames(ctx context.Context, tagger *imagestagger.RAMTagger, frames []image.Image) []imagestagger.RAMTag {
	best := map[string]float32{}
	for _, frame := range frames {
		tags, err := tagger.Tags(ctx, frame, imagestagger.DefaultRAMOptions())
		if err != nil {
			log.Error("Error processing tags for video frame:", err)
			continue
		}
		for _, t := range tags {
			if cur, ok := best[t.Name]; !ok || t.Score > cur {
				best[t.Name] = t.Score
			}
		}
	}

	out := make([]imagestagger.RAMTag, 0, len(best))
	for name, score := range best {
		out = append(out, imagestagger.RAMTag{Name: name, Score: score})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}
