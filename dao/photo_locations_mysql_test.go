// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
)

// Places, against a real MySQL/MariaDB (OTC_TEST_MYSQL_DSN, as
// search_files_mysql_test.go): temporary tables shaped as db.sql's.

const photoLocationsTable = "create temporary table `photo_locations` (`hash` varchar(64) not null, `lat` double null, `lon` double null, primary key (`hash`)) engine=InnoDB"

func placesTestDB(tb testing.TB) *Dao {
	tb.Helper()
	db, d := outOfImagesTestDB(tb)
	if _, err := db.Exec(photoLocationsTable); err != nil {
		tb.Fatal(err)
	}
	return d
}

func ph(i int) string { return fmt.Sprintf("%064x", 0x51ace+i) }

// located is the copies EachLocatedCopy reads with /Private/ kept out,
// grouped by content as the map's index groups them: the oldest copy's
// date, a video when every copy is one, and how many copies came.
func located(tb testing.TB, d *Dao) map[string]LocatedCopy {
	tb.Helper()
	out := map[string]LocatedCopy{}
	if err := d.EachLocatedCopy([]string{"/Private/"}, func(m LocatedCopy) error {
		if old, ok := out[m.Hash]; ok {
			if old.Lat != m.Lat || old.Lon != m.Lon {
				tb.Errorf("%s at two positions", m.Hash)
			}
			if old.Created.Before(m.Created) {
				m.Created = old.Created
			}
			m.Video = m.Video && old.Video
		}
		out[m.Hash] = m
		return nil
	}); err != nil {
		tb.Fatal(err)
	}
	return out
}

// Positions by content: written and overwritten, a NULL row for content
// read without one; the index reads the copies Images may show of each
// photo or video with a position, none in the folders kept out;
// the backfill lists only media without a row, so a NULL one is never
// read again; and a deleted content's row goes.
func TestPhotoLocationsMySQL(t *testing.T) {
	d := placesTestDB(t)
	db := d.db
	day := func(n int) time.Time { return time.Date(2024, 3, n, 9, 0, 0, 0, time.UTC) }
	insertMediaRows(t, db, "files",
		mediaRow{ph(1), "image/jpeg", "/Private/a-old.jpg", day(1)},
		mediaRow{ph(1), "image/jpeg", "/Photos/a.jpg", day(5)},
		mediaRow{ph(1), "image/jpeg", "/Photos/a-copy.jpg", day(7)},
		mediaRow{ph(2), "video/mp4", "/Photos/b.mp4", day(2)},
		mediaRow{ph(3), "image/jpeg", "/Photos/c.jpg", day(3)},
		mediaRow{ph(4), "text/plain", "/Docs/d.txt", day(4)},
		mediaRow{ph(5), "image/heic", "/Photos/e.heic", day(5)},
		mediaRow{ph(6), "image/jpeg", "/Private/f.jpg", day(6)},
	)
	for _, w := range []struct {
		hash     string
		lat, lon float64
		ok       bool
	}{
		{ph(1), 1, 1, true}, {ph(1), 43.77, 11.25, true}, // overwritten
		{ph(2), 40.4, -3.7, true}, {ph(3), 0, 0, false}, {ph(4), 1, 1, true}, {ph(6), 2, 2, true},
	} {
		if err := d.SetPhotoLocation(w.hash, w.lat, w.lon, w.ok); err != nil {
			t.Fatal(err)
		}
	}

	got := located(t, d)
	if len(got) != 2 {
		t.Fatalf("located %v, want the photo and the video", got)
	}
	if a := got[ph(1)]; a.Lat != 43.77 || a.Lon != 11.25 || !a.Created.Equal(day(5)) || a.Video {
		t.Errorf("photo %+v, want 43.77, 11.25 at its oldest copy outside /Private/", a)
	}
	if b := got[ph(2)]; !b.Video {
		t.Errorf("video %+v", b)
	}

	files, err := d.MediaWithoutLocation("", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Hash != ph(5) {
		t.Errorf("without a row %v, want only the HEIC never read", files)
	}
	if files, _ := d.MediaWithoutLocation(ph(5), 10); len(files) != 0 {
		t.Errorf("past the last %v", files)
	}

	shown, err := d.ShownCopies([]string{ph(1), ph(6), ph(9)}, []string{"/Private/"})
	if err != nil {
		t.Fatal(err)
	}
	if len(shown) != 1 || shown[ph(1)].GetPath() != "/Photos/a.jpg" {
		t.Errorf("shown copies %v, want only a.jpg", shown)
	}
	if shown, _ := d.ShownCopies([]string{ph(1)}, nil); shown[ph(1)].GetPath() != "/Private/a-old.jpg" {
		t.Errorf("with nothing kept out the shown copy is %v", shown[ph(1)])
	}

	if err := d.DelPhotoLocation(ph(1)); err != nil {
		t.Fatal(err)
	}
	if got := located(t, d); len(got) != 1 {
		t.Errorf("after the delete %v", got)
	}
}

// SearchPhotos.area: the photos whose position is inside it, across the
// antimeridian too, combined with the other filters, the images-only
// browsing rule kept; the date buckets and the upward half the same; and
// the hashes the map keeps for a filtered search are the search's.
func TestSearchMediaWithinAreaMySQL(t *testing.T) {
	d := placesTestDB(t)
	db := d.db
	day := func(n int) time.Time { return time.Date(2023, time.Month(n), 1, 12, 0, 0, 0, time.UTC) }
	insertMediaRows(t, db, "files",
		mediaRow{ph(1), "image/jpeg", "/P/florence.jpg", day(1)},
		mediaRow{ph(2), "image/jpeg", "/P/rome.jpg", day(2)},
		mediaRow{ph(3), "image/jpeg", "/P/fiji-west.jpg", day(3)},
		mediaRow{ph(4), "image/jpeg", "/P/fiji-east.jpg", day(4)},
		mediaRow{ph(5), "image/jpeg", "/P/nowhere.jpg", day(5)},
		mediaRow{ph(6), "video/mp4", "/P/florence.mp4", day(6)},
		mediaRow{ph(7), "image/jpeg", "/P/unread.jpg", day(7)},
	)
	for h, p := range map[string][2]float64{
		ph(1): {43.7731, 11.2560}, ph(2): {41.9028, 12.4964}, ph(3): {-17.7, 179.9}, ph(4): {-17.8, -179.9}, ph(6): {43.77, 11.25},
	} {
		if err := d.SetPhotoLocation(h, p[0], p[1], true); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.SetPhotoLocation(ph(5), 0, 0, false); err != nil {
		t.Fatal(err)
	}
	for _, tag := range [][2]string{{ph(2), "beach"}, {ph(4), "beach"}, {ph(1), "bridge"}} {
		if _, err := db.Exec("insert into `file_tags` (`hash`, `tag`, `score`) values (?, ?, 1)", tag[0], tag[1]); err != nil {
			t.Fatal(err)
		}
	}
	paths := func(files []*pb.File) []string {
		var out []string
		for _, f := range files {
			out = append(out, f.Path)
		}
		slices.Sort(out)
		return out
	}
	italy := &MediaArea{South: 36, West: 6, North: 47.1, East: 18.6}
	fiji := &MediaArea{South: -21, West: 177, North: -12, East: -178}
	everywhere := &MediaArea{South: -90, West: -180, North: 90, East: 180}
	for _, c := range []struct {
		name       string
		area       *MediaArea
		tags       []string
		imagesOnly bool
		want       []string
	}{
		{"italy", italy, nil, true, []string{"/P/florence.jpg", "/P/rome.jpg"}},
		{"italy with videos", italy, nil, false, []string{"/P/florence.jpg", "/P/florence.mp4", "/P/rome.jpg"}},
		{"across the antimeridian", fiji, nil, true, []string{"/P/fiji-east.jpg", "/P/fiji-west.jpg"}},
		{"italy, beach", italy, []string{"beach"}, true, []string{"/P/rome.jpg"}},
		{"everywhere: only what has a position", everywhere, nil, true, []string{"/P/fiji-east.jpg", "/P/fiji-west.jpg", "/P/florence.jpg", "/P/rome.jpg"}},
		{"no area", nil, nil, true, []string{"/P/fiji-east.jpg", "/P/fiji-west.jpg", "/P/florence.jpg", "/P/nowhere.jpg", "/P/rome.jpg", "/P/unread.jpg"}},
		{"a single point", &MediaArea{South: 41.9028, West: 12.4964, North: 41.9028, East: 12.4964}, nil, true, []string{"/P/rome.jpg"}},
	} {
		files, err := d.SearchMediaWithin(c.area, "", c.tags, nil, "", c.imagesOnly, nil, nil)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := paths(files); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}

	buckets, err := d.SearchMediaDateBucketsWithin(italy, nil, nil, "", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(buckets) != "[{2023-02 1} {2023-01 1}]" {
		t.Errorf("Italy's buckets %v", buckets)
	}
	newer, err := d.SearchMediaNewerThanWithin(fiji, "", nil, nil, "", true, day(3), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(newer); !slices.Equal(got, []string{"/P/fiji-east.jpg"}) {
		t.Errorf("Fiji newer than March %v", got)
	}

	for _, tags := range [][]string{{"beach"}, {"bridge", "beach"}} {
		var hashes []string
		if err := d.EachMatchingMediaHash(tags, nil, "", true, nil, func(h string) { hashes = append(hashes, h) }); err != nil {
			t.Fatal(err)
		}
		files, _ := d.SearchMedia("", tags, nil, "", true, nil, nil)
		var want []string
		for _, f := range files {
			want = append(want, f.Hash)
		}
		slices.Sort(hashes)
		slices.Sort(want)
		if !slices.Equal(hashes, want) {
			t.Errorf("%v: map's hashes %v, the search's %v", tags, hashes, want)
		}
	}
}

// A database the release's script hasn't reached: no position anywhere,
// writes say ErrSchemaBehind, and a delete has nothing to do.
func TestPhotoLocationsWithoutTableMySQL(t *testing.T) {
	db := searchTestDB(t)
	d := NewWithDB(db)
	insertMediaRows(t, db, "files", mediaRow{ph(1), "image/jpeg", "/P/a.jpg", time.Now()})
	calls := 0
	if err := d.EachLocatedCopy(nil, func(LocatedCopy) error { calls++; return nil }); err != nil || calls != 0 {
		t.Errorf("without the table: %d, %v", calls, err)
	}
	if err := d.SetPhotoLocation(ph(1), 1, 1, true); !errors.Is(err, ErrSchemaBehind) {
		t.Errorf("write: %v", err)
	}
	if err := d.DelPhotoLocation(ph(1)); err != nil {
		t.Errorf("delete: %v", err)
	}
	if _, err := d.MediaWithoutLocation("", 10); !errors.Is(err, ErrSchemaBehind) {
		t.Errorf("listing: %v", err)
	}
	if _, err := db.Exec("create temporary table `settings` (`sizes_backfilled` tinyint(1) not null default 0) engine=InnoDB"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("insert into `settings` () values ()"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.LocationsBackfilled(); !errors.Is(err, ErrSchemaBehind) {
		t.Errorf("flag: %v", err)
	}
	if _, err := db.Exec("alter table `settings` add column `locations_backfilled` tinyint(1) not null default 0"); err != nil {
		t.Fatal(err)
	}
	if done, err := d.LocationsBackfilled(); done || err != nil {
		t.Errorf("flag before: %v, %v", done, err)
	}
	if err := d.SetLocationsBackfilled(); err != nil {
		t.Fatal(err)
	}
	if done, err := d.LocationsBackfilled(); !done || err != nil {
		t.Errorf("flag after: %v, %v", done, err)
	}
}

// What the map's index costs to read from 100,000 located photos
// (OTC_TEST_PLACES_LARGE=1, logged; it inserts them first).
func TestEachLocatedCopyLargeMySQL(t *testing.T) {
	if os.Getenv("OTC_TEST_PLACES_LARGE") == "" {
		t.Skip("OTC_TEST_PLACES_LARGE not set")
	}
	d := placesTestDB(t)
	db := d.db
	const n = 100_000
	at := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
	for start := 0; start < n; start += 1000 {
		var fargs, largs []any
		q := "insert into `files` (`hash`, `mime`, `created`, `modified`, `path`, `size`) values "
		lq := "insert into `photo_locations` (`hash`, `lat`, `lon`) values "
		for i := start; i < start+1000; i++ {
			if i > start {
				q += ","
				lq += ","
			}
			q += "(?, 'image/jpeg', ?, ?, ?, 1)"
			lq += "(?, ?, ?)"
			fargs = append(fargs, ph(i), at.Add(time.Duration(i)*time.Minute), at, fmt.Sprintf("/Photos/%06d.jpg", i))
			largs = append(largs, ph(i), 40+float64(i%1000)/100, -3+float64(i/1000)/100)
		}
		if _, err := db.Exec(q, fargs...); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(lq, largs...); err != nil {
			t.Fatal(err)
		}
	}
	for run := range 3 {
		start := time.Now()
		count := 0
		if err := d.EachLocatedCopy(nil, func(LocatedCopy) error { count++; return nil }); err != nil {
			t.Fatal(err)
		}
		t.Logf("run %d: read %d positions in %v", run, count, time.Since(start))
	}
}

// Places' switch: off until turned on; turning it off empties
// photo_locations and clears the backfill's record together; before the
// release it is ErrSchemaBehind (read as off).
func TestPlacesSwitchMySQL(t *testing.T) {
	d := placesTestDB(t)
	db := d.db
	if _, err := db.Exec("create temporary table `settings` (`locations_backfilled` tinyint(1) not null default 0) engine=InnoDB"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("insert into `settings` () values ()"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.PlacesEnabled(); !errors.Is(err, ErrSchemaBehind) {
		t.Errorf("before the release: %v", err)
	}
	if _, err := db.Exec("alter table `settings` add column `places_enabled` tinyint(1) not null default 0"); err != nil {
		t.Fatal(err)
	}
	if on, err := d.PlacesEnabled(); on || err != nil {
		t.Errorf("by default: %v, %v", on, err)
	}
	if err := d.SetPlacesEnabled(true); err != nil {
		t.Fatal(err)
	}
	if on, err := d.PlacesEnabled(); !on || err != nil {
		t.Errorf("turned on: %v, %v", on, err)
	}
	for i := 1; i <= 3; i++ {
		if err := d.SetPhotoLocation(ph(i), 43, 11, i != 3); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.SetLocationsBackfilled(); err != nil {
		t.Fatal(err)
	}

	if err := d.SetPlacesEnabled(false); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := db.QueryRow("select count(*) from `photo_locations`").Scan(&rows); err != nil || rows != 0 {
		t.Errorf("%d positions left after turning it off (%v)", rows, err)
	}
	if on, err := d.PlacesEnabled(); on || err != nil {
		t.Errorf("turned off: %v, %v", on, err)
	}
	if done, err := d.LocationsBackfilled(); done || err != nil {
		t.Errorf("the backfill's record after turning it off: %v, %v", done, err)
	}
}
