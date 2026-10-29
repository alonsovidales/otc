// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// slippyTile is PlaceTile's formula as a client computes it, written out
// independently of mercator.
func slippyTile(lat, lon float64, z int) (x, y int64) {
	n := math.Exp2(float64(z))
	lat = math.Max(-cMercatorMaxLat, math.Min(cMercatorMaxLat, lat))
	r := lat * math.Pi / 180
	x = int64(math.Floor((lon + 180) / 360 * n))
	y = int64(math.Floor((1 - math.Log(math.Tan(r)+1/math.Cos(r))/math.Pi) / 2 * n))
	last := int64(n) - 1
	return min(max(x, 0), last), min(max(y, 0), last)
}

// The index's fixed-point positions put a photo in the tile a client's
// formula puts it in, at every zoom from 0 to 20, the poles in the first
// and last rows and the antimeridian in the last column.
func TestMercatorTilesMatchTheSlippyFormula(t *testing.T) {
	points := [][2]float64{
		{0, 0}, {40.6892, -74.0445}, {43.4674, 11.8851}, {-33.8568, 151.2153},
		{89.9, 10}, {-89.9, -10}, {90, 0}, {-90, 0}, {85.0511, 179.999}, {12, 180}, {12, -180},
		{51.4779, -0.0015}, {-17.7134, 178.065},
	}
	rng := rand.New(rand.NewSource(1))
	for range 500 {
		points = append(points, [2]float64{rng.Float64()*170 - 85, rng.Float64()*360 - 180})
	}
	for _, p := range points {
		x, y := mercator(p[0], p[1])
		for z := 0; z <= cPlacesMaxZoom; z++ {
			wx, wy := slippyTile(p[0], p[1], z)
			// A shift by 32 is 0 in Go, as placesGroups relies on at zoom 0.
			gx, gy := int64(x>>uint(32-z)), int64(y>>uint(32-z))
			// The float formula and the fixed point can only disagree for
			// a point within a rounding error of a tile's edge.
			if (gx != wx || gy != wy) && !nearTileEdge(p[0], p[1], z) {
				t.Errorf("%v at zoom %d: tile %d,%d, the formula says %d,%d", p, z, gx, gy, wx, wy)
			}
		}
	}
	if x, _ := mercator(0, 180); x != math.MaxUint32 {
		t.Errorf("180° is at %d, not in the last column", x)
	}
	if x, _ := mercator(0, -180); x != 0 {
		t.Errorf("-180° is at %d, not in the first column", x)
	}
	if _, y := mercator(90, 0); y != 0 {
		t.Errorf("the north pole is at %d, not in the first row", y)
	}
	if _, y := mercator(-90, 0); y != math.MaxUint32 {
		t.Errorf("the south pole is at %d, not in the last row", y)
	}
}

func nearTileEdge(lat, lon float64, z int) bool {
	n := math.Exp2(float64(z))
	fx := (lon + 180) / 360 * n
	lat = math.Max(-cMercatorMaxLat, math.Min(cMercatorMaxLat, lat))
	r := lat * math.Pi / 180
	fy := (1 - math.Log(math.Tan(r)+1/math.Cos(r))/math.Pi) / 2 * n
	const eps = 1e-6
	return math.Abs(fx-math.Round(fx)) < eps || math.Abs(fy-math.Round(fy)) < eps
}

// The tiles asked for: x modulo 2^zoom (a map shown across the
// antimeridian), rows outside the map left out, each tile once, 64 at
// most.
func TestPlacesTiles(t *testing.T) {
	got := placesTiles(2, []*pb.PlaceTile{{X: -1, Y: 0}, {X: 3, Y: 0}, {X: 4, Y: 1}, {X: 1, Y: -1}, {X: 1, Y: 4}, {X: 9, Y: 3}})
	want := []placeTile{{3, 0}, {0, 1}, {1, 3}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("tiles %v, want %v", got, want)
	}
	var many []*pb.PlaceTile
	for i := range 100 {
		many = append(many, &pb.PlaceTile{X: int32(i), Y: 5})
	}
	if n := len(placesTiles(10, many)); n != cPlacesMaxTiles {
		t.Errorf("%d tiles grouped, want %d", n, cPlacesMaxTiles)
	}
	if got := placesTiles(0, []*pb.PlaceTile{{X: 5, Y: 0}, {X: 0, Y: 1}}); fmt.Sprint(got) != fmt.Sprint([]placeTile{{0, 0}}) {
		t.Errorf("zoom 0 tiles %v, want only 0,0", got)
	}
}

func testEntry(i int, lat, lon float64, created int64, video bool) placeEntry {
	e := placeEntry{lat: lat, lon: lon, created: created, video: video}
	binary.BigEndian.PutUint32(e.hash[:4], uint32(i))
	e.x, e.y = mercator(lat, lon)
	return e
}

func groupsByTile(groups []*placeGroup) map[string]*placeGroup {
	out := map[string]*placeGroup{}
	for _, g := range groups {
		out[fmt.Sprintf("%d,%d", g.cx, g.cy)] = g
	}
	return out
}

// One place per cell of the tiles asked: its count, the photo it is
// drawn at, bounding box and newest photo; photos in tiles not asked for still
// count in the totals and the area holding every photo.
func TestPlacesGroupsCellsOfTheTilesAsked(t *testing.T) {
	// Florence's centre, a few hundred metres apart, and Rome.
	entries := []placeEntry{
		testEntry(1, 43.7731, 11.2560, 100, false),
		testEntry(2, 43.7696, 11.2558, 300, false),
		testEntry(3, 43.7710, 11.2500, 300, false), // as new as 2: the greater hash wins
		testEntry(4, 41.9028, 12.4964, 50, false),
		testEntry(5, 43.7700, 11.2550, 900, true), // a video, kept out below
	}
	z := 8
	fx, fy := slippyTile(43.7731, 11.2560, z)
	keep := func(i int) bool { return !entries[i].video }
	groups, total, area := placesGroups(entries, keep, z, []placeTile{{uint32(fx), uint32(fy)}})
	if total != 4 {
		t.Errorf("total %d, want 4 (every photo kept, asked for or not)", total)
	}
	if area == nil || area.South != 41.9028 || area.North != 43.7731 || area.West != 11.25 || area.East != 12.4964 {
		t.Errorf("area %v", area)
	}
	if len(groups) != 1 {
		t.Fatalf("%d places, want Florence's one (Rome is in another tile): %v", len(groups), groups)
	}
	g := groups[0]
	if g.count != 3 {
		t.Errorf("Florence holds %d, want 3", g.count)
	}
	// On the map (where a degree of latitude is 1/cos(lat) longer than
	// one of longitude) their mean is about 0.0029° from 1, 0.0033° from 0
	// and 0.0039° from 2.
	if g.pin != 1 {
		t.Errorf("drawn at entry %d, want 1: the photo nearest the three's mean", g.pin)
	}
	if g.minLat != 43.7696 || g.maxLat != 43.7731 || g.minLon != 11.25 || g.maxLon != 11.256 {
		t.Errorf("box %v %v %v %v", g.minLat, g.maxLat, g.minLon, g.maxLon)
	}
	if g.cover != 2 {
		t.Errorf("cover %d, want entry 2 (newest, greater hash)", g.cover)
	}
	if tx, ty := g.cx>>cPlaceCellBits, g.cy>>cPlaceCellBits; int64(tx) != fx || int64(ty) != fy {
		t.Errorf("place's tile %d,%d, asked %d,%d", tx, ty, fx, fy)
	}

	// Deeper, the same photos split: at zoom 18 a tile is about 110 m
	// across here, a cell a quarter of that.
	z = 18
	var tiles []placeTile
	for _, e := range entries[:3] {
		x, y := slippyTile(e.lat, e.lon, z)
		tiles = append(tiles, placeTile{uint32(x), uint32(y)})
	}
	groups, _, _ = placesGroups(entries, keep, z, placesTiles(z, toPB(tiles)))
	if len(groups) != 3 {
		t.Errorf("%d places at zoom 18, want 3", len(groups))
	}
}

// A place is drawn at one of its photos, never at their mean: at zoom 2
// Paris and Barcelona share a cell, and their mean is near Lyon, where
// neither was taken. With more photos in Paris the pin is there.
func TestPlacesDrawnAtAPhotoNotTheMean(t *testing.T) {
	entries := []placeEntry{
		testEntry(1, 48.8566, 2.3522, 100, false), // Paris
		testEntry(2, 48.8600, 2.3400, 200, false), // Paris
		testEntry(3, 48.8530, 2.3600, 300, false), // Paris
		testEntry(4, 41.3874, 2.1686, 400, false), // Barcelona, the newest
	}
	z := 2
	x, y := slippyTile(48.8566, 2.3522, z)
	if bx, by := slippyTile(41.3874, 2.1686, z); bx != x || by != y {
		t.Fatalf("Barcelona in tile %d,%d, Paris in %d,%d", bx, by, x, y)
	}
	groups, _, _ := placesGroups(entries, nil, z, []placeTile{{uint32(x), uint32(y)}})
	if len(groups) != 1 || groups[0].count != 4 {
		t.Fatalf("places %v, want one of 4", groups)
	}
	g := groups[0]
	if e := entries[g.pin]; e.lat < 48 {
		t.Errorf("drawn at %v, %v; want one of the Paris photos", e.lat, e.lon)
	}
	if g.cover != 3 {
		t.Errorf("cover %d, want Barcelona's, the newest", g.cover)
	}
}

func toPB(tiles []placeTile) []*pb.PlaceTile {
	var out []*pb.PlaceTile
	for _, t := range tiles {
		out = append(out, &pb.PlaceTile{X: int32(t.x), Y: int32(t.y)})
	}
	return out
}

// Zoom 0 is one tile, the world in 4 x 4 cells; zoom 20 tells apart
// photos a few metres apart.
func TestPlacesGroupsAtZoomZeroAndTwenty(t *testing.T) {
	entries := []placeEntry{
		testEntry(1, 48.8584, 2.2945, 1, false),    // Paris
		testEntry(2, 48.8606, 2.3376, 2, false),    // Paris
		testEntry(3, -33.8568, 151.2153, 3, false), // Sydney
		testEntry(4, 40.6892, -74.0445, 4, false),  // New York
	}
	groups, total, _ := placesGroups(entries, nil, 0, []placeTile{{0, 0}})
	if total != 4 || len(groups) != 3 {
		t.Errorf("zoom 0: %d photos in %d places, want 4 in 3", total, len(groups))
	}
	for _, g := range groups {
		if g.cx > 3 || g.cy > 3 {
			t.Errorf("zoom 0 cell %d,%d outside the 4 x 4", g.cx, g.cy)
		}
	}
	// 15 m apart along a street: two cells at zoom 20 (about 10 m), one at 17.
	a := testEntry(1, 48.858400, 2.294500, 1, false)
	b := testEntry(2, 48.858400, 2.294700, 2, false)
	for _, c := range []struct{ z, want int }{{20, 2}, {17, 1}} {
		x, y := slippyTile(a.lat, a.lon, c.z)
		x2, y2 := slippyTile(b.lat, b.lon, c.z)
		tiles := placesTiles(c.z, []*pb.PlaceTile{{X: int32(x), Y: int32(y)}, {X: int32(x2), Y: int32(y2)}})
		if groups, _, _ := placesGroups([]placeEntry{a, b}, nil, c.z, tiles); len(groups) != c.want {
			t.Errorf("zoom %d: %d places, want %d", c.z, len(groups), c.want)
		}
	}
}

// Photos on both sides of the antimeridian: places in the first and the
// last column, never one across it, and an area across it for them all.
func TestPlacesAcrossTheAntimeridian(t *testing.T) {
	entries := []placeEntry{
		testEntry(1, -17.7, 179.9, 1, false),
		testEntry(2, -17.8, -179.9, 2, false),
		testEntry(3, -16.5, 179.2, 3, false),
	}
	z := 3
	groups, total, area := placesGroups(entries, nil, z, placesTiles(z, []*pb.PlaceTile{{X: -1, Y: 4}, {X: 0, Y: 4}}))
	if total != 3 || len(groups) != 2 {
		t.Fatalf("%d photos in %d places, want 3 in 2", total, len(groups))
	}
	for _, g := range groups {
		if g.minLon < 0 != (g.maxLon < 0) {
			t.Errorf("a place across the antimeridian: %v to %v", g.minLon, g.maxLon)
		}
	}
	if area.West != 179.2 || area.East != -179.9 {
		t.Errorf("area west %v east %v, want 179.2 to -179.9 (across it)", area.West, area.East)
	}
	// Spread around the world, the area is the plain one.
	_, _, area = placesGroups([]placeEntry{testEntry(1, 0, -100, 1, false), testEntry(2, 0, 100, 1, false), testEntry(3, 0, 0, 1, false)}, nil, 0, nil)
	if area.West != -100 || area.East != 100 {
		t.Errorf("area west %v east %v, want -100 to 100", area.West, area.East)
	}
	if _, total, area := placesGroups(nil, nil, 5, nil); total != 0 || area != nil {
		t.Errorf("no photos: total %d, area %v", total, area)
	}
}

// The index is built in the first request, then answers stay with it
// until the library changes; the next one after a change is still the
// old index while the new one is built in the background, at most once
// per cPlacesMinRebuild.
func TestPlacesCacheRebuildsInTheBackground(t *testing.T) {
	var c placesCache
	var builds atomic.Int32
	built := make(chan struct{}, 4)
	build := func(gen uint64) (*placesIndex, error) {
		n := builds.Add(1)
		defer func() { built <- struct{}{} }()
		return &placesIndex{gen: gen, built: time.Now(), entries: make([]placeEntry, n)}, nil
	}
	now := time.Now()
	idx, err := c.index(now, build)
	<-built
	if err != nil || len(idx.entries) != 1 {
		t.Fatalf("first index %v, %v", idx, err)
	}
	if again, _ := c.index(now, build); again != idx || builds.Load() != 1 {
		t.Errorf("unchanged library built again")
	}
	c.changed()
	if again, _ := c.index(now.Add(time.Second), build); again != idx || builds.Load() != 1 {
		t.Errorf("rebuilt within cPlacesMinRebuild")
	}
	if again, _ := c.index(now.Add(cPlacesMinRebuild+time.Second), build); again != idx {
		t.Errorf("a stale request waited for the rebuild")
	}
	<-built
	if next, _ := c.index(now.Add(cPlacesMinRebuild+2*time.Second), build); len(next.entries) != 2 {
		t.Errorf("the rebuilt index isn't served: %d entries", len(next.entries))
	}
}

// A filtered map's photos are kept for the next tiles, and read again
// once the index changes or cPlacesMaskTTL passes.
func TestPlacesMasksAreKeptAMinute(t *testing.T) {
	var c placesCache
	idx := &placesIndex{built: time.Now()}
	c.set(idx)
	fills := 0
	fill := func() ([]bool, error) { fills++; return []bool{true}, nil }
	now := time.Now()
	c.mask(idx, "k", now, fill)
	c.mask(idx, "k", now.Add(30*time.Second), fill)
	if fills != 1 {
		t.Errorf("filled %d times within the minute", fills)
	}
	c.mask(idx, "k", now.Add(cPlacesMaskTTL+time.Second), fill)
	c.mask(idx, "other", now, fill)
	if fills != 3 {
		t.Errorf("filled %d times, want 3", fills)
	}
	c.set(&placesIndex{built: time.Now()})
	if len(c.masks) != 0 {
		t.Errorf("a new index kept the old one's masks")
	}
}

func hashOf(i int) string { return fmt.Sprintf("%064x", i) }

// GetPhotoPlaces end to end over the dao: the index read once, a place
// per cell with its cover at the path the photo search shows it, the
// totals; a filtered map keeps the index's photos the search finds.
func TestPhotoPlacesAnswer(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mg := &Manager{dao: dao.NewWithDB(db)}
	empty := []string{}
	mg.outOfImagesCache.Store(&empty)

	at := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
	// A row per copy: photo 1 has a newer copy, which leaves it older than
	// photo 2; 3 is a video.
	located := sqlmock.NewRows([]string{"hash", "created", "video", "lat", "lon"}).
		AddRow(hashOf(1), at.Add(3*time.Hour), 0, 43.7731, 11.2560).
		AddRow(hashOf(2), at.Add(2*time.Hour), 0, 43.7696, 11.2558).
		AddRow(hashOf(1), at, 0, 43.7731, 11.2560).
		AddRow(hashOf(3), at, 1, 43.7700, 11.2550).
		AddRow("not-a-hash", at, 0, 1.0, 1.0)
	mock.ExpectQuery("select `f`.`hash`, `f`.`created`, `f`.`mime` like 'video%', `l`.`lat`, `l`.`lon` from `files` as `f` join `photo_locations` as `l`").WillReturnRows(located)
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `hash` in \\(\\?\\)").
		WithArgs(hashOf(2)).
		WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).
			AddRow(hashOf(2), "image/jpeg", at.Add(2*time.Hour), at, "/b/copy.jpg", 10).
			AddRow(hashOf(2), "image/jpeg", at.Add(time.Hour), at, "/a/first.jpg", 10))

	x, y := slippyTile(43.7731, 11.2560, 8)
	out, err := mg.PhotoPlaces(8, []*pb.PlaceTile{{X: int32(x), Y: int32(y)}}, nil, nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if out.LocatedTotal != 2 || out.Zoom != 8 || len(out.Places) != 1 {
		t.Fatalf("answer %v", out)
	}
	p := out.Places[0]
	if p.Count != 2 || p.X != int32(x) || p.Y != int32(y) || p.Zoom != 8 {
		t.Errorf("place %v", p)
	}
	if p.Cover.GetPath() != "/a/first.jpg" {
		t.Errorf("cover %v, want the oldest copy of the newest photo", p.Cover)
	}

	// With a tag: the index stays, the search's photos are read once.
	mock.ExpectQuery("select distinct `f`.`hash` from `files` as `f` join \\(select `hash`, sum\\(`score`\\)").
		WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow(hashOf(3)).AddRow(hashOf(9)))
	mock.ExpectQuery("from `files` where `hash` in").WithArgs(hashOf(3)).
		WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).AddRow(hashOf(3), "video/mp4", at, at, "/v.mp4", 10))
	out, err = mg.PhotoPlaces(8, []*pb.PlaceTile{{X: int32(x), Y: int32(y)}}, []string{"bridge"}, nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if out.LocatedTotal != 1 || len(out.Places) != 1 || out.Places[0].Cover.GetPath() != "/v.mp4" {
		t.Errorf("tagged answer %v", out)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// What GetPhotoPlaces costs on a large library: grouping 100,000 photos
// for a screenful of tiles, logged (go test -v -run Timing).
func TestPlacesGroupsTiming(t *testing.T) {
	entries := syntheticEntries(100_000)
	for _, c := range []struct {
		name string
		z    int
	}{{"world", 2}, {"country", 6}, {"city", 12}} {
		// The 4 x 3 tiles around Florence at that zoom, as a phone shows.
		cx, cy := slippyTile(43.77, 11.25, c.z)
		var tiles []*pb.PlaceTile
		for dx := int64(-2); dx < 2; dx++ {
			for dy := int64(-1); dy < 2; dy++ {
				tiles = append(tiles, &pb.PlaceTile{X: int32(cx + dx), Y: int32(cy + dy)})
			}
		}
		asked := placesTiles(c.z, tiles)
		start := time.Now()
		const runs = 20
		var groups []*placeGroup
		for range runs {
			groups, _, _ = placesGroups(entries, func(i int) bool { return !entries[i].video }, c.z, asked)
		}
		t.Logf("%s (zoom %d): %d places from 100,000 photos in %v a request", c.name, c.z, len(groups), time.Since(start)/runs)
	}
}

func BenchmarkPlacesGroups(b *testing.B) {
	entries := syntheticEntries(100_000)
	cx, cy := slippyTile(43.77, 11.25, 10)
	var tiles []*pb.PlaceTile
	for dx := int64(-2); dx < 2; dx++ {
		for dy := int64(-1); dy < 2; dy++ {
			tiles = append(tiles, &pb.PlaceTile{X: int32(cx + dx), Y: int32(cy + dy)})
		}
	}
	asked := placesTiles(10, tiles)
	b.ResetTimer()
	for range b.N {
		placesGroups(entries, nil, 10, asked)
	}
}

// syntheticEntries is a library of n photos: most around a few cities, as
// a family's are, the rest anywhere.
func syntheticEntries(n int) []placeEntry {
	rng := rand.New(rand.NewSource(7))
	cities := [][2]float64{{43.77, 11.25}, {40.42, -3.70}, {48.86, 2.35}, {51.51, -0.13}, {40.71, -74.0}}
	out := make([]placeEntry, n)
	for i := range out {
		var lat, lon float64
		if i%10 == 0 {
			lat, lon = rng.Float64()*140-70, rng.Float64()*360-180
		} else {
			c := cities[i%len(cities)]
			lat, lon = c[0]+rng.NormFloat64()*0.3, c[1]+rng.NormFloat64()*0.3
		}
		out[i] = testEntry(i, lat, lon, int64(rng.Intn(1e9)), i%17 == 0)
	}
	return out
}
