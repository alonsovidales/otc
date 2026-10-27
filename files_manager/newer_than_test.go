// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"database/sql"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"github.com/go-sql-driver/mysql"
)

// Issue #194: after a jump (before), scrolling up asks for the photos after
// the jump's instant (newer_than), oldest first, page after page with its
// token. Every newSearchTest case also runs going up (everySearchKind);
// these are what only going up has.

// cMonthEnd is the last second of June 2025, the instant these jumps go to.
var cMonthEnd = time.Date(2025, 6, 30, 23, 59, 59, 0, time.UTC)

// libPhoto is a row of a test library: content n (its hash) at path.
type libPhoto struct {
	n    int
	path string
	at   time.Time
}

// newerLibrary is a library around cMonthEnd: photos at its very second
// and the one after, a photo with copies on both sides of it, one synced
// back long after it was taken, two copies at one instant.
func newerLibrary() []libPhoto {
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 12, 0, 0, 0, time.UTC) }
	return []libPhoto{
		{0, "/Phone/0.jpg", cMonthEnd},
		{1, "/Phone/1.jpg", cMonthEnd.Add(time.Second)},
		{2, "/Mac/2.jpg", cMonthEnd}, // 2's copies straddle the month's end
		{2, "/Phone/2.jpg", cMonthEnd.Add(time.Second)},
		{3, "/Phone/3.jpg", day(2025, 5, 10)},
		{3, "/Download/3.jpg", day(2026, 1, 1)}, // synced back
		{4, "/Phone/4.jpg", day(2025, 8, 1)},
		{4, "/Mac/4.jpg", day(2025, 8, 1)},
		{5, "/Phone/5.jpg", day(2024, 2, 1)},
		{6, "/Phone/6.jpg", day(2025, 9, 15)},
		{7, "/Phone/7.jpg", day(2025, 6, 15)},
		{8, "/Phone/8.jpg", day(2026, 3, 1)},
		{9, "/Phone/9.jpg", day(2023, 12, 24)},
		{10, "/Phone/10.jpg", cMonthEnd.Add(2 * time.Second)},
		{11, "/Phone/11.jpg", cMonthEnd},
	}
}

// libraryRows is what the search's query answers for lib, with before if
// set (as the query compares it: at the second), in its order: newest
// first and, at one instant, by path backwards.
func libraryRows(base int, lib []libPhoto, before *time.Time) *sqlmock.Rows {
	lib = slices.Clone(lib)
	slices.SortFunc(lib, func(a, b libPhoto) int {
		if c := b.at.Compare(a.at); c != 0 {
			return c
		}
		return strings.Compare(b.path, a.path)
	})
	rows := sqlmock.NewRows(fileCols)
	for _, p := range lib {
		if before != nil && p.at.After(before.Truncate(time.Second)) {
			continue
		}
		rows.AddRow(fmt.Sprintf("%064x", base+p.n), "image/jpeg", p.at, p.at, p.path, 1)
	}
	return rows
}

// photosIn is how many photos (contents) of lib have their oldest copy
// at or before cutoff.
func photosIn(lib []libPhoto, cutoff time.Time) (below, above int) {
	oldest := map[int]time.Time{}
	for _, p := range lib {
		if t, ok := oldest[p.n]; !ok || p.at.Before(t) {
			oldest[p.n] = p.at
		}
	}
	for _, t := range oldest {
		if t.After(cutoff.Truncate(time.Second)) {
			above++
		} else {
			below++
		}
	}
	return below, above
}

func writeThumbs(t *testing.T, ses *session.Session, base int, lib []libPhoto) {
	t.Helper()
	for _, p := range lib {
		h := fmt.Sprintf("%064x", base+p.n)
		if err := blobstore.WriteBytes(thumbnailPath(h), ses, []byte("thumb")); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Remove(thumbnailPath(h)) })
	}
}

func describePage(files []*pb.File) []string {
	var out []string
	for _, f := range files {
		out = append(out, fmt.Sprintf("%s@%s×%d", f.Path, f.Created.AsTime().Format(time.DateTime), f.GetCopies()))
	}
	return out
}

// Going up: the photos above the instant only - not one at its very second,
// nor one whose oldest copy is below it however recent its other copies -
// oldest first, a first page of the limit asked, then pages of the default
// or, without thumbnails, of a larger limit; the token remembers the
// instant, and the last page ends it.
func TestImageSearchNewerThanPagesOldestFirst(t *testing.T) {
	_, ses := galleryTestEnv(t)
	withDefaultPage(t, 2)
	const base = 0x3000
	lib := newerLibrary()
	writeThumbs(t, ses, base, lib)
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectQuery("select `f`.`hash`, `f`.`mime`").WithoutArgs().WillReturnRows(libraryRows(base, lib, nil))
	expectCopies(mock, 3)
	mg := keptOut(&Manager{dao: dao.NewWithDB(db), searchTokens: newSearchTokenCache(1000)})

	cutoff := cMonthEnd.Add(999 * time.Millisecond) // the web's end of June
	var got []string
	token, have := "", int32(0)
	for i, page := range []struct {
		limit int32
		omit  bool
		size  int
	}{{1, false, 1}, {0, false, 2}, {200, true, 2}} {
		files, next, err := mg.ImageSearch(ses, "", nil, token, false, nil, "", nil, &cutoff, have, page.limit, false, page.omit)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != page.size {
			t.Fatalf("page %d (limit %d, omit %v): %d photos, want %d", i, page.limit, page.omit, len(files), page.size)
		}
		for _, f := range files {
			if page.omit != (f.Content == nil) {
				t.Errorf("page %d, %s: content %q (omit %v)", i, f.Path, f.Content, page.omit)
			}
		}
		got = append(got, describePage(files)...)
		have += int32(len(files))
		if (next == "") != (i == 2) || (token != "" && next != "" && next != token) {
			t.Fatalf("page %d: token %q after %q", i, next, token)
		}
		if next != "" {
			if cur, ok := mg.searchTokens.load(next); !ok || cur.newerThan == nil || !cur.newerThan.Equal(cutoff) {
				t.Fatalf("page %d: the token doesn't remember the instant: %v", i, cur)
			}
		}
		token = next
	}
	// Not 0 or 11 (at June's last second), nor 2 (its oldest copy there),
	// nor 3 (taken in May, synced back in 2026); 4 at its smaller path.
	want := []string{
		"/Phone/1.jpg@2025-07-01 00:00:00×1",
		"/Phone/10.jpg@2025-07-01 00:00:01×1",
		"/Mac/4.jpg@2025-08-01 12:00:00×1",
		"/Phone/6.jpg@2025-09-15 12:00:00×1",
		"/Phone/8.jpg@2026-03-01 12:00:00×1",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("going up:\n got %s\nwant %s", strings.Join(got, " "), strings.Join(want, " "))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A token only goes on for its own direction and instant: sent by a
// request of the other direction, or going up from another instant, it is
// one the device doesn't hold - that request searches again past its have,
// under a new token, and the token sent stays as it was. The same instant
// in another time zone is the same instant.
func TestImageSearchTokenOfAnotherSearchIsUnknown(t *testing.T) {
	_, ses := galleryTestEnv(t)
	withDefaultPage(t, 2)
	const base = 0x3100
	lib := newerLibrary()
	writeThumbs(t, ses, base, lib)
	db, mock, _ := sqlmock.New()
	defer db.Close()
	for i := 0; i < 4; i++ { // down, up from the down token, down from the up one, up from another instant
		mock.ExpectQuery("select `f`.`hash`, `f`.`mime`").WithoutArgs().WillReturnRows(libraryRows(base, lib, nil))
	}
	expectCopies(mock, 5)
	mg := keptOut(&Manager{dao: dao.NewWithDB(db), searchTokens: newSearchTokenCache(1000)})
	search := func(token string, newerThan *time.Time, have int32) (string, string) {
		t.Helper()
		files, next, err := mg.ImageSearch(ses, "", nil, token, false, nil, "", nil, newerThan, have, 0, false, false)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(describePage(files), " "), next
	}
	june := cMonthEnd
	july := cMonthEnd.Add(time.Second)

	// Down: 8, 6, 4, 10, 1, 11, 0, 2, 7, 3, 5, 9; up from June's end: 1,
	// 10, 4, 6, 8; up from July's first second: 10, 4, 6, 8.
	got, down := search("", nil, 0)
	if got != "/Phone/8.jpg@2026-03-01 12:00:00×1 /Phone/6.jpg@2025-09-15 12:00:00×1" || down == "" {
		t.Fatalf("down: %s (token %q)", got, down)
	}
	downCur, _ := mg.searchTokens.load(down)

	got, up := search(down, &june, 2) // the down token going up: past 2 of the photos above June
	if got != "/Mac/4.jpg@2025-08-01 12:00:00×1 /Phone/6.jpg@2025-09-15 12:00:00×1" || up == "" || up == down {
		t.Fatalf("up with the down token: %s (token %q)", got, up)
	}
	if cur, _ := mg.searchTokens.load(down); cur != downCur {
		t.Error("the down token changed")
	}
	upCur, _ := mg.searchTokens.load(up)

	got, again := search(up, nil, 2) // the up token going down: past 2 from the newest
	if got != "/Mac/4.jpg@2025-08-01 12:00:00×1 /Phone/10.jpg@2025-07-01 00:00:01×1" || again == "" || again == up {
		t.Fatalf("down with the up token: %s (token %q)", got, again)
	}
	got, other := search(up, &july, 2) // another instant: above July's first second, past 2
	if got != "/Phone/6.jpg@2025-09-15 12:00:00×1 /Phone/8.jpg@2026-03-01 12:00:00×1" || other != "" {
		t.Fatalf("up from another instant: %s (token %q)", got, other)
	}
	if cur, _ := mg.searchTokens.load(up); cur != upCur {
		t.Error("the up token changed")
	}
	inMadrid := june.In(time.FixedZone("CEST", 2*3600))
	got, next := search(up, &inMadrid, 4) // the same instant: it goes on
	if got != "/Phone/8.jpg@2026-03-01 12:00:00×1" || next != "" {
		t.Fatalf("up from the same instant elsewhere: %s (token %q)", got, next)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// newer_than wins over before: the query has no date in it, the photos
// come oldest first above newer_than, and a page with a token goes on with
// it - a jump (before alone) would start again.
func TestImageSearchNewerThanWinsOverBefore(t *testing.T) {
	_, ses := galleryTestEnv(t)
	withDefaultPage(t, 3)
	const base = 0x3200
	lib := newerLibrary()
	writeThumbs(t, ses, base, lib)
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectQuery("select `f`.`hash`, `f`.`mime`").WithoutArgs().WillReturnRows(libraryRows(base, lib, nil))
	expectCopies(mock, 2)
	mg := keptOut(&Manager{dao: dao.NewWithDB(db), searchTokens: newSearchTokenCache(1000)})
	june, before := cMonthEnd, cMonthEnd.AddDate(-1, 0, 0)
	var got []string
	token, have := "", int32(0)
	for i := 0; i < 2; i++ {
		files, next, err := mg.ImageSearch(ses, "", nil, token, false, nil, "", &before, &june, have, 0, false, false)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, describePage(files)...)
		have += int32(len(files))
		token = next
	}
	want := "/Phone/1.jpg@2025-07-01 00:00:00×1 /Phone/10.jpg@2025-07-01 00:00:01×1 /Mac/4.jpg@2025-08-01 12:00:00×1 /Phone/6.jpg@2025-09-15 12:00:00×1 /Phone/8.jpg@2026-03-01 12:00:00×1"
	if strings.Join(got, " ") != want || token != "" {
		t.Errorf("with before and newer_than:\n got %s (token %q)\nwant %s", strings.Join(got, " "), token, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err) // one query, no date in it
	}
}

// splitPages pages through the plain search, the jump to cutoff (before on
// its first page, then its token, as the grids ask) and the search up from
// it (newer_than on every page), each page with the have the grid holds,
// and checks that the jump's photos and those above them are the plain
// search's, every photo once, at the same copy: those above oldest first,
// so that, turned round, they sit right on top of the jump's.
func splitPages(t *testing.T, mg *Manager, ses *session.Session, cutoff time.Time, omit bool) (plain []string) {
	t.Helper()
	pages := func(before, newerThan *time.Time) []string {
		t.Helper()
		var got []string
		token, have := "", int32(0)
		for page := 0; ; page++ {
			b := before
			if token != "" {
				b = nil
			}
			files, next, err := mg.ImageSearch(ses, "", nil, token, false, nil, "", b, newerThan, have, 0, false, omit)
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range files {
				if omit == (f.Content != nil) {
					t.Errorf("%s: content %q (omit %v)", f.Path, f.Content, omit)
				}
				at := f.Created.AsTime()
				if (before != nil && at.After(cutoff)) || (newerThan != nil && !at.After(cutoff)) {
					t.Errorf("%s at %s is on the wrong side of %s", f.Path, at, cutoff)
				}
			}
			got = append(got, describePage(files)...)
			have += int32(len(files))
			if next == "" {
				return got
			}
			if page > 50 {
				t.Fatal("the pages don't end")
			}
			token = next
		}
	}
	plain = pages(nil, nil)
	down := pages(&cutoff, nil)
	up := pages(nil, &cutoff)
	seen := map[string]bool{}
	for _, p := range append(slices.Clone(down), up...) {
		name := p[:strings.Index(p, "@")]
		name = name[strings.LastIndex(name, "/")+1:] // the content: n.jpg at any path
		if seen[name] {
			t.Errorf("split at %s: photo %s twice", cutoff, name)
		}
		seen[name] = true
	}
	turned := slices.Clone(up)
	slices.Reverse(turned)
	if got, want := strings.Join(append(turned, down...), " "), strings.Join(plain, " "); got != want {
		t.Errorf("split at %s:\nup   %s\ndown %s\nwant %s", cutoff.Format(time.RFC3339Nano), strings.Join(up, " "), strings.Join(down, " "), want)
	}
	return plain
}

// splitCutoffs are the instants the splits are checked at: the end of
// June as the web sends it, its very second, a second before and after, and
// instants below and above every photo.
func splitCutoffs() []time.Time {
	return []time.Time{
		cMonthEnd.Add(999 * time.Millisecond), cMonthEnd, cMonthEnd.Add(-time.Second), cMonthEnd.Add(time.Second),
		cMonthEnd.Add(999_999_999), time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// The jump's search and the one up from it with the same instant split the
// library: the plain search's photos, each once (a photo at the instant's
// very second below it, one with copies on both sides at its oldest), in
// both page kinds. The rows are what the query answers (libraryRows, as
// dao's TestSearchMediaNewerThanSplitsWithBeforeMySQL checks on MariaDB).
func TestImageSearchNewerAndJumpSplitTheLibrary(t *testing.T) {
	bothPageKinds(t, func(t *testing.T, omit bool) {
		_, ses := galleryTestEnv(t)
		withDefaultPage(t, 2)
		base := 0x3300
		if omit {
			base += cOmittedBase
		}
		lib := newerLibrary()
		writeThumbs(t, ses, base, lib)
		for _, cutoff := range splitCutoffs() {
			db, mock, _ := sqlmock.New()
			mock.ExpectQuery("select `f`.`hash`, `f`.`mime`").WithoutArgs().WillReturnRows(libraryRows(base, lib, nil))
			mock.ExpectQuery("`f`.`created` <= \\?").WithArgs(cutoff.Truncate(time.Second)).WillReturnRows(libraryRows(base, lib, &cutoff))
			mock.ExpectQuery("select `f`.`hash`, `f`.`mime`").WithoutArgs().WillReturnRows(libraryRows(base, lib, nil))
			below, above := photosIn(lib, cutoff)
			pagesOf := func(n int) int { return (n + 1) / 2 }
			expectCopies(mock, pagesOf(below+above)+pagesOf(below)+pagesOf(above))
			mg := keptOut(&Manager{dao: dao.NewWithDB(db), searchTokens: newSearchTokenCache(1000)})
			splitPages(t, mg, ses, cutoff, omit)
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("split at %s: %v", cutoff, err)
			}
			db.Close()
		}
	})
}

// As above against a real MySQL/MariaDB (OTC_TEST_MYSQL_DSN, as dao's
// MySQL tests: a temporary `files` table shaped as db.sql's), with the
// copies counted there.
func TestImageSearchNewerAndJumpSplitTheLibraryMySQL(t *testing.T) {
	dsn := os.Getenv("OTC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("OTC_TEST_MYSQL_DSN not set")
	}
	bothPageKinds(t, func(t *testing.T, omit bool) {
		_, ses := galleryTestEnv(t)
		withDefaultPage(t, 2)
		base := 0x3400
		if omit {
			base += cOmittedBase
		}
		lib := newerLibrary()
		writeThumbs(t, ses, base, lib)
		db := mysqlFilesTable(t, dsn)
		for _, p := range lib {
			if _, err := db.Exec("insert into `files` (`hash`, `mime`, `created`, `modified`, `path`, `size`) values (?, 'image/jpeg', ?, ?, ?, 1)", fmt.Sprintf("%064x", base+p.n), p.at, p.at, p.path); err != nil {
				t.Fatal(err)
			}
		}
		mg := keptOut(&Manager{dao: dao.NewWithDB(db), searchTokens: newSearchTokenCache(1000)})
		for _, cutoff := range splitCutoffs() {
			plain := strings.Join(splitPages(t, mg, ses, cutoff, omit), " ")
			if !strings.Contains(plain, "/Mac/2.jpg@2025-06-30 23:59:59×2") || !strings.Contains(plain, "/Phone/3.jpg@2025-05-10 12:00:00×2") {
				t.Fatalf("the copies weren't counted: %s", plain)
			}
		}
	})
}

// mysqlFilesTable is a connection to dsn holding a temporary `files`
// table as db.sql makes it - nothing in that database is read or changed.
func mysqlFilesTable(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	c, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	c.ParseTime = true
	c.InterpolateParams = true
	conn, err := mysql.NewConnector(c)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(conn)
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1) // the temporary table is the connection's
	db.SetMaxIdleConns(1)
	if _, err := db.Exec("create temporary table `files` (" +
		"`hash` varchar(64) not null, `mime` varchar(150) not null, `created` datetime not null, `modified` datetime not null, " +
		"`path` varchar(768) character set utf8mb4 collate utf8mb4_bin not null, `size` bigint not null, `cloud_id` varchar(255) null, " +
		"key (`hash`), unique (`path`), index using btree (`created`), index using btree (`cloud_id`)" +
		") engine=InnoDB"); err != nil {
		t.Fatal(err)
	}
	return db
}
