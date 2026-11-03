// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Issue #206: Images' plain browse served by keyset. Every newSearchTest
// case runs again with its pages served by keyset (keysetScenarios): over
// an in-memory BrowseMedia (fakeBrowse, always) and over the real one on
// MySQL/MariaDB (OTC_TEST_MYSQL_DSN) - the same pages, served again,
// #147, keep-out, retries, jump and deletes, in every kind.

// scenarioKeyset is how newKindSearchTest serves its case: as every
// search before #206 (a mocked query answering the whole library), or by
// keyset over fakeBrowse or MySQL.
var scenarioKeyset = keysetOff

const (
	keysetOff = iota
	keysetFake
	keysetMySQL
)

// cKeysetBase moves a keyset run's hashes off the others' (newKindSearchTest):
// the runs share the storage folder, and a page that omits its thumbnails
// sees one a run before it left.
const cKeysetBase = 0x100000

// keysetLibrary is newKindSearchTest's case served by keyset. Going up,
// the rows the mocked query answers (dated so the photos above the cutoff,
// oldest first, are the case's paths in order, and two that no page may
// show); going down, the case's paths a minute apart, newest first, with
// a document and a video among them that no page may show either (the
// pages ask for images only).
func (st *searchTest) keysetLibrary(k searchKind, db *sql.DB, mock sqlmock.Sqlmock) {
	t := st.t
	t.Helper()
	// The same case runs again with other chunks: its thumbnails go with
	// it (TestSearchPageServedAgainAfterASkippedRow writes one late).
	t.Cleanup(func() {
		for i := range st.paths {
			removeThumbnails(blobPath(st.hash(i)))
		}
	})
	row := func(hash, mime, path string, at time.Time) *pb.File {
		f := &pb.File{Hash: hash, Mime: mime, Path: path, Created: timestamppb.New(at), Modified: timestamppb.New(at)}
		dao.SetFileSize(f, 1)
		return f
	}
	var rows []*pb.File
	if k.up {
		cutoff := cUpCutoff
		st.newerThan = &cutoff
		monthEnd := cutoff.Truncate(time.Second)
		synced := fmt.Sprintf("%064x", 0xbe10+st.base)
		rows = append(rows, row(synced, "image/jpeg", "/Download/old.jpg", monthEnd.Add(1000*time.Hour)))
		for i, p := range st.paths {
			rows = append(rows, row(st.hash(i), "image/jpeg", p, monthEnd.Add(time.Duration(i+1)*time.Hour)))
		}
		rows = append(rows, row(fmt.Sprintf("%064x", 0xbe00+st.base), "image/jpeg", "/p/month-end.jpg", monthEnd))
		rows = append(rows, row(synced, "image/jpeg", "/Phone/old.jpg", monthEnd.Add(-time.Hour)))
	} else {
		top := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
		for i, p := range st.paths {
			rows = append(rows, row(st.hash(i), "image/jpeg", p, top.Add(-time.Duration(i)*time.Minute)))
		}
		rows = append(rows,
			row(fmt.Sprintf("%064x", 0xbd00+st.base), "text/plain", "/p/notes.txt", top.Add(-90*time.Second)),
			row(fmt.Sprintf("%064x", 0xbd10+st.base), "video/mp4", "/p/clip.mp4", top.Add(-150*time.Second)))
	}
	mg := &Manager{dao: dao.NewWithDB(db), searchTokens: newSearchTokenCache(1000), browse: fakeBrowse(rows)}
	st.mock = mock
	mock.MatchExpectationsInOrder(false)
	if scenarioKeyset == keysetMySQL {
		mdb := mysqlFilesTable(t, os.Getenv("OTC_TEST_MYSQL_DSN"))
		for _, f := range rows {
			if _, err := mdb.Exec("insert into `files` (`hash`, `mime`, `created`, `modified`, `path`, `size`) values (?, ?, ?, ?, ?, 1)",
				f.Hash, f.Mime, f.Created.AsTime(), f.Modified.AsTime(), f.Path); err != nil {
				t.Fatal(err)
			}
		}
		d := dao.NewWithDB(mdb)
		mg.dao, mg.browse = d, d.BrowseMedia
		st.mock = nil // the copies are counted there
		st.filterLibrary(mg, mdb, rows)
	}
	st.mg = keptOut(mg)
}

// scenarioFilter is the filter keysetScenarios asks the newSearchTest
// cases with over MySQL/MariaDB (issue #206: every search read as it
// scrolls): none (the plain browse), or one every photo of the case has -
// the dates, a score, the stars and the cosines set so that its pages are
// the case's paths in order - and the document and video don't.
var scenarioFilter = filterNone

const (
	filterNone = iota
	filterTag
	filterPerson
	filterCollection
	filterArea
	filterByStars
	filterText
	filterCount
)

var filterNames = []string{"plain", "tag", "person", "collection", "area", "by stars", "text"}

// filterRanked says whether scenarioFilter's search is ranked, which
// never goes up (ImageSearchRated drops newer_than for it).
func filterRanked() bool { return scenarioFilter == filterByStars || scenarioFilter == filterText }

// filterLibrary gives the case's photos (every image row: the synced and
// month-end photos going up too) scenarioFilter's filter, and st the
// search for it. Photo i's tag score and stars go down in steps every few
// photos (ties a chunk keeps whole) and its cosine down with i, so a
// ranked search's order is the case's.
func (st *searchTest) filterLibrary(mg *Manager, db *sql.DB, rows []*pb.File) {
	t := st.t
	t.Helper()
	if scenarioFilter == filterNone {
		return
	}
	for _, q := range []string{
		"create temporary table `file_tags` (`hash` varchar(64) not null, `tag` varchar(150) not null, `score` float not null, key (`hash`), key (`tag`), unique key (`hash`, `tag`)) engine=InnoDB",
		"create temporary table `faces` (`id` varchar(36) not null, `hash` varchar(64) not null, `person_id` varchar(36) not null, `bbox_x` int not null, `bbox_y` int not null, `bbox_w` int not null, `bbox_h` int not null, `embedding` blob not null, `thumbnail` mediumblob not null, `created` datetime not null, primary key (`id`), key (`hash`), key (`person_id`)) engine=InnoDB",
		"create temporary table `image_groups` (`id` varchar(36) not null, `name` varchar(150) not null, `created` datetime not null, `rating` tinyint(1) not null default 0, primary key (`id`)) engine=InnoDB",
		"create temporary table `image_group_files` (`group_id` varchar(36) not null, `hash` varchar(64) not null, `added` datetime not null, primary key (`group_id`, `hash`), key (`hash`)) engine=InnoDB",
		"create temporary table `photo_locations` (`hash` varchar(64) not null, `lat` double null, `lon` double null, primary key (`hash`)) engine=InnoDB",
		"create temporary table `photo_ratings` (`group_id` varchar(36) not null, `hash` varchar(64) not null, `stars` tinyint not null default 0, `comment` blob null, `shared_comment` blob null, `link` varchar(64) null, `updated` datetime not null, primary key (`group_id`, `hash`), key (`hash`)) engine=InnoDB",
		"insert into `image_groups` values ('g', 'Trip', now(), 0), ('gr', 'Shoot', now(), 1)",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	index := map[string]int{}
	for i := range st.paths {
		index[st.hash(i)] = i
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	// A photo without the filter, with its thumbnail, between the case's
	// first two: any page that shows it didn't filter.
	if len(st.paths) > 1 {
		at := rows[0].Created.AsTime().Add(-30 * time.Second)
		if len(rows) > 2 && st.newerThan != nil {
			at = rows[1].Created.AsTime().Add(30 * time.Minute)
		}
		hash := fmt.Sprintf("%064x", 0xbf00+st.base)
		exec("insert into `files` (`hash`, `mime`, `created`, `modified`, `path`, `size`) values (?, 'image/jpeg', ?, ?, '/p/unfiltered.jpg', 1)", hash, at, at)
		if err := blobstore.WriteBytes(thumbnailPath(hash), st.ses, []byte("unfiltered")); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { removeThumbnails(blobPath(hash)) })
	}
	vectors := map[string][]float32{}
	seen := map[string]bool{}
	for _, f := range rows {
		if f.Mime != "image/jpeg" || seen[f.Hash] {
			continue
		}
		seen[f.Hash] = true
		i, ok := index[f.Hash]
		if !ok {
			i = len(st.paths) // the up kind's photos no page shows
		}
		switch scenarioFilter {
		case filterTag:
			exec("insert into `file_tags` values (?, 't', ?)", f.Hash, 1-float64(i/3)/16)
			exec("insert into `file_tags` values (?, 'other', 0.5)", f.Hash)
		case filterPerson:
			exec("insert into `faces` values (?, ?, 'p', 0, 0, 1, 1, '', '', now())", "a"+f.Hash[34:], f.Hash)
			if i%2 == 0 {
				exec("insert into `faces` values (?, ?, 'p', 0, 0, 1, 1, '', '', now())", "b"+f.Hash[34:], f.Hash) // twice in it
			}
		case filterCollection:
			exec("insert into `image_group_files` values ('g', ?, now())", f.Hash)
		case filterArea:
			exec("insert into `photo_locations` values (?, ?, ?)", f.Hash, 40.4+float64(i)/1000, -3.7)
		case filterByStars:
			exec("insert into `image_group_files` values ('gr', ?, now())", f.Hash)
			if stars := 5 - i/8; stars > 0 {
				exec("insert into `photo_ratings` (`group_id`, `hash`, `stars`, `updated`) values ('gr', ?, ?, now())", f.Hash, stars)
			}
		case filterText:
			// A cosine of 1 - i/250 to the query [1, 0, 0]: all over the cut.
			a := 1 - float64(i)/250
			vectors[f.Hash] = []float32{float32(a), float32(math.Sqrt(1 - a*a)), 0}
		}
	}
	switch scenarioFilter {
	case filterTag:
		st.tags = []string{"t"}
	case filterPerson:
		st.people = []string{"p"}
	case filterCollection:
		st.group = "g"
	case filterArea:
		st.area = &dao.MediaArea{South: 40, West: -4, North: 41, East: -3}
	case filterByStars:
		st.group, st.rating = "gr", &dao.RatingQuery{ByRating: true}
	case filterText:
		st.text = "a photo"
		mg.smart.loaded.Store(true)
		mg.smart.on.Store(true)
		mg.setEmbedder(&fakeEmbedder{text: []float32{1, 0, 0}})
		x := &mg.smart.index
		x.mu.Lock()
		x.loaded, x.pos = true, map[string]int{}
		for h, v := range vectors {
			scale, q, err := unpackVec(packVec(v))
			if err != nil {
				t.Fatal(err)
			}
			x.addLocked(h, scale, q)
		}
		x.mu.Unlock()
		t.Cleanup(x.drop)
	}
}

// fakeBrowse is dao.BrowseMedia over rows, in memory, for the tests that
// run without a database (dao's MySQL tests check the real one against
// SearchMedia): each photo once at its oldest visible copy, newest first
// (oldest first going up), at least want and a whole second more. Rows'
// dates are whole seconds, as a DATETIME's.
func fakeBrowse(rows []*pb.File) browseFunc {
	return func(b *dao.MediaBrowse, from *time.Time, want int) ([]*pb.File, *time.Time, bool, error) {
		visible := func(f *pb.File) bool {
			if underFolders(f.Path, false, b.Excluded) {
				return false
			}
			return strings.HasPrefix(f.Mime, "image") || (!b.ImagesOnly && strings.HasPrefix(f.Mime, "video"))
		}
		older := func(a, c *pb.File) bool {
			if x := a.Created.AsTime().Compare(c.Created.AsTime()); x != 0 {
				return x < 0
			}
			return a.Path < c.Path
		}
		oldest := map[string]*pb.File{}
		for _, f := range rows {
			if o := oldest[f.Hash]; visible(f) && (o == nil || older(f, o)) {
				oldest[f.Hash] = f
			}
		}
		up := b.NewerThan != nil
		var picked []*pb.File
		for _, f := range oldest {
			at := f.Created.AsTime()
			switch {
			case up && from == nil && !at.After(b.NewerThan.Truncate(time.Second)):
			case up && from != nil && !at.After(*from):
			case !up && b.Before != nil && at.After(b.Before.Truncate(time.Second)):
			case !up && from != nil && !at.Before(*from):
			default:
				picked = append(picked, f)
			}
		}
		slices.SortFunc(picked, func(a, c *pb.File) int {
			if older(a, c) == up {
				return -1
			}
			return 1
		})
		var out []*pb.File
		for _, f := range picked {
			out = append(out, proto.Clone(f).(*pb.File))
		}
		if len(out) <= want {
			return out, nil, true, nil
		}
		edge := out[want].Created.AsTime()
		n := want + 1
		for n < len(out) && out[n].Created.AsTime().Equal(edge) {
			n++
		}
		return out[:n], &edge, false, nil
	}
}

// The newSearchTest cases (and the one of copies_test.go that needs the
// mock) served by keyset. TestSearchPageServedAgainAfterTheTokenIsCompacted
// isn't among them: it checks what a whole search's token holds (the rows
// from its last page to the end); a keyset token holds less, which
// TestKeysetTokenHoldsLittle checks.
var keysetCases = []struct {
	name string
	run  func(*testing.T)
	mock bool // it adds expectations to the mocked database
}{
	{"LostIsServedAgain", TestSearchPageLostIsServedAgain, false},
	{"GoOnWithTheHaveTheyLeft", TestSearchPagesGoOnWithTheHaveTheyLeft, false},
	{"WithoutHaveGoOnAsBefore", TestSearchPagesWithoutHaveGoOnAsBefore, false},
	{"WithAnUnplacedHaveGoOn", TestSearchPagesWithAnUnplacedHaveGoOn, false},
	{"AskedTwiceAtOnce", TestSearchPageAskedTwiceAtOnce, false},
	{"ServedAgainAfterASkippedRow", TestSearchPageServedAgainAfterASkippedRow, false},
	{"ServedAgainAfterAFolderIsKeptOut", TestSearchPageServedAgainAfterAFolderIsKeptOut, false},
	{"ServedAgainOnlySoManyTimes", TestSearchPageServedAgainOnlySoManyTimes, false},
	{"ResumedFromAnUnknownToken", TestSearchResumedFromAnUnknownToken, false},
	{"JumpLeavesItsTokenAlone", TestSearchJumpLeavesItsTokenAlone, false},
	{"RetryOvertakingKeepsItsPlace", TestSearchPageRetryOvertakingTheFirstRequestKeepsItsPlace, false},
	{"RetryOvertakingRepeatsNothing", TestSearchPageRetryOvertakingTheFirstRequestRepeatsNothing, false},
	{"NotServedAgainAfterADelete", TestSearchPageNotServedAgainAfterADelete, false},
	{"ServedWithoutItsCopies", TestImageSearchPageServedWithoutItsCopies, true},
}

// keysetScenarios runs keysetCases served as mode says, fetching what each
// page needs and no more (browseChunk 1: a chunk ends inside nearly every
// page) and a chunk ahead of the largest page; over MySQL, for the plain
// browse and every filter of scenarioFilter, and the date-ordered ones both
// by keyset and through their list (cListMax 0 and past the case).
func keysetScenarios(t *testing.T, mode int) {
	filters := []int{filterNone}
	if mode == keysetMySQL {
		filters = nil
		for f := filterNone; f < filterCount; f++ {
			filters = append(filters, f)
		}
	}
	for _, filter := range filters {
		listMaxes := []int{cListMax}
		if mode == keysetMySQL && filter != filterNone && filter != filterTag && filter != filterByStars && filter != filterText {
			listMaxes = []int{0, 1 << 20}
		}
		for _, listMax := range listMaxes {
			for _, chunk := range []int{1, browseChunk} {
				name := fmt.Sprintf("%s, chunk %d", filterNames[filter], chunk)
				if len(listMaxes) > 1 {
					name += fmt.Sprintf(", list max %d", listMax)
				}
				t.Run(name, func(t *testing.T) {
					origMode, origChunk, origFilter, origMax := scenarioKeyset, browseChunk, scenarioFilter, cListMax
					scenarioKeyset, browseChunk, scenarioFilter, cListMax = mode, chunk, filter, listMax
					t.Cleanup(func() {
						scenarioKeyset, browseChunk, scenarioFilter, cListMax = origMode, origChunk, origFilter, origMax
					})
					for _, c := range keysetCases {
						if c.mock && mode == keysetMySQL {
							continue
						}
						t.Run(c.name, c.run)
					}
				})
			}
		}
	}
}

func TestSearchScenariosKeyset(t *testing.T) { keysetScenarios(t, keysetFake) }

func TestSearchScenariosKeysetMySQL(t *testing.T) {
	if os.Getenv("OTC_TEST_MYSQL_DSN") == "" {
		t.Skip("OTC_TEST_MYSQL_DSN not set")
	}
	keysetScenarios(t, keysetMySQL)
}

// A keyset token holds the pages served from its last one on and what it
// fetched past them - never the library: walking 60 photos three at a
// time, it never holds more than a few pages' rows.
func TestKeysetTokenHoldsLittle(t *testing.T) {
	for _, chunk := range []int{1, 8} {
		t.Run(fmt.Sprint("chunk ", chunk), func(t *testing.T) {
			orig := scenarioKeyset
			scenarioKeyset = keysetFake
			t.Cleanup(func() { scenarioKeyset = orig })
			origChunk := browseChunk
			browseChunk = chunk
			t.Cleanup(func() { browseChunk = origChunk })
			st := newSearchTest(t, true, 0x2600, 3, photoPaths(60))
			got, tok := st.page("", 0)
			st.expect("first page", got, 0, 1, 2)
			for have := int32(3); have < 57; have += 3 {
				got, next := st.page(tok, have)
				h := int(have)
				st.expect(fmt.Sprint("the page at ", have), got, h, h+1, h+2)
				if next != tok {
					t.Fatalf("the token changed at %d: %q", have, next)
				}
				cur, _ := st.mg.searchTokens.load(tok)
				if len(cur.all) > 2*(3+chunk+1) {
					t.Fatalf("at %d the token holds %d rows", have, len(cur.all))
				}
			}
			st.last(tok, 57, 57, 58, 59)
		})
	}
}

// A page that needs more of the library fails when it can't be read (the
// client asks again with the same token and have, and gets it); the look
// past a full page that fails doesn't fail it: it comes with a token, and
// the next page reads what follows.
func TestKeysetFetchThatFails(t *testing.T) {
	orig := scenarioKeyset
	scenarioKeyset = keysetFake
	t.Cleanup(func() { scenarioKeyset = orig })
	origChunk := browseChunk
	browseChunk = 1
	t.Cleanup(func() { browseChunk = origChunk })

	st := newSearchTest(t, false, 0x2700, 3, photoPaths(9))
	var fail atomic.Bool
	works := st.mg.browse
	st.mg.browse = func(b *dao.MediaBrowse, from *time.Time, want int) ([]*pb.File, *time.Time, bool, error) {
		if fail.Load() {
			return nil, nil, false, errors.New("connection lost")
		}
		return works(b, from, want)
	}
	got, tok := st.page("", 0)
	st.expect("first page", got, 0, 1, 2)
	fail.Store(true)
	if _, _, err := st.search(tok, 3); err == nil {
		t.Fatal("a page that couldn't be read was served")
	}
	fail.Store(false)
	got, _ = st.page(tok, 3) // asked again
	st.expect("the page asked again", got, 3, 4, 5)
	st.last(tok, 6, 6, 7, 8)

	// Chunks that end where the page does, so a full page has to look past
	// itself; the first look fails.
	st = newSearchTest(t, false, 0x2710, 3, photoPaths(6))
	works = st.mg.browse
	var calls atomic.Int32
	st.mg.browse = func(b *dao.MediaBrowse, from *time.Time, want int) ([]*pb.File, *time.Time, bool, error) {
		if calls.Add(1) == 2 {
			return nil, nil, false, errors.New("connection lost")
		}
		files, next, done, err := works(b, from, want)
		if !done && len(files) > want && want > 0 {
			files = files[:want]
			at := files[want-1].Created.AsTime()
			next = &at
		}
		return files, next, done, err
	}
	got, tok = st.page("", 0)
	st.expect("a page whose look past it failed", got, 0, 1, 2)
	if tok == "" {
		t.Fatal("no token: the page was taken for the last one")
	}
	st.last(tok, 3, 3, 4, 5)
}

// Every search of a Manager with browse (the device's own, Init) is read
// as it scrolls; one without (the mocked databases) reads it whole, as
// before. Which way each search is read is in searchFor (search_lists_test.go).
func TestSearchesReadAsTheyScrollOnlyWithBrowse(t *testing.T) {
	mg := &Manager{browse: func(*dao.MediaBrowse, *time.Time, int) ([]*pb.File, *time.Time, bool, error) {
		return nil, nil, true, nil
	}}
	if !mg.readsAsItScrolls() {
		t.Error("a Manager with browse reads its searches whole")
	}
	if (&Manager{}).readsAsItScrolls() {
		t.Error("a Manager without browse read as it scrolls")
	}
}

// Issue #194's split, served by keyset: the jump's pages and the ones up
// from it are the plain browse's, each photo once at the same copy - over
// fakeBrowse (every photo counts its own path: the mock has no rows).
func TestImageSearchNewerAndJumpSplitTheLibraryKeyset(t *testing.T) {
	bothPageKinds(t, func(t *testing.T, omit bool) {
		_, ses := galleryTestEnv(t)
		withDefaultPage(t, 2)
		base := 0x3500
		if omit {
			base += cOmittedBase
		}
		lib := newerLibrary()
		writeThumbs(t, ses, base, lib)
		var rows []*pb.File
		for _, p := range lib {
			rows = append(rows, &pb.File{Hash: fmt.Sprintf("%064x", base+p.n), Mime: "image/jpeg", Path: p.path,
				Created: timestamppb.New(p.at), Modified: timestamppb.New(p.at)})
		}
		for _, chunk := range []int{1, browseChunk} {
			orig := browseChunk
			browseChunk = chunk
			db, mock, _ := sqlmock.New()
			mock.MatchExpectationsInOrder(false)
			for i := 0; i < 200; i++ {
				mock.ExpectQuery(reCopies).WillReturnRows(copiesRows())
			}
			mg := keptOut(&Manager{dao: dao.NewWithDB(db), searchTokens: newSearchTokenCache(1000), browse: fakeBrowse(rows)})
			for _, cutoff := range splitCutoffs() {
				splitPages(t, mg, ses, cutoff, omit)
			}
			db.Close()
			browseChunk = orig
		}
	})
}

// As above against a real MySQL/MariaDB, whose pages must be the ones a
// whole search gives there (TestImageSearchNewerAndJumpSplitTheLibraryMySQL),
// copies counted and all.
func TestImageSearchNewerAndJumpSplitTheLibraryKeysetMySQL(t *testing.T) {
	dsn := os.Getenv("OTC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("OTC_TEST_MYSQL_DSN not set")
	}
	bothPageKinds(t, func(t *testing.T, omit bool) {
		_, ses := galleryTestEnv(t)
		withDefaultPage(t, 2)
		base := 0x3600
		if omit {
			base += cOmittedBase
		}
		lib := newerLibrary()
		writeThumbs(t, ses, base, lib)
		db := mysqlFilesTable(t, dsn)
		for _, p := range lib {
			if _, err := db.Exec("insert into `files` (`hash`, `mime`, `created`, `modified`, `path`, `size`) values (?, 'image/jpeg', ?, ?, ?, 1)",
				fmt.Sprintf("%064x", base+p.n), p.at, p.at, p.path); err != nil {
				t.Fatal(err)
			}
		}
		d := dao.NewWithDB(db)
		whole := keptOut(&Manager{dao: d, searchTokens: newSearchTokenCache(1000)})
		for _, chunk := range []int{1, browseChunk} {
			orig := browseChunk
			browseChunk = chunk
			keyset := keptOut(&Manager{dao: d, searchTokens: newSearchTokenCache(1000), browse: d.BrowseMedia})
			for _, cutoff := range splitCutoffs() {
				got := strings.Join(splitPages(t, keyset, ses, cutoff, omit), " ")
				want := strings.Join(splitPages(t, whole, ses, cutoff, omit), " ")
				if got != want {
					t.Fatalf("chunk %d, split at %s: keyset\n%s\nwhole\n%s", chunk, cutoff, got, want)
				}
				if !strings.Contains(got, "/Mac/2.jpg@2025-06-30 23:59:59×2") {
					t.Fatalf("the copies weren't counted: %s", got)
				}
			}
			browseChunk = orig
		}
	})
}
