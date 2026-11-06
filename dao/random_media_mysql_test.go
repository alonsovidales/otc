// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"fmt"
	"os"
	"slices"
	"testing"
	"time"
)

// A slideshow's random order (RandomPhotos, random_media.go), against a
// real MySQL/MariaDB (OTC_TEST_MYSQL_DSN, as search_files_mysql_test.go):
// a round gives every photo of the library, or of a collection, once -
// whatever the page size, in the same order for the same seed and in
// another for another seed - and never a video, a document, a content
// whose only rows are kept out of Images, or what isn't the collection's.

// randomLibrary fills db's temporary `files` and `image_group_files`:
// photos (some at two paths, one of them kept out), photos only in the
// kept-out folder, videos and documents; the collection "trip" holds
// photos, a video, a kept-out photo and a content with no row.
func randomLibrary(t *testing.T) (d *Dao, photos, trip []string) {
	t.Helper()
	db := searchTestDB(t)
	if _, err := db.Exec("create temporary table `image_group_files` (`group_id` varchar(36) not null, `hash` varchar(64) not null, " +
		"`added` datetime not null, primary key (`group_id`, `hash`), key (`hash`)) engine=InnoDB"); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	add := func(hash, mime, path string) {
		t.Helper()
		if _, err := db.Exec("insert into `files` (`hash`, `mime`, `created`, `modified`, `path`, `size`) values (?, ?, ?, ?, ?, 1)",
			hash, mime, at, at, path); err != nil {
			t.Fatal(err)
		}
	}
	member := func(hash string) {
		t.Helper()
		if _, err := db.Exec("insert into `image_group_files` (`group_id`, `hash`, `added`) values ('trip', ?, ?)", hash, at); err != nil {
			t.Fatal(err)
		}
	}
	h := func(n int) string { return fmt.Sprintf("%064x", 0x5d00+n) }
	for i := range 60 {
		add(h(i), "image/jpeg", fmt.Sprintf("/Phone/DCIM/IMG_%04d.jpg", i))
		photos = append(photos, h(i))
		if i%7 == 0 {
			// The same photo downloaded and synced back, and kept out.
			add(h(i), "image/jpeg", fmt.Sprintf("/Private/IMG_%04d.jpg", i))
		}
		if i%3 == 0 {
			member(h(i))
			trip = append(trip, h(i))
		}
	}
	for i := 100; i < 105; i++ {
		add(h(i), "image/png", fmt.Sprintf("/Private/scan-%d.png", i)) // only kept out
		add(h(i+10), "video/mp4", fmt.Sprintf("/Phone/DCIM/VID_%d.mp4", i))
		add(h(i+20), "application/pdf", fmt.Sprintf("/Documents/%d.pdf", i))
	}
	member(h(100)) // only kept out
	member(h(110)) // a video
	member(h(130)) // no row at all
	return NewWithDB(db), photos, trip
}

// roundOf pages through seed's round n at a time.
func roundOf(t *testing.T, d *Dao, group, seed string, excluded []string, n int) []string {
	t.Helper()
	var all []string
	after := ""
	for pages := 0; ; pages++ {
		if pages > 200 {
			t.Fatal("the round never ends")
		}
		hashes, last, done, err := d.RandomMedia(group, excluded, seed, after, n)
		if err != nil {
			t.Fatal(err)
		}
		if len(hashes) > n {
			t.Fatalf("a page of %d holds %d", n, len(hashes))
		}
		if !RandomKeyValid(last) {
			t.Fatalf("a page's last key %q isn't one", last)
		}
		all = append(all, hashes...)
		if done {
			return all
		}
		if last == after {
			t.Fatal("a page that isn't the last didn't move")
		}
		after = last
	}
}

func TestRandomMediaRoundsMySQL(t *testing.T) {
	if os.Getenv("OTC_TEST_MYSQL_DSN") == "" {
		t.Skip("OTC_TEST_MYSQL_DSN not set")
	}
	d, photos, trip := randomLibrary(t)
	excluded := []string{"/Private/"}
	for _, c := range []struct {
		name, group string
		want        []string
	}{
		{"the library", "", photos},
		{"a collection", "trip", trip},
	} {
		t.Run(c.name, func(t *testing.T) {
			first := roundOf(t, d, c.group, "a1b2c3d4", excluded, 7)
			if n, err := d.RandomMediaCount(c.group, excluded); err != nil || n != len(c.want) {
				t.Fatalf("the round's count: %d %v, want %d", n, err, len(c.want))
			}
			if got, want := slices.Sorted(slices.Values(first)), slices.Sorted(slices.Values(c.want)); !slices.Equal(got, want) {
				t.Fatalf("a round gave %d contents, want every photo once (%d):\n%v\n%v", len(got), len(want), got, want)
			}
			// Pages of any size are the same round.
			for _, n := range []int{1, 3, 50, 1000} {
				if got := roundOf(t, d, c.group, "a1b2c3d4", excluded, n); !slices.Equal(got, first) {
					t.Fatalf("pages of %d: another order", n)
				}
			}
			// Another seed, another order (60 or 20 photos: the same order
			// by chance is out of the question).
			other := roundOf(t, d, c.group, "ffee0011", excluded, 7)
			if slices.Equal(other, first) {
				t.Fatal("another seed gave the same order")
			}
			if !slices.Equal(slices.Sorted(slices.Values(other)), slices.Sorted(slices.Values(first))) {
				t.Fatal("another seed gave other photos")
			}
		})
	}

	// Nothing kept out: the photos only in /Private count too.
	all := roundOf(t, d, "", "a1b2c3d4", nil, 10)
	if len(all) != len(photos)+5 {
		t.Fatalf("with nothing kept out a round gave %d, want %d", len(all), len(photos)+5)
	}
	// A collection nobody has, and a page of nothing.
	if got := roundOf(t, d, "nobody", "a1b2c3d4", excluded, 10); len(got) != 0 {
		t.Fatalf("an empty collection gave %v", got)
	}
	if hashes, _, done, err := d.RandomMedia("", excluded, "x", "", 0); err != nil || len(hashes) != 0 || !done {
		t.Fatalf("a page of none: %v %v %v", hashes, done, err)
	}
	// The last key of a round's last page: nothing after it.
	if hashes, _, done, err := d.RandomMedia("", excluded, "x", "ffffffffffffffffffffffffffffffff"+fmt.Sprintf("%064x", 0), 5); err != nil || len(hashes) != 0 || !done {
		t.Fatalf("after the last key: %v %v %v", hashes, done, err)
	}
}

// What a page costs on a big library: OTC_TEST_RANDOM_LARGE=1, 50,000
// photos and 10,000 other rows.
func TestRandomMediaLargeMySQL(t *testing.T) {
	if os.Getenv("OTC_TEST_MYSQL_DSN") == "" || os.Getenv("OTC_TEST_RANDOM_LARGE") == "" {
		t.Skip("OTC_TEST_MYSQL_DSN and OTC_TEST_RANDOM_LARGE not set")
	}
	db := searchTestDB(t)
	at := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	for start := 0; start < 60000; start += 1000 {
		q := "insert into `files` (`hash`, `mime`, `created`, `modified`, `path`, `size`) values "
		var args []any
		for i := start; i < start+1000; i++ {
			mime := "image/jpeg"
			if i >= 50000 {
				mime = "application/pdf"
			}
			if i > start {
				q += ", "
			}
			q += "(?, ?, ?, ?, ?, 1)"
			args = append(args, fmt.Sprintf("%064x", i), mime, at, at, fmt.Sprintf("/Phone/%06d.jpg", i))
		}
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	d := NewWithDB(db)
	after := ""
	for page := range 3 {
		started := time.Now()
		hashes, last, _, err := d.RandomMedia("", []string{"/Private/"}, "seed", after, 20)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("page %d: %d photos in %v", page, len(hashes), time.Since(started))
		after = last
	}
}
