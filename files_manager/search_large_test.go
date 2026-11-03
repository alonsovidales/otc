// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"database/sql"
	"fmt"
	"math"
	"math/rand"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
)

// Issue #206's other searches on a large library (OTC_TEST_BROWSE_LARGE=1,
// as TestImageSearchKeysetLargeMySQL): fillLibrary's 200k photos with
// tags on most and a few on fewer, a person in a tenth of them and others
// in fewer, a big collection imported years down and small ones, a home
// where most were taken and trips, a rated shoot, and every photo
// described for Smart search. Each search's first page as a whole search
// served it and as it is read now: the time, what the search allocated
// and what its token keeps; then its first pages both ways, which must be
// the same - thumbnails, copies and all - at two page sizes.

// fillSearchTables gives fillLibrary's n photos what the searches filter
// by, in temporary tables on db's one connection. Scores come in steps of
// 1/256, so photos tie.
func fillSearchTables(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	for _, q := range []string{
		"create temporary table `file_tags` (`hash` varchar(64) not null, `tag` varchar(150) not null, `score` float not null, key (`hash`), key (`tag`), unique key (`hash`, `tag`)) engine=InnoDB",
		"create temporary table `faces` (`id` varchar(36) not null, `hash` varchar(64) not null, `person_id` varchar(36) not null, `bbox_x` int not null, `bbox_y` int not null, `bbox_w` int not null, `bbox_h` int not null, `embedding` blob not null, `thumbnail` mediumblob not null, `created` datetime not null, primary key (`id`), key (`hash`), key (`person_id`)) engine=InnoDB",
		"create temporary table `image_groups` (`id` varchar(36) not null, `name` varchar(150) not null, `created` datetime not null, `rating` tinyint(1) not null default 0, primary key (`id`)) engine=InnoDB",
		"create temporary table `image_group_files` (`group_id` varchar(36) not null, `hash` varchar(64) not null, `added` datetime not null, primary key (`group_id`, `hash`), key (`hash`)) engine=InnoDB",
		"create temporary table `photo_locations` (`hash` varchar(64) not null, `lat` double null, `lon` double null, primary key (`hash`)) engine=InnoDB",
		"create temporary table `photo_ratings` (`group_id` varchar(36) not null, `hash` varchar(64) not null, `stars` tinyint not null default 0, `comment` blob null, `shared_comment` blob null, `link` varchar(64) null, `updated` datetime not null, primary key (`group_id`, `hash`), key (`hash`)) engine=InnoDB",
		"insert into `image_groups` values ('favourites', 'Favourites', now(), 0), ('trip', 'Imported', now(), 0), ('shoot', 'Shoot', now(), 1)",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	insert := func(table string, cols int) (add func(vals ...any), flush func()) {
		var args []any
		rows := 0
		flush = func() {
			if rows == 0 {
				return
			}
			ph := "(" + strings.TrimSuffix(strings.Repeat("?, ", cols), ", ") + ")"
			if _, err := db.Exec("insert into `"+table+"` values "+strings.TrimSuffix(strings.Repeat(ph+", ", rows), ", "), args...); err != nil {
				t.Fatal(err)
			}
			args, rows = args[:0], 0
		}
		add = func(vals ...any) {
			args = append(args, vals...)
			if rows++; rows == 1000 {
				flush()
			}
		}
		return add, flush
	}
	hash := func(i int) string { return fmt.Sprintf("%064x", 0x2060000000+i) }
	score := func(i, salt int) float64 { return 0.25 + float64((i*7919+salt*104729)%192)/256 }
	tag, tags := insert("file_tags", 3)
	face, faces := insert("faces", 10)
	member, members := insert("image_group_files", 3)
	rating, ratings := insert("photo_ratings", 7)
	place, places := insert("photo_locations", 3)
	now := time.Now()
	for i := 0; i < n; i++ {
		h := hash(i)
		if i%5 < 2 {
			tag(h, "outdoor", score(i, 1))
		}
		if i%20 == 1 {
			tag(h, "beach", score(i, 3))
		}
		if i%33 == 2 {
			tag(h, "dog", score(i, 4))
		}
		addFace := func(person string) { face(fmt.Sprintf("face-%d-%s", i, person), h, person, 0, 0, 1, 1, "", "", now) }
		if i%10 == 1 {
			addFace("big")
		}
		if i%100 == 7 || i%100 == 14 {
			addFace("mid")
		}
		if i%1000 == 24 {
			addFace("small")
		}
		if i%200 == 9 {
			member("favourites", h, now)
		}
		if i >= n/3 && i < n/3+n/10 {
			member("trip", h, now)
		}
		if i >= n/2 && i < n/2+n/100 {
			member("shoot", h, now)
			if i%5 != 0 {
				var comment any
				if i%10 == 3 {
					comment = []byte("sealed")
				}
				rating("shoot", h, (i*31)%6, comment, nil, nil, now)
			}
		}
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
	for _, f := range []func(){tags, faces, members, ratings, places} {
		f()
	}
	for _, table := range []string{"file_tags", "faces", "image_group_files", "photo_locations", "photo_ratings"} {
		if _, err := db.Exec("analyze table `" + table + "`"); err != nil {
			t.Fatal(err)
		}
	}
}

// describeAll puts every photo of fillLibrary's n in mg's Smart search
// index, a random vector each (32 numbers: the scan is what it is, the
// memory a test's), and turns Smart search on with a model answering q.
func describeAll(mg *Manager, n int, q []float32, seed int64) {
	r := rand.New(rand.NewSource(seed))
	mg.smart.loaded.Store(true)
	mg.smart.on.Store(true)
	mg.setEmbedder(&fakeEmbedder{text: q})
	x := &mg.smart.index
	x.mu.Lock()
	defer x.mu.Unlock()
	x.loaded, x.pos = true, make(map[string]int, n)
	for i := 0; i < n; i++ {
		v := make([]float32, len(q))
		var norm float64
		for k := range v {
			v[k] = float32(r.NormFloat64())
			norm += float64(v[k]) * float64(v[k])
		}
		for k := range v {
			v[k] /= float32(math.Sqrt(norm))
		}
		scale, packed, _ := unpackVec(packVec(v))
		x.addLocked(fmt.Sprintf("%064x", 0x2060000000+i), scale, packed)
	}
}

type largeSearch struct {
	what   string
	path   string
	tags   []string
	people []string
	group  string
	area   *dao.MediaArea
	rating *dao.RatingQuery
	text   string
}

func (s largeSearch) page(mg *Manager, ses *session.Session, token string, have, limit int32) ([]*pb.File, string, error) {
	return mg.ImageSearchRated(s.rating, s.text, s.area, ses, s.path, s.tags, token, false, s.people, s.group, nil, nil, have, limit, false, false)
}

func TestImageSearchesLargeMySQL(t *testing.T) {
	if os.Getenv("OTC_TEST_BROWSE_LARGE") != "1" {
		t.Skip("OTC_TEST_BROWSE_LARGE not set")
	}
	dsn := os.Getenv("OTC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("OTC_TEST_MYSQL_DSN not set")
	}
	_, ses := galleryTestEnv(t)
	withDefaultPage(t, 30)
	db := mysqlFilesTable(t, dsn)
	const n = 200000
	fillLibrary(t, db, n)
	fillSearchTables(t, db, n)
	d := dao.NewWithDB(db)
	excluded := []string{"/Private/"}
	q := make([]float32, 32)
	q[0] = 1
	manager := func(keyset bool) *Manager {
		mg := keptOut(&Manager{dao: d, searchTokens: newSearchTokenCache(cSearchTokensMaxRows)}, excluded...)
		if keyset {
			mg.browse = d.BrowseMedia
		}
		describeAll(mg, n, q, 206)
		return mg
	}
	whole, keyset := manager(false), manager(true)
	t.Cleanup(whole.smart.index.drop)
	t.Cleanup(keyset.smart.index.drop)
	home := &dao.MediaArea{South: 40, West: -4, North: 41, East: -3}
	searches := []largeSearch{
		{what: "tag outdoor (40%)", tags: []string{"outdoor"}},
		{what: "tag dog (3%)", tags: []string{"dog"}},
		{what: "tags beach+dog", tags: []string{"beach", "dog"}},
		{what: "person big (10%)", people: []string{"big"}},
		{what: "person mid (2%)", people: []string{"mid"}},
		{what: "person small (0.1%)", people: []string{"small"}},
		{what: "people big+mid", people: []string{"big", "mid"}},
		{what: "collection imported (10%)", group: "trip"},
		{what: "collection favourites (0.5%)", group: "favourites"},
		{what: "area home (45%)", area: home},
		{what: "area trip (5%)", area: &dao.MediaArea{South: 48.8, West: 2.3, North: 48.9, East: 2.4}},
		{what: "area block (0.1%)", area: &dao.MediaArea{South: 48.80, West: 2.30, North: 48.801, East: 2.301}},
		{what: "area world (60%)", area: &dao.MediaArea{South: -90, West: -180, North: 90, East: 180}},
		{what: "rating 3+ stars", group: "shoot", rating: &dao.RatingQuery{MinStars: 3}},
		{what: "rating by stars", group: "shoot", rating: &dao.RatingQuery{ByRating: true}},
		{what: "text", text: "a photo"},
		{what: "text + person mid", text: "a photo", people: []string{"mid"}},
		{what: "text + tag outdoor", text: "a photo", tags: []string{"outdoor"}},
		{what: "person big + tag outdoor", tags: []string{"outdoor"}, people: []string{"big"}},
		{what: "area home + person big", area: home, people: []string{"big"}},
		{what: "folder /Backup/", path: "/Backup/"},
	}
	for _, s := range searches {
		// The photos the first pages show need their thumbnails (#147).
		shown, err := d.SearchMediaRated(s.area, s.path, s.tags, s.people, s.group, true, nil, excluded, s.rating)
		if err != nil {
			t.Fatal(err)
		}
		if s.text != "" {
			if shown, err = whole.rankByText(ses, s.text, shown); err != nil {
				t.Fatal(err)
			}
		}
		for _, f := range shown[:min(len(shown), 12+4*30+60)] {
			if err := blobstore.WriteBytes(thumbnailPath(f.Hash), ses, []byte("thumb")); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { removeThumbnails(blobPath(f.Hash)) })
		}
		line := fmt.Sprintf("%-28s %6d photos:", s.what, len(shown))
		for _, kind := range []struct {
			name string
			mg   *Manager
		}{{"before", whole}, {"now", keyset}} {
			var took []time.Duration
			var allocated, kept []uint64
			for run := 0; run < 5; run++ {
				kind.mg.searchTokens = newSearchTokenCache(cSearchTokensMaxRows)
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				t0 := time.Now()
				files, _, err := s.page(kind.mg, ses, "", 0, 12)
				took = append(took, time.Since(t0))
				if err != nil || len(files) != min(12, len(shown)) {
					t.Fatalf("%s, %s: %d photos, %v", s.what, kind.name, len(files), err)
				}
				runtime.ReadMemStats(&after)
				allocated = append(allocated, after.TotalAlloc-before.TotalAlloc)
				runtime.GC()
				runtime.ReadMemStats(&after)
				kept = append(kept, after.HeapAlloc-min(after.HeapAlloc, before.HeapAlloc))
			}
			slices.Sort(took)
			slices.Sort(allocated)
			slices.Sort(kept)
			line += fmt.Sprintf("  %s %8v %6.2f MB alloc %6.2f MB kept", kind.name, took[2].Round(100*time.Microsecond),
				float64(allocated[2])/1e6, float64(kept[2])/1e6)
		}
		t.Log(line)
		for _, limits := range [][]int32{{12, 0, 0, 0, 0}, {7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7}} {
			walk := func(mg *Manager) []string {
				mg.searchTokens = newSearchTokenCache(cSearchTokensMaxRows)
				var got []string
				token, have := "", int32(0)
				for page, limit := range limits {
					if page > 0 && token == "" {
						break
					}
					files, next, err := s.page(mg, ses, token, have, limit)
					if err != nil {
						t.Fatal(err)
					}
					for _, f := range files {
						got = append(got, fmt.Sprintf("%s %s %s×%d %q", f.Path, f.Hash, f.Created.AsTime().Format(time.DateTime), f.GetCopies(), f.Content))
					}
					have += int32(len(files))
					token = next
				}
				return got
			}
			want, got := walk(whole), walk(keyset)
			if len(want) == 0 && len(shown) > 0 {
				t.Fatalf("%s: the whole search's pages are empty", s.what)
			}
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				for i := range min(len(got), len(want)) {
					if got[i] != want[i] {
						t.Fatalf("%s, pages %v: photo %d now %s, before %s", s.what, limits, i, got[i], want[i])
					}
				}
				t.Fatalf("%s, pages %v: %d photos now, %d before", s.what, limits, len(got), len(want))
			}
		}
	}
}
