// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// On this day, against a real MySQL/MariaDB (OTC_TEST_MYSQL_DSN, as
// search_files_mysql_test.go): temporary tables shaped as db.sql's.

func memoriesTestDB(tb testing.TB) *Dao {
	tb.Helper()
	db, d := outOfImagesTestDB(tb)
	for _, q := range []string{
		"create temporary table `photo_quality` (`hash` varchar(64) not null, `aesthetic` float null, `sharpness` float not null, `dark` float not null, `bright` float not null, `brightness` float not null, `width` int not null, `height` int not null, `camera` tinyint(1) null, `scored` datetime not null, primary key (`hash`)) engine=InnoDB",
		"create temporary table `memories_hidden` (`kind` varchar(16) not null, `value` varchar(64) not null, `hidden` datetime not null, primary key (`kind`, `value`)) engine=InnoDB",
		"create temporary table `settings` (`domain` varchar(64) not null default '', `memories_enabled` tinyint(1) not null default 1) engine=InnoDB",
		"insert into `settings` (`domain`) values ('t')",
		"create temporary table `social_publications` (`uuid` varchar(64) not null, `dt` datetime not null, `text` text not null, `likes` int default 0, `friend_domain` varchar(128) not null, `own_publication` boolean not null, unique(`uuid`)) engine=InnoDB",
		"create temporary table `social_publication_likes` (`uuid` varchar(64) not null, `pub_uuid` varchar(64) not null, `dt` datetime not null, `friend_domain` varchar(128) not null, key (`pub_uuid`)) engine=InnoDB",
		"create temporary table `social_publications_comments` (`uuid` varchar(64) not null, `pub_uuid` varchar(64) not null, `dt` datetime not null, `comment` text not null, `publisher_name` varchar(64) not null, key (`pub_uuid`)) engine=InnoDB",
		"create temporary table `social_publications_files` (`pos` int not null, `hash` varchar(64) not null, `uuid` varchar(64) not null, `mime` varchar(150) not null, `created` datetime not null, `modified` datetime not null, `size` bigint not null, key (`hash`), key (`uuid`)) engine=InnoDB",
	} {
		if _, err := db.Exec(q); err != nil {
			tb.Fatal(err)
		}
	}
	return d
}

func f32(v float32) *float32 { return &v }

// The photos taken on the days asked for, each at its oldest copy Images
// shows, with its measures: a copy synced back on that day years later
// doesn't count, kept-out folders and videos don't, and a photo with two
// copies on the day comes once, at the older.
func TestMemoryCandidatesMySQL(t *testing.T) {
	d := memoriesTestDB(t)
	db := d.db
	at := func(y, m, day, h int) time.Time { return time.Date(y, time.Month(m), day, h, 0, 0, 0, time.UTC) }
	insertMediaRows(t, db, "files",
		mediaRow{ph(1), "image/jpeg", "/Phone/2019/a.jpg", at(2019, 10, 30, 10)},
		mediaRow{ph(1), "image/jpeg", "/Phone/2019/a copy.jpg", at(2019, 10, 30, 12)},
		mediaRow{ph(2), "image/jpeg", "/Phone/2019/b.jpg", at(2019, 6, 1, 10)},
		mediaRow{ph(2), "image/jpeg", "/Downloads/b.jpg", at(2024, 10, 30, 9)}, // synced back
		mediaRow{ph(3), "image/jpeg", "/Private/c.jpg", at(2021, 10, 30, 9)},
		mediaRow{ph(4), "video/mp4", "/Phone/d.mp4", at(2021, 10, 30, 9)},
		mediaRow{ph(5), "image/heic", "/Phone/e.heic", at(2022, 10, 31, 1)},
		mediaRow{ph(6), "image/jpeg", "/Phone/f.jpg", at(2022, 11, 2, 1)},
	)
	if err := d.SetPhotoQuality(ph(1), PhotoQuality{Aesthetic: f32(6.25), Sharpness: 120, Dark: 0.01, Bright: 0.02, Brightness: 110, Width: 1000, Height: 750, Camera: new(bool)}); err != nil {
		t.Fatal(err)
	}
	got, err := d.MemoryCandidates([]string{"10-29", "10-30", "10-31"}, []string{"/Private/"})
	if err != nil {
		t.Fatal(err)
	}
	byHash := map[string]MemoryCandidate{}
	for _, c := range got {
		byHash[c.Hash] = c
	}
	if len(got) != 2 || byHash[ph(1)].Hash == "" || byHash[ph(5)].Hash == "" {
		t.Fatalf("candidates %+v, want a.jpg and e.heic", got)
	}
	a := byHash[ph(1)]
	if a.Path != "/Phone/2019/a.jpg" || !a.Created.Equal(at(2019, 10, 30, 10)) {
		t.Errorf("a at %s %v, want its older copy", a.Path, a.Created)
	}
	if q := a.Quality; q == nil || q.Aesthetic == nil || *q.Aesthetic != 6.25 || q.Sharpness != 120 || q.Width != 1000 || q.Camera == nil || *q.Camera {
		t.Errorf("a's measures %+v", a.Quality)
	}
	if byHash[ph(5)].Quality != nil {
		t.Error("e has measures it was never given")
	}
	if got, _ := d.MemoryCandidates(nil, nil); got != nil {
		t.Errorf("no days: %v", got)
	}
}

// What the backfill measures: photos outside the kept-out folders with no
// row, or - once the model is there - a row measured without it.
func TestPhotosToScoreMySQL(t *testing.T) {
	d := memoriesTestDB(t)
	day := time.Date(2024, 3, 1, 9, 0, 0, 0, time.UTC)
	insertMediaRows(t, d.db, "files",
		mediaRow{ph(1), "image/jpeg", "/P/a.jpg", day},
		mediaRow{ph(2), "image/jpeg", "/P/b.jpg", day},
		mediaRow{ph(3), "image/jpeg", "/P/c.jpg", day},
		mediaRow{ph(4), "image/jpeg", "/Private/d.jpg", day},
		mediaRow{ph(5), "video/mp4", "/P/e.mp4", day},
		mediaRow{ph(6), "text/plain", "/P/f.txt", day},
	)
	if err := d.SetPhotoQuality(ph(1), PhotoQuality{Aesthetic: f32(5), Sharpness: 1}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetPhotoQuality(ph(2), PhotoQuality{Sharpness: 1}); err != nil {
		t.Fatal(err)
	}
	hashes := func(withoutAesthetic bool, after string) []string {
		files, err := d.PhotosToScore(after, 10, withoutAesthetic, []string{"/Private/"})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, f := range files {
			out = append(out, f.Hash)
		}
		return out
	}
	if got := hashes(false, ""); !slices.Equal(got, []string{ph(3)}) {
		t.Errorf("without the model %v, want only c", got)
	}
	if got := hashes(true, ""); !slices.Equal(got, []string{ph(2), ph(3)}) {
		t.Errorf("with the model %v, want b (measured without it) and c", got)
	}
	if got := hashes(true, ph(2)); !slices.Equal(got, []string{ph(3)}) {
		t.Errorf("after b %v", got)
	}
	if err := d.DelPhotoQuality(ph(1)); err != nil {
		t.Fatal(err)
	}
	if got := hashes(false, ""); !slices.Equal(got, []string{ph(1), ph(3)}) {
		t.Errorf("after a's row went %v", got)
	}
}

// Tags, people (no one's faces left out), collections, own posts with
// their likes and comments, the commonest score-1 tags, hiding and the
// switch.
func TestMemoriesSignalsMySQL(t *testing.T) {
	d := memoriesTestDB(t)
	db := d.db
	now := time.Date(2024, 3, 1, 9, 0, 0, 0, time.UTC)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"insert into `file_tags` (`hash`, `tag`, `score`) values (?, 'beach', 0.8), (?, 'Valencia', 1), (?, 'Spain', 1), (?, 'Madrid', 1), (?, 'Spain', 1)", []any{ph(1), ph(1), ph(1), ph(2), ph(2)}},
		{"insert into `people` (`id`, `name`, `created`) values ('p1', 'Lucía', ?), ('p2', '', ?)", []any{now, now}},
		{"insert into `faces` (`id`, `hash`, `person_id`, `bbox_x`, `bbox_y`, `bbox_w`, `bbox_h`, `embedding`, `thumbnail`, `created`) values ('f1', ?, 'p1', 0, 0, 1, 1, '', '', ?), ('f2', ?, 'p1', 0, 0, 1, 1, '', '', ?), ('f3', ?, 'p2', 0, 0, 1, 1, '', '', ?), ('f4', ?, '', 0, 0, 1, 1, '', '', ?)", []any{ph(1), now, ph(1), now, ph(1), now, ph(2), now}},
		{"insert into `image_group_files` (`group_id`, `hash`, `added`) values ('g', ?, ?)", []any{ph(2), now}},
		{"insert into `social_publications` (`uuid`, `dt`, `text`, `friend_domain`, `own_publication`) values ('mine', ?, '', '', 1), ('theirs', ?, '', 'friend', 0)", []any{now, now}},
		{"insert into `social_publications_files` (`pos`, `hash`, `uuid`, `mime`, `created`, `modified`, `size`) values (0, ?, 'mine', 'image/jpeg', ?, ?, 1), (0, ?, 'theirs', 'image/jpeg', ?, ?, 1)", []any{ph(1), now, now, ph(2), now, now}},
		{"insert into `social_publication_likes` (`uuid`, `pub_uuid`, `dt`, `friend_domain`) values ('l1', 'mine', ?, 'a'), ('l2', 'mine', ?, 'b')", []any{now, now}},
		{"insert into `social_publications_comments` (`uuid`, `pub_uuid`, `dt`, `comment`, `publisher_name`) values ('c1', 'mine', ?, 'nice', 'a')", []any{now}},
	} {
		if _, err := db.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	hashes := []string{ph(1), ph(2), ph(3)}
	tags, err := d.TagsOfHashes(hashes)
	if err != nil || len(tags[ph(1)]) != 3 || len(tags[ph(2)]) != 2 || len(tags[ph(3)]) != 0 {
		t.Errorf("tags %v %v", tags, err)
	}
	faces, err := d.FacesOfHashes(hashes)
	if err != nil {
		t.Fatal(err)
	}
	if f := faces[ph(1)]; len(f) != 2 || len(faces[ph(2)]) != 0 {
		t.Errorf("faces %v, want Lucía once and an unnamed person on a, no one on b", faces)
	}
	names := map[string]string{}
	for _, f := range faces[ph(1)] {
		names[f.PersonID] = f.Name
	}
	if names["p1"] != "Lucía" || names["p2"] != "" {
		t.Errorf("names %v", names)
	}
	if got, err := d.CollectedHashes(hashes); err != nil || !got[ph(2)] || got[ph(1)] {
		t.Errorf("collected %v %v", got, err)
	}
	posted, err := d.PostedHashes(hashes)
	if err != nil {
		t.Fatal(err)
	}
	if p := posted[ph(1)]; p.Posts != 1 || p.Likes != 2 || p.Comments != 1 || len(posted) != 1 {
		t.Errorf("posted %+v, want a once with 2 likes and a comment, not a friend's post", posted)
	}
	counts, err := d.PlaceTagCounts(10)
	if err != nil || len(counts) != 3 || counts[0].Tag != "Spain" || counts[0].Count != 2 {
		t.Errorf("place tags %v %v", counts, err)
	}

	for _, h := range []HiddenMemory{{HideMemoryPhoto, ph(1)}, {HideMemoryDay, "2023-10-30"}, {HideMemoryDay, "2023-10-30"}, {HideMemoryPerson, "p1"}} {
		if err := d.HideMemory(h); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := d.HiddenMemories(); err != nil || len(got) != 3 {
		t.Errorf("hidden %v %v", got, err)
	}
	if err := d.ShowMemory(HiddenMemory{HideMemoryDay, "2023-10-30"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.HiddenMemories(); len(got) != 2 {
		t.Errorf("after showing the day %v", got)
	}
	if err := d.ShowAllMemories(); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.HiddenMemories(); len(got) != 0 {
		t.Errorf("after showing all %v", got)
	}

	if on, err := d.MemoriesEnabled(); err != nil || !on {
		t.Errorf("on by default: %v %v", on, err)
	}
	if err := d.SetMemoriesEnabled(false); err != nil {
		t.Fatal(err)
	}
	if on, _ := d.MemoriesEnabled(); on {
		t.Error("still on")
	}
}

// How long a day's and a month's candidates take from 100k photos spread
// over ten years, a third measured (OTC_TEST_MEMORIES_LARGE=1): the query
// reads every image row's date, as no index holds the month and day.
func TestMemoryCandidatesLargeMySQL(t *testing.T) {
	if os.Getenv("OTC_TEST_MEMORIES_LARGE") != "1" {
		t.Skip("OTC_TEST_MEMORIES_LARGE not set")
	}
	d := memoriesTestDB(t)
	start := time.Date(2016, 1, 1, 0, 0, 0, 0, time.UTC)
	const n = 100000
	for b := 0; b < n; b += 1000 {
		var sb strings.Builder
		var qb strings.Builder
		args, qargs := []any{}, []any{}
		for i := b; i < b+1000; i++ {
			if sb.Len() > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("(?, 'image/jpeg', ?, ?, ?, 1)")
			at := start.Add(time.Duration(i) * 3153 * time.Second) // ten years
			args = append(args, ph(i), at, at, fmt.Sprintf("/P/%07d.jpg", i))
			if i%3 == 0 {
				if qb.Len() > 0 {
					qb.WriteString(",")
				}
				qb.WriteString("(?, 5.5, 100, 0, 0, 120, 1000, 750, 1, now())")
				qargs = append(qargs, ph(i))
			}
		}
		if _, err := d.db.Exec("insert into `files` (`hash`, `mime`, `created`, `modified`, `path`, `size`) values "+sb.String(), args...); err != nil {
			t.Fatal(err)
		}
		if _, err := d.db.Exec("insert into `photo_quality` (`hash`, `aesthetic`, `sharpness`, `dark`, `bright`, `brightness`, `width`, `height`, `camera`, `scored`) values "+qb.String(), qargs...); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name string
		days []string
	}{
		{"day", []string{"10-29", "10-30", "10-31"}},
		{"month", func() []string {
			var out []string
			for day := 1; day <= 31; day++ {
				out = append(out, fmt.Sprintf("10-%02d", day))
			}
			return append(out, "09-30", "11-01")
		}()},
	} {
		began := time.Now()
		got, err := d.MemoryCandidates(c.days, []string{"/Private/"})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d candidates of %d photos in %v", c.name, len(got), n, time.Since(began))
	}
}

// Before the release's script: no measures, nothing hidden, the switch
// reads as behind - and the photos are still candidates.
func TestMemoriesWithoutTheirTablesMySQL(t *testing.T) {
	db, d := outOfImagesTestDB(t)
	if _, err := db.Exec("create temporary table `settings` (`domain` varchar(64) not null default '') engine=InnoDB"); err != nil {
		t.Fatal(err)
	}
	insertMediaRows(t, db, "files", mediaRow{ph(1), "image/jpeg", "/P/a.jpg", time.Date(2020, 10, 30, 9, 0, 0, 0, time.UTC)})
	if got, err := d.MemoryCandidates([]string{"10-30"}, nil); err != nil || len(got) != 1 || got[0].Quality != nil {
		t.Errorf("candidates %v %v", got, err)
	}
	if err := d.SetPhotoQuality(ph(1), PhotoQuality{}); !errors.Is(err, ErrSchemaBehind) {
		t.Errorf("measures: %v", err)
	}
	if err := d.DelPhotoQuality(ph(1)); err != nil {
		t.Errorf("forgetting: %v", err)
	}
	if _, err := d.PhotosToScore("", 10, false, nil); !errors.Is(err, ErrSchemaBehind) {
		t.Errorf("to score: %v", err)
	}
	if got, err := d.HiddenMemories(); err != nil || got != nil {
		t.Errorf("hidden %v %v", got, err)
	}
	if err := d.HideMemory(HiddenMemory{HideMemoryPhoto, ph(1)}); !errors.Is(err, ErrSchemaBehind) {
		t.Errorf("hiding: %v", err)
	}
	if _, err := d.MemoriesEnabled(); !errors.Is(err, ErrSchemaBehind) {
		t.Errorf("the switch: %v", err)
	}
}
