// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"strings"
	"testing"
	"time"
)

// Collection Rating (release 163), against a real MySQL/MariaDB
// (OTC_TEST_MYSQL_DSN, as out_of_images_mysql_test.go): temporary tables
// shaped as db.sql's.
func ratingsTestDB(tb testing.TB) *Dao {
	tb.Helper()
	db, d := outOfImagesTestDB(tb)
	for _, q := range []string{
		"alter table `image_groups` add column `rating` tinyint(1) not null default 0, add column `rating_key` blob null",
		"create temporary table `photo_ratings` (`group_id` varchar(36) not null, `hash` varchar(64) not null, `stars` tinyint not null default 0, `comment` blob null, `shared_comment` blob null, `link` varchar(64) null, `updated` datetime not null, primary key (`group_id`, `hash`), key (`hash`)) engine=InnoDB",
		"create temporary table `shared_links` (`uuid` varchar(64) not null, `size` bigint not null, `created` datetime not null, `kind` varchar(16) not null default 'archive', `description` blob null, `files` int not null default 0, `opens` int not null default 0, `last_opened` datetime null, `expires` datetime null, `group_id` varchar(36) null, `rating` tinyint(1) not null default 0, `rating_alerted` datetime null, primary key (`uuid`)) engine=InnoDB",
	} {
		if _, err := db.Exec(q); err != nil {
			tb.Fatal(err)
		}
	}
	return d
}

func TestPhotoRatingsMySQL(t *testing.T) {
	d := ratingsTestDB(t)
	db := d.db
	insertMediaRows(t, db, "files",
		mediaRow{"A", "image/jpeg", "/Phone/a.jpg", month(1)},
		mediaRow{"A", "image/jpeg", "/Backup/a.jpg", month(5)},
		mediaRow{"B", "image/jpeg", "/Phone/b.jpg", month(2)},
		mediaRow{"C", "image/jpeg", "/Phone/c.jpg", month(3)},
		mediaRow{"D", "image/jpeg", "/Phone/d.jpg", month(4)},
	)
	for _, q := range []string{
		"insert into `image_groups` (`id`, `name`, `created`) values ('g1', 'Trip', now()), ('g2', 'Beach', now())",
		"insert into `image_group_files` values ('g1', 'A', now()), ('g1', 'B', now()), ('g1', 'C', now()), ('g1', 'D', now()), ('g2', 'A', now())",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.SetImageGroupRating("g1", true); err != nil {
		t.Fatal(err)
	}
	if exists, on, member, err := d.RatingTarget("g1", "A"); err != nil || !exists || !on || !member {
		t.Fatalf("target: %v %v %v %v", exists, on, member, err)
	}
	if exists, on, member, err := d.RatingTarget("g2", "B"); err != nil || !exists || on || member {
		t.Fatalf("g2: %v %v %v %v", exists, on, member, err)
	}

	at := time.Date(2026, 11, 2, 21, 0, 0, 0, time.UTC)
	set := func(hash string, stars int, comment string) bool {
		t.Helper()
		r := PhotoRatingRow{Hash: hash, Stars: stars, Updated: at}
		if comment != "" {
			r.Comment = []byte(comment)
		}
		stored, err := d.SetPhotoRating("g1", r)
		if err != nil {
			t.Fatal(err)
		}
		return stored
	}
	set("A", 3, "")
	set("B", 5, "sealed")
	set("C", 0, "only a comment")
	// The same again within the second changes nothing, and is stored.
	if !set("A", 3, "") {
		t.Error("an unchanged rating isn't stored")
	}
	// Not a member: nothing written.
	if set("Z", 4, "") {
		t.Error("a photo outside the collection was rated")
	}
	got, err := d.PhotoRatings("g1", []string{"A", "B", "C", "D", "Z"})
	if err != nil || len(got) != 3 || got["A"].Stars != 3 || got["B"].Stars != 5 || string(got["C"].Comment) != "only a comment" {
		t.Fatalf("ratings %+v %v", got, err)
	}

	paths := func(q *RatingQuery) string {
		t.Helper()
		files, err := d.SearchMediaRated(nil, "", nil, nil, "g1", false, nil, nil, q)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(searchPaths(files), ",")
	}
	// A at its oldest copy; by date, newest first.
	if got, want := paths(nil), "/Phone/d.jpg,/Phone/c.jpg,/Phone/b.jpg,/Phone/a.jpg"; got != want {
		t.Errorf("by date %s, want %s", got, want)
	}
	if got, want := paths(&RatingQuery{ByRating: true}), "/Phone/b.jpg,/Phone/a.jpg,/Phone/d.jpg,/Phone/c.jpg"; got != want {
		t.Errorf("by rating %s, want %s", got, want)
	}
	if got, want := paths(&RatingQuery{MinStars: 4}), "/Phone/b.jpg"; got != want {
		t.Errorf("4 stars or more %s, want %s", got, want)
	}
	if got, want := paths(&RatingQuery{MinStars: 1}), "/Phone/b.jpg,/Phone/a.jpg"; got != want {
		t.Errorf("rated %s, want %s", got, want)
	}
	if got, want := paths(&RatingQuery{Unrated: true}), "/Phone/d.jpg,/Phone/c.jpg"; got != want {
		t.Errorf("not rated %s, want %s", got, want)
	}
	if got, want := paths(&RatingQuery{WithComment: true, ByRating: true}), "/Phone/b.jpg,/Phone/c.jpg"; got != want {
		t.Errorf("with a comment %s, want %s", got, want)
	}

	// Cleared, and taken out of the collection: gone.
	set("C", 0, "")
	if err := d.RemovePathsFromImageGroup("g1", []string{"/Backup/a.jpg"}); err != nil {
		t.Fatal(err)
	}
	if got, err := d.PhotoRatings("g1", []string{"A", "B", "C"}); err != nil || len(got) != 1 || got["B"].Stars != 5 {
		t.Fatalf("after removing: %+v %v", got, err)
	}
	if err := d.DeleteImageGroup("g1"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow("select count(*) from `photo_ratings`").Scan(&n); err != nil || n != 0 {
		t.Fatalf("ratings left after the collection: %d %v", n, err)
	}
}

// A rated gallery's row, Settings' list and the alert's hour.
func TestRatedSharedLinksMySQL(t *testing.T) {
	d := ratingsTestDB(t)
	db := d.db
	if _, err := db.Exec("insert into `image_groups` (`id`, `name`, `created`, `rating`) values ('g1', 'Trip', now(), 1)"); err != nil {
		t.Fatal(err)
	}
	exp := time.Now().Add(time.Hour)
	if err := d.InsertSharedGallery("u1", 10, 1, nil, exp, "g1"); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertSharedGallery("u2", 10, 1, nil, exp, ""); err != nil {
		t.Fatal(err)
	}
	if g, r, err := d.GetSharedLinkRating("u1"); err != nil || g != "g1" || !r {
		t.Fatalf("u1: %s %v %v", g, r, err)
	}
	rated := func() map[string]bool {
		t.Helper()
		rows, err := d.ListSharedLinks()
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, r := range rows {
			out[r.Uuid] = r.Rating
		}
		return out
	}
	if got := rated(); !got["u1"] || got["u2"] {
		t.Errorf("list %v", got)
	}
	if err := d.SetImageGroupRating("g1", false); err != nil {
		t.Fatal(err)
	}
	if got := rated(); got["u1"] {
		t.Errorf("rating off, still rated: %v", got)
	}

	now := time.Now().UTC().Truncate(time.Second)
	for i, c := range []struct {
		at   time.Time
		want bool
	}{{now, true}, {now.Add(time.Minute), false}, {now.Add(time.Hour), true}} {
		if ok, err := d.ClaimRatingAlert("u1", time.Hour, c.at); err != nil || ok != c.want {
			t.Errorf("claim %d: %v %v, want %v", i, ok, err, c.want)
		}
	}
}
