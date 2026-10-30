// SPDX-License-Identifier: AGPL-3.0-or-later

package exifinfo

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"sort"
	"testing"
	"time"
)

// exifTag is one TIFF directory entry for exifJPEG: its type and count,
// and its value as it is stored.
type exifTag struct {
	id, typ uint16
	count   uint32
	data    []byte
}

func asciiTag(id uint16, s string) exifTag {
	b := append([]byte(s), 0)
	return exifTag{id, 2, uint32(len(b)), b}
}

func rationalTag(id uint16, vals ...[2]uint32) exifTag {
	var b []byte
	for _, v := range vals {
		b = binary.LittleEndian.AppendUint32(b, v[0])
		b = binary.LittleEndian.AppendUint32(b, v[1])
	}
	return exifTag{id, 5, uint32(len(vals)), b}
}

func longTag(id uint16, v uint32) exifTag {
	return exifTag{id, 4, 1, binary.LittleEndian.AppendUint32(nil, v)}
}

// tiffDirSize is the bytes a directory of n entries takes, and dataSize
// what its values over four bytes take after it.
func tiffDirSize(n int) int { return 2 + 12*n + 4 }

func dataSize(tags []exifTag) int {
	n := 0
	for _, t := range tags {
		if len(t.data) > 4 {
			n += len(t.data) + len(t.data)%2
		}
	}
	return n
}

// writeDir writes tags as the directory at offset at, its long values
// right after it.
func writeDir(b *bytes.Buffer, tags []exifTag, at int) {
	sort.Slice(tags, func(i, j int) bool { return tags[i].id < tags[j].id })
	le := binary.LittleEndian
	data := at + tiffDirSize(len(tags))
	b.Write(le.AppendUint16(nil, uint16(len(tags))))
	var blobs []byte
	for _, t := range tags {
		b.Write(le.AppendUint16(nil, t.id))
		b.Write(le.AppendUint16(nil, t.typ))
		b.Write(le.AppendUint32(nil, t.count))
		if len(t.data) <= 4 {
			v := make([]byte, 4)
			copy(v, t.data)
			b.Write(v)
			continue
		}
		b.Write(le.AppendUint32(nil, uint32(data+len(blobs))))
		blobs = append(blobs, t.data...)
		if len(t.data)%2 == 1 {
			blobs = append(blobs, 0)
		}
	}
	b.Write(le.AppendUint32(nil, 0))
	b.Write(blobs)
}

// exifJPEG is a JPEG made of an EXIF segment alone, with these tags in
// IFD0, the Exif sub-IFD and the GPS one: all the readers look at.
func exifJPEG(ifd0, exifIFD, gps []exifTag) []byte {
	n0 := len(ifd0)
	if len(exifIFD) > 0 {
		n0++
	}
	if len(gps) > 0 {
		n0++
	}
	offExif := 8 + tiffDirSize(n0) + dataSize(ifd0)
	offGPS := offExif
	if len(exifIFD) > 0 {
		offGPS += tiffDirSize(len(exifIFD)) + dataSize(exifIFD)
	}
	dir0 := append([]exifTag{}, ifd0...)
	if len(exifIFD) > 0 {
		dir0 = append(dir0, longTag(0x8769, uint32(offExif)))
	}
	if len(gps) > 0 {
		dir0 = append(dir0, longTag(0x8825, uint32(offGPS)))
	}
	var tiff bytes.Buffer
	tiff.WriteString("II*\x00")
	tiff.Write(binary.LittleEndian.AppendUint32(nil, 8))
	writeDir(&tiff, dir0, 8)
	if len(exifIFD) > 0 {
		writeDir(&tiff, exifIFD, offExif)
	}
	if len(gps) > 0 {
		writeDir(&tiff, gps, offGPS)
	}
	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	out := []byte{0xFF, 0xD8, 0xFF, 0xE1}
	out = binary.BigEndian.AppendUint16(out, uint16(len(payload)+2))
	out = append(out, payload...)
	return append(out, 0xFF, 0xD9)
}

func utc(y int, mo time.Month, d, h, mi, s int) time.Time {
	return time.Date(y, mo, d, h, mi, s, 0, time.UTC)
}

// When a photo was taken: the clock as written, made an instant with the
// file's own offset, else with the zone the GPS clock gives, else read as
// UTC - and the same whatever zone the process runs in.
func TestTakenAt(t *testing.T) {
	gpsTime := func(h, m, s uint32) exifTag {
		return rationalTag(0x0007, [2]uint32{h, 1}, [2]uint32{m, 1}, [2]uint32{s, 1})
	}
	gpsDate := func(s string) exifTag { return asciiTag(0x001D, s) }
	original := func(s string) exifTag { return asciiTag(0x9003, s) }
	for _, c := range []struct {
		name          string
		ifd0, ex, gps []exifTag
		ok            bool
		at, clock     time.Time
	}{
		{name: "no date at all", ok: false},
		{name: "the clock alone is read as UTC",
			ex: []exifTag{original("2014:07:19 21:05:33")},
			ok: true, at: utc(2014, 7, 19, 21, 5, 33), clock: utc(2014, 7, 19, 21, 5, 33)},
		{name: "its offset makes it exact",
			ex: []exifTag{original("2014:07:19 21:05:33"), asciiTag(0x9011, "+02:00")},
			ok: true, at: utc(2014, 7, 19, 19, 5, 33), clock: utc(2014, 7, 19, 21, 5, 33)},
		{name: "a negative half-hour offset",
			ex: []exifTag{original("2014:07:19 21:05:33"), asciiTag(0x9011, "-03:30")},
			ok: true, at: utc(2014, 7, 20, 0, 35, 33), clock: utc(2014, 7, 19, 21, 5, 33)},
		{name: "OffsetTime when it is the only one",
			ex: []exifTag{original("2016:01:02 08:00:00"), asciiTag(0x9010, "+09:00")},
			ok: true, at: utc(2016, 1, 1, 23, 0, 0), clock: utc(2016, 1, 2, 8, 0, 0)},
		{name: "a blank offset is none",
			ex: []exifTag{original("2014:07:19 21:05:33"), asciiTag(0x9011, "   :  ")},
			ok: true, at: utc(2014, 7, 19, 21, 5, 33), clock: utc(2014, 7, 19, 21, 5, 33)},
		{name: "an offset no zone has is none",
			ex: []exifTag{original("2014:07:19 21:05:33"), asciiTag(0x9011, "+15:00")},
			ok: true, at: utc(2014, 7, 19, 21, 5, 33), clock: utc(2014, 7, 19, 21, 5, 33)},
		{name: "the GPS clock gives the zone",
			ex:  []exifTag{original("2013:08:10 22:41:07")},
			gps: []exifTag{gpsDate("2013:08:10"), gpsTime(20, 40, 12)},
			ok:  true, at: utc(2013, 8, 10, 20, 41, 7), clock: utc(2013, 8, 10, 22, 41, 7)},
		{name: "the GPS clock on the next UTC day",
			ex:  []exifTag{original("2013:08:10 19:30:00")},
			gps: []exifTag{gpsDate("2013:08:11"), gpsTime(2, 30, 0)},
			ok:  true, at: utc(2013, 8, 11, 2, 30, 0), clock: utc(2013, 8, 10, 19, 30, 0)},
		{name: "a GPS fix nearly a day off is not a zone",
			ex:  []exifTag{original("2008:10:22 16:28:39")},
			gps: []exifTag{gpsDate("2008:10:23"), gpsTime(14, 27, 7)},
			ok:  true, at: utc(2008, 10, 22, 16, 28, 39), clock: utc(2008, 10, 22, 16, 28, 39)},
		{name: "a GPS time of exactly midnight is none",
			ex:  []exifTag{original("2013:08:10 22:41:07")},
			gps: []exifTag{gpsDate("2013:08:10"), gpsTime(0, 0, 0)},
			ok:  true, at: utc(2013, 8, 10, 22, 41, 7), clock: utc(2013, 8, 10, 22, 41, 7)},
		{name: "the file's offset wins over the GPS clock",
			ex:  []exifTag{original("2019:05:01 12:00:00"), asciiTag(0x9011, "+01:00")},
			gps: []exifTag{gpsDate("2019:05:01"), gpsTime(9, 0, 0)},
			ok:  true, at: utc(2019, 5, 1, 11, 0, 0), clock: utc(2019, 5, 1, 12, 0, 0)},
		{name: "DateTimeDigitized when there is no original",
			ex: []exifTag{asciiTag(0x9004, "2012:03:04 05:06:07"), asciiTag(0x9012, "-05:00")},
			ok: true, at: utc(2012, 3, 4, 10, 6, 7), clock: utc(2012, 3, 4, 5, 6, 7)},
		{name: "IFD0's DateTime last",
			ifd0: []exifTag{asciiTag(0x0132, "2011:11:11 11:11:11")},
			ok:   true, at: utc(2011, 11, 11, 11, 11, 11), clock: utc(2011, 11, 11, 11, 11, 11)},
		{name: "a zeroed original falls back to DateTime",
			ifd0: []exifTag{asciiTag(0x0132, "2011:11:11 11:11:11")},
			ex:   []exifTag{original("0000:00:00 00:00:00")},
			ok:   true, at: utc(2011, 11, 11, 11, 11, 11), clock: utc(2011, 11, 11, 11, 11, 11)},
		{name: "a fraction after the seconds is ignored",
			ex: []exifTag{original("2014:07:19 21:05:33.250")},
			ok: true, at: utc(2014, 7, 19, 21, 5, 33), clock: utc(2014, 7, 19, 21, 5, 33)},
	} {
		info, err := FromJPEG(exifJPEG(c.ifd0, c.ex, c.gps))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if info.HasTakenAt != c.ok {
			t.Errorf("%s: HasTakenAt %v, want %v", c.name, info.HasTakenAt, c.ok)
			continue
		}
		if !c.ok {
			continue
		}
		if !info.TakenAt.Equal(c.at) || info.TakenAt.Location() != time.UTC {
			t.Errorf("%s: TakenAt %v, want %v", c.name, info.TakenAt, c.at)
		}
		if !info.TakenClock.Equal(c.clock) {
			t.Errorf("%s: TakenClock %v, want %v", c.name, info.TakenClock, c.clock)
		}
	}
}

// The zone the process runs in changes nothing (goexif's DateTime() read
// the clock in time.Local).
func TestTakenAtIgnoresTheLocalZone(t *testing.T) {
	jpg := exifJPEG(nil, []exifTag{asciiTag(0x9003, "2014:07:19 21:05:33")}, nil)
	prev := time.Local
	defer func() { time.Local = prev }()
	for _, zone := range []string{"UTC", "Europe/London", "America/Los_Angeles", "Asia/Tokyo"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Skip(err)
		}
		time.Local = loc
		info, err := FromJPEG(jpg)
		if err != nil || !info.TakenAt.Equal(utc(2014, 7, 19, 21, 5, 33)) {
			t.Errorf("%s: %v, %v", zone, info, err)
		}
	}
}

// The real camera fixtures: the Nikon's GPS fix is 22 hours off its clock
// (not a zone), so its clock is read as UTC; the iPhone wrote its offset
// (-05:00, Missouri), which makes its instant exact.
func TestTakenAtFixtures(t *testing.T) {
	jpg, err := os.ReadFile("testdata/gps_test.jpg")
	if err != nil {
		t.Fatal(err)
	}
	info, err := FromJPEG(jpg)
	if err != nil || !info.TakenAt.Equal(utc(2008, 10, 22, 16, 28, 39)) {
		t.Errorf("JPEG: %v, %v", info, err)
	}
	heic, err := os.ReadFile("testdata/gps_test.heic")
	if err != nil {
		t.Fatal(err)
	}
	info, err = FromHEIC(heic)
	if err != nil || !info.TakenAt.Equal(utc(2021, 4, 11, 20, 47, 53)) || !info.TakenClock.Equal(utc(2021, 4, 11, 15, 47, 53)) {
		t.Errorf("HEIC: %v, %v", info, err)
	}
}

func TestParseOffset(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"+00:00": 0, "+02:00": 2 * time.Hour, "-05:30": -5*time.Hour - 30*time.Minute,
		"+14:00": 14 * time.Hour, "-12:00": -12 * time.Hour, "+05:45": 5*time.Hour + 45*time.Minute,
	} {
		if got, ok := parseOffset(in); !ok || got != want {
			t.Errorf("parseOffset(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "   :  ", "+2:00", "02:00", "+02:60", "-13:00", "+14:30", "+0a:00", "+02:00Z"} {
		if got, ok := parseOffset(in); ok {
			t.Errorf("parseOffset(%q) = %v, want none", in, got)
		}
	}
}

// A video's creation_time is UTC, taken as it is.
func TestFromVideoReadsCreationTime(t *testing.T) {
	requireFFmpeg(t)
	path := t.TempDir() + "/clip.mp4"
	out, err := runFFmpeg("-y", "-f", "lavfi", "-i", "testsrc=duration=1:size=64x64:rate=5",
		"-metadata", "creation_time=2014-07-19T19:05:33.000000Z", "-pix_fmt", "yuv420p", path)
	if err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	info, err := FromVideoSource(path)
	if err != nil {
		t.Fatal(err)
	}
	want := utc(2014, 7, 19, 19, 5, 33)
	if !info.HasTakenAt || !info.TakenAt.Equal(want) || !info.TakenClock.Equal(want) || info.TakenAt.Location() != time.UTC {
		t.Errorf("got %v / %v, want %v", info.TakenAt, info.TakenClock, want)
	}
}

func runFFmpeg(args ...string) ([]byte, error) {
	return exec.Command("ffmpeg", args...).CombinedOutput()
}
