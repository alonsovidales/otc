// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Issue #192, against a real MySQL/MariaDB (OTC_TEST_MYSQL_DSN, as
// search_files_mysql_test.go): temporary tables shaped as db.sql's, so
// nothing in that database is read or changed.

// outOfImagesTestDB is searchTestDB's `files` plus the other tables the
// folders kept out of Images touch.
func outOfImagesTestDB(tb testing.TB) (*sql.DB, *Dao) {
	tb.Helper()
	db := searchTestDB(tb)
	for _, q := range []string{
		"create temporary table `file_versions` (`path` varchar(768) character set utf8mb4 collate utf8mb4_bin not null, `hash` varchar(64) not null, `mime` varchar(150) not null, `size` bigint not null, `created` datetime not null, `modified` datetime not null, `replaced` datetime not null, key (`path`), key (`hash`)) engine=InnoDB",
		"create temporary table `file_tags` (`hash` varchar(64) not null, `tag` varchar(150) not null, `score` float not null, key (`hash`), key (`tag`), unique key (`hash`, `tag`)) engine=InnoDB",
		"create temporary table `people` (`id` varchar(36) not null, `name` varchar(150) not null default '', `created` datetime not null, `cover_face_id` varchar(36) default null, `cohesion` float default null, primary key (`id`)) engine=InnoDB",
		"create temporary table `faces` (`id` varchar(36) not null, `hash` varchar(64) not null, `person_id` varchar(36) not null, `bbox_x` int not null, `bbox_y` int not null, `bbox_w` int not null, `bbox_h` int not null, `embedding` blob not null, `thumbnail` mediumblob not null, `created` datetime not null, primary key (`id`), key (`hash`), key (`person_id`)) engine=InnoDB",
		"create temporary table `image_groups` (`id` varchar(36) not null, `name` varchar(150) not null, `created` datetime not null, primary key (`id`)) engine=InnoDB",
		"create temporary table `image_group_files` (`group_id` varchar(36) not null, `hash` varchar(64) not null, `added` datetime not null, primary key (`group_id`, `hash`), key (`hash`)) engine=InnoDB",
		"create temporary table `skipped_analysis` (`hash` varchar(64) not null, `skipped` datetime not null, primary key (`hash`)) engine=InnoDB",
		"create temporary table `pending_analysis` (`hash` varchar(64) not null, `queued` datetime not null, primary key (`hash`)) engine=InnoDB",
	} {
		if _, err := db.Exec(q); err != nil {
			tb.Fatal(err)
		}
	}
	return db, NewWithDB(db)
}

type mediaRow struct {
	hash, mime, path string
	created          time.Time
}

func insertMediaRows(tb testing.TB, db *sql.DB, table string, rows ...mediaRow) {
	tb.Helper()
	for _, r := range rows {
		var err error
		if table == "files" {
			_, err = db.Exec("insert into `files` (`hash`, `mime`, `created`, `modified`, `path`, `size`) values (?, ?, ?, ?, ?, 1)", r.hash, r.mime, r.created, r.created, r.path)
		} else {
			_, err = db.Exec("insert into `file_versions` (`path`, `hash`, `mime`, `size`, `created`, `modified`, `replaced`) values (?, ?, ?, 1, ?, ?, ?)", r.path, r.hash, r.mime, r.created, r.created, r.created)
		}
		if err != nil {
			tb.Fatal(err)
		}
	}
}

func month(m int) time.Time { return time.Date(2026, time.Month(m), 10, 12, 0, 0, 0, time.UTC) }

func pathsOf(files []string) string {
	sort.Strings(files)
	return strings.Join(files, ",")
}

func TestSearchMediaLeavesOutKeptOutFoldersMySQL(t *testing.T) {
	db, d := outOfImagesTestDB(t)
	insertMediaRows(t, db, "files",
		mediaRow{"A", "image/jpeg", "/Phone/a.jpg", month(1)},
		mediaRow{"A", "image/jpeg", "/Private/a.jpg", month(1)}, // the same photo, kept out here
		mediaRow{"B", "image/jpeg", "/Private/b.jpg", month(2)},
		mediaRow{"C", "image/jpeg", "/Private/Sub/c.jpg", month(2)},
		mediaRow{"D", "image/jpeg", "/Photos (2020)/d.jpg", month(3)},
		mediaRow{"E", "image/jpeg", "/a_b/e.jpg", month(3)},
		mediaRow{"F", "image/jpeg", "/axb/f.jpg", month(3)}, // _ is no wildcard
		mediaRow{"G", "image/jpeg", "/100%/g.jpg", month(3)},
		mediaRow{"H", "image/jpeg", "/100x/h.jpg", month(3)}, // % neither
		mediaRow{"I", "image/heic", "/mac/Alonso’s_MacBook_Air/Pictures/i.HEIC", month(4)},
		mediaRow{"J", "image/jpeg", "/private/j.jpg", month(4)}, // case is exact
		mediaRow{"K", "image/jpeg", "/Café/k.jpg", month(4)},
		mediaRow{"L", "image/jpeg", "/Cafe/l.jpg", month(4)}, // accents too
		mediaRow{"V", "video/mp4", "/Phone/v.mp4", month(5)},
		mediaRow{"T", "text/plain", "/Phone/t.txt", month(5)},
	)
	excluded := []string{"/Private/", "/Private/Sub/", "/Photos (2020)/", "/a_b/", "/100%/", "/mac/Alonso’s_MacBook_Air/", "/Café/"}
	search := func(tags, people []string, group string, imagesOnly bool, before *time.Time) string {
		t.Helper()
		files, err := d.SearchMedia("", tags, people, group, imagesOnly, before, excluded)
		if err != nil {
			t.Fatal(err)
		}
		var paths []string
		for _, f := range files {
			paths = append(paths, f.Path)
		}
		return pathsOf(paths)
	}

	if got, want := search(nil, nil, "", false, nil), "/100x/h.jpg,/Cafe/l.jpg,/Phone/a.jpg,/Phone/v.mp4,/axb/f.jpg,/private/j.jpg"; got != want {
		t.Errorf("browsing:\n%s\nwant\n%s", got, want)
	}
	if got, want := search(nil, nil, "", true, nil), "/100x/h.jpg,/Cafe/l.jpg,/Phone/a.jpg,/axb/f.jpg,/private/j.jpg"; got != want {
		t.Errorf("images only: %s", got)
	}

	for _, q := range []string{
		"insert into `file_tags` values ('A', 'beach', 0.9), ('B', 'beach', 0.8), ('C', 'beach', 0.7)",
		"insert into `people` (`id`, `created`) values ('p1', now()), ('p2', now())",
		"insert into `faces` values ('f1', 'A', 'p1', 0, 0, 1, 1, '', '', now()), ('f2', 'A', 'p2', 0, 0, 1, 1, '', '', now()), ('f3', 'B', 'p1', 0, 0, 1, 1, '', '', now()), ('f4', 'B', 'p2', 0, 0, 1, 1, '', '', now())",
		"insert into `image_groups` values ('g1', 'Trip', now())",
		"insert into `image_group_files` values ('g1', 'A', now()), ('g1', 'B', now())",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if got := search([]string{"beach"}, nil, "", true, nil); got != "/Phone/a.jpg" {
		t.Errorf("tags: %s", got)
	}
	if got := search(nil, []string{"p1", "p2"}, "", true, nil); got != "/Phone/a.jpg" {
		t.Errorf("two people: %s", got)
	}
	if got := search(nil, nil, "g1", true, nil); got != "/Phone/a.jpg" {
		t.Errorf("group: %s", got)
	}
	before := month(2).Add(24 * time.Hour)
	if got := search(nil, nil, "", true, &before); got != "/Phone/a.jpg" {
		t.Errorf("before: %s", got)
	}
	// Every filter at once: the args must still line up.
	if got := search([]string{"beach"}, []string{"p1", "p2"}, "g1", true, &before); got != "/Phone/a.jpg" {
		t.Errorf("every filter: %s", got)
	}

	buckets, err := d.SearchMediaDateBuckets(nil, nil, "", false, excluded)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, b := range buckets {
		got[b.Month] = b.Count
	}
	if want := map[string]int{"2026-01": 1, "2026-03": 2, "2026-04": 2, "2026-05": 1}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("buckets %v, want %v", got, want)
	}
}

func TestImageGroupsLeaveOutKeptOutMembersMySQL(t *testing.T) {
	db, d := outOfImagesTestDB(t)
	insertMediaRows(t, db, "files",
		mediaRow{"A", "image/jpeg", "/Phone/a.jpg", month(1)},
		mediaRow{"A", "image/jpeg", "/Private/a.jpg", month(1)},
		mediaRow{"B", "image/jpeg", "/Private/b.jpg", month(1)},
	)
	for _, q := range []string{
		"insert into `image_groups` values ('g1', 'Trip', now())",
		"insert into `image_group_files` values ('g1', 'A', now()), ('g1', 'B', now())",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 10; i++ {
		g, err := d.GetImageGroup("g1", []string{"/Private/"})
		var me *mysql.MySQLError
		if errors.As(err, &me) && me.Number == 1137 {
			t.Skip("this server can't use a temporary table twice in one query:", err)
		}
		if err != nil {
			t.Fatal(err)
		}
		if g.FileCount != 1 || g.CoverHash != "A" {
			t.Fatalf("count %d, cover %q: want 1 and A", g.FileCount, g.CoverHash)
		}
	}
	groups, err := d.ListImageGroups(nil)
	if err != nil || len(groups) != 1 || groups[0].FileCount != 2 {
		t.Errorf("nothing kept out: %+v, %v", groups, err)
	}
	var members int
	if err := db.QueryRow("select count(*) from `image_group_files`").Scan(&members); err != nil || members != 2 {
		t.Errorf("membership rows: %d, %v", members, err)
	}
}

func TestVisibleHashesMySQL(t *testing.T) {
	db, d := outOfImagesTestDB(t)
	insertMediaRows(t, db, "files",
		mediaRow{"A", "image/jpeg", "/Private/a.jpg", month(1)},
		mediaRow{"A", "image/jpeg", "/Phone/a.jpg", month(1)},
		mediaRow{"B", "image/jpeg", "/Private/b.jpg", month(1)},
		mediaRow{"C", "image/jpeg", "/Private/c.jpg", month(1)},
	)
	insertMediaRows(t, db, "file_versions",
		mediaRow{"C", "image/jpeg", "/Backup/c.jpg", month(1)},  // a version outside: shown
		mediaRow{"D", "image/jpeg", "/Private/d.jpg", month(1)}, // a version inside: kept out
	)
	// Above one chunk: 600 more kept out, one of them shown too.
	var many []string
	for i := 0; i < cHashChunk+100; i++ {
		h := fmt.Sprintf("%064x", i)
		many = append(many, h)
		insertMediaRows(t, db, "files", mediaRow{h, "image/jpeg", fmt.Sprintf("/Private/n%d.jpg", i), month(1)})
	}
	insertMediaRows(t, db, "files", mediaRow{many[cHashChunk+50], "image/jpeg", "/Phone/shown.jpg", month(1)})

	hashes := append([]string{"A", "B", "C", "D", "X"}, many...)
	folders := []string{"/Private/"}
	visible, err := d.VisibleHashes(hashes, folders)
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 3 || !visible["A"] || !visible["C"] || !visible[many[cHashChunk+50]] {
		t.Errorf("visible: %v", visible)
	}
	under, err := d.HashesWithRowsUnder(hashes, folders)
	if err != nil {
		t.Fatal(err)
	}
	if len(under) != 4+len(many) || under["X"] {
		t.Errorf("%d under, want %d", len(under), 4+len(many))
	}
	// No folder: everything a row holds.
	if all, err := d.VisibleHashes([]string{"A", "D", "X"}, nil); err != nil || len(all) != 2 || all["X"] {
		t.Errorf("no folders: %v, %v", all, err)
	}
}

func TestMediaHashesUnderMySQL(t *testing.T) {
	db, d := outOfImagesTestDB(t)
	insertMediaRows(t, db, "files",
		mediaRow{"A", "image/jpeg", "/F/a.jpg", month(1)},
		mediaRow{"B", "video/quicktime", "/F/b.mov", month(1)},
		mediaRow{"C", "application/octet-stream", "/F/c.HEIC", month(1)},
		mediaRow{"D", "application/pdf", "/F/d.pdf", month(1)},
		mediaRow{"E", "image/png", "/F/sub/e.png", month(1)},
		mediaRow{"G", "image/jpeg", "/Fx/g.jpg", month(1)},
		mediaRow{"H", "application/octet-stream", "/F/h.heic", month(1)}, // isMedia's rule is .HEIC
		mediaRow{"A", "image/jpeg", "/F/again.jpg", month(1)},
	)
	insertMediaRows(t, db, "file_versions", mediaRow{"V", "image/jpeg", "/F/a.jpg", month(1)})
	hashes, err := d.MediaHashesUnder("/F/")
	if err != nil {
		t.Fatal(err)
	}
	if got := pathsOf(hashes); got != "A,B,C,E,V" {
		t.Errorf("media under /F/: %s", got)
	}
}

func TestGetOutOfImagesFoldersWithoutTableMySQL(t *testing.T) {
	db, d := outOfImagesTestDB(t)
	var real int
	if err := db.QueryRow("select count(*) from information_schema.tables where table_schema = database() and table_name = 'out_of_images_folders'").Scan(&real); err != nil {
		t.Fatal(err)
	}
	if real == 0 {
		if folders, err := d.GetOutOfImagesFolders(); err != nil || folders != nil {
			t.Errorf("without the table: %v, %v", folders, err)
		}
	}
	if _, err := db.Exec("create temporary table `out_of_images_folders` (`path` varchar(768) character set utf8mb4 collate utf8mb4_bin not null, primary key (`path`)) engine=InnoDB"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"/A/", "/A/B/", "/Ab/", "/Other/"} {
		if err := d.SetOutOfImagesFolder(f, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.SetOutOfImagesFolder("/A/", true); err != nil {
		t.Errorf("flagging twice: %v", err)
	}
	if err := d.DelOutOfImagesUnder("/A/"); err != nil {
		t.Fatal(err)
	}
	folders, err := d.GetOutOfImagesFolders()
	if err != nil || pathsOf(folders) != "/Ab/,/Other/" {
		t.Errorf("after deleting /A/: %v, %v", folders, err)
	}
}

func TestDelFacesByHashesMySQL(t *testing.T) {
	db, d := outOfImagesTestDB(t)
	for _, q := range []string{
		"insert into `people` (`id`, `name`, `created`, `cover_face_id`) values ('p1', '', now(), 'f1'), ('p2', 'Ana', now(), 'f2'), ('p3', '', now(), 'f4')",
		"insert into `faces` values ('f1', 'A', 'p1', 0, 0, 1, 1, '', '', now()), ('f2', 'B', 'p2', 0, 0, 1, 1, '', '', now()), ('f3', 'C', 'p3', 0, 0, 1, 1, '', '', now()), ('f4', 'D', 'p3', 0, 0, 1, 1, '', '', now())",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	gone, err := d.DelFacesByHashes([]string{"A", "B", "C", "Z"})
	if err != nil || len(gone) != 3 {
		t.Fatalf("%v, %v", gone, err)
	}
	people := map[string]string{}
	rows, err := db.Query("select `id`, coalesce(`cover_face_id`, '') from `people`")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, cover string
		rows.Scan(&id, &cover)
		people[id] = cover
	}
	rows.Close()
	// p1: unnamed, no face left - gone. p2: named - stays, its cover
	// cleared. p3: still has f4 - stays, cover kept.
	if want := map[string]string{"p2": "", "p3": "f4"}; fmt.Sprint(people) != fmt.Sprint(want) {
		t.Errorf("people %v, want %v", people, want)
	}
	var faces int
	if err := db.QueryRow("select count(*) from `faces`").Scan(&faces); err != nil || faces != 1 {
		t.Errorf("%d faces left, want 1", faces)
	}
}

func TestSkippedAnalysisRoundTripMySQL(t *testing.T) {
	_, d := outOfImagesTestDB(t)
	many := make([]string, cHashChunk+10)
	for i := range many {
		many[i] = fmt.Sprintf("%064x", i)
	}
	if err := d.AddSkippedAnalysis(many); err != nil {
		t.Fatal(err)
	}
	if err := d.AddSkippedAnalysis(many[:3]); err != nil {
		t.Errorf("recording twice: %v", err)
	}
	// One of them already pending (an upload of the same content).
	if err := d.AddPendingAnalysis(many[cHashChunk]); err != nil {
		t.Fatal(err)
	}
	taken, err := d.TakeSkippedAnalysis(append([]string{"nope"}, many[cHashChunk-1:]...))
	if err != nil || len(taken) != 11 {
		t.Fatalf("taken %d, %v", len(taken), err)
	}
	if pending, err := d.PendingAnalysis(); err != nil || pathsOf(pending) != pathsOf(append([]string{}, many[cHashChunk-1:]...)) {
		t.Errorf("pending after the take: %d, %v", len(pending), err)
	}
	if err := d.DelSkippedAnalysis(many[0], many[1]); err != nil {
		t.Fatal(err)
	}
	left, err := d.SkippedAnalysis()
	if err != nil || len(left) != cHashChunk-3 {
		t.Errorf("%d left, %v", len(left), err)
	}
}
