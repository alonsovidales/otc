// Package exifinfo reads camera/EXIF metadata out of photos and videos for
// the photo gallery's "More info" panel (issue #41) and for location
// tagging (issue #42). It never modifies the original file — everything
// here is read-only extraction of whatever metadata is already embedded.
package exifinfo

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jdeng/goheif"
	"github.com/rwcarlsen/goexif/exif"
)

// Info is the metadata extracted from one photo/video, independent of the
// source format — callers don't need to know whether it came from a JPEG's
// EXIF segment, a HEIC's EXIF box, or a video container's format tags.
type Info struct {
	CameraMake   string
	CameraModel  string
	TakenAt      time.Time
	HasTakenAt   bool
	ExposureTime string // e.g. "1/300s"
	FNumber      string // e.g. "f/5.9"
	ISO          int32
	FocalLength  string // e.g. "24mm"
	Width        int32
	Height       int32
	HasGPS       bool
	Latitude     float64
	Longitude    float64
}

// FromJPEG reads standard EXIF (APP1 segment) out of JPEG bytes.
func FromJPEG(content []byte) (*Info, error) {
	x, err := exif.Decode(bytes.NewReader(content))
	if err != nil {
		return nil, err
	}
	return fromExif(x), nil
}

// FromHEIC reads EXIF out of a HEIC file's dedicated Exif item (a different
// container than JPEG's, but the same TIFF-based EXIF payload once
// extracted — hence reusing the same tag reader below).
func FromHEIC(content []byte) (*Info, error) {
	raw, err := goheif.ExtractExif(bytes.NewReader(content))
	if err != nil {
		return nil, err
	}
	x, err := exif.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	return fromExif(x), nil
}

func fromExif(x *exif.Exif) *Info {
	info := &Info{}

	if v, err := x.Get(exif.Make); err == nil {
		info.CameraMake, _ = v.StringVal()
	}
	if v, err := x.Get(exif.Model); err == nil {
		info.CameraModel, _ = v.StringVal()
	}
	if dt, err := x.DateTime(); err == nil {
		info.TakenAt = dt
		info.HasTakenAt = true
	}
	if v, err := x.Get(exif.ExposureTime); err == nil {
		if num, den, err := v.Rat2(0); err == nil && den != 0 {
			info.ExposureTime = formatExposure(num, den)
		}
	}
	if v, err := x.Get(exif.FNumber); err == nil {
		if num, den, err := v.Rat2(0); err == nil && den != 0 {
			info.FNumber = fmt.Sprintf("f/%.1f", float64(num)/float64(den))
		}
	}
	if v, err := x.Get(exif.ISOSpeedRatings); err == nil {
		if i, err := v.Int(0); err == nil {
			info.ISO = int32(i)
		}
	}
	if v, err := x.Get(exif.FocalLength); err == nil {
		if num, den, err := v.Rat2(0); err == nil && den != 0 {
			info.FocalLength = fmt.Sprintf("%.0fmm", float64(num)/float64(den))
		}
	}
	if v, err := x.Get(exif.PixelXDimension); err == nil {
		if i, err := v.Int(0); err == nil {
			info.Width = int32(i)
		}
	}
	if v, err := x.Get(exif.PixelYDimension); err == nil {
		if i, err := v.Int(0); err == nil {
			info.Height = int32(i)
		}
	}
	if lat, lon, err := x.LatLong(); err == nil {
		info.HasGPS = true
		info.Latitude = lat
		info.Longitude = lon
	}

	return info
}

// formatExposure renders an EXIF exposure-time rational as "1/300s" for
// sub-second exposures (the common case) or "2s" for exposures a full
// second or longer.
func formatExposure(num, den int64) string {
	if num == 0 {
		return ""
	}
	if num < den {
		// Reduce to the familiar "1/N" form photographers expect, not
		// whatever odd fraction the camera happened to encode.
		return fmt.Sprintf("1/%.0fs", float64(den)/float64(num))
	}
	return fmt.Sprintf("%.1fs", float64(num)/float64(den))
}

// iso6709 matches a QuickTime/MP4 "location" format tag, e.g.
// "+40.6892-074.0445/" or "+40.6892-074.0445+015.000/" with altitude.
var iso6709 = regexp.MustCompile(`^([+-][0-9.]+)([+-][0-9.]+)`)

// FromVideo shells out to ffprobe (already a dependency for video frame
// sampling, see files_manager/video_frames.go) to read a video container's
// format-level metadata tags — creation time and, on videos that have it
// (mainly ones recorded on a phone with location services on), GPS as an
// ISO 6709 "location" tag.
func FromVideo(content []byte) (*Info, error) {
	tmp, err := os.CreateTemp("", "otc-exif-video-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp file for video: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("writing temp video: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("closing temp video: %w", err)
	}

	out, err := exec.Command(
		"ffprobe", "-v", "error",
		"-show_entries", "format_tags=location,location-eng,creation_time",
		"-of", "default=noprint_wrappers=1",
		tmpPath,
	).Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe: %w", err)
	}

	info := &Info{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		k, v, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		switch k {
		case "TAG:location", "TAG:location-eng":
			if lat, lon, ok := parseISO6709(v); ok {
				info.HasGPS = true
				info.Latitude = lat
				info.Longitude = lon
			}
		case "TAG:creation_time":
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				info.TakenAt = t
				info.HasTakenAt = true
			}
		}
	}
	return info, nil
}

func parseISO6709(s string) (lat, lon float64, ok bool) {
	m := iso6709.FindStringSubmatch(s)
	if len(m) != 3 {
		return 0, 0, false
	}
	lat, errLat := strconv.ParseFloat(m[1], 64)
	lon, errLon := strconv.ParseFloat(m[2], 64)
	if errLat != nil || errLon != nil {
		return 0, 0, false
	}
	return lat, lon, true
}
