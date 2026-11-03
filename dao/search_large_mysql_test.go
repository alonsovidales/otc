// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
)

// Issue #206's searches on a large library (OTC_TEST_BROWSE_LARGE=1, as
// TestBrowseMediaLargeMySQL): the 200k photos of fillLargeLibrary with
// tags, faces, collections, positions and ratings as a real library has
// them - a few tags on most photos and many on few, a person in a tenth of
// them and others in fewer, a big imported collection and small ones, a
// home where most photos were taken and trips.

// searchTablesDB is searchTestDB with every table a search reads.
func searchTablesDB(tb testing.TB) (*sql.DB, *Dao) {
	tb.Helper()
	db, d := outOfImagesTestDB(tb)
	for _, q := range []string{
		"alter table `image_groups` add column `rating` tinyint(1) not null default 0",
		photoLocationsTable,
		"create temporary table `photo_ratings` (`group_id` varchar(36) not null, `hash` varchar(64) not null, `stars` tinyint not null default 0, `comment` blob null, `shared_comment` blob null, `link` varchar(64) null, `updated` datetime not null, primary key (`group_id`, `hash`), key (`hash`)) engine=InnoDB",
	} {
		if _, err := db.Exec(q); err != nil {
			tb.Fatal(err)
		}
	}
	return db, d
}

// largeHash is fillLargeLibrary's i-th photo.
func largeHash(i int) string { return fmt.Sprintf("%064x", 0x2060000000+i) }

// fillLargeSearchTables gives fillLargeLibrary's n photos what a search
// filters by. Deterministic; scores come in steps of 1/256, so photos tie.
func fillLargeSearchTables(tb testing.TB, db *sql.DB, n int) {
	tb.Helper()
	insert := func(table, cols string, flushAt int) func(vals ...any) {
		var rows [][]any
		return func(vals ...any) {
			if vals != nil {
				rows = append(rows, vals)
			}
			if len(rows) == 0 || (vals != nil && len(rows) < flushAt) {
				return
			}
			ph := "(" + strings.TrimSuffix(strings.Repeat("?, ", len(rows[0])), ", ") + ")"
			var args []any
			for _, r := range rows {
				args = append(args, r...)
			}
			q := "insert into `" + table + "` " + cols + " values " + strings.TrimSuffix(strings.Repeat(ph+", ", len(rows)), ", ")
			if _, err := db.Exec(q, args...); err != nil {
				tb.Fatal(err)
			}
			rows = rows[:0]
		}
	}
	score := func(i, salt int) float64 { return 0.25 + float64((i*7919+salt*104729)%192)/256 }

	tag := insert("file_tags", "(`hash`, `tag`, `score`)", 1000)
	for i := 0; i < n; i++ {
		h := largeHash(i)
		if i%5 < 2 {
			tag(h, "outdoor", score(i, 1))
		}
		if i%10 < 3 {
			tag(h, "people", score(i, 2))
		}
		if i%20 == 1 {
			tag(h, "beach", score(i, 3))
		}
		if i%33 == 2 {
			tag(h, "dog", score(i, 4))
		}
		if i%1000 == 5 {
			tag(h, "sunset", score(i, 5))
		}
	}
	tag()

	if _, err := db.Exec("insert into `people` (`id`, `created`) values ('big', now()), ('mid', now()), ('small', now())"); err != nil {
		tb.Fatal(err)
	}
	face := insert("faces", "(`id`, `hash`, `person_id`, `bbox_x`, `bbox_y`, `bbox_w`, `bbox_h`, `embedding`, `thumbnail`, `created`)", 1000)
	faceID := 0
	addFace := func(i int, person string) {
		faceID++
		face(fmt.Sprintf("face-%d", faceID), largeHash(i), person, 0, 0, 1, 1, "", "", time.Now())
	}
	for i := 0; i < n; i++ {
		if i%10 == 1 {
			addFace(i, "big")
			if i%30 == 1 {
				addFace(i, "big") // twice in one photo: a mirror
			}
		}
		if i%100 == 7 || i%100 == 14 {
			addFace(i, "mid")
		}
		if i%1000 == 24 {
			addFace(i, "small")
		}
		if i%50 == 3 {
			addFace(i, "") // taken off its person
		}
	}
	face()

	if _, err := db.Exec("insert into `image_groups` (`id`, `name`, `created`, `rating`) values " +
		"('favourites', 'Favourites', now(), 0), ('trip', 'Imported', now(), 0), ('small', 'Small', now(), 0), ('shoot', 'Shoot', now(), 1)"); err != nil {
		tb.Fatal(err)
	}
	member := insert("image_group_files", "(`group_id`, `hash`, `added`)", 1000)
	rating := insert("photo_ratings", "(`group_id`, `hash`, `stars`, `comment`, `updated`)", 1000)
	for i := 0; i < n; i++ {
		h := largeHash(i)
		if i%200 == 9 {
			member("favourites", h, time.Now())
		}
		if i >= n/3 && i < n/3+n/10 {
			member("trip", h, time.Now())
		}
		if i%2000 == 11 {
			member("small", h, time.Now())
		}
		if i >= n/2 && i < n/2+n/100 {
			member("shoot", h, time.Now())
			if i%5 != 0 {
				var comment any
				if i%10 == 3 {
					comment = []byte("sealed")
				}
				rating("shoot", h, (i*31)%6, comment, time.Now())
			}
		}
	}
	member()
	rating()

	place := insert("photo_locations", "(`hash`, `lat`, `lon`)", 1000)
	for i := 0; i < n; i++ {
		h := largeHash(i)
		j := float64((i*7907)%1000) / 1000
		switch k := i % 20; {
		case k < 9: // home
			place(h, 40.2+0.4*j, -3.9+0.4*j)
		case k == 9: // a trip
			place(h, 48.8+0.1*j, 2.3+0.1*j)
		case k < 12: // anywhere
			place(h, -60+120*j, -170+340*j)
		case k < 16: // read, no position
			place(h, nil, nil)
		}
	}
	place()
	for _, t := range []string{"files", "file_tags", "faces", "image_group_files", "photo_locations", "photo_ratings"} {
		if _, err := db.Exec("analyze table `" + t + "`"); err != nil {
			tb.Fatal(err)
		}
	}
}

// largeSearch is one search of the large library.
type largeSearch struct {
	what   string
	tags   []string
	people []string
	group  string
	area   *MediaArea
	rating *RatingQuery
}

var (
	areaHome  = &MediaArea{South: 40, West: -4, North: 41, East: -3}
	areaTrip  = &MediaArea{South: 48.8, West: 2.3, North: 48.9, East: 2.4}
	areaBlock = &MediaArea{South: 48.80, West: 2.30, North: 48.801, East: 2.301}
	areaWorld = &MediaArea{South: -90, West: -180, North: 90, East: 180}
)

func largeSearches() []largeSearch {
	return []largeSearch{
		{what: "tag outdoor (40%)", tags: []string{"outdoor"}},
		{what: "tag dog (3%)", tags: []string{"dog"}},
		{what: "tag sunset (0.1%)", tags: []string{"sunset"}},
		{what: "tags beach+dog", tags: []string{"beach", "dog"}},
		{what: "person big (10%)", people: []string{"big"}},
		{what: "person mid (2%)", people: []string{"mid"}},
		{what: "person small (0.1%)", people: []string{"small"}},
		{what: "people big+mid", people: []string{"big", "mid"}},
		{what: "collection favourites (0.5%)", group: "favourites"},
		{what: "collection imported (10%)", group: "trip"},
		{what: "collection small (0.05%)", group: "small"},
		{what: "area home (45%)", area: areaHome},
		{what: "area trip (5%)", area: areaTrip},
		{what: "area block (0.005%)", area: areaBlock},
		{what: "area world (60%)", area: areaWorld},
		{what: "rating 3+ stars", group: "shoot", rating: &RatingQuery{MinStars: 3}},
		{what: "rating by stars", group: "shoot", rating: &RatingQuery{ByRating: true}},
		{what: "person big + tag outdoor", tags: []string{"outdoor"}, people: []string{"big"}},
		{what: "area home + person big", people: []string{"big"}, area: areaHome},
	}
}

// Every search of the large library, as SearchMediaRated read it whole and
// as its first chunk is read now (by keyset, or its list's first chunk,
// the list's read included): the time and what it allocates, and the chunk
// must be the whole search's first photos.
func TestLargeSearchesMySQL(t *testing.T) {
	if os.Getenv("OTC_TEST_BROWSE_LARGE") != "1" {
		t.Skip("OTC_TEST_BROWSE_LARGE not set")
	}
	db, d := searchTablesDB(t)
	start := time.Now()
	n := fillLargeLibrary(t, d, 200000)
	fillLargeSearchTables(t, db, 200000)
	t.Logf("library: %d rows in %v", n, time.Since(start).Round(time.Millisecond))
	excluded := []string{"/Private/"}
	if _, err := d.SearchMedia("", nil, nil, "", false, nil, excluded); err != nil {
		t.Fatal(err)
	}
	const limit = 30000 // files_manager's cListMax
	for _, s := range largeSearches() {
		var old []*pb.File
		took, bytes := measure(t, 3, func() {
			var err error
			if old, err = d.SearchMediaRated(s.area, "", s.tags, s.people, s.group, false, nil, excluded, s.rating); err != nil {
				t.Fatal(err)
			}
		})
		b := &MediaBrowse{MediaFilters: MediaFilters{Tags: s.tags, PersonIDs: s.people, GroupID: s.group, Area: s.area, Rating: s.rating}, Excluded: excluded}
		var first []*pb.File
		how := ""
		nowTook, nowBytes := measure(t, 3, func() {
			var err error
			ranked := len(s.tags) > 0 || (s.rating != nil && s.rating.ByRating)
			if ranked {
				how = "ranked list"
				var list []ListedHash
				if list, err = d.RankMedia(&b.MediaFilters); err == nil {
					first, _, err = d.MediaListChunk(b, list, 0, 13, ListRanked)
				}
			} else {
				var list []ListedHash
				var ok bool
				if list, ok, err = d.DatedMedia(b, limit); err == nil && ok {
					how = "dated list"
					first, _, err = d.MediaListChunk(b, list, 0, 13, ListDated)
				} else if err == nil {
					how = "keyset"
					first, _, _, err = d.BrowseMedia(b, nil, 13)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
		})
		sameRows(t, s.what, first[:min(len(first), 13)], old[:min(len(old), 13)])
		t.Logf("%-30s %6d photos: whole %9v %6.1f MB | first chunk (%s) %9v %6.2f MB", s.what, len(old), took, float64(bytes)/1e6,
			how, nowTook, float64(nowBytes)/1e6)
	}
}
