// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
)

// Issue #206, against a real MySQL/MariaDB (OTC_TEST_MYSQL_DSN, as
// browse_mysql_test.go): every search but the plain browse - tags, people,
// collections, areas, ratings, folders and their mixes, down, from jumps
// and up - read a chunk at a time, by keyset (BrowseMedia) and through its
// list (DatedMedia, RankMedia and MediaListChunk), is SearchMediaRated's
// (SearchMediaNewerThanWithin's) photos: the same rows, copy for copy, in
// the same order, whatever the chunks.

// searchesLibrary is browseLibrary with what the searches filter by, on
// its 700 photos (0x20600000+i): tags with scores that tie (and sums of two
// or three of them, exact in binary), on documents and on content no row
// holds too; people, with photos where one is twice, faces taken off their
// person; collections, a rated one with stars, comments and ratings of
// photos not in it; positions in clusters, across the antimeridian, and
// read without one. Deterministic.
func searchesLibrary(tb testing.TB) (*sql.DB, *Dao) {
	tb.Helper()
	db, d := searchTablesDB(tb)
	browseLibrary(tb, d)
	r := rand.New(rand.NewSource(2060))
	hash := func(i int) string { return fmt.Sprintf("%064x", 0x20600000+i) }
	var tags, faces, members, ratings, places []string
	var tagArgs, faceArgs, memberArgs, ratingArgs, placeArgs []any
	scores := []float64{0.25, 0.5, 0.75, 0.125}
	for i := 0; i < 720; i++ { // the last 20: content no files row holds
		h := hash(i)
		for _, t := range []struct {
			tag  string
			odds int
		}{{"beach", 40}, {"dog", 20}, {"sunset", 6}} {
			if r.Intn(100) < t.odds {
				tags = append(tags, "(?, ?, ?)")
				tagArgs = append(tagArgs, h, t.tag, scores[r.Intn(len(scores))])
			}
		}
		addFace := func(person string) {
			faces = append(faces, "(?, ?, ?, 0, 0, 1, 1, '', '', now())")
			faceArgs = append(faceArgs, fmt.Sprintf("face-%d-%d", i, len(faces)), h, person)
		}
		if r.Intn(100) < 30 {
			addFace("p1")
			if r.Intn(5) == 0 {
				addFace("p1") // twice in one photo
			}
		}
		if r.Intn(100) < 12 {
			addFace("p2")
		}
		if r.Intn(100) < 8 {
			addFace("") // taken off its person
		}
		for _, g := range []struct {
			id   string
			odds int
		}{{"g1", 30}, {"g2", 5}, {"gr", 25}} {
			if r.Intn(100) < g.odds {
				members = append(members, "(?, ?, now())")
				memberArgs = append(memberArgs, g.id, h)
				if g.id == "gr" && r.Intn(4) != 0 {
					var comment any
					if r.Intn(3) == 0 {
						comment = []byte("sealed")
					}
					ratings = append(ratings, "('gr', ?, ?, ?, now())")
					ratingArgs = append(ratingArgs, h, r.Intn(6), comment)
				}
			}
		}
		if r.Intn(40) == 0 { // a rating of a photo not in the collection
			ratings = append(ratings, "('g2', ?, 5, null, now())")
			ratingArgs = append(ratingArgs, h)
		}
		switch k := r.Intn(10); {
		case k < 4: // Madrid
			places = append(places, "(?, ?, ?)")
			placeArgs = append(placeArgs, h, 40.3+r.Float64()/5, -3.8+r.Float64()/5)
		case k < 6: // Fiji, either side of the antimeridian
			places = append(places, "(?, ?, ?)")
			placeArgs = append(placeArgs, h, -17.5+r.Float64(), 179.5+r.Float64()-(map[bool]float64{true: 360}[r.Intn(2) == 0]))
		case k < 7:
			places = append(places, "(?, null, null)")
			placeArgs = append(placeArgs, h)
		}
	}
	for _, q := range []struct {
		table string
		vals  []string
		args  []any
	}{
		{"`file_tags` (`hash`, `tag`, `score`)", tags, tagArgs},
		{"`faces` (`id`, `hash`, `person_id`, `bbox_x`, `bbox_y`, `bbox_w`, `bbox_h`, `embedding`, `thumbnail`, `created`)", faces, faceArgs},
		{"`image_group_files` (`group_id`, `hash`, `added`)", members, memberArgs},
		{"`photo_ratings` (`group_id`, `hash`, `stars`, `comment`, `updated`)", ratings, ratingArgs},
		{"`photo_locations` (`hash`, `lat`, `lon`)", places, placeArgs},
	} {
		if _, err := db.Exec("insert into "+q.table+" values "+strings.Join(q.vals, ", "), q.args...); err != nil {
			tb.Fatal(err)
		}
	}
	for _, q := range []string{
		"insert into `people` (`id`, `created`) values ('p1', now()), ('p2', now())",
		"insert into `image_groups` (`id`, `name`, `created`, `rating`) values ('g1', 'One', now(), 0), ('g2', 'Two', now(), 0), ('gr', 'Rated', now(), 1)",
	} {
		if _, err := db.Exec(q); err != nil {
			tb.Fatal(err)
		}
	}
	return db, d
}

// oldSearch is what the whole search gives: SearchMediaRated, or going up
// SearchMediaNewerThanWithin.
func oldSearch(tb testing.TB, d *Dao, b *MediaBrowse) []*pb.File {
	tb.Helper()
	m := b.MediaFilters
	var files []*pb.File
	var err error
	if b.NewerThan != nil {
		files, err = d.SearchMediaNewerThanWithin(m.Area, m.Path, m.Tags, m.PersonIDs, m.GroupID, b.ImagesOnly, *b.NewerThan, b.Excluded)
	} else {
		files, err = d.SearchMediaRated(m.Area, m.Path, m.Tags, m.PersonIDs, m.GroupID, b.ImagesOnly, b.Before, b.Excluded, m.Rating)
	}
	if err != nil {
		tb.Fatal(err)
	}
	return files
}

// listAll is every chunk of list, want photos at a time, checked against
// MediaListChunk's promises as it goes.
func listAll(tb testing.TB, d *Dao, b *MediaBrowse, list []ListedHash, want int, order ListOrder) []*pb.File {
	tb.Helper()
	var all []*pb.File
	at := 0
	for chunks := 0; at < len(list); chunks++ {
		if chunks > 100000 {
			tb.Fatal("the chunks never end")
		}
		files, next, err := d.MediaListChunk(b, list, at, want, order)
		if err != nil {
			tb.Fatal(err)
		}
		if next <= at {
			tb.Fatalf("a chunk from %d ends at %d", at, next)
		}
		if next < len(list) && len(files) < want {
			tb.Fatalf("a chunk that isn't the last holds %d photos, asked for %d", len(files), want)
		}
		all = append(all, files...)
		at = next
	}
	return all
}

// filterSearches is every date-ordered search the library is tested with
// (by keyset and through DatedMedia), and rankedSearches every ranked one.
func filterSearches() []MediaFilters {
	madrid := &MediaArea{South: 40.3, West: -3.8, North: 40.5, East: -3.6}
	corner := &MediaArea{South: 40.3, West: -3.8, North: 40.34, East: -3.76}
	fiji := &MediaArea{South: -18, West: 179, North: -16, East: -179} // across the antimeridian
	return []MediaFilters{
		{PersonIDs: []string{"p1"}},
		{PersonIDs: []string{"p2"}},
		{PersonIDs: []string{"p1", "p2"}},
		{PersonIDs: []string{"p1", "p1"}},
		{PersonIDs: []string{""}},
		{GroupID: "g1"},
		{GroupID: "g2"},
		{GroupID: "favourites"}, // empty
		{Area: madrid},
		{Area: corner},
		{Area: fiji},
		{GroupID: "gr", Rating: &RatingQuery{MinStars: 3}},
		{GroupID: "gr", Rating: &RatingQuery{Unrated: true}},
		{GroupID: "gr", Rating: &RatingQuery{WithComment: true}},
		{GroupID: "gr", Rating: &RatingQuery{MinStars: 2, WithComment: true}},
		{GroupID: "g1", Rating: &RatingQuery{MinStars: 3}}, // no ratings: none pass
		{GroupID: "g1", PersonIDs: []string{"p1"}},
		{Area: madrid, PersonIDs: []string{"p1"}},
		{Area: fiji, GroupID: "g1"},
		{Path: "/Phone/DCIM/"},
		{Path: "/Mac/", GroupID: "g1"},
	}
}

func rankedSearches() []MediaFilters {
	madrid := &MediaArea{South: 40.3, West: -3.8, North: 40.5, East: -3.6}
	return []MediaFilters{
		{Tags: []string{"beach"}},
		{Tags: []string{"beach", "dog"}},
		{Tags: []string{"beach", "dog", "sunset"}},
		{Tags: []string{"none"}},
		{Tags: []string{"beach"}, PersonIDs: []string{"p1"}},
		{Tags: []string{"dog"}, GroupID: "g1", Area: madrid},
		{Tags: []string{"beach"}, Path: "/Phone/DCIM/"},
		{Tags: []string{"beach"}, GroupID: "gr", Rating: &RatingQuery{MinStars: 2}},
		{GroupID: "gr", Rating: &RatingQuery{ByRating: true}},
		{GroupID: "gr", Rating: &RatingQuery{ByRating: true, MinStars: 1}},
		{GroupID: "gr", Rating: &RatingQuery{ByRating: true, WithComment: true}},
		{GroupID: "gr", Rating: &RatingQuery{ByRating: true}, Tags: []string{"beach", "dog"}},
		{GroupID: "gr", Rating: &RatingQuery{ByRating: true}, PersonIDs: []string{"p1"}, Area: madrid},
	}
}

func describeSearch(m MediaFilters, imagesOnly bool, excluded []string) string {
	s := fmt.Sprintf("path %q tags %v people %q group %q", m.Path, m.Tags, m.PersonIDs, m.GroupID)
	if m.Area != nil {
		s += fmt.Sprintf(" area %+v", *m.Area)
	}
	if m.Rating != nil {
		s += fmt.Sprintf(" rating %+v", *m.Rating)
	}
	return s + fmt.Sprintf(", images only %v, kept out %v", imagesOnly, excluded)
}

func TestFilteredSearchesByKeysetAndListMySQL(t *testing.T) {
	_, d := searchesLibrary(t)
	base := time.Date(2024, 3, 1, 9, 0, 0, 0, time.UTC)
	instants := []time.Time{
		base.Add(time.Second),                           // inside the crowd
		base.Add(time.Second + 999*time.Millisecond),    // a web jump's .999
		base.Add(90*time.Minute + 500*time.Millisecond), // the middle
		base.Add(-time.Hour),                            // before everything
	}
	searched := 0
	// A tag search going up is in date order too (SearchMediaNewerThan
	// sorts it so): only up.
	var tagsUp []MediaFilters
	for _, m := range rankedSearches() {
		if m.Rating == nil || !m.Rating.ByRating {
			tagsUp = append(tagsUp, m)
		}
	}
	for i, m := range append(filterSearches(), tagsUp...) {
		onlyUp := i >= len(filterSearches())
		for _, imagesOnly := range []bool{true, false} {
			for _, excluded := range [][]string{nil, {"/Private/"}, {"/Private/", "/Mac/a b/"}} {
				kind := describeSearch(m, imagesOnly, excluded)
				var browses []*MediaBrowse
				for _, before := range append([]*time.Time{nil}, ptrs(instants)...) {
					if !onlyUp {
						browses = append(browses, &MediaBrowse{MediaFilters: m, ImagesOnly: imagesOnly, Before: before, Excluded: excluded})
					}
				}
				for i := range instants {
					// Up; Before is ignored there.
					browses = append(browses, &MediaBrowse{MediaFilters: m, ImagesOnly: imagesOnly, NewerThan: &instants[i], Before: &instants[0], Excluded: excluded})
				}
				for _, b := range browses {
					what := fmt.Sprintf("%s, before %v, newer than %v", kind, b.Before, b.NewerThan)
					want := oldSearch(t, d, b)
					searched += len(want)
					for _, n := range []int{0, 1, 5, 1000} {
						sameRows(t, fmt.Sprintf("%s, by keyset %d at a time", what, n), browseAll(t, d, b, n), want)
					}
					list, ok, err := d.DatedMedia(b, 1<<30)
					if err != nil {
						t.Fatal(err)
					}
					if !ok {
						t.Fatalf("%s: no list", what)
					}
					for _, n := range []int{1, 5, 1000} {
						sameRows(t, fmt.Sprintf("%s, listed %d at a time", what, n), listAll(t, d, b, list, n, ListDated), want)
					}
					if len(list) != len(want) {
						t.Fatalf("%s: %d listed, %d photos", what, len(list), len(want))
					}
				}
			}
		}
	}
	if searched < 20000 {
		t.Fatalf("only %d photos found in all: the library doesn't exercise the searches", searched)
	}
}

func TestRankedSearchesThroughTheirListMySQL(t *testing.T) {
	_, d := searchesLibrary(t)
	jump := time.Date(2024, 3, 1, 10, 30, 0, 0, time.UTC)
	for _, m := range rankedSearches() {
		list, err := d.RankMedia(&m)
		if err != nil {
			t.Fatal(err)
		}
		for _, imagesOnly := range []bool{true, false} {
			for _, excluded := range [][]string{nil, {"/Private/"}} {
				for _, before := range []*time.Time{nil, &jump} {
					b := &MediaBrowse{MediaFilters: m, ImagesOnly: imagesOnly, Before: before, Excluded: excluded}
					what := fmt.Sprintf("%s, before %v", describeSearch(m, imagesOnly, excluded), before)
					want := oldSearch(t, d, b)
					if len(m.Tags) > 0 && m.Tags[0] == "none" {
						if len(want) != 0 || len(list) != 0 {
							t.Fatalf("%s: %d photos, %d listed", what, len(want), len(list))
						}
					} else if before == nil && len(want) < 10 {
						t.Fatalf("%s: only %d photos", what, len(want))
					}
					for _, n := range []int{1, 2, 5, 1000} {
						sameRows(t, fmt.Sprintf("%s, %d at a time", what, n), listAll(t, d, b, list, n, ListRanked), want)
					}
				}
			}
		}
	}
	// A search that isn't ranked has no ranking.
	if _, err := d.RankMedia(&MediaFilters{GroupID: "gr"}); err == nil {
		t.Error("a collection by date was ranked")
	}
}

// EstimateMedia counts by index, at least the photos: the smallest
// filter's count; none without a filter; nothing for people no photo can
// have all of. DatedMedia gives no list past its limit.
func TestEstimateMediaMySQL(t *testing.T) {
	_, d := searchesLibrary(t)
	count := func(m MediaFilters) int {
		t.Helper()
		n, err := d.EstimateMedia(&m)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(MediaFilters{}); n != -1 {
		t.Errorf("no filter: %d", n)
	}
	for _, m := range filterSearches() {
		n := count(m)
		photos := len(oldSearch(t, d, &MediaBrowse{MediaFilters: m}))
		if n < photos {
			t.Errorf("%s: estimated %d, %d photos", describeSearch(m, false, nil), n, photos)
		}
	}
	if n := count(MediaFilters{PersonIDs: []string{"p1", "p1"}}); n != 0 {
		t.Errorf("a person twice: %d", n)
	}
	p1, g2 := count(MediaFilters{PersonIDs: []string{"p1"}}), count(MediaFilters{GroupID: "g2"})
	if both := count(MediaFilters{PersonIDs: []string{"p1"}, GroupID: "g2"}); both != min(p1, g2) {
		t.Errorf("p1 %d, g2 %d, both %d", p1, g2, both)
	}
	if _, ok, err := d.DatedMedia(&MediaBrowse{MediaFilters: MediaFilters{PersonIDs: []string{"p1"}}}, p1-1); ok || err != nil {
		t.Errorf("listed past its limit: %v %v", ok, err)
	}
	if _, ok, err := d.DatedMedia(&MediaBrowse{}, 1<<30); ok || err != nil {
		t.Errorf("listed the plain browse: %v %v", ok, err)
	}
}
