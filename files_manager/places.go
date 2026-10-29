// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"encoding/hex"
	"math"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// Places, the map in Images (GetPhotoPlaces): the photos with a position
// grouped by the cells of the map tiles a client shows. Every answer
// groups the whole library, so it is read from an index in memory (about
// 72 bytes a photo: 7 MB for 100,000), not from the database: built the
// first time the map asks, again in the background once the library has
// changed (placesCache.changed), and dropped after cPlacesIdle without a
// request. A filtered map (tags, people, collection) keeps the index's
// photos among those the same search finds (EachMatchingMediaHash), a
// query whose result is kept a minute for the next tiles.

const (
	// cPlacesMaxZoom is the deepest zoom GetPhotoPlaces groups at: 4 cells
	// a tile at zoom 20 are about 10 m across at the equator.
	cPlacesMaxZoom = 20
	// cPlacesMaxTiles is the most tiles one request groups.
	cPlacesMaxTiles = 64
	// cPlaceCellBits: a tile is 2^2 x 2^2 cells, 64 px of a 256 px tile.
	cPlaceCellBits = 2
	// cMercatorMaxLat is the latitude where Web Mercator's square map ends.
	cMercatorMaxLat = 85.0511287798066
	// cPlacesMinRebuild is the least time between two builds of the index
	// while the library changes (a phone's first sync): answers meanwhile
	// come from the one before.
	cPlacesMinRebuild = 15 * time.Second
	// cPlacesMaxAge rebuilds an index older than this at the next request
	// even when nothing said the library changed.
	cPlacesMaxAge = 10 * time.Minute
	// cPlacesIdle drops the index when the map hasn't asked for this long.
	cPlacesIdle = 15 * time.Minute
	// cPlacesMaskTTL is how long a filtered map's photos are kept, and
	// cPlacesMasks how many filters at most.
	cPlacesMaskTTL = time.Minute
	cPlacesMasks   = 4
)

// placeEntry is one photo of the index. x and y are its position on the
// Web Mercator square in 1/2^32ths of its side (mercator): a tile's and a
// cell's numbers at any zoom are their top bits.
type placeEntry struct {
	hash     [32]byte
	lat, lon float64
	x, y     uint32
	created  int64 // Unix seconds: the date SearchPhotos shows it at
	video    bool
}

// placesIndex is the library's photos with a position, by hash. gen is
// placesCache.gen read before the library was.
type placesIndex struct {
	gen     uint64
	built   time.Time
	entries []placeEntry
}

// placesMask is the index's photos a filtered search finds.
type placesMask struct {
	key  string
	made time.Time
	keep []bool
}

// placesCache holds the index (see the top of the file).
type placesCache struct {
	gen     atomic.Uint64
	buildMu sync.Mutex // one build in a request at a time
	mu      sync.Mutex
	idx     *placesIndex
	masks   []*placesMask
	// building: a rebuild runs in the background.
	building bool
	lastUsed time.Time
	idle     *time.Timer
}

// changed says the library changed: a path added, replaced or deleted, a
// folder kept out of Images or shown again, a position written. The next
// request rebuilds the index.
func (c *placesCache) changed() { c.gen.Add(1) }

// index is the index for a request at now: built in it the first time,
// and after a change still the one before while build makes the next in
// the background (at most once per cPlacesMinRebuild).
func (c *placesCache) index(now time.Time, build func(gen uint64) (*placesIndex, error)) (*placesIndex, error) {
	gen := c.gen.Load()
	c.mu.Lock()
	c.lastUsed = now
	if idx := c.idx; idx != nil {
		stale := idx.gen != gen || now.Sub(idx.built) > cPlacesMaxAge
		if stale && !c.building && now.Sub(idx.built) >= cPlacesMinRebuild {
			c.building = true
			go c.rebuild(build, gen)
		}
		c.mu.Unlock()
		return idx, nil
	}
	c.mu.Unlock()

	c.buildMu.Lock()
	defer c.buildMu.Unlock()
	c.mu.Lock()
	if idx := c.idx; idx != nil {
		c.mu.Unlock()
		return idx, nil
	}
	c.mu.Unlock()
	idx, err := build(gen)
	if err != nil {
		return nil, err
	}
	c.set(idx)
	return idx, nil
}

func (c *placesCache) rebuild(build func(gen uint64) (*placesIndex, error), gen uint64) {
	idx, err := build(gen)
	c.mu.Lock()
	c.building = false
	c.mu.Unlock()
	if err != nil {
		log.Error("places: could not read the photos' positions again, the map keeps the ones it had:", err)
		return
	}
	c.set(idx)
}

// set puts idx in place (the masks were of the one before) and arms the
// idle drop.
func (c *placesCache) set(idx *placesIndex) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.idx = idx
	c.masks = nil
	if c.idle == nil {
		c.idle = time.AfterFunc(cPlacesIdle, c.dropIfIdle)
	}
}

// dropIfIdle frees the index once the map hasn't asked for cPlacesIdle,
// and otherwise looks again when that much has passed since the last ask.
func (c *placesCache) dropIfIdle() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if left := cPlacesIdle - time.Since(c.lastUsed); left > 0 {
		c.idle = time.AfterFunc(left, c.dropIfIdle)
		return
	}
	c.idx, c.masks, c.idle = nil, nil, nil
}

// mask is the keep list of the filter key over idx, made by fill when no
// fresh one is kept.
func (c *placesCache) mask(idx *placesIndex, key string, now time.Time, fill func() ([]bool, error)) ([]bool, error) {
	c.mu.Lock()
	if c.idx == idx {
		for _, m := range c.masks {
			if m.key == key && now.Sub(m.made) < cPlacesMaskTTL {
				c.mu.Unlock()
				return m.keep, nil
			}
		}
	}
	c.mu.Unlock()
	keep, err := fill()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.idx == idx {
		c.masks = slices.DeleteFunc(c.masks, func(m *placesMask) bool { return m.key == key })
		c.masks = append(c.masks, &placesMask{key: key, made: now, keep: keep})
		if len(c.masks) > cPlacesMasks {
			c.masks = c.masks[1:]
		}
	}
	return keep, nil
}

// mercator is where lat, lon is on the Web Mercator square, in 1/2^32ths
// of its side from its top left: the bits below a tile's (or a cell's)
// number, so floor(x / 2^(32-z)) is the tile's x at zoom z, as
// PlaceTile's formula. Latitude is clamped to the square, and the
// antimeridian (180°) is in the last column.
func mercator(lat, lon float64) (x, y uint32) {
	lat = max(-cMercatorMaxLat, min(cMercatorMaxLat, lat))
	fy := (1 - math.Asinh(math.Tan(lat*math.Pi/180))/math.Pi) / 2
	return fixed32((lon + 180) / 360), fixed32(fy)
}

// fixed32 is f (0 to 1) in 1/2^32ths, clamped.
func fixed32(f float64) uint32 {
	if !(f > 0) {
		return 0
	}
	v := f * (1 << 32)
	if v >= 1<<32 {
		return math.MaxUint32
	}
	return uint32(v)
}

// placeTile is a tile asked for, as GetPhotoPlaces numbers it at zoom.
type placeTile struct{ x, y uint32 }

// placesTiles is the tiles asked for at zoom: x modulo 2^zoom, those with
// y outside the map left out, each once, at most cPlacesMaxTiles.
func placesTiles(zoom int, tiles []*pb.PlaceTile) []placeTile {
	n := int64(1) << zoom
	seen := map[placeTile]bool{}
	var out []placeTile
	for _, t := range tiles {
		if len(out) == cPlacesMaxTiles {
			break
		}
		y := int64(t.GetY())
		if y < 0 || y >= n {
			continue
		}
		x := ((int64(t.GetX()) % n) + n) % n
		pt := placeTile{uint32(x), uint32(y)}
		if !seen[pt] {
			seen[pt] = true
			out = append(out, pt)
		}
	}
	return out
}

// placeGroup is one cell's photos as the grouping adds them up.
type placeGroup struct {
	cx, cy                         uint32 // the cell, at zoom + cPlaceCellBits
	count                          int
	sumLat, sumLon                 float64
	minLat, maxLat, minLon, maxLon float64
	cover                          int // the newest photo, in entries
}

// placesGroups is the photos of entries that keep says (nil: all) in the
// cells of tiles at zoom, a group per cell holding any, and the totals of
// every photo kept: how many and the smallest area holding them all.
func placesGroups(entries []placeEntry, keep func(i int) bool, zoom int, tiles []placeTile) (groups []*placeGroup, total int, area *pb.PlaceArea) {
	asked := make(map[placeTile]bool, len(tiles))
	minTX, minTY, maxTX, maxTY := uint32(math.MaxUint32), uint32(math.MaxUint32), uint32(0), uint32(0)
	for _, t := range tiles {
		asked[t] = true
		minTX, maxTX = min(minTX, t.x), max(maxTX, t.x)
		minTY, maxTY = min(minTY, t.y), max(maxTY, t.y)
	}
	tileShift := uint(32 - zoom)
	cellShift := uint(32 - zoom - cPlaceCellBits)
	byCell := map[uint64]*placeGroup{}
	ext := newPlacesExtent()
	for i := range entries {
		if keep != nil && !keep(i) {
			continue
		}
		e := &entries[i]
		total++
		ext.add(e.lat, e.lon)
		if len(tiles) == 0 {
			continue
		}
		// At zoom 0 the shift is 32: every photo is in tile 0, 0.
		tx, ty := e.x>>tileShift, e.y>>tileShift
		if tx < minTX || tx > maxTX || ty < minTY || ty > maxTY || !asked[placeTile{tx, ty}] {
			continue
		}
		cx, cy := e.x>>cellShift, e.y>>cellShift
		key := uint64(cx)<<32 | uint64(cy)
		g := byCell[key]
		if g == nil {
			g = &placeGroup{cx: cx, cy: cy, minLat: e.lat, maxLat: e.lat, minLon: e.lon, maxLon: e.lon, cover: i}
			byCell[key] = g
			groups = append(groups, g)
		}
		g.count++
		g.sumLat += e.lat
		g.sumLon += e.lon
		g.minLat, g.maxLat = min(g.minLat, e.lat), max(g.maxLat, e.lat)
		g.minLon, g.maxLon = min(g.minLon, e.lon), max(g.maxLon, e.lon)
		if newerPlaceEntry(e, &entries[g.cover]) {
			g.cover = i
		}
	}
	return groups, total, ext.area()
}

// newerPlaceEntry is whether a comes before b in the photo search (newest
// first; at one date, the greater hash - any fixed order would do).
func newerPlaceEntry(a, b *placeEntry) bool {
	if a.created != b.created {
		return a.created > b.created
	}
	return bytes.Compare(a.hash[:], b.hash[:]) > 0
}

// placesExtent adds up the smallest area holding some positions: their
// least and greatest latitude, and their longitudes both as they are and
// moved to 0-360°, whichever spans less - so photos on both sides of the
// antimeridian (Fiji, the Bering Strait) make an area across it rather
// than one around the world.
type placesExtent struct {
	n                                          int
	minLat, maxLat, minLon, maxLon, minE, maxE float64
}

func newPlacesExtent() *placesExtent {
	return &placesExtent{minLat: math.Inf(1), maxLat: math.Inf(-1), minLon: math.Inf(1), maxLon: math.Inf(-1), minE: math.Inf(1), maxE: math.Inf(-1)}
}

func (x *placesExtent) add(lat, lon float64) {
	x.n++
	x.minLat, x.maxLat = min(x.minLat, lat), max(x.maxLat, lat)
	x.minLon, x.maxLon = min(x.minLon, lon), max(x.maxLon, lon)
	e := lon
	if e < 0 {
		e += 360
	}
	x.minE, x.maxE = min(x.minE, e), max(x.maxE, e)
}

// area is the extent, nil for none.
func (x *placesExtent) area() *pb.PlaceArea {
	if x.n == 0 {
		return nil
	}
	a := &pb.PlaceArea{South: x.minLat, North: x.maxLat, West: x.minLon, East: x.maxLon}
	if x.maxE-x.minE < x.maxLon-x.minLon {
		a.West, a.East = x.minE, x.maxE
		if a.West > 180 {
			a.West -= 360
		}
		if a.East > 180 {
			a.East -= 360
		}
	}
	return a
}

// buildPlacesIndex reads the index: the photos Images may show with a
// position (outside the folders kept out of Images), by hash. One whose
// hash isn't SHA-256 hex - none the device writes - is left out.
func (mg *Manager) buildPlacesIndex(gen uint64) (*placesIndex, error) {
	started := time.Now()
	excluded, err := mg.OutOfImagesFolders()
	if err != nil {
		return nil, err
	}
	var entries []placeEntry
	// The photo's place in entries while its copies come.
	at := map[[32]byte]int32{}
	odd := 0
	err = mg.dao.EachLocatedCopy(excluded, func(m dao.LocatedCopy) error {
		var h [32]byte
		if !decodeHash(&h, m.Hash) {
			odd++
			return nil
		}
		created := m.Created.Unix()
		if i, ok := at[h]; ok {
			// Another copy: the photo's date is its oldest's, and it is a
			// video only when every copy is one.
			e := &entries[i]
			e.created = min(e.created, created)
			e.video = e.video && m.Video
			return nil
		}
		e := placeEntry{hash: h, lat: m.Lat, lon: m.Lon, created: created, video: m.Video}
		e.x, e.y = mercator(m.Lat, m.Lon)
		at[h] = int32(len(entries))
		entries = append(entries, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(entries, func(a, b placeEntry) int { return bytes.Compare(a.hash[:], b.hash[:]) })
	if odd > 0 {
		log.Error("places: left out", odd, "photo(s) whose content hash is not SHA-256 hex")
	}
	log.Debug("places: read", len(entries), "photo positions in", time.Since(started))
	return &placesIndex{gen: gen, built: time.Now(), entries: entries}, nil
}

// placesMaskFor is the index's photos the search with these filters
// finds (SearchMedia's, the people's and tags' rules included).
func (mg *Manager) placesMaskFor(idx *placesIndex, tags, personIDs []string, groupID string, includeVideos bool, excluded []string) ([]bool, error) {
	keep := make([]bool, len(idx.entries))
	var h [32]byte
	err := mg.dao.EachMatchingMediaHash(tags, personIDs, groupID, !includeVideos, excluded, func(hash string) {
		if !decodeHash(&h, hash) {
			return
		}
		if i, ok := slices.BinarySearchFunc(idx.entries, h, func(e placeEntry, t [32]byte) int { return bytes.Compare(e.hash[:], t[:]) }); ok {
			keep[i] = true
		}
	})
	return keep, err
}

// decodeHash reads a content hash (SHA-256 in hex) into h.
func decodeHash(h *[32]byte, hash string) bool {
	if len(hash) != 2*len(h) {
		return false
	}
	_, err := hex.Decode(h[:], []byte(hash))
	return err == nil
}

// placesFilterKey names a filtered search for the masks kept.
func placesFilterKey(tags, personIDs []string, groupID string, includeVideos bool) string {
	key := strings.Join(tags, "\x00") + "\x01" + strings.Join(personIDs, "\x00") + "\x01" + groupID
	if includeVideos {
		key += "\x01videos"
	}
	return key
}

// PhotoPlaces answers GetPhotoPlaces: the places of tiles at zoom (see the
// proto) for the photos SearchPhotos would show with these filters.
func (mg *Manager) PhotoPlaces(zoom int32, tiles []*pb.PlaceTile, tags, personIDs []string, groupID string, includeVideos bool) (*pb.PhotoPlaces, error) {
	z := int(max(0, min(cPlacesMaxZoom, zoom)))
	asked := placesTiles(z, tiles)
	excluded, err := mg.OutOfImagesFolders()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	idx, err := mg.places.index(now, mg.buildPlacesIndex)
	if err != nil {
		return nil, err
	}
	var keep func(i int) bool
	if len(tags) > 0 || len(personIDs) > 0 || groupID != "" {
		mask, err := mg.places.mask(idx, placesFilterKey(tags, personIDs, groupID, includeVideos), now, func() ([]bool, error) {
			return mg.placesMaskFor(idx, tags, personIDs, groupID, includeVideos, excluded)
		})
		if err != nil {
			return nil, err
		}
		keep = func(i int) bool { return mask[i] }
	} else if !includeVideos {
		// SearchPhotos' own browsing: images only.
		keep = func(i int) bool { return !idx.entries[i].video }
	}
	groups, total, area := placesGroups(idx.entries, keep, z, asked)

	out := &pb.PhotoPlaces{LocatedTotal: int32(total), LocatedArea: area, Zoom: int32(z)}
	covers := make([]string, 0, len(groups))
	for _, g := range groups {
		covers = append(covers, hex.EncodeToString(idx.entries[g.cover].hash[:]))
	}
	shown, err := mg.dao.ShownCopies(covers, excluded)
	if err != nil {
		return nil, err
	}
	cellShift := uint(cPlaceCellBits)
	for i, g := range groups {
		p := &pb.PhotoPlace{
			Latitude:  g.sumLat / float64(g.count),
			Longitude: g.sumLon / float64(g.count),
			Count:     int32(g.count),
			Area:      &pb.PlaceArea{South: g.minLat, West: g.minLon, North: g.maxLat, East: g.maxLon},
			Zoom:      int32(z),
			X:         int32(g.cx >> cellShift),
			Y:         int32(g.cy >> cellShift),
		}
		if f, ok := shown[covers[i]]; ok {
			p.Cover = f
		}
		out.Places = append(out.Places, p)
	}
	return out, nil
}
