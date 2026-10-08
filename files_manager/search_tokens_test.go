// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
)

func cursorOf(n int) *searchCursor {
	c := &searchCursor{}
	for i := 0; i < n; i++ {
		c.all = append(c.all, &pb.File{Path: fmt.Sprint(i)})
	}
	return c
}

// Past the cap the least recently used tokens go - never the one just
// stored, even when it alone is over it - and an evicted token is unknown.
func TestSearchTokensEvictTheLeastRecentlyUsed(t *testing.T) {
	c := newSearchTokenCache(10)
	now := time.Now()
	c.store("old", cursorOf(4), now, 0)
	c.store("mid", cursorOf(4), now.Add(time.Second), 0)
	c.store("new", cursorOf(4), now.Add(2*time.Second), 0) // 12 > 10: "old" goes
	if _, ok := c.load("old"); ok {
		t.Error("the least recently used token stayed")
	}
	for _, tok := range []string{"mid", "new"} {
		if _, ok := c.load(tok); !ok {
			t.Errorf("%s was evicted", tok)
		}
	}
	if c.rows != 8 {
		t.Errorf("%d rows counted, want 8", c.rows)
	}
	// Used again (a new page stored), "mid" is now the newest.
	c.store("mid", cursorOf(3), now.Add(3*time.Second), 0)
	c.store("big", cursorOf(20), now.Add(4*time.Second), 0)
	if _, ok := c.load("big"); !ok {
		t.Error("the token just stored was evicted")
	}
	if len(c.by) != 1 || c.rows != 20 {
		t.Errorf("%d tokens, %d rows left, want only the one just stored", len(c.by), c.rows)
	}
}

func restPaths(cur *searchCursor) string {
	var paths []string
	for _, f := range cur.all[cur.off:] {
		paths = append(paths, f.Path)
	}
	return strings.Join(paths, ",")
}

// cursorOfPaths is a cursor at off with no last page to serve again.
func cursorOfPaths(off int, paths ...string) *searchCursor {
	c := &searchCursor{off: off, prev: off}
	for _, p := range paths {
		c.all = append(c.all, &pb.File{Path: p})
	}
	return c
}

// Issue #192: a page stored by a search that started before a folder was
// kept out has that folder's rows left out; after a clear, when what is
// kept out isn't known, it isn't stored at all.
func TestSearchTokenStoredAcrossAFlag(t *testing.T) {
	c := newSearchTokenCache(100)
	now := time.Now()
	gen := c.generation()
	c.keepOut([]string{"/Private/"})
	c.store("t", cursorOfPaths(1, "/Phone/0.jpg", "/Private/a.jpg", "/Phone/b.jpg"), now, gen)
	cur, ok := c.load("t")
	if !ok || restPaths(cur) != "/Phone/b.jpg" {
		t.Fatalf("stored across the flag: %v %q", ok, restPaths(cur))
	}
	if c.rows != 1 {
		t.Errorf("%d rows counted, want 1", c.rows)
	}
	// From a search that started after it: as found.
	fresh := cursorOfPaths(0, "/Phone/c.jpg")
	c.store("u", fresh, now, c.generation())
	if cur, _ := c.load("u"); cur != fresh {
		t.Error("a page of the current generation was copied")
	}

	gen = c.generation()
	c.clear()
	c.store("t", cursorOfPaths(0, "/Phone/b.jpg"), now, gen)
	if _, ok := c.load("t"); ok || c.rows != 0 {
		t.Errorf("a page from before a clear was stored (%d rows)", c.rows)
	}
}

// A cursor keeps the rows before its last page only until they outnumber
// those from the page on, then those are copied once - same rows, same
// order, the page still there to be served again.
func TestPageCursorCompactsOnlyOnceMostIsServed(t *testing.T) {
	c := cursorOf(10)
	for _, page := range []struct{ from, prev, off, rows int }{
		{0, 0, 3, 10}, // rows 0-2 served
		{3, 3, 6, 10}, // 3-5: three before the page, seven from it
		{6, 0, 3, 4},  // 6-8: six before it, four from it: copied
	} {
		if c.off != page.from {
			t.Fatalf("the page starts at %d, want %d", c.off, page.from)
		}
		c = pageCursor(c, c.all, c.off, 3, 0, 3, false)
		if c.prev != page.prev || c.off != page.off || len(c.all) != page.rows {
			t.Fatalf("after rows to %d: prev %d off %d len %d, want %d %d %d", page.from+3, c.prev, c.off, len(c.all), page.prev, page.off, page.rows)
		}
	}
	for i, f := range c.all {
		if f.Path != fmt.Sprint(6+i) {
			t.Errorf("row %d is %s, want %d", i, f.Path, 6+i)
		}
	}
	if c.pages != 3 {
		t.Errorf("%d pages counted, want 3", c.pages)
	}
}

// Paging through a search with its token gives every row once, in order,
// with its thumbnail, and the token ends with the results.
func TestImageSearchPagesThroughItsToken(t *testing.T) {
	_, ses := galleryTestEnv(t) // max-images-search=2
	db, mock, _ := sqlmock.New()
	defer db.Close()
	rows := sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"})
	for i := 0; i < 5; i++ {
		hash := strings.Repeat(fmt.Sprint(i), 64)
		if err := blobstore.WriteBytes(blobPath(hash)+"_thumbnail", ses, []byte("thumb-"+fmt.Sprint(i))); err != nil {
			t.Fatal(err)
		}
		rows.AddRow(hash, "image/jpeg", time.Now(), time.Now(), fmt.Sprintf("/p/%d.jpg", i), 1)
	}
	mock.ExpectQuery("select `f`.`hash`, `f`.`mime`").WillReturnRows(rows)
	mg := keptOut(&Manager{dao: dao.NewWithDB(db), searchTokens: newSearchTokenCache(1000)})

	var got []string
	token := ""
	for page := 0; page < 3; page++ {
		files, next, err := mg.ImageSearch(ses, "", nil, token, false, nil, "", nil, 0, 0, false, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			got = append(got, f.Path+"="+string(f.Content))
		}
		token = next
		if token == "" {
			break
		}
	}
	want := "/p/0.jpg=thumb-0 /p/1.jpg=thumb-1 /p/2.jpg=thumb-2 /p/3.jpg=thumb-3 /p/4.jpg=thumb-4"
	if strings.Join(got, " ") != want || token != "" {
		t.Errorf("pages gave %v (token %q), want %s", got, token, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err) // one query for every page
	}
}

// A limit (SearchPhotos.limit) shrinks only the page that asks for it: the
// rest stays behind the token, and the pages after it, sent without one,
// are the device's own size. A limit over that size, or none, gets it.
func TestImageSearchLimitShrinksOnlyItsPage(t *testing.T) {
	_, ses := galleryTestEnv(t)
	orig := maxImagesSearch
	maxImagesSearch = func() int { return 30 }
	t.Cleanup(func() { maxImagesSearch = orig })

	const total = 45
	hash := func(i int) string { return fmt.Sprintf("%064x", 0x100+i) }
	for i := 0; i < total; i++ {
		if err := blobstore.WriteBytes(blobPath(hash(i))+"_thumbnail", ses, []byte("thumb")); err != nil {
			t.Fatal(err)
		}
	}
	db, mock, _ := sqlmock.New()
	defer db.Close()
	for search := 0; search < 5; search++ {
		rows := sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"})
		for i := 0; i < total; i++ {
			rows.AddRow(hash(i), "image/jpeg", time.Now(), time.Now(), fmt.Sprintf("/p/%d.jpg", i), 1)
		}
		mock.ExpectQuery("select `f`.`hash`, `f`.`mime`").WillReturnRows(rows)
	}
	mg := keptOut(&Manager{dao: dao.NewWithDB(db), searchTokens: newSearchTokenCache(1000)})
	search := func(token string, limit int32) ([]*pb.File, string) {
		t.Helper()
		files, next, err := mg.ImageSearch(ses, "", nil, token, false, nil, "", nil, 0, limit, false, false)
		if err != nil {
			t.Fatal(err)
		}
		return files, next
	}

	var got []string
	token := ""
	for i, want := range []struct {
		limit int32
		size  int
	}{{12, 12}, {0, 30}, {0, 3}} {
		files, next := search(token, want.limit)
		if len(files) != want.size {
			t.Fatalf("page %d (limit %d) has %d photos, want %d", i, want.limit, len(files), want.size)
		}
		for _, f := range files {
			got = append(got, f.Path)
		}
		if token = next; (token == "") != (i == 2) {
			t.Fatalf("page %d: token %q", i, token)
		}
	}
	for i, p := range got {
		if p != fmt.Sprintf("/p/%d.jpg", i) {
			t.Fatalf("photo %d is %s: the pages skip or repeat", i, p)
		}
	}

	if files, _ := search("", 50); len(files) != 30 {
		t.Errorf("a limit over the default gave %d photos, want 30", len(files))
	}
	if files, _ := search("", 0); len(files) != 30 {
		t.Errorf("no limit gave %d photos, want 30", len(files))
	}
	if files, _ := search("", -5); len(files) != 30 {
		t.Errorf("a negative limit gave %d photos, want 30", len(files))
	}
	if files, _ := search("", 30); len(files) != 30 {
		t.Errorf("a limit equal to the default gave %d photos, want 30", len(files))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err) // one query for each new search, none for a page
	}
}

// searchTest is one search, paths in its order, run once against a mocked
// database (a second query fails the test), with pages of size photos.
// Row i's thumbnail is written unless i is in missing (issue #147). With
// omit, as a grid that keeps its thumbnails asks for its pages
// (SearchPhotos.omit_thumbnails): every page must be the same.
type searchTest struct {
	t     *testing.T
	mg    *Manager
	ses   *session.Session
	paths []string
	base  int
	omit  bool
}

// bothPageKinds runs test as a grid asking for its thumbnails and as one
// omitting them: the same rows, order, token and pages served again.
func bothPageKinds(t *testing.T, test func(t *testing.T, omit bool)) {
	t.Run("thumbnails", func(t *testing.T) { test(t, false) })
	t.Run("omitted", func(t *testing.T) { test(t, true) })
}

// cOmittedBase moves an omitting run's hashes off the other run's: the
// two share the storage folder, and a test writes or removes thumbnails.
const cOmittedBase = 0x80

func newSearchTest(t *testing.T, omit bool, base, size int, paths []string, missing ...int) *searchTest {
	t.Helper()
	_, ses := galleryTestEnv(t)
	orig := maxImagesSearch
	maxImagesSearch = func() int { return size }
	t.Cleanup(func() { maxImagesSearch = orig })
	if omit {
		base += cOmittedBase
	}
	st := &searchTest{t: t, ses: ses, paths: paths, base: base, omit: omit}
	skip := map[int]bool{}
	for _, i := range missing {
		skip[i] = true
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		db.Close()
	})
	rows := sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"})
	for i, p := range paths {
		if !skip[i] {
			st.thumb(i)
		}
		rows.AddRow(st.hash(i), "image/jpeg", time.Now(), time.Now(), p, 1)
	}
	mock.ExpectQuery("select `f`.`hash`, `f`.`mime`").WillReturnRows(rows)
	st.mg = keptOut(&Manager{dao: dao.NewWithDB(db), searchTokens: newSearchTokenCache(1000)})
	return st
}

// photoPaths is /p/0.jpg ... /p/<n-1>.jpg.
func photoPaths(n int) []string {
	var paths []string
	for i := 0; i < n; i++ {
		paths = append(paths, fmt.Sprintf("/p/%d.jpg", i))
	}
	return paths
}

func (st *searchTest) hash(i int) string { return fmt.Sprintf("%064x", st.base+i) }

func (st *searchTest) thumb(i int) {
	st.t.Helper()
	if err := blobstore.WriteBytes(thumbnailPath(st.hash(i)), st.ses, []byte(fmt.Sprint("thumb-", st.base+i))); err != nil {
		st.t.Fatal(err)
	}
}

// page asks as a grid does: token "" starts the search.
func (st *searchTest) page(token string, have int32) (string, string) {
	st.t.Helper()
	files, next, err := st.mg.ImageSearch(st.ses, "", nil, token, false, nil, "", nil, have, 0, false, st.omit)
	if err != nil {
		st.t.Fatal(err)
	}
	return st.pagePaths(files), next
}

// pagePaths is the page's photos, each with its thumbnail's content - or,
// in a page that omits it, the thumbnail its row stands for (a row's hash
// is its thumbnail's number), once checked that none came.
func (st *searchTest) pagePaths(files []*pb.File) string {
	var got []string
	for _, f := range files {
		thumb := strings.TrimPrefix(string(f.Content), "thumb-")
		if st.omit {
			if f.Content != nil {
				st.t.Errorf("%s came with content in a page that omits it", f.Path)
			}
			n, err := strconv.ParseInt(f.Hash, 16, 64)
			if err != nil {
				st.t.Errorf("%s: hash %q", f.Path, f.Hash)
			}
			thumb = fmt.Sprint(n)
		}
		if f.ThumbnailSmall == nil || f.GetThumbnailSmall() {
			st.t.Errorf("%s: small %v, want false (no small thumbnail was asked for)", f.Path, f.ThumbnailSmall)
		}
		got = append(got, strings.TrimSuffix(strings.TrimPrefix(f.Path, "/p/"), ".jpg")+"="+thumb)
	}
	return strings.Join(got, " ")
}

// want is the page of rows is (indexes in paths), as pagePaths writes it.
func (st *searchTest) want(is ...int) string {
	var w []string
	for _, i := range is {
		w = append(w, strings.TrimSuffix(strings.TrimPrefix(st.paths[i], "/p/"), ".jpg")+"="+fmt.Sprint(st.base+i))
	}
	return strings.Join(w, " ")
}

// last asks for the search's last page, which must be rows is.
func (st *searchTest) last(token string, have int32, is ...int) {
	st.t.Helper()
	got, next := st.page(token, have)
	st.expect("the last page", got, is...)
	if next != "" {
		st.t.Errorf("token %q after the last page", next)
	}
}

// expect fails the test unless got is the page of rows is.
func (st *searchTest) expect(what, got string, is ...int) {
	st.t.Helper()
	if want := st.want(is...); got != want {
		st.t.Fatalf("%s: got [%s], want [%s]", what, got, want)
	}
}

// The bug this fixes: a page whose answer never reached the client (a
// timeout that drops late answers, a socket closed mid-reply), asked again
// with the same token and the same have, was answered with the page after
// it, and the lost photos never showed. Now it is the same page again, and
// the grid goes on from there with every photo once.
func TestSearchPageLostIsServedAgain(t *testing.T) {
	bothPageKinds(t, func(t *testing.T, omit bool) {
		st := newSearchTest(t, omit, 0x1000, 3, photoPaths(10))
		got, tok := st.page("", 0)
		st.expect("first page", got, 0, 1, 2)
		got, next := st.page(tok, 3)
		st.expect("second page", got, 3, 4, 5)
		if next != tok {
			t.Fatalf("the token changed: %q", next)
		}
		// Its answer is lost: asked again with the same have, the same page,
		// thumbnails and all.
		for i := 0; i < 2; i++ {
			got, _ = st.page(tok, 3)
			st.expect("the lost page asked again", got, 3, 4, 5)
		}
		// It came: the grid goes on, every photo once.
		got, _ = st.page(tok, 6)
		st.expect("the page after it", got, 6, 7, 8)
		st.last(tok, 9, 9)
	})
}

// A grid that gets every answer goes on page after page, each photo once;
// a lost last page is served again too (the token stays where it was).
func TestSearchPagesGoOnWithTheHaveTheyLeft(t *testing.T) {
	bothPageKinds(t, func(t *testing.T, omit bool) {
		st := newSearchTest(t, omit, 0x1100, 4, photoPaths(10))
		got, tok := st.page("", 0)
		st.expect("first page", got, 0, 1, 2, 3)
		got, _ = st.page(tok, 4)
		st.expect("second page", got, 4, 5, 6, 7)
		st.last(tok, 8, 8, 9)
		st.last(tok, 8, 8, 9) // its answer lost, asked again
	})
}

// An app that sends no have (older ones, the pickers that don't count)
// gets the next page for every request, as before: the device can't tell
// a lost page from the next one.
func TestSearchPagesWithoutHaveGoOnAsBefore(t *testing.T) {
	bothPageKinds(t, func(t *testing.T, omit bool) {
		st := newSearchTest(t, omit, 0x1200, 3, photoPaths(10))
		got, tok := st.page("", 0)
		st.expect("first page", got, 0, 1, 2)
		got, _ = st.page(tok, 0)
		st.expect("second page", got, 3, 4, 5)
		got, _ = st.page(tok, 0)
		st.expect("the same request again", got, 6, 7, 8)
	})
}

// A have the token can't place - neither what its last page left nor what
// the client had before it - goes on, as before; and a page asked for with
// such a count is never served again (the client doesn't count what the
// token sends, so a have it repeats tells nothing).
func TestSearchPagesWithAnUnplacedHaveGoOn(t *testing.T) {
	bothPageKinds(t, func(t *testing.T, omit bool) {
		st := newSearchTest(t, omit, 0x1300, 3, photoPaths(14))
		got, tok := st.page("", 0)
		st.expect("first page", got, 0, 1, 2)
		got, _ = st.page(tok, 7) // a grid that also counts something else
		st.expect("a have of 7 after 3 photos", got, 3, 4, 5)
		got, _ = st.page(tok, 7)
		st.expect("the same have again", got, 6, 7, 8)
		got, _ = st.page(tok, 2) // fewer than it ever had
		st.expect("a have of 2", got, 9, 10, 11)
		// Counting from here, it is in step again.
		got, _ = st.page(tok, 5)
		st.expect("a have of 5 after 3 more", got, 12, 13)
	})
}

// Two requests naming the same token with the same have at once (a quick
// double scroll, issue #171): both get the same page, whichever stores
// first, and the token goes on from there, never corrupted.
func TestSearchPageAskedTwiceAtOnce(t *testing.T) {
	bothPageKinds(t, func(t *testing.T, omit bool) {
		const rounds = 15
		st := newSearchTest(t, omit, 0x1400, 3, photoPaths(3+3*rounds+3))
		got, tok := st.page("", 0)
		st.expect("first page", got, 0, 1, 2)
		for round := 0; round < rounds; round++ {
			have := int32(3 + 3*round)
			var wg sync.WaitGroup
			pages := make([]string, 2)
			for i := range pages {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					files, _, err := st.mg.ImageSearch(st.ses, "", nil, tok, false, nil, "", nil, have, 0, false, st.omit)
					if err != nil {
						t.Error(err)
					}
					pages[i] = st.pagePaths(files)
				}(i)
			}
			wg.Wait()
			h := int(have)
			for _, p := range pages {
				st.expect(fmt.Sprint("a double request at ", have), p, h, h+1, h+2)
			}
			cur, ok := st.mg.searchTokens.load(tok)
			if !ok || cur.before != have || cur.after != have+3 || cur.pages != round+1 || cur.all[cur.off].Path != st.paths[h+3] {
				t.Fatalf("token after a double request at %d: %v %+v", have, ok, cur)
			}
		}
		st.last(tok, 3+3*rounds, 3+3*rounds, 4+3*rounds, 5+3*rounds)
	})
}

// Issue #147: a page fills past a photo without its thumbnail. Asked again
// once the thumbnail is there, the page brings it, in its place, and the
// photo that no longer fits comes first on the next page: none skipped.
// Without it, the same page as before.
func TestSearchPageServedAgainAfterASkippedRow(t *testing.T) {
	bothPageKinds(t, func(t *testing.T, omit bool) {
		st := newSearchTest(t, omit, 0x1500, 3, photoPaths(10), 4)
		got, tok := st.page("", 0)
		st.expect("first page", got, 0, 1, 2)
		got, _ = st.page(tok, 3)
		st.expect("second page, row 4 not ready", got, 3, 5, 6)
		got, _ = st.page(tok, 3)
		st.expect("asked again, still not ready", got, 3, 5, 6)
		st.thumb(4)
		got, _ = st.page(tok, 3)
		st.expect("asked again once it is", got, 3, 4, 5)
		got, _ = st.page(tok, 6)
		st.expect("the page after", got, 6, 7, 8)
		st.last(tok, 9, 9)
	})
}

// Once most of a search is served the token copies what is left
// (pageCursor) - the last page with it, so it can still be served again.
func TestSearchPageServedAgainAfterTheTokenIsCompacted(t *testing.T) {
	bothPageKinds(t, func(t *testing.T, omit bool) {
		st := newSearchTest(t, omit, 0x1600, 2, photoPaths(10))
		got, tok := st.page("", 0)
		st.expect("page 1", got, 0, 1)
		for have, rows := int32(2), []int{2, 3}; have <= 6; have, rows = have+2, []int{rows[0] + 2, rows[1] + 2} {
			got, _ = st.page(tok, have)
			st.expect(fmt.Sprint("the page at ", have), got, rows...)
		}
		cur, _ := st.mg.searchTokens.load(tok)
		if len(cur.all) != 4 || cur.prev != 0 || cur.off != 2 {
			t.Fatalf("the token wasn't compacted to its last page on: prev %d off %d of %d rows", cur.prev, cur.off, len(cur.all))
		}
		got, _ = st.page(tok, 6)
		st.expect("the lost page asked again", got, 6, 7)
		st.last(tok, 8, 8, 9)
	})
}

// Issue #192: a folder kept out of Images between a lost page and the
// request asking for it again: the page comes without its photos, filled
// from further on, and nothing after it skips.
func TestSearchPageServedAgainAfterAFolderIsKeptOut(t *testing.T) {
	bothPageKinds(t, func(t *testing.T, omit bool) {
		paths := photoPaths(10)
		paths[4] = "/Private/4.jpg"
		paths[7] = "/Private/7.jpg"
		st := newSearchTest(t, omit, 0x1700, 3, paths)
		got, tok := st.page("", 0)
		st.expect("first page", got, 0, 1, 2)
		got, _ = st.page(tok, 3)
		st.expect("second page", got, 3, 4, 5)
		st.mg.searchTokens.keepOut([]string{"/Private/"})
		got, _ = st.page(tok, 3)
		st.expect("asked again after the flag", got, 3, 5, 6)
		st.last(tok, 6, 8, 9)
	})
}

// A grid that keeps asking for the same page with the same have although
// each answer reaches it (it found every photo a duplicate) is served it
// again only cMaxServedAgain times in a row, then goes on.
func TestSearchPageServedAgainOnlySoManyTimes(t *testing.T) {
	bothPageKinds(t, func(t *testing.T, omit bool) {
		st := newSearchTest(t, omit, 0x1800, 2, photoPaths(20))
		got, tok := st.page("", 0)
		st.expect("first page", got, 0, 1)
		got, _ = st.page(tok, 2)
		st.expect("second page", got, 2, 3)
		for i := 0; i < cMaxServedAgain; i++ {
			got, _ = st.page(tok, 2)
			st.expect(fmt.Sprint("asked again, ", i+1), got, 2, 3)
		}
		got, _ = st.page(tok, 2)
		st.expect("asked once too often", got, 4, 5)
		// Going on in step starts the count again.
		got, _ = st.page(tok, 4)
		st.expect("the page after", got, 6, 7)
		got, _ = st.page(tok, 4)
		st.expect("that one lost", got, 6, 7)
	})
}

// A token this device no longer holds: the search starts again past the
// client's have (unchanged). That page, if it brought only photos the
// client had (uploads moved the search on), is asked for with the same
// have - and goes on, since the client wasn't in step yet; once it is, a
// lost page is served again as with any token.
func TestSearchResumedFromAnUnknownToken(t *testing.T) {
	bothPageKinds(t, func(t *testing.T, omit bool) {
		st := newSearchTest(t, omit, 0x1900, 3, photoPaths(15))
		got, tok := st.page("expired-token", 3)
		st.expect("resumed past 3", got, 3, 4, 5)
		if tok == "" || tok == "expired-token" {
			t.Fatalf("token %q", tok)
		}
		got, _ = st.page(tok, 3)
		st.expect("a page that added nothing", got, 6, 7, 8)
		got, _ = st.page(tok, 6)
		st.expect("in step", got, 9, 10, 11)
		got, _ = st.page(tok, 6)
		st.expect("that one lost", got, 9, 10, 11)
		st.last(tok, 9, 12, 13, 14)
	})
}

// A jump (before) with a token runs the jump's search past the client's
// have, under a new token, and leaves the old token as it was.
func TestSearchJumpLeavesItsTokenAlone(t *testing.T) {
	bothPageKinds(t, func(t *testing.T, omit bool) {
		st := newSearchTest(t, omit, 0x1a00, 3, photoPaths(9))
		got, tok := st.page("", 0)
		st.expect("first page", got, 0, 1, 2)
		cur, _ := st.mg.searchTokens.load(tok)

		db, mock, _ := sqlmock.New()
		defer db.Close()
		rows := sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"})
		for i := 3; i < 9; i++ {
			rows.AddRow(st.hash(i), "image/jpeg", time.Now(), time.Now(), st.paths[i], 1)
		}
		mock.ExpectQuery("select `f`.`hash`, `f`.`mime`").WillReturnRows(rows)
		jumpMg := keptOut(&Manager{dao: dao.NewWithDB(db), searchTokens: st.mg.searchTokens})
		before := time.Now()
		files, jumpTok, err := jumpMg.ImageSearch(st.ses, "", nil, tok, false, nil, "", &before, 3, 0, false, st.omit)
		if err != nil {
			t.Fatal(err)
		}
		// The jump's rows start at 3; past the have of 3: 6, 7, 8.
		st.expect("the jump's page", st.pagePaths(files), 6, 7, 8)
		if jumpTok != "" {
			t.Errorf("token %q after the jump's last page", jumpTok)
		}
		if now, _ := st.mg.searchTokens.load(tok); now != cur {
			t.Error("the jump changed the token it was sent with")
		}
		got, _ = st.page(tok, 3)
		st.expect("the old token goes on", got, 3, 4, 5)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
}

// A request finishing late never puts its token back: a cursor behind the
// one stored (fewer pages, or the same page by a request started before
// the stored one's) is dropped, one as far on replaces it.
func TestSearchTokenNeverGoesBack(t *testing.T) {
	c := newSearchTokenCache(100)
	now := time.Now()
	ahead := cursorOfPaths(2, "/a", "/b", "/c")
	ahead.pages = 2
	c.store("t", ahead, now, 0)
	behind := cursorOfPaths(1, "/a", "/b", "/c")
	behind.pages = 1
	c.store("t", behind, now, 0)
	if cur, _ := c.load("t"); cur != ahead {
		t.Error("a cursor behind the one stored replaced it")
	}
	same := cursorOfPaths(2, "/a", "/b", "/c")
	same.pages, same.seq = 2, 5
	c.store("t", same, now, 0)
	if cur, _ := c.load("t"); cur != same || c.rows != 3 {
		t.Errorf("a cursor as far on wasn't stored (%d rows)", c.rows)
	}
	earlier := cursorOfPaths(1, "/a", "/b", "/c")
	earlier.pages, earlier.seq = 2, 4
	c.store("t", earlier, now, 0)
	if cur, _ := c.load("t"); cur != same {
		t.Error("the same page by a request started earlier replaced it")
	}
	later := cursorOfPaths(1, "/a", "/b", "/c")
	later.pages, later.seq = 2, 6
	c.store("t", later, now, 0)
	if cur, _ := c.load("t"); cur != later {
		t.Error("the same page by a request started later wasn't stored")
	}
}

// Issue #192: the rows of a token's last page are kept out with the rest,
// and its place stays the same photo; a cursor stored from before the flag
// is filtered the same way.
func TestKeepOutFiltersTheLastPage(t *testing.T) {
	c := newSearchTokenCache(100)
	now := time.Now()
	cur := cursorOfPaths(4, "/a", "/P/1", "/b", "/c", "/P/2", "/d")
	cur.prev = 1
	gen := c.generation()
	c.store("t", cur, now, gen)
	c.keepOut([]string{"/P/"})
	got, _ := c.load("t")
	served := func(cur *searchCursor) string {
		var paths []string
		for _, f := range cur.all[cur.prev:cur.off] {
			paths = append(paths, f.Path)
		}
		return strings.Join(paths, ",")
	}
	if served(got) != "/b,/c" || restPaths(got) != "/d" || c.rows != 3 {
		t.Errorf("after keepOut: last page %q, rest %q, %d rows", served(got), restPaths(got), c.rows)
	}
	// A page in flight across the flag.
	c.store("u", cur, now, gen)
	if got, _ := c.load("u"); served(got) != "/b,/c" || restPaths(got) != "/d" {
		t.Errorf("stored across the flag: last page %q, rest %q", served(got), restPaths(got))
	}
}

// slowFirst makes the first page served from now on wait, once it has
// loaded its token, until release is closed: a request the device is
// still serving when the client's timeout sends the retry.
func slowFirst(t *testing.T, size int) (entered, release chan struct{}) {
	entered, release = make(chan struct{}), make(chan struct{})
	var calls int32
	orig := maxImagesSearch
	t.Cleanup(func() { maxImagesSearch = orig })
	maxImagesSearch = func() int {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(entered)
			<-release
		}
		return size
	}
	return entered, release
}

// slowPage starts a request for the page at have that slowFirst holds,
// and gives what it served once released.
func (st *searchTest) slowPage(token string, have int32) (entered, release chan struct{}, done chan string) {
	entered, release = slowFirst(st.t, 3)
	done = make(chan string, 1)
	go func() {
		files, _, err := st.mg.ImageSearch(st.ses, "", nil, token, false, nil, "", nil, have, 0, false, st.omit)
		if err != nil {
			st.t.Error(err)
		}
		done <- st.pagePaths(files)
	}()
	return entered, release, done
}

// A request still being served when the client's retry comes (its timeout
// dropped it), both for the same page from the same cursor, while a
// thumbnail goes (its content deleted) between the retry's reads and the
// first one's: the first ends one row further. The client holds the
// retry's page, so the token keeps the retry's place, though the first
// stores last - nothing skipped.
func TestSearchPageRetryOvertakingTheFirstRequestKeepsItsPlace(t *testing.T) {
	bothPageKinds(t, func(t *testing.T, omit bool) {
		st := newSearchTest(t, omit, 0x1b00, 3, photoPaths(12))
		got, tok := st.page("", 0)
		st.expect("first page", got, 0, 1, 2)
		entered, release, done := st.slowPage(tok, 3)
		<-entered
		got, _ = st.page(tok, 3) // the retry: the client keeps this one
		st.expect("the retry's page", got, 3, 4, 5)
		removeThumbnails(blobPath(st.hash(4)))
		close(release)
		st.expect("the first request's page, dropped", <-done, 3, 5, 6)
		got, _ = st.page(tok, 6)
		st.expect("the page after the retry's", got, 6, 7, 8)
	})
}

// As above, with a thumbnail coming between the retry's reads (without
// it) and the first request's (with it): the first ends one row sooner,
// and the next page doesn't bring again a photo the client holds.
func TestSearchPageRetryOvertakingTheFirstRequestRepeatsNothing(t *testing.T) {
	bothPageKinds(t, func(t *testing.T, omit bool) {
		st := newSearchTest(t, omit, 0x1c00, 3, photoPaths(12), 4)
		got, tok := st.page("", 0)
		st.expect("first page", got, 0, 1, 2)
		entered, release, done := st.slowPage(tok, 3)
		<-entered
		got, _ = st.page(tok, 3) // the retry: the client keeps this one
		st.expect("the retry's page, row 4 not ready", got, 3, 5, 6)
		st.thumb(4)
		close(release)
		st.expect("the first request's page, dropped", <-done, 3, 4, 5)
		got, _ = st.page(tok, 6)
		st.expect("the page after the retry's", got, 7, 8, 9)
	})
}

// A client that got a page and then deleted as many photos as it brought
// asks for the next one with the have it had before that page. After a
// delete that is never taken for a lost page: the next page comes, not
// the one the client holds, again and again.
func TestSearchPageNotServedAgainAfterADelete(t *testing.T) {
	bothPageKinds(t, func(t *testing.T, omit bool) {
		st := newSearchTest(t, omit, 0x1d00, 3, photoPaths(20))
		got, tok := st.page("", 0)
		st.expect("first page", got, 0, 1, 2)
		got, _ = st.page(tok, 3)
		st.expect("second page", got, 3, 4, 5)
		for i := 0; i < 3; i++ {
			st.mg.searchTokens.noteDelete() // what delFile does
		}
		got, _ = st.page(tok, 3)
		st.expect("the page after a delete of 3", got, 6, 7, 8)
		// In step again, a lost page is served again as before.
		got, _ = st.page(tok, 6)
		st.expect("the page after", got, 9, 10, 11)
		got, _ = st.page(tok, 6)
		st.expect("that one lost", got, 9, 10, 11)
	})
}
