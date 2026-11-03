// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Issue #206's month counts against a real MySQL/MariaDB (OTC_TEST_MYSQL_DSN,
// as browse_mysql_test.go): kept as the library changes through every dao
// write of `files`, they are SearchMediaDateBuckets' answer - images only
// and with videos, the folders kept out changed meanwhile - and their lease
// throws away what a process that died kept.

// monthsTestDB is searchesLibrary with the counts' tables, a settings row
// and the folders kept out.
func monthsTestDB(t *testing.T) (*sql.DB, *Dao) {
	t.Helper()
	db, d := searchesLibrary(t)
	for _, q := range []string{
		"create temporary table `settings` (`device_uuid` varchar(128) not null default '', `photo_months_for` mediumtext null, `photo_months_run` varchar(32) null) engine=InnoDB",
		"insert into `settings` (`device_uuid`) values ('d')",
		"create temporary table `photo_months` (`month` int not null, `images` int not null default 0, `media` int not null default 0, primary key (`month`)) engine=InnoDB",
		"create temporary table `photo_month_hashes` (`hash` varchar(64) not null, `images` int null, `media` int null, primary key (`hash`)) engine=InnoDB",
		"create temporary table `out_of_images_folders` (`path` varchar(768) character set utf8mb4 collate utf8mb4_bin not null, primary key (`path`)) engine=InnoDB",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return db, d
}

func describeBuckets(b []DateBucket) string {
	var out []string
	for _, x := range b {
		out = append(out, fmt.Sprintf("%s:%d", x.Month, x.Count))
	}
	return strings.Join(out, " ")
}

// waitBuilt waits for the background rebuild a read started.
func waitBuilt(t *testing.T, d *Dao) {
	t.Helper()
	for deadline := time.Now().Add(time.Minute); d.months.rebuilding.Load(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the month counts are never built")
		}
	}
}

// sameMonths checks the counts against the library's, both kinds; kept
// says they must come from the counts (built for excluded).
func sameMonths(t *testing.T, d *Dao, what string, excluded []string, kept bool) {
	t.Helper()
	for _, imagesOnly := range []bool{true, false} {
		want, err := d.SearchMediaDateBucketsWithin(nil, nil, nil, "", imagesOnly, excluded)
		if err != nil {
			t.Fatal(err)
		}
		got, err := d.PhotoMonths(&MediaBrowse{ImagesOnly: imagesOnly, Excluded: excluded})
		if err != nil {
			t.Fatal(err)
		}
		if g, w := describeBuckets(got), describeBuckets(want); g != w {
			t.Fatalf("%s, images only %v:\n got %s\nwant %s", what, imagesOnly, g, w)
		}
		if kept {
			if _, ok := d.keptMonths(imagesOnly, excluded); !ok {
				t.Fatalf("%s: not from the counts", what)
			}
		}
	}
}

func TestMonthCountsKeptAsTheLibraryChangesMySQL(t *testing.T) {
	db, d := monthsTestDB(t)
	if err := d.LeaseMonths(); err != nil {
		t.Fatal(err)
	}
	// Not built: counted the old way, built meanwhile.
	sameMonths(t, d, "before the first build", nil, false)
	waitBuilt(t, d)
	sameMonths(t, d, "built", nil, true)

	r := rand.New(rand.NewSource(20601))
	hash := func() string { return fmt.Sprintf("%064x", 0x20600000+r.Intn(760)) }
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	when := func() time.Time {
		return base.Add(time.Duration(r.Intn(720*24)) * time.Hour).Add(time.Duration(r.Intn(3600)) * time.Second)
	}
	mimes := []string{"image/jpeg", "image/jpeg", "image/heic", "video/mp4", "application/pdf"}
	folders := []string{"/Phone/", "/Mac/", "/Private/", "/Backup/"}
	paths := func() []string {
		rows, err := db.Query("select `path` from `files`")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var p string
			rows.Scan(&p)
			out = append(out, p)
		}
		return out
	}
	file := func(path string) *pb.File {
		at := when()
		f := &pb.File{Hash: hash(), Mime: mimes[r.Intn(len(mimes))], Path: path, Created: timestamppb.New(at), Modified: timestamppb.New(at)}
		SetFileSize(f, 1)
		return f
	}
	excluded := []string(nil)
	for round := 0; round < 12; round++ {
		for i := 0; i < 25; i++ {
			all := paths()
			switch op := r.Intn(6); op {
			case 0, 1: // a new path, often content already there (a copy)
				f := file(fmt.Sprintf("%s%d-%d.x", folders[r.Intn(len(folders))], round, i))
				if _, err := d.StoreNewFile(f, ""); err != nil {
					t.Fatal(err)
				}
			case 2: // a delete
				if _, err := d.DelFileByPathHash(all[r.Intn(len(all))]); err != nil {
					t.Fatal(err)
				}
			case 3: // other content at a path
				f := file(all[r.Intn(len(all))])
				if _, err := d.OverrideFile(f, ""); err != nil {
					t.Fatal(err)
				}
			case 4: // other content, the old one kept as a version
				f := file(all[r.Intn(len(all))])
				if err := d.ReplaceFileKeepingVersion(f, ""); err != nil {
					t.Fatal(err)
				}
			case 5: // a date taken, earlier
				if _, err := d.MoveToDateTaken(hash(), base.Add(-time.Duration(r.Intn(400))*24*time.Hour), base.AddDate(-2, 0, 0)); err != nil {
					t.Fatal(err)
				}
			}
		}
		sameMonths(t, d, fmt.Sprintf("after round %d", round), excluded, true)
		if round%4 == 3 {
			// Another folder kept out: counted the old way until built again.
			folder := folders[round/4%len(folders)]
			if _, err := db.Exec("insert ignore into `out_of_images_folders` values (?)", folder); err != nil {
				t.Fatal(err)
			}
			excluded = append(excluded, folder)
			sameMonths(t, d, "a folder kept out", excluded, false)
			waitBuilt(t, d)
			sameMonths(t, d, "built again for it", excluded, true)
		}
	}
	// The minute's pass counts what changed without a read.
	f := file("/Phone/late.jpg")
	f.Mime = "image/jpeg"
	if _, err := d.StoreNewFile(f, ""); err != nil {
		t.Fatal(err)
	}
	if err := d.ReconcileMonths(); err != nil || d.marked() != 0 {
		t.Fatalf("reconciled: %v, %d left", err, d.marked())
	}
	sameMonths(t, d, "after the minute's pass", excluded, true)
}

// A process that finds the counts another one kept and didn't give back (a
// run that died with changes not counted) builds them again; one that gave
// them back at a clean stop leaves them built for the next.
func TestMonthCountsLeaseMySQL(t *testing.T) {
	db, d := monthsTestDB(t)
	if err := d.LeaseMonths(); err != nil {
		t.Fatal(err)
	}
	sameMonths(t, d, "first", nil, false)
	waitBuilt(t, d)
	built := func() bool {
		var b sql.NullString
		if err := db.QueryRow("select `photo_months_for` from `settings`").Scan(&b); err != nil {
			t.Fatal(err)
		}
		return b.Valid
	}
	if !built() {
		t.Fatal("not built")
	}
	// Changes this process never counted, and it dies.
	f := &pb.File{Hash: fmt.Sprintf("%064x", 0x20690000), Mime: "image/jpeg", Path: "/Phone/new.jpg", Created: timestamppb.New(time.Date(2001, 5, 1, 0, 0, 0, 0, time.UTC))}
	f.Modified = f.Created
	if _, err := d.StoreNewFile(f, ""); err != nil {
		t.Fatal(err)
	}
	next := NewWithDB(db)
	if err := next.LeaseMonths(); err != nil {
		t.Fatal(err)
	}
	if built() {
		t.Fatal("counts a dead run kept were trusted")
	}
	sameMonths(t, next, "after a death", nil, false)
	waitBuilt(t, next)
	sameMonths(t, next, "built again", nil, true)
	// A clean stop counts its changes and gives the lease back.
	f.Hash, f.Path = fmt.Sprintf("%064x", 0x20690001), "/Phone/newer.jpg"
	if _, err := next.StoreNewFile(f, ""); err != nil {
		t.Fatal(err)
	}
	next.releaseMonths()
	after := NewWithDB(db)
	if err := after.LeaseMonths(); err != nil {
		t.Fatal(err)
	}
	if !built() {
		t.Fatal("counts given back at a clean stop were thrown away")
	}
	sameMonths(t, after, "after a clean stop", nil, true)
}

// Without the columns (release 168's script hasn't run) nothing is kept,
// and the counts are the library's, as before.
func TestMonthCountsWithoutTheirColumnsMySQL(t *testing.T) {
	db, d := searchesLibrary(t)
	if _, err := db.Exec("create temporary table `settings` (`device_uuid` varchar(128) not null default '') engine=InnoDB"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("insert into `settings` values ('d')"); err != nil {
		t.Fatal(err)
	}
	if err := d.LeaseMonths(); err == nil {
		t.Fatal("leased without the columns")
	}
	want, _ := d.SearchMediaDateBucketsWithin(nil, nil, nil, "", false, nil)
	got, err := d.PhotoMonths(&MediaBrowse{})
	if err != nil || describeBuckets(got) != describeBuckets(want) {
		t.Fatalf("%v: %s, want %s", err, describeBuckets(got), describeBuckets(want))
	}
}

// Filtered month counts are SearchMediaDateBucketsWithin's, read from the
// filter's photos.
func TestFilteredMonthCountsMySQL(t *testing.T) {
	_, d := searchesLibrary(t)
	for _, m := range append(filterSearches(), rankedSearches()...) {
		for _, imagesOnly := range []bool{true, false} {
			for _, excluded := range [][]string{nil, {"/Private/"}} {
				want, err := d.SearchMediaDateBucketsWithin(m.Area, m.Tags, m.PersonIDs, m.GroupID, imagesOnly, excluded)
				if err != nil {
					t.Fatal(err)
				}
				got, err := d.PhotoMonths(&MediaBrowse{MediaFilters: m, ImagesOnly: imagesOnly, Excluded: excluded})
				if err != nil {
					t.Fatal(err)
				}
				if g, w := describeBuckets(got), describeBuckets(want); g != w {
					t.Fatalf("%s:\n got %s\nwant %s", describeSearch(m, imagesOnly, excluded), g, w)
				}
			}
		}
	}
}

// The month counts of a large library (OTC_TEST_BROWSE_LARGE=1): the old
// pass over it, the counts' build, and a read of them.
func TestMonthCountsLargeMySQL(t *testing.T) {
	if os.Getenv("OTC_TEST_BROWSE_LARGE") != "1" {
		t.Skip("OTC_TEST_BROWSE_LARGE not set")
	}
	db, d := searchTablesDB(t)
	fillLargeLibrary(t, d, 200000)
	fillLargeSearchTables(t, db, 200000)
	for _, q := range []string{
		"create temporary table `settings` (`device_uuid` varchar(128) not null default '', `photo_months_for` mediumtext null, `photo_months_run` varchar(32) null) engine=InnoDB",
		"insert into `settings` (`device_uuid`) values ('d')",
		"create temporary table `photo_months` (`month` int not null, `images` int not null default 0, `media` int not null default 0, primary key (`month`)) engine=InnoDB",
		"create temporary table `photo_month_hashes` (`hash` varchar(64) not null, `images` int null, `media` int null, primary key (`hash`)) engine=InnoDB",
		"create temporary table `out_of_images_folders` (`path` varchar(768) character set utf8mb4 collate utf8mb4_bin not null, primary key (`path`)) engine=InnoDB",
		"insert into `out_of_images_folders` values ('/Private/')",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	excluded := []string{"/Private/"}
	var want []DateBucket
	took, _ := measure(t, 3, func() {
		var err error
		if want, err = d.SearchMediaDateBucketsWithin(nil, nil, nil, "", false, excluded); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("the library's pass (every Images opening before): %d months in %v", len(want), took)
	if err := d.LeaseMonths(); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	if err := d.RebuildMonths(); err != nil {
		t.Fatal(err)
	}
	t.Logf("the counts built (once): %v", time.Since(t0).Round(time.Millisecond))
	var got []DateBucket
	took, bytes := measure(t, 5, func() {
		var err error
		if got, err = d.PhotoMonths(&MediaBrowse{Excluded: excluded}); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("read from the counts: %v, %.1f KB allocated", took, float64(bytes)/1e3)
	if describeBuckets(got) != describeBuckets(want) {
		t.Fatal("the counts aren't the library's")
	}
	// Uploads meanwhile: each counted before the next read.
	for i := 0; i < 100; i++ {
		at := time.Date(2012, 3, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour)
		f := &pb.File{Hash: fmt.Sprintf("%064x", 0x4060000000+i), Mime: "image/jpeg", Path: fmt.Sprintf("/Phone/new/%d.jpg", i), Created: timestamppb.New(at), Modified: timestamppb.New(at)}
		if _, err := d.StoreNewFile(f, ""); err != nil {
			t.Fatal(err)
		}
	}
	t0 = time.Now()
	if got, err := d.PhotoMonths(&MediaBrowse{Excluded: excluded}); err != nil || len(got) == 0 {
		t.Fatal(err)
	}
	t.Logf("read after 100 uploads (counted first): %v", time.Since(t0).Round(100*time.Microsecond))
	want, _ = d.SearchMediaDateBucketsWithin(nil, nil, nil, "", false, excluded)
	got, _ = d.PhotoMonths(&MediaBrowse{Excluded: excluded})
	if describeBuckets(got) != describeBuckets(want) {
		t.Fatal("the counts aren't the library's after the uploads")
	}
	for _, s := range []struct {
		what string
		m    MediaFilters
	}{
		{"person big", MediaFilters{PersonIDs: []string{"big"}}},
		{"area home", MediaFilters{Area: areaHome}},
		{"collection imported", MediaFilters{GroupID: "trip"}},
	} {
		took, _ := measure(t, 3, func() {
			if _, err := d.SearchMediaDateBucketsWithin(s.m.Area, s.m.Tags, s.m.PersonIDs, s.m.GroupID, false, excluded); err != nil {
				t.Fatal(err)
			}
		})
		now, _ := measure(t, 3, func() {
			if _, err := d.PhotoMonths(&MediaBrowse{MediaFilters: s.m, Excluded: excluded}); err != nil {
				t.Fatal(err)
			}
		})
		t.Logf("month counts of %s: %v before, %v now", s.what, took, now)
	}
}
