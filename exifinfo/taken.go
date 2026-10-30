// SPDX-License-Identifier: AGPL-3.0-or-later

package exifinfo

import (
	"bytes"
	"strconv"
	"strings"
	"time"

	"github.com/rwcarlsen/goexif/exif"
	"github.com/rwcarlsen/goexif/tiff"
)

// When a photo was taken. EXIF keeps the camera's clock, a local wall time
// with no zone ("2014:07:19 21:05:33"). goexif's DateTime() reads that
// clock in the process's own zone (time.Local), so the same photo had one
// instant on a device set to Europe/London and another in a container on
// UTC. takenAt reads it the same way everywhere, from the best of:
//
//  1. the offset the file gives with it (OffsetTimeOriginal and its kin,
//     EXIF 2.31, written by most phones since about 2017): exact;
//  2. the GPS clock, which is UTC (GPSDateStamp and GPSTimeStamp): the gap
//     between the two clocks, to the nearest quarter of an hour, is the
//     zone the camera was in - when it is one a zone can be, -12 h to
//     +14 h; a GPS fix much older than the photo gives a gap outside that
//     and is not used;
//  3. otherwise the clock read as UTC: the instant is then off by the zone
//     the photo was taken in (14 h at most), while the clock's day and
//     time are kept as written.

const (
	// exifClockLayout is how EXIF writes a date and time.
	exifClockLayout = "2006:01:02 15:04:05"

	// The EXIF 2.31 offset tags ("+02:00"), in the Exif sub-IFD, which
	// goexif doesn't know.
	tagOffsetTime          = 0x9010
	tagOffsetTimeOriginal  = 0x9011
	tagOffsetTimeDigitized = 0x9012

	// The widest offsets a time zone has.
	minZoneOffset = -12 * time.Hour
	maxZoneOffset = 14 * time.Hour
)

// takenAt is when the photo x describes was taken, as an instant (in UTC),
// and clock, the reading it is from (the camera's wall time, as UTC):
// DateTimeOriginal, else DateTimeDigitized, else DateTime (when the file
// was last written, which a camera writes as the moment it took the
// photo). ok is false when none can be read.
func takenAt(x *exif.Exif) (at, clock time.Time, ok bool) {
	offsets := offsetTags(x)
	for _, src := range []struct {
		name   exif.FieldName
		offset uint16
	}{
		{exif.DateTimeOriginal, tagOffsetTimeOriginal},
		{exif.DateTimeDigitized, tagOffsetTimeDigitized},
		{exif.DateTime, tagOffsetTime},
	} {
		clock, ok := exifClock(x, src.name)
		if !ok {
			continue
		}
		// Its own offset first, then any the camera wrote: they are the
		// same clock's.
		for _, id := range []uint16{src.offset, tagOffsetTimeOriginal, tagOffsetTime, tagOffsetTimeDigitized} {
			if off, ok := offsets[id]; ok {
				return clock.Add(-off), clock, true
			}
		}
		if off, ok := gpsOffset(x, clock); ok {
			return clock.Add(-off), clock, true
		}
		return clock, clock, true
	}
	return time.Time{}, time.Time{}, false
}

// exifClock reads the date and time tag name as written, in UTC. Some
// writers add a fraction or a zone after the seconds, which is ignored; a
// blank or zeroed one ("0000:00:00 00:00:00") is none.
func exifClock(x *exif.Exif, name exif.FieldName) (time.Time, bool) {
	tag, err := x.Get(name)
	if err != nil {
		return time.Time{}, false
	}
	s := tagText(tag)
	if len(s) > len(exifClockLayout) {
		s = s[:len(exifClockLayout)]
	}
	for _, layout := range []string{exifClockLayout, "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// tagText is an ASCII tag's text without its NULs and spaces.
func tagText(tag *tiff.Tag) string {
	s, err := tag.StringVal()
	if err != nil {
		s = string(tag.Val)
	}
	return strings.TrimSpace(strings.TrimRight(s, "\x00"))
}

// offsetTags reads the offset tags of x's Exif sub-IFD, by tag id; the
// ones missing or not an offset a zone can have are left out.
func offsetTags(x *exif.Exif) (offsets map[uint16]time.Duration) {
	offsets = map[uint16]time.Duration{}
	// Malformed metadata never costs more than the offsets.
	defer func() {
		if recover() != nil {
			offsets = map[uint16]time.Duration{}
		}
	}()
	if x == nil || x.Tiff == nil {
		return offsets
	}
	ptr, err := x.Get(exif.ExifIFDPointer)
	if err != nil {
		return offsets
	}
	at, err := ptr.Int64(0)
	if err != nil || at <= 0 || at >= int64(len(x.Raw)) {
		return offsets
	}
	r := bytes.NewReader(x.Raw)
	if _, err := r.Seek(at, 0); err != nil {
		return offsets
	}
	dir, _, err := tiff.DecodeDir(r, x.Tiff.Order)
	if err != nil {
		return offsets
	}
	for _, tag := range dir.Tags {
		switch tag.Id {
		case tagOffsetTime, tagOffsetTimeOriginal, tagOffsetTimeDigitized:
			if off, ok := parseOffset(tagText(tag)); ok {
				offsets[tag.Id] = off
			}
		}
	}
	return offsets
}

// parseOffset reads an EXIF offset, "+02:00" or "-05:30".
func parseOffset(s string) (time.Duration, bool) {
	if len(s) != 6 || (s[0] != '+' && s[0] != '-') || s[3] != ':' {
		return 0, false
	}
	h, errH := strconv.Atoi(s[1:3])
	m, errM := strconv.Atoi(s[4:6])
	if errH != nil || errM != nil || h < 0 || m < 0 || m >= 60 {
		return 0, false
	}
	off := time.Duration(h)*time.Hour + time.Duration(m)*time.Minute
	if s[0] == '-' {
		off = -off
	}
	if off < minZoneOffset || off > maxZoneOffset {
		return 0, false
	}
	return off, true
}

// gpsOffset is the zone clock was in, worked out from x's GPS clock (UTC):
// their gap to the nearest quarter of an hour, when that is an offset a
// zone can have. A GPS time of exactly midnight is what some cameras
// write without a fix, and is not used.
func gpsOffset(x *exif.Exif, clock time.Time) (off time.Duration, ok bool) {
	defer func() {
		if recover() != nil {
			off, ok = 0, false
		}
	}()
	dateTag, err := x.Get(exif.GPSDateStamp)
	if err != nil {
		return 0, false
	}
	date, err := time.ParseInLocation("2006:01:02", tagText(dateTag), time.UTC)
	if err != nil {
		return 0, false
	}
	timeTag, err := x.Get(exif.GPSTimeStamp)
	if err != nil || timeTag.Count < 3 {
		return 0, false
	}
	var parts [3]float64
	for i := range parts {
		num, den, err := timeTag.Rat2(i)
		if err != nil || den == 0 || num < 0 {
			return 0, false
		}
		parts[i] = float64(num) / float64(den)
	}
	if parts[0] >= 24 || parts[1] >= 60 || parts[2] >= 61 || parts[0]+parts[1]+parts[2] == 0 {
		return 0, false
	}
	secs := parts[0]*3600 + parts[1]*60 + parts[2]
	utc := date.Add(time.Duration(secs * float64(time.Second)))
	off = clock.Sub(utc).Round(15 * time.Minute)
	if off < minZoneOffset || off > maxZoneOffset {
		return 0, false
	}
	return off, true
}
