// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/go-sql-driver/mysql"
)

// Issue #201, against a real MySQL/MariaDB (OTC_TEST_MYSQL_DSN, as
// out_of_images_mysql_test.go): a photo stored at several paths is one
// entry in Images, at its oldest copy among those Images may show, and
// counts once in the buckets and collections.

func at(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 12, 0, 0, 0, time.UTC) }

// copiesLibrary is a library where photos came back at other paths.
func copiesLibrary(t *testing.T) *Dao {
	t.Helper()
	db, d := outOfImagesTestDB(t)
	insertMediaRows(t, db, "files",
		// A: taken in 2019, downloaded to a phone and synced back in 2026,
		// and an even older copy in a folder kept out of Images.
		mediaRow{"A", "image/jpeg", "/Phone/a.jpg", at(2019, 6, 1)},
		mediaRow{"A", "image/jpeg", "/android/x/Download/a.jpg", at(2026, 10, 5)},
		mediaRow{"A", "image/jpeg", "/Private/a.jpg", at(2018, 1, 1)},
		mediaRow{"B", "image/jpeg", "/Phone/b.jpg", at(2025, 3, 1)},
		// C: two copies at the same instant: the smaller path.
		mediaRow{"C", "image/jpeg", "/Phone/c.jpg", at(2018, 1, 10)},
		mediaRow{"C", "image/jpeg", "/Mac/c.jpg", at(2018, 1, 10)},
		mediaRow{"D", "video/mp4", "/Phone/d.mp4", at(2024, 5, 1)},
		mediaRow{"D", "video/mp4", "/Mac/d.mp4", at(2026, 10, 6)},
		mediaRow{"E", "image/jpeg", "/Private/e.jpg", at(2026, 10, 7)}, // only kept out
		mediaRow{"T", "text/plain", "/Phone/t.txt", at(2026, 10, 7)},
	)
	// An older version holding A isn't a copy.
	insertMediaRows(t, db, "file_versions", mediaRow{"A", "image/jpeg", "/Backup/old.jpg", at(2017, 1, 1)})
	for _, q := range []string{
		// Before #201 a tag's score was summed over A's two shown copies
		// (1.0) and put it first.
		"insert into `file_tags` values ('A', 'beach', 0.5), ('B', 'beach', 0.6), ('C', 'beach', 0.4), ('C', 'sunset', 0.3)",
		"insert into `people` (`id`, `created`) values ('p1', now()), ('p2', now())",
		"insert into `faces` values ('f1', 'A', 'p1', 0, 0, 1, 1, '', '', now()), ('f2', 'A', 'p2', 0, 0, 1, 1, '', '', now()), ('f3', 'B', 'p1', 0, 0, 1, 1, '', '', now()), ('f4', 'C', 'p2', 0, 0, 1, 1, '', '', now()), ('f5', 'A', 'p1', 5, 5, 1, 1, '', '', now())",
		"insert into `image_groups` values ('g1', 'Trip', now())",
		"insert into `image_group_files` values ('g1', 'A', now()), ('g1', 'D', now())",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func describeFiles(files []*pb.File) string {
	var out []string
	for _, f := range files {
		out = append(out, f.Hash+"="+f.Path+"@"+f.Created.AsTime().Format("2006-01-02"))
	}
	return strings.Join(out, " ")
}

func TestSearchMediaShowsEachPhotoAtItsOldestCopyMySQL(t *testing.T) {
	d := copiesLibrary(t)
	private := []string{"/Private/"}
	search := func(tags, people []string, group string, imagesOnly bool, before *time.Time, excluded []string) string {
		t.Helper()
		files, err := d.SearchMedia("", tags, people, group, imagesOnly, before, excluded)
		if err != nil {
			t.Fatal(err)
		}
		return describeFiles(files)
	}
	for _, c := range []struct {
		what     string
		tags     []string
		people   []string
		group    string
		images   bool
		before   *time.Time
		excluded []string
		want     string
	}{
		{what: "browsing", images: true, excluded: private,
			want: "B=/Phone/b.jpg@2025-03-01 A=/Phone/a.jpg@2019-06-01 C=/Mac/c.jpg@2018-01-10"},
		{what: "with videos", excluded: private,
			want: "B=/Phone/b.jpg@2025-03-01 D=/Phone/d.mp4@2024-05-01 A=/Phone/a.jpg@2019-06-01 C=/Mac/c.jpg@2018-01-10"},
		{what: "nothing kept out: the older copy there", images: true,
			want: "E=/Private/e.jpg@2026-10-07 B=/Phone/b.jpg@2025-03-01 C=/Mac/c.jpg@2018-01-10 A=/Private/a.jpg@2018-01-01"},
		// A jump between A's copies: A once, below the cutoff, at its
		// oldest copy - never the 2026 one above it.
		{what: "before 2020", images: true, before: ptr(at(2020, 1, 1)), excluded: private,
			want: "A=/Phone/a.jpg@2019-06-01 C=/Mac/c.jpg@2018-01-10"},
		{what: "before A's oldest shown copy", images: true, before: ptr(at(2019, 1, 1)), excluded: private,
			want: "C=/Mac/c.jpg@2018-01-10"},
		{what: "before the end", images: true, before: ptr(at(2026, 12, 1)), excluded: private,
			want: "B=/Phone/b.jpg@2025-03-01 A=/Phone/a.jpg@2019-06-01 C=/Mac/c.jpg@2018-01-10"},
		{what: "a tag, by the photo's own score", tags: []string{"beach"}, images: true, excluded: private,
			want: "B=/Phone/b.jpg@2025-03-01 A=/Phone/a.jpg@2019-06-01 C=/Mac/c.jpg@2018-01-10"},
		{what: "two tags, summed", tags: []string{"beach", "sunset"}, images: true, excluded: private,
			want: "C=/Mac/c.jpg@2018-01-10 B=/Phone/b.jpg@2025-03-01 A=/Phone/a.jpg@2019-06-01"},
		{what: "two people: both in the same photo", people: []string{"p1", "p2"}, images: true, excluded: private,
			want: "A=/Phone/a.jpg@2019-06-01"},
		{what: "one person", people: []string{"p1"}, images: true, excluded: private,
			want: "B=/Phone/b.jpg@2025-03-01 A=/Phone/a.jpg@2019-06-01"},
		{what: "a collection, videos in it", group: "g1", images: true, excluded: private,
			want: "D=/Phone/d.mp4@2024-05-01 A=/Phone/a.jpg@2019-06-01"},
		{what: "every filter", tags: []string{"beach"}, people: []string{"p1"}, group: "g1", images: true, before: ptr(at(2020, 1, 1)), excluded: private,
			want: "A=/Phone/a.jpg@2019-06-01"},
	} {
		if got := search(c.tags, c.people, c.group, c.images, c.before, c.excluded); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.what, got, c.want)
		}
	}

	// A folder's gallery: its own copies only.
	files, err := d.SearchMedia("/Phone/", nil, nil, "", false, nil, private)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := describeFiles(files), "B=/Phone/b.jpg@2025-03-01 D=/Phone/d.mp4@2024-05-01 A=/Phone/a.jpg@2019-06-01 C=/Phone/c.jpg@2018-01-10"; got != want {
		t.Errorf("a folder:\n got %s\nwant %s", got, want)
	}
}

func ptr[T any](v T) *T { return &v }

// Each photo counts once, in the month of the copy the search shows it at:
// the buckets add up to the search, and a copy synced back adds no month.
func TestDateBucketsCountEachPhotoOnceMySQL(t *testing.T) {
	d := copiesLibrary(t)
	private := []string{"/Private/"}
	buckets := func(tags, people []string, group string, imagesOnly bool) string {
		t.Helper()
		b, err := d.SearchMediaDateBuckets(tags, people, group, imagesOnly, private)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, x := range b {
			out = append(out, fmt.Sprintf("%s:%d", x.Month, x.Count))
		}
		return strings.Join(out, " ")
	}
	for _, c := range []struct {
		what         string
		tags, people []string
		group        string
		images       bool
		want         string
	}{
		{what: "browsing", images: true, want: "2025-03:1 2019-06:1 2018-01:1"},
		{what: "with videos", want: "2025-03:1 2024-05:1 2019-06:1 2018-01:1"},
		{what: "a tag", tags: []string{"beach"}, images: true, want: "2025-03:1 2019-06:1 2018-01:1"},
		{what: "two people", people: []string{"p1", "p2"}, images: true, want: "2019-06:1"},
		{what: "one person", people: []string{"p1"}, images: true, want: "2025-03:1 2019-06:1"},
		{what: "a collection", group: "g1", images: true, want: "2024-05:1 2019-06:1"},
	} {
		if got := buckets(c.tags, c.people, c.group, c.images); got != c.want {
			t.Errorf("%s: %s, want %s", c.what, got, c.want)
		}
	}
	// The same photos as the search, month for month.
	files, err := d.SearchMedia("", nil, nil, "", false, nil, private)
	if err != nil {
		t.Fatal(err)
	}
	months := map[string]int{}
	for _, f := range files {
		months[f.Created.AsTime().Format("2006-01")]++
	}
	b, err := d.SearchMediaDateBuckets(nil, nil, "", false, private)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range b {
		if months[x.Month] != x.Count {
			t.Errorf("%s: %d in the buckets, %d in the search", x.Month, x.Count, months[x.Month])
		}
		delete(months, x.Month)
	}
	if len(months) != 0 {
		t.Errorf("months the buckets miss: %v", months)
	}
}

// A collection counts a photo once however many copies it has; People's
// count is its faces, which copies don't add to.
func TestCollectionsAndPeopleCountEachPhotoOnceMySQL(t *testing.T) {
	d := copiesLibrary(t)
	for _, excluded := range [][]string{nil, {"/Private/"}} {
		g, err := d.GetImageGroup("g1", excluded)
		var me *mysql.MySQLError
		if errors.As(err, &me) && me.Number == 1137 {
			t.Skip("this server can't use a temporary table twice in one query:", err)
		}
		if err != nil {
			t.Fatal(err)
		}
		if g.FileCount != 2 || (g.CoverHash != "A" && g.CoverHash != "D") {
			t.Errorf("excluded %v: count %d, cover %q", excluded, g.FileCount, g.CoverHash)
		}
	}
	people, err := d.ListPeople(0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int32{}
	for _, p := range people {
		got[p.Id] = p.FaceCount
	}
	if got["p1"] != 3 || got["p2"] != 2 {
		t.Errorf("face counts %v, want p1 3 (A twice, B), p2 2", got)
	}
}

// File.copies: every `files` row of a hash, kept out of Images or not;
// versions aren't paths; a hash nothing holds isn't there.
func TestCountCopiesMySQL(t *testing.T) {
	d := copiesLibrary(t)
	counts, err := d.CountCopies([]string{"A", "B", "C", "D", "E", "X"})
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]int32{"A": 3, "B": 1, "C": 2, "D": 2, "E": 1}; fmt.Sprint(counts) != fmt.Sprint(want) {
		t.Errorf("counts %v, want %v", counts, want)
	}
	paths, err := d.PathsOfHash("A")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(paths, ","), "/Phone/a.jpg,/Private/a.jpg,/android/x/Download/a.jpg"; got != want {
		t.Errorf("paths of A: %s, want %s", got, want)
	}
}
