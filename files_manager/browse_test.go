// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
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
	}
	st.mg = keptOut(mg)
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
// page) and a chunk ahead of the largest page.
func keysetScenarios(t *testing.T, mode int) {
	for _, chunk := range []int{1, browseChunk} {
		t.Run(fmt.Sprint("chunk ", chunk), func(t *testing.T) {
			origMode, origChunk := scenarioKeyset, browseChunk
			scenarioKeyset, browseChunk = mode, chunk
			t.Cleanup(func() { scenarioKeyset, browseChunk = origMode, origChunk })
			for _, c := range keysetCases {
				if c.mock && mode == keysetMySQL {
					continue
				}
				t.Run(c.name, c.run)
			}
		})
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

// Only the plain browse is served by keyset: a tag, a person, a
// collection, a map area, a text or a folder's gallery reads its rows
// whole when it starts, as before.
func TestKeysetOnlyForThePlainBrowse(t *testing.T) {
	mg := &Manager{browse: func(*dao.MediaBrowse, *time.Time, int) ([]*pb.File, *time.Time, bool, error) {
		return nil, nil, true, nil
	}}
	area := &dao.MediaArea{}
	byRating := &dao.RatingQuery{ByRating: true}
	for _, c := range []struct {
		what    string
		path    string
		tags    []string
		people  []string
		group   string
		area    *dao.MediaArea
		text    string
		rating  *dao.RatingQuery
		keyset  bool
		noFetch bool
	}{
		{what: "the plain browse", keyset: true},
		{what: "a folder", path: "/Phone/"},
		{what: "a tag", tags: []string{"beach"}},
		{what: "a person", people: []string{"p1"}},
		{what: "a collection", group: "g1"},
		{what: "a map area", area: area},
		{what: "a text", text: "dogs"},
		{what: "a rating", group: "g1", rating: byRating},
	} {
		if got := mg.plainBrowse(c.path, c.tags, c.people, c.group, c.area, c.text, c.rating); got != c.keyset {
			t.Errorf("%s: keyset %v, want %v", c.what, got, c.keyset)
		}
	}
	if (&Manager{}).plainBrowse("", nil, nil, "", nil, "", nil) {
		t.Error("a Manager without browse served by keyset")
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
