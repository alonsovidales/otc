// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"fmt"
	"sort"
	"testing"
	"time"
)

// The storage integrity check's queries, against a real MySQL/MariaDB
// (OTC_TEST_MYSQL_DSN, as search_files_mysql_test.go): the distinct
// hashes a page at a time from each table, and the paths of only the
// missing ones, in batches of cInBatch.
func TestIntegrityQueriesMySQL(t *testing.T) {
	db, d := outOfImagesTestDB(t)
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	var rows []mediaRow
	for i := 0; i < 1200; i++ {
		// Two paths for every third hash.
		h := fmt.Sprintf("%064x", i/3*3)
		rows = append(rows, mediaRow{hash: h, mime: "image/jpeg", path: fmt.Sprintf("/p/%04d.jpg", i), created: at})
	}
	insertMediaRows(t, db, "files", rows...)
	insertMediaRows(t, db, "file_versions", mediaRow{hash: fmt.Sprintf("%064x", 3), mime: "image/jpeg", path: "/p/0003.jpg", created: at},
		mediaRow{hash: fmt.Sprintf("%064x", 99999), mime: "image/jpeg", path: "/v/old.jpg", created: at})

	for _, versions := range []bool{false, true} {
		var all []string
		after := ""
		for {
			page, err := d.HashesPage(versions, after, 150)
			if err != nil {
				t.Fatal(err)
			}
			all = append(all, page...)
			if len(page) < 150 {
				break
			}
			after = page[len(page)-1]
		}
		want := 400
		if versions {
			want = 2
		}
		if len(all) != want || !sort.StringsAreSorted(all) {
			t.Fatalf("versions %v: %d hashes (sorted %v), want %d", versions, len(all), sort.StringsAreSorted(all), want)
		}
	}

	var missing []string
	for i := 0; i < 1200; i += 3 {
		missing = append(missing, fmt.Sprintf("%064x", i))
	}
	missing = append(missing, fmt.Sprintf("%064x", 99999), fmt.Sprintf("%064x", 1)) // a version's; one nothing uses
	paths, err := d.ContentPathsOf(missing)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 401 {
		t.Fatalf("%d hashes with paths, want 401", len(paths))
	}
	if got := paths[fmt.Sprintf("%064x", 3)]; len(got) != 4 { // three files and a version
		t.Fatalf("paths of hash 3: %v", got)
	}
	if got := paths[fmt.Sprintf("%064x", 99999)]; len(got) != 1 || got[0] != "/v/old.jpg" {
		t.Fatalf("a version's paths: %v", got)
	}
}

// The posts' files in use: every hash of social_publications_files once.
func TestSocialMediaHashesMySQL(t *testing.T) {
	db := searchTestDB(t)
	if _, err := db.Exec("create temporary table `social_publications_files` (`pos` int not null, `hash` varchar(64) not null, `uuid` varchar(64) not null, " +
		"`mime` varchar(150) not null, `created` datetime not null, `modified` datetime not null, `size` bigint not null, key (`hash`), key (`uuid`)) engine=InnoDB"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i, r := range [][2]string{{"p1", "aa"}, {"p1", "bb"}, {"p2", "aa"}} {
		if _, err := db.Exec("insert into `social_publications_files` values (?, ?, ?, 'image/jpeg', ?, ?, 1)", i, r[1], r[0], now, now); err != nil {
			t.Fatal(err)
		}
	}
	got, err := NewWithDB(db).SocialMediaHashes()
	if err != nil || len(got) != 2 || !got["aa"] || !got["bb"] {
		t.Fatalf("%v, %v", got, err)
	}
}

// GetThumbnails' rows in one query.
func TestGetFilesByPathsMySQL(t *testing.T) {
	db := searchTestDB(t)
	insertSearchRows(t, db, []string{"/g/a.jpg", "/g/b.jpg", "/g/c.pdf"})
	got, err := NewWithDB(db).GetFilesByPaths([]string{"/g/a.jpg", "/g/gone.jpg", "/g/c.pdf", "/g/a.jpg"})
	if err != nil || len(got) != 2 || got["/g/a.jpg"] == nil || got["/g/c.pdf"] == nil || got["/g/c.pdf"].Mime != "application/pdf" {
		t.Fatalf("%v, %v", got, err)
	}
}
