// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"fmt"
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
		"create temporary table `photo_rating_sources` (`group_id` varchar(36) not null, `hash` varchar(64) not null, `source` varchar(64) not null, `stars` tinyint not null default 0, `comment` blob null, `link_comment` blob null, `updated` datetime(6) not null, primary key (`group_id`, `hash`, `source`), key (`hash`)) engine=InnoDB",
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
	for _, table := range []string{"photo_ratings", "photo_rating_sources"} {
		var n int
		if err := db.QueryRow("select count(*) from `" + table + "`").Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s left after the collection: %d %v", table, n, err)
		}
	}
}

// Release 169: each link's visitors' own ratings. A link reads its rows
// only - never the owner's own, nor another link's - and what the owner
// sees (and searches by) is the latest change of anyone's; a link taking
// its rating back shows the owner the latest left, the owner's own clear
// hides the older ones; the later of two changes in one second is the
// latest; ratings stored before (one shared row) get their source at the
// next start, the owner's comments' copies for links dropped.
func TestRatingsPerLinkMySQL(t *testing.T) {
	d := ratingsTestDB(t)
	db := d.db
	insertMediaRows(t, db, "files",
		mediaRow{"A", "image/jpeg", "/a.jpg", month(1)},
		mediaRow{"B", "image/jpeg", "/b.jpg", month(2)},
	)
	for _, q := range []string{
		"insert into `image_groups` (`id`, `name`, `created`, `rating`) values ('g1', 'Shoot', now(), 1)",
		"insert into `image_group_files` values ('g1', 'A', now()), ('g1', 'B', now())",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	step := 0
	rate := func(source, hash string, stars int, comment string) {
		t.Helper()
		step++
		r := PhotoRatingRow{Hash: hash, Stars: stars, Link: source, Updated: at.Add(time.Duration(step) * time.Minute)}
		if comment != "" {
			r.Comment = []byte("owner-sealed " + comment)
			if source != "" {
				r.SharedComment = []byte("link-sealed " + comment)
			}
		}
		if stored, err := d.SetPhotoRating("g1", r); err != nil || !stored {
			t.Fatalf("%s rates %s: %v %v", source, hash, stored, err)
		}
	}
	shown := func(hash string) string {
		t.Helper()
		got, err := d.PhotoRatings("g1", []string{hash})
		if err != nil {
			t.Fatal(err)
		}
		r, ok := got[hash]
		if !ok {
			return "none"
		}
		if r.Link == "" && r.SharedComment != nil {
			t.Errorf("%s: a copy of the owner's comment for links", hash)
		}
		return fmt.Sprintf("%d %q from %q", r.Stars, r.Comment, r.Link)
	}
	seen := func(link, hash string) string {
		t.Helper()
		got, err := d.LinkRatings("g1", link, []string{"A", "B", "C"})
		if err != nil {
			t.Fatal(err)
		}
		r, ok := got[hash]
		if !ok {
			return "none"
		}
		return fmt.Sprintf("%d %q", r.Stars, r.SharedComment)
	}

	rate("", "A", 5, "mine")
	rate("L1", "A", 2, "too dark")
	rate("L2", "B", 4, "")
	if got := seen("L1", "A"); got != `2 "link-sealed too dark"` {
		t.Errorf("L1 sees %s of A", got)
	}
	if got := seen("L2", "A"); got != "none" {
		t.Errorf("L2 sees %s of A: L1's or the owner's", got)
	}
	if got := seen("L1", "B"); got != "none" {
		t.Errorf("L1 sees %s of B: L2's", got)
	}
	if got := shown("A"); got != `2 "owner-sealed too dark" from "L1"` {
		t.Errorf("the owner sees %s of A", got)
	}

	// L1 takes theirs back: the owner's own shows again, untouched.
	rate("L1", "A", 0, "")
	if got := shown("A"); got != `5 "owner-sealed mine" from ""` {
		t.Errorf("after L1 took theirs back the owner sees %s", got)
	}
	// The owner clears theirs: none; a link's later one shows, and taken
	// back leaves none - the owner's clear is the latest left.
	rate("", "A", 0, "")
	if got := shown("A"); got != "none" {
		t.Errorf("after the owner's clear: %s", got)
	}
	rate("L2", "A", 3, "")
	if got := shown("A"); got != `3 "" from "L2"` {
		t.Errorf("L2 after the owner's clear: %s", got)
	}
	rate("L2", "A", 0, "")
	if got := shown("A"); got != "none" {
		t.Errorf("L2 took it back: %s", got)
	}

	// Two changes in one second: the later is the latest.
	same := at.Add(time.Hour)
	for i, r := range []PhotoRatingRow{
		{Hash: "B", Stars: 1, Link: "L1", Updated: same.Add(100 * time.Millisecond)},
		{Hash: "B", Stars: 2, Updated: same.Add(200 * time.Millisecond)},
	} {
		if _, err := d.SetPhotoRating("g1", r); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := d.SetPhotoRating("g1", PhotoRatingRow{Hash: "B", Link: "L1", Updated: same.Add(300 * time.Millisecond)}); err != nil {
		t.Fatal(err)
	}
	if got := shown("B"); got != `2 "" from ""` {
		t.Errorf("of one second's changes the owner sees %s", got)
	}

	// Taken out of the collection: every source's goes.
	if err := d.RemovePathsFromImageGroup("g1", []string{"/a.jpg"}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow("select count(*) from `photo_rating_sources` where `hash` = 'A'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("A's sources after it left: %d %v", n, err)
	}

	// Stored before release 169: the owner's row with its copy for links,
	// a link's row with its visitors' copy. At start each gets its source,
	// the owner's copy dropped; again, nothing changes.
	for _, q := range []string{
		"delete from `photo_rating_sources`",
		"delete from `photo_ratings`",
		"insert into `image_group_files` values ('g1', 'C', now())",
		"insert into `photo_ratings` values ('g1', 'B', 4, 'owner B', 'shared owner B', null, '2026-11-01 10:00:00'), ('g1', 'C', 2, 'owner C', 'shared C', 'L7', '2026-11-01 11:00:00')",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := d.ReconcileRatingSources(); err != nil {
			t.Fatal(err)
		}
	}
	if got := seen("L7", "C"); got != `2 "shared C"` {
		t.Errorf("L7 sees %s of C after the start", got)
	}
	var sources int
	var ownerCopy []byte
	if err := db.QueryRow("select count(*) from `photo_rating_sources`").Scan(&sources); err != nil || sources != 2 {
		t.Fatalf("%d sources, %v", sources, err)
	}
	var linkComment []byte
	if err := db.QueryRow("select `link_comment` from `photo_rating_sources` where `hash` = 'B' and `source` = ''").Scan(&linkComment); err != nil || linkComment != nil {
		t.Fatalf("the owner's source has a copy for links: %q %v", linkComment, err)
	}
	if err := db.QueryRow("select `shared_comment` from `photo_ratings` where `hash` = 'B'").Scan(&ownerCopy); err != nil || ownerCopy != nil {
		t.Fatalf("the owner's copy for links kept: %q %v", ownerCopy, err)
	}
	if got := shown("C"); got != `2 "owner C" from "L7"` {
		t.Errorf("the owner sees %s of C", got)
	}
	// A rating an older binary stored after a source's: taken at the next
	// start; one older than its source's is not.
	if _, err := db.Exec("update `photo_ratings` set `stars` = 5, `updated` = '2026-11-02 09:00:00' where `hash` = 'C'"); err != nil {
		t.Fatal(err)
	}
	if err := d.ReconcileRatingSources(); err != nil {
		t.Fatal(err)
	}
	if got := seen("L7", "C"); got != `5 "shared C"` {
		t.Errorf("after an older binary's rating, L7 sees %s", got)
	}
	if _, err := db.Exec("update `photo_ratings` set `stars` = 1, `updated` = '2026-10-01 09:00:00' where `hash` = 'C'"); err != nil {
		t.Fatal(err)
	}
	if err := d.ReconcileRatingSources(); err != nil {
		t.Fatal(err)
	}
	if got := seen("L7", "C"); got != `5 "shared C"` {
		t.Errorf("an older rating than its source's was taken: %s", got)
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
