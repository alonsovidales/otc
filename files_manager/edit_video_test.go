// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"math"
	"slices"
	"strings"
	"testing"

	pb "github.com/alonsovidales/otc/proto/generated"
)

// The cut's range inside the video: the start at 0 at least, the end the
// video's end when it is 0, past it, or within the slack of it.
func TestCutRange(t *testing.T) {
	for _, c := range []struct {
		start, end, duration float64
		wantStart, wantEnd   float64
	}{
		{1.5, 3.2, 10, 1.5, 3.2},
		{0, 0, 10, 0, 10},
		{-2, 4, 10, 0, 4},
		{2, 12, 10, 2, 10},
		{2, 9.97, 10, 2, 10}, // a handle left at the end, in tenths
		{2, 9.9, 10, 2, 9.9},
		{math.NaN(), math.NaN(), 10, 0, 10},
		{11, 0, 10, 11, 10}, // past the end: too short, the caller says
	} {
		s, e := cutRange(c.start, c.end, c.duration)
		if s != c.wantStart || e != c.wantEnd {
			t.Errorf("cutRange(%v, %v, %v) = %v, %v; want %v, %v", c.start, c.end, c.duration, s, e, c.wantStart, c.wantEnd)
		}
	}
}

// A video is cut into its own container: by its type, else (an unknown or
// generic type) by its extension; anything else can't be cut.
func TestCutFormatOf(t *testing.T) {
	for _, c := range []struct {
		path, mime, want string
	}{
		{"/p/IMG_1.MOV", "video/quicktime", "mov"},
		{"/p/clip.mp4", "video/mp4", "mp4"},
		{"/p/clip.m4v", "video/x-m4v", "mp4"},
		{"/p/clip.3gp", "video/3gpp", "3gp"},
		{"/p/clip.mkv", "video/x-matroska", "matroska"},
		{"/p/clip.webm", "video/webm; codecs=vp9", "webm"},
		{"/p/clip.avi", "video/x-msvideo", "avi"},
		{"/p/clip.MP4", "application/octet-stream", "mp4"},
		{"/p/clip.mkv", "", "matroska"},
		{"/p/clip.ts", "video/mp2t", ""},
		{"/p/clip.wmv", "video/x-ms-wmv", ""},
		{"/p/IMG_1.JPG", "image/jpeg", ""},
		{"/p/photo.mp4", "image/jpeg", ""}, // a photo named as a video
		{"/p/noext", "application/octet-stream", ""},
	} {
		if got := cutFormatOf(&pb.File{Path: c.path, Mime: c.mime}); got != c.want {
			t.Errorf("cutFormatOf(%s, %s) = %q; want %q", c.path, c.mime, got, c.want)
		}
	}
}

// ffmpeg's command line: -ss ahead of the input only for a start, -t only
// for an end before the video's, the streams copied with the video's own
// tags, the MP4 family's index first and its timestamps left for an edit
// list, the others' moved to 0.
func TestCutArgs(t *testing.T) {
	saved := ffmpegThreads
	ffmpegThreads = ""
	defer func() { ffmpegThreads = saved }()

	args := cutArgs("http://127.0.0.1:1/media/x", "/tmp/.cut-1", "mov", 1.5, 3.25, cutInfo{duration: 10, streams: []int{0, 1}})
	line := strings.Join(args, " ")
	for _, want := range []string{
		"-ss 1.500 -i http://127.0.0.1:1/media/x -t 1.750",
		"-map 0:0 -map 0:1 -c copy -map_metadata 0",
		"-movflags +faststart+use_metadata_tags -f mov /tmp/.cut-1",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("args lack %q: %s", want, line)
		}
	}
	// A MOV's frames before the start stay, hidden by an edit list.
	if slices.Contains(args, "-avoid_negative_ts") {
		t.Errorf("a MOV's timestamps are moved: %s", line)
	}
	if args[len(args)-1] != "/tmp/.cut-1" {
		t.Errorf("the output isn't last: %s", line)
	}

	// From the start to the end: neither -ss nor -t. An MKV has no edit
	// lists: it starts at its keyframe, at 0.
	args = cutArgs("in", "out", "matroska", 0, 10, cutInfo{duration: 10})
	if slices.Contains(args, "-ss") || slices.Contains(args, "-t") || slices.Contains(args, "-movflags") {
		t.Errorf("a cut of the whole video seeks, stops or sets MP4 flags: %v", args)
	}
	if !strings.Contains(strings.Join(args, " "), "-avoid_negative_ts make_zero -f matroska out") {
		t.Errorf("an MKV's timestamps: %v", args)
	}
	// No streams known: every picture and sound stream.
	if !strings.Contains(strings.Join(args, " "), "-map 0:v -map 0:a? -c copy") {
		t.Errorf("no streams known: %v", args)
	}
	// To the end only: no -t.
	args = cutArgs("in", "out", "webm", 2, 10, cutInfo{duration: 10})
	if !slices.Contains(args, "-ss") || slices.Contains(args, "-t") {
		t.Errorf("a cut to the end: %v", args)
	}

	// Low-memory mode's threads go before the input, as everywhere.
	ffmpegThreads = "2"
	args = cutArgs("in", "out", "mp4", 1, 2, cutInfo{duration: 10})
	if !strings.Contains(strings.Join(args, " "), "-threads 2 -i in") {
		t.Errorf("no threads before the input: %v", args)
	}
}

// probeCut keeps the picture and the sound ffmpeg has a codec for: an
// iPhone's spatial audio (APAC, no codec in ffmpeg 7) and its timed
// metadata are left out; with no picture known, every stream is kept.
func TestParseCutInfo(t *testing.T) {
	iphone := []byte(`{"streams": [
		{"index": 0, "codec_type": "video", "codec_name": "hevc"},
		{"index": 1, "codec_type": "audio", "codec_name": "aac"},
		{"index": 2, "codec_type": "audio"},
		{"index": 3, "codec_type": "data", "codec_name": "bin_data"},
		{"index": 4, "codec_type": "data"}
	], "format": {"duration": "12.345000"}}`)
	info, err := parseCutInfo(iphone)
	if err != nil || info.duration != 12.345 || !slices.Equal(info.streams, []int{0, 1}) {
		t.Errorf("an iPhone's: %+v %v", info, err)
	}
	info, err = parseCutInfo([]byte(`{"streams": [{"index": 0, "codec_type": "audio", "codec_name": "aac"}], "format": {"duration": "3"}}`))
	if err != nil || info.streams != nil {
		t.Errorf("no picture known: %+v %v", info, err)
	}
	if _, err := parseCutInfo([]byte(`{"streams": [], "format": {}}`)); err == nil {
		t.Error("no length taken")
	}
}
