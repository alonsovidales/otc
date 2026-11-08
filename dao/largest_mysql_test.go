// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// Settings › Largest files against a real MariaDB/MySQL (OTC_TEST_MYSQL_DSN,
// as search_files_mysql_test.go): temporary `files` and `file_versions`,
// so nothing in that database is read or changed.

// largestPages reads every page of LargestFiles, limit at a time, as
// "<hash>@<path>" each.
func largestPages(t *testing.T, d *Dao, limit int) ([]string, int) {
	t.Helper()
	var out []string
	after, pages := LargestAfter{First: true}, 0
	for {
		files, next, more, err := d.LargestFiles(after, limit)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, f := range files {
			out = append(out, fmt.Sprintf("%s@%s", f.Hash, f.Path))
		}
		if !more {
			return out, pages
		}
		if pages > 1000 {
			t.Fatal("the pages never end")
		}
		after = next
	}
}

// The largest first; a content held at three paths once, at its first
// path in byte order; contents of the same size by hash, a page boundary
// falling among them; older versions not listed; every kind of file and
// every folder.
func TestLargestFilesMySQL(t *testing.T) {
	db, d := folderUsageTestDB(t)
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	add := func(table, hash, path string, size int64) {
		t.Helper()
		var err error
		if table == "files" {
			_, err = db.Exec("insert into `files` (`hash`, `mime`, `created`, `modified`, `path`, `size`) values (?, 'application/octet-stream', ?, ?, ?, ?)", hash, at, at, path, size)
		} else {
			_, err = db.Exec("insert into `file_versions` (`path`, `hash`, `mime`, `size`, `created`, `modified`, `replaced`) values (?, ?, 'application/octet-stream', ?, ?, ?, ?)", path, hash, size, at, at, at)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	add("files", "v", "/phone/VID.MOV", 5<<30) // past 4 GiB
	add("files", "a", "/phone/a.jpg", 1000)
	add("files", "a", "/Mac/a.jpg", 1000)
	add("files", "a", "/backup/a.jpg", 1000)
	for _, h := range []string{"t3", "t1", "t4", "t2"} { // the same size: by hash
		add("files", h, "/docs/"+h+".pdf", 500)
	}
	add("files", "s", "/Private/s.txt", 10) // a folder kept out of Images too
	add("file_versions", "old", "/backup/old.jpg", 1<<40)

	want := []string{"v@/phone/VID.MOV", "a@/Mac/a.jpg", "t1@/docs/t1.pdf", "t2@/docs/t2.pdf", "t3@/docs/t3.pdf", "t4@/docs/t4.pdf", "s@/Private/s.txt"}
	for _, limit := range []int{1, 2, 3, 4, 7, 50} {
		got, pages := largestPages(t, d, limit)
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("pages of %d:\n got %v\nwant %v", limit, got, want)
		}
		if wantPages := len(want)/limit + 1; pages != wantPages {
			t.Errorf("pages of %d: %d pages, want %d", limit, pages, wantPages)
		}
	}
	// A page starting inside the ties: after t2 at 500 bytes.
	files, next, more, err := d.LargestFiles(LargestAfter{Size: 500, Hash: "t2"}, 2)
	if err != nil || len(files) != 2 || files[0].Hash != "t3" || files[1].Hash != "t4" || !more || next != (LargestAfter{Size: 500, Hash: "t4"}) {
		t.Errorf("after t2: %v %+v %v %v", files, next, more, err)
	}
	if FileSize(files[0]) != 500 {
		t.Errorf("t3's size %d", FileSize(files[0]))
	}
	// An empty library.
	if _, err := db.Exec("delete from `files`"); err != nil {
		t.Fatal(err)
	}
	if files, _, more, err := d.LargestFiles(LargestAfter{First: true}, 50); err != nil || len(files) != 0 || more {
		t.Errorf("no files: %v %v %v", files, more, err)
	}
}

// A large library (OTC_TEST_BROWSE_LARGE=1, as TestBrowseMediaLargeMySQL):
// 180k rows, 150k contents - a tenth of them at two or three paths, most
// of them photos of a few MB with many at the same size - read a page of
// 50 at a time from the top, and further down.
func TestLargestFilesLargeMySQL(t *testing.T) {
	if os.Getenv("OTC_TEST_BROWSE_LARGE") != "1" {
		t.Skip("OTC_TEST_BROWSE_LARGE not set")
	}
	db, d := folderUsageTestDB(t)
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	const contents = 150_000
	var args []any
	rows := 0
	flush := func() {
		if len(args) == 0 {
			return
		}
		n := len(args) / 6
		q := "insert into `files` (`hash`, `mime`, `created`, `modified`, `path`, `size`) values " + strings.TrimSuffix(strings.Repeat("(?, ?, ?, ?, ?, ?), ", n), ", ")
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
		rows += n
		args = args[:0]
	}
	seed := uint32(7)
	for i := 0; i < contents; i++ {
		seed = seed*1664525 + 1013904223
		size := int64(1_000_000 + seed%8_000_000)
		if i%7 == 0 {
			size = 3_145_728 // many of one size
		}
		if i%500 == 0 {
			size = int64(1+seed%4) << 30 // videos
		}
		hash := fmt.Sprintf("%064x", 0x3070000000+i)
		copies := 1 + i%10/9 + i%30/29 // a tenth at two or three paths
		for c := 0; c < copies; c++ {
			args = append(args, hash, "image/jpeg", at, at, fmt.Sprintf("/library/%d/%05d-%d.jpg", i%300, i, c), size)
		}
		if len(args) >= 6*1000 {
			flush()
		}
	}
	flush()
	t.Logf("%d rows, %d contents", rows, contents)
	if _, err := db.Exec("analyze table `files`"); err != nil {
		t.Logf("analyze: %v", err)
	}
	var times []time.Duration
	after := LargestAfter{First: true}
	for page := 0; page < 40; page++ {
		start := time.Now()
		files, next, more, err := d.LargestFiles(after, 50)
		times = append(times, time.Since(start))
		if err != nil || len(files) != 50 || !more {
			t.Fatalf("page %d: %d files, %v", page, len(files), err)
		}
		after = next
	}
	// Deep in the list: past most of it.
	start := time.Now()
	if _, _, _, err := d.LargestFiles(LargestAfter{Size: 1_200_000, Hash: ""}, 50); err != nil {
		t.Fatal(err)
	}
	deep := time.Since(start)
	first := times[0]
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	t.Logf("a page of 50: median %v, slowest %v, the first %v; deep in the list %v", times[len(times)/2], times[len(times)-1], first, deep)
}
