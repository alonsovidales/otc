// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
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

func cursorOfPaths(off int, paths ...string) *searchCursor {
	c := &searchCursor{off: off}
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

// A cursor keeps the rows already served only until they outnumber what's
// left, then what's left is copied once - same rows, same order.
func TestNextCursorCompactsOnlyOnceMostIsServed(t *testing.T) {
	c := cursorOf(10)
	c = nextCursor(c.all, c.off, 3)
	if c.off != 3 || len(c.all) != 10 {
		t.Fatalf("after 3 of 10: off %d len %d, want the same rows kept", c.off, len(c.all))
	}
	c = nextCursor(c.all, c.off, 3)
	if c.off != 0 || len(c.all) != 4 {
		t.Fatalf("after 6 of 10: off %d len %d, want the 4 left copied", c.off, len(c.all))
	}
	for i, f := range c.all[c.off:] {
		if f.Path != fmt.Sprint(6+i) {
			t.Errorf("row %d is %s, want %d", i, f.Path, 6+i)
		}
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
		files, next, err := mg.ImageSearch(ses, "", nil, token, false, nil, "", nil, 0, 0, false)
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
		files, next, err := mg.ImageSearch(ses, "", nil, token, false, nil, "", nil, 0, limit, false)
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
