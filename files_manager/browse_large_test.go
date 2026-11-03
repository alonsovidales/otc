// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"database/sql"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/dao"
)

// Issue #206 on a large library (OTC_TEST_BROWSE_LARGE=1, against
// OTC_TEST_MYSQL_DSN as the other MySQL tests): 200k photos over fifteen
// years, a tenth synced back to a second path years later (the newest
// rows of all) and a fiftieth also kept out, and documents. Images' first
// page as a whole search served it and as the keyset serves it: the time,
// what the search allocated and what its token keeps; then the first
// pages of both, which must be the same, thumbnails, copies and all.
func TestImageSearchKeysetLargeMySQL(t *testing.T) {
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
	fillLibrary(t, db, 200000)
	d := dao.NewWithDB(db)
	excluded := []string{"/Private/"}
	shown, err := d.SearchMedia("", nil, nil, "", true, nil, excluded)
	if err != nil {
		t.Fatal(err)
	}
	const pages = 12 + 4*30
	for _, f := range shown[:pages+60] {
		if err := blobstore.WriteBytes(thumbnailPath(f.Hash), ses, []byte("thumb")); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { removeThumbnails(blobPath(f.Hash)) })
	}

	walk := func(mg *Manager) []string {
		var got []string
		token, have := "", int32(0)
		for page := 0; page < 5; page++ {
			limit := int32(0)
			if page == 0 {
				limit = 12
			}
			files, next, err := mg.ImageSearch(ses, "", nil, token, false, nil, "", nil, nil, have, limit, false, false)
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
	for _, kind := range []struct {
		name   string
		keyset bool
	}{{"whole search (before #206)", false}, {"keyset", true}} {
		var took []time.Duration
		var allocated, kept []uint64
		for run := 0; run < 5; run++ {
			mg := keptOut(&Manager{dao: d, searchTokens: newSearchTokenCache(cSearchTokensMaxRows)}, excluded...)
			if kind.keyset {
				mg.browse = d.BrowseMedia
			}
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			t0 := time.Now()
			files, tok, err := mg.ImageSearch(ses, "", nil, "", false, nil, "", nil, nil, 0, 12, false, false)
			took = append(took, time.Since(t0))
			if err != nil || len(files) != 12 || tok == "" {
				t.Fatalf("%s: %d photos, token %q, %v", kind.name, len(files), tok, err)
			}
			runtime.ReadMemStats(&after)
			allocated = append(allocated, after.TotalAlloc-before.TotalAlloc)
			runtime.GC()
			runtime.ReadMemStats(&after)
			kept = append(kept, after.HeapAlloc-min(after.HeapAlloc, before.HeapAlloc))
			runtime.KeepAlive(mg)
		}
		slices.Sort(took)
		slices.Sort(allocated)
		slices.Sort(kept)
		t.Logf("%s: first page of 12 in %v (median of 5; %v-%v), %.2f MB allocated, %.2f MB kept by its token",
			kind.name, took[2].Round(100*time.Microsecond), took[0].Round(100*time.Microsecond), took[4].Round(100*time.Microsecond),
			float64(allocated[2])/1e6, float64(kept[2])/1e6)
	}
	whole := keptOut(&Manager{dao: d, searchTokens: newSearchTokenCache(cSearchTokensMaxRows)}, excluded...)
	keyset := keptOut(&Manager{dao: d, searchTokens: newSearchTokenCache(cSearchTokensMaxRows), browse: d.BrowseMedia}, excluded...)
	want, got := walk(whole), walk(keyset)
	if len(want) != pages {
		t.Fatalf("the whole search's pages hold %d photos, want %d", len(want), pages)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		for i := range min(len(got), len(want)) {
			if got[i] != want[i] {
				t.Fatalf("photo %d: keyset %s, whole %s", i, got[i], want[i])
			}
		}
		t.Fatalf("keyset %d photos, whole %d", len(got), len(want))
	}
}

// fillLibrary adds n photos over fifteen years (two at one second now and
// then, a video in twenty), a tenth synced back years later under
// /Backup/, a fiftieth also under /Private/ an hour before (kept out), and
// n/7 documents.
func fillLibrary(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	first := time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC)
	span := int64(15 * 365 * 24 * time.Hour)
	var args []any
	rows := 0
	flush := func() {
		if rows == 0 {
			return
		}
		q := "insert into `files` (`hash`, `mime`, `created`, `modified`, `path`, `size`) values " +
			strings.TrimSuffix(strings.Repeat("(?, ?, ?, ?, ?, 1), ", rows), ", ")
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
		args, rows = args[:0], 0
	}
	add := func(hash, mime, path string, at time.Time) {
		args = append(args, hash, mime, at, at, path)
		if rows++; rows == 1000 {
			flush()
		}
	}
	for i := 0; i < n; i++ {
		k := i
		if i%11 == 10 {
			k = i - 1
		}
		at := first.Add(time.Duration(span / int64(n) * int64(k))).Truncate(time.Second)
		hash := fmt.Sprintf("%064x", 0x2060000000+i)
		mime, ext := "image/jpeg", ".jpg"
		if i%20 == 0 {
			mime, ext = "video/mp4", ".mp4"
		}
		add(hash, mime, fmt.Sprintf("/Phone/DCIM/%07d%s", i, ext), at)
		if i%10 == 3 {
			add(hash, mime, fmt.Sprintf("/Backup/%07d%s", i, ext), time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i)*time.Second))
		}
		if i%50 == 7 {
			add(hash, mime, fmt.Sprintf("/Private/%07d%s", i, ext), at.Add(-time.Hour))
		}
	}
	for i := 0; i < n/7; i++ {
		at := first.Add(time.Duration(span / int64(n/7) * int64(i))).Truncate(time.Second)
		add(fmt.Sprintf("%064x", 0x3060000000+i), "application/pdf", fmt.Sprintf("/Docs/%07d.pdf", i), at)
	}
	flush()
	if _, err := db.Exec("analyze table `files`"); err != nil {
		t.Fatal(err)
	}
}
