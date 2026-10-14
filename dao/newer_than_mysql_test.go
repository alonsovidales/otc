// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"slices"
	"strings"
	"testing"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
)

// Issue #194, against a real MySQL/MariaDB (OTC_TEST_MYSQL_DSN, as
// out_of_images_mysql_test.go): a jump's half (SearchMedia with before)
// and the half above it (SearchMediaNewerThan) with the same instant hold
// every photo of a plain search exactly once, at the same copy, for every
// filter - photos with copies on both sides of the instant, at its very
// second and a second after, and instants with a fraction included.

func splitLibrary(t *testing.T) *Dao {
	t.Helper()
	db, d := outOfImagesTestDB(t)
	june := time.Date(2022, 6, 30, 23, 59, 59, 0, time.UTC) // June's last second
	july := june.Add(time.Second)
	insertMediaRows(t, db, "files",
		mediaRow{"A", "image/jpeg", "/Phone/a.jpg", at(2019, 6, 1)},
		mediaRow{"A", "image/jpeg", "/android/x/Download/a.jpg", at(2026, 10, 5)}, // synced back
		mediaRow{"A", "image/jpeg", "/Private/a.jpg", at(2018, 1, 1)},             // kept out
		mediaRow{"B", "image/jpeg", "/Phone/b.jpg", at(2025, 3, 1)},
		mediaRow{"C", "image/jpeg", "/Phone/c.jpg", at(2018, 1, 10)},
		mediaRow{"C", "image/jpeg", "/Mac/c.jpg", at(2018, 1, 10)},
		mediaRow{"D", "video/mp4", "/Phone/d.mp4", at(2024, 5, 1)},
		mediaRow{"D", "video/mp4", "/Mac/d.mp4", at(2026, 10, 6)},
		mediaRow{"E", "image/jpeg", "/Private/e.jpg", at(2026, 10, 7)},
		mediaRow{"M", "image/jpeg", "/Phone/m.jpg", june},
		mediaRow{"N", "image/jpeg", "/Phone/n.jpg", july},
		mediaRow{"S", "image/jpeg", "/Phone/s.jpg", july}, // S's copies straddle the month's end
		mediaRow{"S", "image/jpeg", "/Mac/s.jpg", june},
		mediaRow{"V", "video/mp4", "/Phone/v.mp4", july},
		mediaRow{"V", "video/mp4", "/Mac/v.mp4", june.Add(-time.Second)},
		mediaRow{"T", "text/plain", "/Phone/t.txt", july},
	)
	for _, q := range []string{
		"insert into `file_tags` values ('A', 'beach', 0.5), ('B', 'beach', 0.6), ('C', 'beach', 0.4), ('M', 'beach', 0.5), ('N', 'beach', 0.9), ('S', 'beach', 0.5), ('V', 'beach', 0.2)",
		"insert into `people` (`id`, `created`) values ('p1', now())",
		"insert into `faces` values ('f1', 'A', 'p1', 0, 0, 1, 1, '', '', now()), ('f2', 'M', 'p1', 0, 0, 1, 1, '', '', now()), ('f3', 'S', 'p1', 0, 0, 1, 1, '', '', now()), ('f4', 'N', 'p1', 0, 0, 1, 1, '', '', now())",
		"insert into `image_groups` values ('g1', 'Trip', now())",
		"insert into `image_group_files` values ('g1', 'A', now()), ('g1', 'D', now()), ('g1', 'S', now()), ('g1', 'N', now())",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func TestSearchMediaNewerThanSplitsWithBeforeMySQL(t *testing.T) {
	d := splitLibrary(t)
	private := []string{"/Private/"}
	type search struct {
		what     string
		path     string
		tags     []string
		people   []string
		group    string
		images   bool
		excluded []string
	}
	searches := []search{
		{what: "browsing", images: true, excluded: private},
		{what: "with videos", excluded: private},
		{what: "nothing kept out", images: true},
		{what: "a tag", tags: []string{"beach"}, images: true, excluded: private},
		{what: "a person", people: []string{"p1"}, images: true, excluded: private},
		{what: "a collection", group: "g1", images: true, excluded: private},
		{what: "a folder", path: "/Phone/", excluded: private},
	}
	plainOf := func(s search) []*pb.File {
		t.Helper()
		files, err := d.SearchMedia(s.path, s.tags, s.people, s.group, s.images, nil, s.excluded)
		if err != nil {
			t.Fatal(err)
		}
		return files
	}
	// Every instant a row is at, a second either side, and fractions of
	// its second: the web sends a month's end as .999.
	var instants []time.Time
	for _, f := range plainOf(search{what: "everything"}) {
		c := f.Created.AsTime()
		instants = append(instants, c, c.Add(-time.Second), c.Add(time.Second), c.Add(500*time.Millisecond), c.Add(999*time.Millisecond), c.Add(999_999_999))
	}
	instants = append(instants, at(1990, 1, 1), at(2040, 1, 1))

	for _, s := range searches {
		plain := plainOf(s)
		if len(plain) == 0 {
			t.Fatalf("%s: no photos", s.what)
		}
		for _, x := range instants {
			older, err := d.SearchMedia(s.path, s.tags, s.people, s.group, s.images, &x, s.excluded)
			if err != nil {
				t.Fatal(err)
			}
			newer, err := d.SearchMediaNewerThan(s.path, s.tags, s.people, s.group, s.images, x, s.excluded)
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range older {
				if f.Created.AsTime().After(x) {
					t.Errorf("%s, before %s: %s is after it", s.what, x, f.Path)
				}
			}
			for _, f := range newer {
				if !f.Created.AsTime().After(x) {
					t.Errorf("%s, newer than %s: %s isn't after it", s.what, x, f.Path)
				}
			}
			// Oldest first, by date then path.
			if !slices.IsSortedFunc(newer, func(a, b *pb.File) int {
				if c := a.Created.AsTime().Compare(b.Created.AsTime()); c != 0 {
					return c
				}
				return compareStrings(a.Path, b.Path)
			}) {
				t.Errorf("%s, newer than %s: not oldest first: %s", s.what, x, describeOrder(newer))
			}
			// Together, every photo once, at the same copy.
			both := append(append([]*pb.File{}, older...), newer...)
			if got, want := sortedDescriptions(both), sortedDescriptions(plain); got != want {
				t.Errorf("%s, split at %s:\nolder %s\nnewer %s\nplain %s", s.what, x.Format(time.RFC3339Nano), describeOrder(older), describeOrder(newer), describeOrder(plain))
			}
			// In date order the newer half, turned round, sits right above
			// the jump's: the plain search's order.
			if len(s.tags) == 0 {
				up := slices.Clone(newer)
				slices.Reverse(up)
				if got, want := describeOrder(append(up, older...)), describeOrder(plain); got != want {
					t.Errorf("%s, split at %s: in order\n got %s\nwant %s", s.what, x.Format(time.RFC3339Nano), got, want)
				}
			}
		}
	}
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// sortedDescriptions is files as describeOrder writes them, sorted: the
// same for the same photos at the same copies, and a photo twice shows.
func sortedDescriptions(files []*pb.File) string {
	var out []string
	for _, f := range files {
		out = append(out, describeOrder([]*pb.File{f}))
	}
	slices.Sort(out)
	return strings.Join(out, " ")
}
