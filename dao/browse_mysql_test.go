// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
)

// Issue #206, against a real MySQL/MariaDB (OTC_TEST_MYSQL_DSN, as
// search_files_mysql_test.go): BrowseMedia's chunks, one after the other,
// are SearchMedia's photos - the same rows, copy for copy, in the same
// order - going down, from a jump and going up.

// browseLibrary fills db's `files` with a library built to trip a keyset:
// many photos at one second (so a chunk ends inside a crowd), copies at
// the same second and at others, older copies kept out of Images, copies
// of another type, videos, documents, and paths with spaces, accents and
// capitals. Deterministic.
func browseLibrary(tb testing.TB, d *Dao) {
	tb.Helper()
	r := rand.New(rand.NewSource(206))
	base := time.Date(2024, 3, 1, 9, 0, 0, 0, time.UTC)
	// 180 seconds, a few of them very busy.
	second := func() time.Time {
		switch n := r.Intn(10); {
		case n < 2:
			return base.Add(time.Duration(r.Intn(3)) * time.Second) // the crowd
		default:
			return base.Add(time.Duration(r.Intn(180)) * time.Minute)
		}
	}
	folders := []string{"/Phone/DCIM/", "/Mac/Photos Library/", "/Private/", "/Ünïcode/Fotos/", "/phone/dcim/", "/Mac/a b/"}
	type row struct {
		hash, mime, path string
		at               time.Time
	}
	var rows []row
	seen := map[string]bool{}
	path := func(folder, name string) string {
		for {
			p := folder + name
			if !seen[p] {
				seen[p] = true
				return p
			}
			name = "x" + name
		}
	}
	for i := 0; i < 700; i++ {
		hash := fmt.Sprintf("%064x", 0x20600000+i)
		mime, ext := "image/jpeg", ".jpg"
		switch n := r.Intn(20); {
		case n == 0:
			mime, ext = "video/mp4", ".mp4"
		case n == 1:
			mime, ext = "application/pdf", ".pdf"
		case n == 2:
			mime, ext = "image/heic", ".HEIC"
		}
		at := second()
		rows = append(rows, row{hash, mime, path(folders[r.Intn(len(folders))], fmt.Sprintf("IMG_%04d%s", r.Intn(5000), ext)), at})
		// Copies: at the same second, earlier or later, anywhere - one
		// of another type sometimes (not media in a plain browse).
		for c := r.Intn(4) - 1; c > 0; c-- {
			cat := at
			switch r.Intn(3) {
			case 0:
				cat = second()
			case 1:
				cat = at.Add(time.Duration(r.Intn(3)-1) * time.Second)
			}
			cmime := mime
			if r.Intn(8) == 0 {
				cmime = "application/octet-stream"
			}
			rows = append(rows, row{hash, cmime, path(folders[r.Intn(len(folders))], fmt.Sprintf("copy %d of %d%s", c, i, ext)), cat})
		}
	}
	for start := 0; start < len(rows); start += 500 {
		batch := rows[start:min(start+500, len(rows))]
		var args []any
		for i, rw := range batch {
			args = append(args, rw.hash, rw.mime, rw.at, rw.at.Add(time.Duration(i)*time.Second), rw.path, int64(1000+start+i))
		}
		q := "insert into `files` (`hash`, `mime`, `created`, `modified`, `path`, `size`) values " +
			strings.TrimSuffix(strings.Repeat("(?, ?, ?, ?, ?, ?), ", len(batch)), ", ")
		if _, err := d.db.Exec(q, args...); err != nil {
			tb.Fatal(err)
		}
	}
}

// describeRows is files, every field a page carries from the query.
func describeRows(files []*pb.File) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, fmt.Sprintf("%s %s %s %s %s %d", f.Hash, f.Path, f.Mime,
			f.Created.AsTime().Format(time.DateTime), f.Modified.AsTime().Format(time.DateTime), FileSize(f)))
	}
	return out
}

// browseAll is every chunk of b, want photos at a time, checked against
// BrowseMedia's promises as it goes.
func browseAll(tb testing.TB, d *Dao, b *MediaBrowse, want int) []*pb.File {
	tb.Helper()
	var all []*pb.File
	var from *time.Time
	for chunks := 0; ; chunks++ {
		if chunks > 100000 {
			tb.Fatal("the chunks never end")
		}
		files, next, done, err := d.BrowseMedia(b, from, want)
		if err != nil {
			tb.Fatal(err)
		}
		all = append(all, files...)
		if done {
			if next != nil {
				tb.Fatalf("the last chunk has a next: %v", next)
			}
			return all
		}
		if len(files) <= want {
			tb.Fatalf("a chunk that isn't the last holds %d photos, asked for %d", len(files), want)
		}
		last := files[len(files)-1].Created.AsTime()
		if next == nil || !next.Equal(last) {
			tb.Fatalf("next %v, the chunk ends at %v", next, last)
		}
		from = next
	}
}

func sameRows(tb testing.TB, what string, got, want []*pb.File) {
	tb.Helper()
	g, w := describeRows(got), describeRows(want)
	if len(g) != len(w) {
		tb.Fatalf("%s: %d photos, want %d", what, len(g), len(w))
	}
	for i := range g {
		if g[i] != w[i] {
			tb.Fatalf("%s: photo %d is\n  %s\nwant\n  %s", what, i, g[i], w[i])
		}
	}
}

func TestBrowseMediaIsSearchMediaMySQL(t *testing.T) {
	db := searchTestDB(t)
	d := NewWithDB(db)
	browseLibrary(t, d)
	base := time.Date(2024, 3, 1, 9, 0, 0, 0, time.UTC)
	instants := []time.Time{
		base.Add(time.Second),                           // inside the crowd
		base.Add(time.Second + 999*time.Millisecond),    // a web jump's .999
		base.Add(90*time.Minute + 500*time.Millisecond), // the middle
		base.Add(-time.Hour),                            // before everything
		base.Add(48 * time.Hour),                        // after everything
	}
	for _, imagesOnly := range []bool{true, false} {
		for _, excluded := range [][]string{nil, {"/Private/"}, {"/Private/", "/Mac/"}} {
			kind := fmt.Sprintf("images only %v, kept out %v", imagesOnly, excluded)
			// Down, whole and from each jump.
			for _, before := range append([]*time.Time{nil}, ptrs(instants)...) {
				want, err := d.SearchMedia("", nil, nil, "", imagesOnly, before, excluded)
				if err != nil {
					t.Fatal(err)
				}
				if before == nil && len(want) < 300 {
					t.Fatalf("%s: only %d photos to browse", kind, len(want))
				}
				for _, n := range []int{0, 1, 5, 1000} {
					got := browseAll(t, d, &MediaBrowse{ImagesOnly: imagesOnly, Before: before, Excluded: excluded}, n)
					sameRows(t, fmt.Sprintf("%s, before %v, %d at a time", kind, before, n), got, want)
				}
			}
			// Up from each instant.
			for _, newer := range instants {
				want, err := d.SearchMediaNewerThan("", nil, nil, "", imagesOnly, newer, excluded)
				if err != nil {
					t.Fatal(err)
				}
				for _, n := range []int{0, 1, 5, 1000} {
					// Before is ignored going up, as SearchMediaNewerThan has none.
					got := browseAll(t, d, &MediaBrowse{ImagesOnly: imagesOnly, NewerThan: &newer, Before: &instants[0], Excluded: excluded}, n)
					sameRows(t, fmt.Sprintf("%s, newer than %v, %d at a time", kind, newer, n), got, want)
				}
			}
		}
	}
}

func ptrs(ts []time.Time) []*time.Time {
	out := make([]*time.Time, len(ts))
	for i := range ts {
		out[i] = &ts[i]
	}
	return out
}

// An empty library browses to nothing, done at once, either way.
func TestBrowseMediaOfNothingMySQL(t *testing.T) {
	d := NewWithDB(searchTestDB(t))
	now := time.Now()
	for _, b := range []*MediaBrowse{{}, {NewerThan: &now}} {
		files, next, done, err := d.BrowseMedia(b, nil, 30)
		if err != nil || len(files) != 0 || next != nil || !done {
			t.Errorf("%+v: %d photos, next %v, done %v, %v", b, len(files), next, done, err)
		}
	}
}

// The plain browse of a large library (OTC_TEST_BROWSE_LARGE=1): 200k
// photos over fifteen years with a tenth synced back to a second path
// years later (the newest rows of all: every one of them is passed over
// by the first chunk) and 30k documents. SearchMedia, the whole library
// at every new search, against BrowseMedia's first chunk: time and bytes
// allocated; then a whole walk, chunk by chunk, which must be the same
// photos in the same order, and the same from a jump and going up.
func TestBrowseMediaLargeMySQL(t *testing.T) {
	if os.Getenv("OTC_TEST_BROWSE_LARGE") != "1" {
		t.Skip("OTC_TEST_BROWSE_LARGE not set")
	}
	d := NewWithDB(searchTestDB(t))
	start := time.Now()
	n := fillLargeLibrary(t, d, 200000)
	t.Logf("library: %d rows in %v", n, time.Since(start).Round(time.Millisecond))
	if _, err := d.db.Exec("analyze table `files`"); err != nil {
		t.Fatal(err)
	}
	excluded := []string{"/Private/"}
	plain := &MediaBrowse{ImagesOnly: false, Excluded: excluded}

	// Warm the buffer pool as a device's would be after its first search.
	if _, err := d.SearchMedia("", nil, nil, "", false, nil, excluded); err != nil {
		t.Fatal(err)
	}
	var old []*pb.File
	oldTime, oldBytes := measure(t, 3, func() {
		var err error
		if old, err = d.SearchMedia("", nil, nil, "", false, nil, excluded); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("SearchMedia (every new search before #206): %d photos, %v, %.1f MB allocated", len(old), oldTime, float64(oldBytes)/1e6)
	for _, want := range []int{12, 30, 120, 200} {
		var files []*pb.File
		took, bytes := measure(t, 5, func() {
			var err error
			if files, _, _, err = d.BrowseMedia(plain, nil, want); err != nil {
				t.Fatal(err)
			}
		})
		t.Logf("BrowseMedia's first chunk of %d: %d photos, %v, %.1f KB allocated", want, len(files), took, float64(bytes)/1e3)
		sameRows(t, "the first chunk", files, old[:len(files)])
	}

	// The whole library, chunk by chunk.
	for _, want := range []int{30, 120} {
		var got []*pb.File
		took, _ := measure(t, 1, func() { got = browseAll(t, d, plain, want) })
		t.Logf("a whole walk %d at a time: %v", want, took)
		sameRows(t, fmt.Sprintf("a whole walk %d at a time", want), got, old)
	}
	// A jump to the middle and up from it.
	mid := old[len(old)/2].Created.AsTime()
	jump, err := d.SearchMedia("", nil, nil, "", false, &mid, excluded)
	if err != nil {
		t.Fatal(err)
	}
	took, _ := measure(t, 3, func() {
		if _, _, _, err := d.BrowseMedia(&MediaBrowse{Before: &mid, Excluded: excluded}, nil, 30); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("the first chunk after a jump to the middle: %v", took)
	sameRows(t, "from a jump", browseAll(t, d, &MediaBrowse{Before: &mid, Excluded: excluded}, 120), jump)
	up, err := d.SearchMediaNewerThan("", nil, nil, "", false, mid, excluded)
	if err != nil {
		t.Fatal(err)
	}
	took, _ = measure(t, 3, func() {
		if _, _, _, err := d.BrowseMedia(&MediaBrowse{NewerThan: &mid, Excluded: excluded}, nil, 30); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("the first chunk going up from the middle: %v", took)
	sameRows(t, "going up", browseAll(t, d, &MediaBrowse{NewerThan: &mid, Excluded: excluded}, 120), up)
	// Resuming a search the device no longer holds a token for, 5000 in.
	took, _ = measure(t, 3, func() {
		if _, _, _, err := d.BrowseMedia(plain, nil, 5000+30); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("a first chunk past 5000 photos (a token gone): %v", took)
}

// fillLargeLibrary adds n photos over fifteen years (two at one second
// now and then), a tenth of them synced back years later under
// /Backup/ and a fiftieth also under /Private/ (kept out), and n/7
// documents; it answers the rows added.
func fillLargeLibrary(tb testing.TB, d *Dao, n int) int {
	tb.Helper()
	first := time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC)
	span := 15 * 365 * 24 * time.Hour
	type row struct {
		hash, mime, path string
		at               time.Time
	}
	var batch []row
	rows := 0
	flush := func() {
		if len(batch) == 0 {
			return
		}
		var args []any
		for _, r := range batch {
			args = append(args, r.hash, r.mime, r.at, r.at, r.path, int64(len(r.path)))
		}
		q := "insert into `files` (`hash`, `mime`, `created`, `modified`, `path`, `size`) values " +
			strings.TrimSuffix(strings.Repeat("(?, ?, ?, ?, ?, ?), ", len(batch)), ", ")
		if _, err := d.db.Exec(q, args...); err != nil {
			tb.Fatal(err)
		}
		rows += len(batch)
		batch = batch[:0]
	}
	add := func(r row) {
		batch = append(batch, r)
		if len(batch) == 1000 {
			flush()
		}
	}
	for i := 0; i < n; i++ {
		hash := fmt.Sprintf("%064x", 0x2060000000+i)
		k := i
		if i%11 == 10 {
			k = i - 1 // a burst: two photos at one second
		}
		at := first.Add(time.Duration(int64(span) / int64(n) * int64(k))).Truncate(time.Second)
		mime, ext := "image/jpeg", ".jpg"
		if i%20 == 0 {
			mime, ext = "video/mp4", ".mp4"
		}
		add(row{hash, mime, fmt.Sprintf("/Phone/DCIM/%07d%s", i, ext), at})
		if i%10 == 3 {
			add(row{hash, mime, fmt.Sprintf("/Backup/%07d%s", i, ext), time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second)})
		}
		if i%50 == 7 {
			add(row{hash, mime, fmt.Sprintf("/Private/%07d%s", i, ext), at.Add(-time.Hour)})
		}
	}
	for i := 0; i < n/7; i++ {
		at := first.Add(time.Duration(int64(span) / int64(n/7) * int64(i))).Truncate(time.Second)
		add(row{fmt.Sprintf("%064x", 0x3060000000+i), "application/pdf", fmt.Sprintf("/Docs/%07d.pdf", i), at})
	}
	flush()
	return rows
}

// measure runs f runs times and answers its median time and the bytes it
// allocated (the median run's).
func measure(tb testing.TB, runs int, f func()) (time.Duration, uint64) {
	tb.Helper()
	type run struct {
		took  time.Duration
		bytes uint64
	}
	var all []run
	for i := 0; i < runs; i++ {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		t0 := time.Now()
		f()
		took := time.Since(t0)
		runtime.ReadMemStats(&after)
		all = append(all, run{took, after.TotalAlloc - before.TotalAlloc})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].took < all[j].took })
	m := all[len(all)/2]
	return m.took.Round(100 * time.Microsecond), m.bytes
}

// FilesOfHashes (ResumePendingAnalysis' files, issue #206): each hash's
// row with the smallest path, past cHashChunk hashes; a hash no row holds
// is left out.
func TestFilesOfHashesMySQL(t *testing.T) {
	d := NewWithDB(searchTestDB(t))
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var hashes []string
	for i := 0; i < cHashChunk+20; i++ {
		h := fmt.Sprintf("%064x", 0x2061000+i)
		hashes = append(hashes, h)
		for _, p := range []string{"/b/", "/a/"} {
			if _, err := d.db.Exec("insert into `files` (`hash`, `mime`, `created`, `modified`, `path`, `size`) values (?, 'image/jpeg', ?, ?, ?, 7)",
				h, at, at, fmt.Sprintf("%s%d.jpg", p, i)); err != nil {
				t.Fatal(err)
			}
		}
	}
	gone := fmt.Sprintf("%064x", 0x2062000)
	files, err := d.FilesOfHashes(append(hashes, gone))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(hashes) || files[gone] != nil {
		t.Fatalf("%d files for %d hashes (gone: %v)", len(files), len(hashes), files[gone])
	}
	for i, h := range hashes {
		f := files[h]
		if f == nil || f.Path != fmt.Sprintf("/a/%d.jpg", i) || f.Mime != "image/jpeg" || FileSize(f) != 7 || !f.Created.AsTime().Equal(at) {
			t.Fatalf("%s: %v", h, f)
		}
	}
}
