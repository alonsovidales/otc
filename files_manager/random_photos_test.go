// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// A slideshow's pages (RandomPhotos, random_photos.go) over MySQL/MariaDB
// (OTC_TEST_MYSQL_DSN): every processed photo of the round once, in the
// round's order, at the copy Images shows; never one not processed yet, a
// video or what is kept out; the small thumbnails as content when asked;
// and a key no page gave ends the round.
func TestRandomPhotosPagesMySQL(t *testing.T) {
	dsn := os.Getenv("OTC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("OTC_TEST_MYSQL_DSN not set")
	}
	_, ses := galleryTestEnv(t)
	db := mysqlFilesTable(t, dsn)
	if _, err := db.Exec("create temporary table `image_group_files` (`group_id` varchar(36) not null, `hash` varchar(64) not null, " +
		"`added` datetime not null, primary key (`group_id`, `hash`), key (`hash`)) engine=InnoDB"); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	h := func(n int) string { return fmt.Sprintf("%064x", 0x7a5000+n) }
	add := func(hash, mime, path string, created time.Time) {
		t.Helper()
		if _, err := db.Exec("insert into `files` (`hash`, `mime`, `created`, `modified`, `path`, `size`) values (?, ?, ?, ?, ?, 1)",
			hash, mime, created, created, path); err != nil {
			t.Fatal(err)
		}
	}
	smallOnes := map[string]bool{}
	thumbs := func(hash string, small bool) {
		t.Helper()
		smallOnes[hash] = small
		if err := blobstore.WriteBytes(thumbnailPath(hash), ses, []byte("big-"+hash[58:])); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Remove(thumbnailPath(hash)) })
		if small {
			if err := blobstore.WriteBytes(smallThumbnailPath(hash), ses, []byte("small-"+hash[58:])); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Remove(smallThumbnailPath(hash)) })
		}
	}
	var processed []string
	for i := range 25 {
		add(h(i), "image/jpeg", fmt.Sprintf("/Phone/IMG_%02d.jpg", i), at.Add(time.Duration(i)*time.Minute))
		if i == 4 {
			continue // still being processed: no thumbnail yet
		}
		thumbs(h(i), i%2 == 0)
		processed = append(processed, h(i))
	}
	// Photo 3 synced back later at another path: shown at its oldest copy.
	add(h(3), "image/jpeg", "/Downloads/IMG_03.jpg", at.Add(48*time.Hour))
	// A video and a photo kept out.
	add(h(50), "video/mp4", "/Phone/VID_50.mp4", at)
	thumbs(h(50), true)
	add(h(51), "image/jpeg", "/Private/IMG_51.jpg", at)
	thumbs(h(51), true)
	for _, n := range []int{0, 3, 4, 6, 50, 51} {
		if _, err := db.Exec("insert into `image_group_files` (`group_id`, `hash`, `added`) values ('trip', ?, ?)", h(n), at); err != nil {
			t.Fatal(err)
		}
	}
	mg := keptOut(&Manager{dao: dao.NewWithDB(db), searchTokens: newSearchTokenCache(100)}, "/Private/")

	totals := map[string]int32{}
	round := func(group, seed string, limit int32, small bool) []*pb.File {
		t.Helper()
		var all []*pb.File
		after := ""
		for pages := 0; pages < 100; pages++ {
			files, token, total, err := mg.RandomPhotos(ses, group, seed, after, limit, small)
			if err != nil {
				t.Fatal(err)
			}
			if after == "" {
				totals[group] = total
			} else if total != 0 {
				t.Fatalf("a page after the first says the round holds %d", total)
			}
			if limit > 0 && len(files) > int(limit) {
				t.Fatalf("a page of %d holds %d", limit, len(files))
			}
			all = append(all, files...)
			if token == "" {
				return all
			}
			after = token
		}
		t.Fatal("the round never ends")
		return nil
	}
	hashes := func(files []*pb.File) []string {
		out := make([]string, len(files))
		for i, f := range files {
			out[i] = f.Hash
		}
		return out
	}

	first := round("", "5eed", 4, true)
	// The round's total counts the photo not processed yet, which no page
	// gives.
	if totals[""] != int32(len(processed)+1) {
		t.Fatalf("the round says it holds %d, want %d", totals[""], len(processed)+1)
	}
	if got, want := slices.Sorted(slices.Values(hashes(first))), slices.Sorted(slices.Values(processed)); !slices.Equal(got, want) {
		t.Fatalf("the round gave\n%v\nwant every processed photo once\n%v", got, want)
	}
	for _, f := range first {
		if f.Hash == h(3) && f.Path != "/Phone/IMG_03.jpg" {
			t.Fatalf("photo 3 at %s, not its oldest copy", f.Path)
		}
		wantSmall := smallOnes[f.Hash]
		if f.GetThumbnailSmall() != wantSmall {
			t.Fatalf("%s: thumbnail_small %v", f.Path, f.GetThumbnailSmall())
		}
		prefix := "big-"
		if wantSmall {
			prefix = "small-"
		}
		if string(f.Content) != prefix+f.Hash[58:] {
			t.Fatalf("%s came with %q", f.Path, f.Content)
		}
	}
	// The same round in other pages, without content; another round in
	// another order.
	again := round("", "5eed", 0, false)
	if !slices.Equal(hashes(again), hashes(first)) {
		t.Fatal("the same seed in pages of 20 gave another order")
	}
	for _, f := range again {
		if f.Content != nil || f.ThumbnailSmall != nil {
			t.Fatalf("%s came with content without small_thumbnails", f.Path)
		}
	}
	if other := round("", "0ther", 7, false); slices.Equal(hashes(other), hashes(first)) {
		t.Fatal("another seed gave the same order")
	}
	// A collection: its processed photos, not its video, its unprocessed
	// photo or the one kept out.
	trip := round("trip", "5eed", 2, false)
	if got, want := slices.Sorted(slices.Values(hashes(trip))), []string{h(0), h(3), h(6)}; !slices.Equal(got, want) {
		t.Fatalf("the collection's round gave %v, want %v", got, want)
	}
	if totals["trip"] != 4 { // and the photo not processed yet
		t.Fatalf("the collection's round says it holds %d, want 4", totals["trip"])
	}
	// A key no page gave: the round is over.
	files, token, _, err := mg.RandomPhotos(ses, "", "5eed", "not a key", 5, false)
	if err != nil || len(files) != 0 || token != "" {
		t.Fatalf("after a bad key: %v %q %v", files, token, err)
	}
}
