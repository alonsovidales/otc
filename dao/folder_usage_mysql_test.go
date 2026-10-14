// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

// Issue #195 against a real MariaDB/MySQL (OTC_TEST_MYSQL_DSN, as
// search_files_mysql_test.go): temporary `files` and `file_versions` shaped
// as db.sql's, so nothing in that database is read or changed.

func folderUsageTestDB(tb testing.TB) (*sql.DB, *Dao) {
	tb.Helper()
	db := searchTestDB(tb)
	if _, err := db.Exec("create temporary table `file_versions` (`path` varchar(768) character set utf8mb4 collate utf8mb4_bin not null, " +
		"`hash` varchar(64) not null, `mime` varchar(150) not null, `size` bigint not null, `created` datetime not null, " +
		"`modified` datetime not null, `replaced` datetime not null, key (`path`), key (`hash`)) engine=InnoDB"); err != nil {
		tb.Fatal(err)
	}
	return db, NewWithDB(db)
}

// sizedRow is a file or a version at path with size bytes.
type sizedRow struct {
	path string
	size int64
}

func insertSized(tb testing.TB, db *sql.DB, table string, rows ...sizedRow) {
	tb.Helper()
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for start := 0; start < len(rows); start += 500 {
		batch := rows[start:min(start+500, len(rows))]
		var args []any
		var q string
		for i, r := range batch {
			hash := fmt.Sprintf("%064x", start+i+1)
			if table == "files" {
				args = append(args, hash, "application/octet-stream", at, at, r.path, r.size)
			} else {
				args = append(args, r.path, hash, "application/octet-stream", r.size, at, at, at)
			}
		}
		if table == "files" {
			q = "insert into `files` (`hash`, `mime`, `created`, `modified`, `path`, `size`) values " +
				strings.TrimSuffix(strings.Repeat("(?, ?, ?, ?, ?, ?), ", len(batch)), ", ")
		} else {
			q = "insert into `file_versions` (`path`, `hash`, `mime`, `size`, `created`, `modified`, `replaced`) values " +
				strings.TrimSuffix(strings.Repeat("(?, ?, ?, ?, ?, ?, ?), ", len(batch)), ", ")
		}
		if _, err := db.Exec(q, args...); err != nil {
			tb.Fatal(err)
		}
	}
}

// usageLines is what usageText makes of these entries' lines: sorted, one a
// line.
func usageLines(l ...string) string {
	sort.Strings(l)
	return strings.Join(l, "\n")
}

func folderUsageOf(t *testing.T, d *Dao, folder string) string {
	t.Helper()
	entries, err := d.FolderUsage(folder)
	if err != nil {
		t.Fatalf("%s: %v", folder, err)
	}
	return usageText(entries)
}

// Sums, counts and versions at the root and in folders, sizes past 4 GiB,
// a file next to a folder of the same name, and names LIKE or a looser
// collation would mix up.
func TestFolderUsageMySQL(t *testing.T) {
	db, d := folderUsageTestDB(t)
	big := int64(5) << 30
	insertSized(t, db, "files",
		sizedRow{"/Photos/2024/a.jpg", 100},
		sizedRow{"/Photos/2024/b.jpg", 200},
		sizedRow{"/Photos/2025/Trip/c.mov", big},
		sizedRow{"/Photos/d.jpg", 50},
		sizedRow{"/Photos/x", 7},        // a file named x...
		sizedRow{"/Photos/x/inside", 9}, // ...next to a folder x
		sizedRow{"/a_b/e.txt", 1},
		sizedRow{"/axb/f.txt", 2}, // _ is no wildcard
		sizedRow{"/100%/g.txt", 3},
		sizedRow{"/100ab/h.txt", 4}, // % is none either
		sizedRow{"/Café/i.txt", 5},
		sizedRow{"/Cafe/j.txt", 6}, // accents and case count
		sizedRow{"/CAFE/k.txt", 7},
		sizedRow{"/mac/Alonso’s_MacBook_Air/Docs/l.pdf", 8},
		sizedRow{`/back\slash/m.txt`, 9},
		sizedRow{"/top.txt", 10},
	)
	insertSized(t, db, "file_versions",
		sizedRow{"/Photos/x", 70},          // the file x's version
		sizedRow{"/Photos/x/inside", 90},   // the folder x's
		sizedRow{"/Photos/2024/a.jpg", 30}, // two versions of a.jpg
		sizedRow{"/Photos/2024/a.jpg", 31},
		sizedRow{"/Photos/Gone/old.jpg", 40}, // only a version left
	)

	if got, want := folderUsageOf(t, d, "/"), usageLines(
		"/100% dir=true files=3/1 versions=0",
		"/100ab dir=true files=4/1 versions=0",
		"/CAFE dir=true files=7/1 versions=0",
		"/Cafe dir=true files=6/1 versions=0",
		"/Café dir=true files=5/1 versions=0",
		fmt.Sprintf("/Photos dir=true files=%d/6 versions=%d", big+100+200+50+7+9, 70+90+30+31+40),
		"/a_b dir=true files=1/1 versions=0",
		"/axb dir=true files=2/1 versions=0",
		`/back\slash dir=true files=9/1 versions=0`,
		"/mac dir=true files=8/1 versions=0",
		"/top.txt dir=false files=10/1 versions=0",
	); got != want {
		t.Errorf("the root:\n got\n%s\nwant\n%s", got, want)
	}

	if got, want := folderUsageOf(t, d, "/Photos/"), usageLines(
		"/Photos/2024 dir=true files=300/2 versions=61",
		fmt.Sprintf("/Photos/2025 dir=true files=%d/1 versions=0", big),
		"/Photos/Gone dir=true files=0/0 versions=40",
		"/Photos/d.jpg dir=false files=50/1 versions=0",
		"/Photos/x dir=false files=7/1 versions=70",
		"/Photos/x dir=true files=9/1 versions=90",
	); got != want {
		t.Errorf("/Photos/:\n got\n%s\nwant\n%s", got, want)
	}

	for folder, want := range map[string]string{
		"/a_b/":                        "/a_b/e.txt dir=false files=1/1 versions=0",
		"/100%/":                       "/100%/g.txt dir=false files=3/1 versions=0",
		"/Cafe/":                       "/Cafe/j.txt dir=false files=6/1 versions=0",
		"/Café/":                       "/Café/i.txt dir=false files=5/1 versions=0",
		"/mac/Alonso’s_MacBook_Air/":   "/mac/Alonso’s_MacBook_Air/Docs dir=true files=8/1 versions=0",
		`/back\slash/`:                 `/back\slash/m.txt dir=false files=9/1 versions=0`,
		"/Photos/2025/Trip/":           fmt.Sprintf("/Photos/2025/Trip/c.mov dir=false files=%d/1 versions=0", big),
		"/Photos/x/":                   "/Photos/x/inside dir=false files=9/1 versions=90",
		"/nothing/":                    "",
		"/Photos/2024/a.jpg/":          "", // a file isn't a folder
		"/Photos/2024/a.jpg/../../../": "",
	} {
		if got := folderUsageOf(t, d, folder); got != want {
			t.Errorf("%s:\n got %s\nwant %s", folder, got, want)
		}
	}
}

// What the Storage view joins with: each entry with files is the very
// (path, folder or not) a listing of the same folder shows.
func TestFolderUsageMatchesTheListingMySQL(t *testing.T) {
	db, d := folderUsageTestDB(t)
	insertSized(t, db, "files",
		sizedRow{"/Docs/a.pdf", 1},
		sizedRow{"/Docs/Work/b.pdf", 2},
		sizedRow{"/Docs/Work/Deep/c.pdf", 3},
		sizedRow{"/Docs/Fotos (2020)/d.jpg", 4},
		sizedRow{"/Docs/x", 5},
		sizedRow{"/Docs/x/y", 6},
		sizedRow{"/Docs/Alonso’s notes/e.txt", 7},
	)
	for _, folder := range []string{"/", "/Docs/", "/Docs/Work/"} {
		listed, err := d.GetFilesByPath(folder, false, false)
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		for _, f := range listed {
			want = append(want, fmt.Sprintf("%s %v", f.Path, f.Mime == "inode/directory"))
		}
		entries, err := d.FolderUsage(folder)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, e := range entries {
			got = append(got, fmt.Sprintf("%s %v", e.Path, e.IsDir))
		}
		sort.Strings(want)
		sort.Strings(got)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%s:\n usage   %q\n listing %q", folder, got, want)
		}
	}
}

// A small folder is read by its range on `path` in both tables, not by a
// scan of either - the reason the folder query uses underPrefix.
func TestFolderUsageReadsTheRangeMySQL(t *testing.T) {
	db, _ := folderUsageTestDB(t)
	var rows, versions []sizedRow
	for i := range 3000 {
		rows = append(rows, sizedRow{fmt.Sprintf("/Big/%d/file_%d.bin", i%30, i), int64(i)})
		if i%10 == 0 {
			versions = append(versions, sizedRow{fmt.Sprintf("/Big/%d/file_%d.bin", i%30, i), int64(i)})
		}
	}
	for i := range 20 {
		rows = append(rows, sizedRow{fmt.Sprintf("/Small/s_%d.txt", i), 1})
	}
	insertSized(t, db, "files", rows...)
	insertSized(t, db, "file_versions", versions...)
	if _, err := db.Exec("analyze table `files`, `file_versions`"); err != nil {
		t.Fatal(err)
	}

	q, args := folderUsageQuery("/Small/")
	res, err := db.Query("explain "+q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	cols, err := res.Columns()
	if err != nil {
		t.Fatal(err)
	}
	tables := 0
	for res.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := res.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		row := map[string]string{}
		for i, c := range cols {
			row[c] = vals[i].String
		}
		if row["table"] != "files" && row["table"] != "file_versions" {
			continue // the union's own result, on servers that list it
		}
		tables++
		if row["type"] != "range" || row["key"] != "path" {
			t.Errorf("%s is read by %s on %q, not a range on path", row["table"], row["type"], row["key"])
		}
	}
	if err := res.Err(); err != nil {
		t.Fatal(err)
	}
	if tables != 2 {
		t.Errorf("explain listed %d of the two tables", tables)
	}
}

// The measurement in folder_usage.go, through FolderUsage itself: a
// library of 100k files - 45k photos and videos in one phone folder, 50k
// documents three or four folders deep, 5k source files, a 250-file
// folder - and 10k older versions; "listing" is ListFiles' own read of the
// root, for comparison:
//
//	OTC_TEST_MYSQL_DSN=... go test -run '^$' -bench FolderUsageMySQL ./dao
func BenchmarkFolderUsageMySQL(b *testing.B) {
	db, d := folderUsageTestDB(b)
	var files, versions []sizedRow
	for i := 1; i <= 45000; i++ {
		ext, size := ".HEIC", int64(1500000+i*7919%4000000)
		if i%10 == 0 {
			ext, size = ".MOV", int64(40000000+i*997%90000000)
		}
		files = append(files, sizedRow{fmt.Sprintf("/ios/Alonso’s iPhone/Camera Roll/IMG_%06d%s", i, ext), size})
	}
	doc := func(i int) string {
		deep := ""
		if i%3 == 0 {
			deep = fmt.Sprintf("/Deep %d", i%5)
		}
		return fmt.Sprintf("/mac/Alonso’s_MacBook_Air/Documents/Project %d/Part_%d%s/doc_%d.pdf", i%40, i%7, deep, i)
	}
	for i := 1; i <= 50000; i++ {
		files = append(files, sizedRow{doc(i), int64(20000 + i*104729%3000000)})
	}
	for i := 1; i <= 5000; i++ {
		files = append(files, sizedRow{fmt.Sprintf("/linux/box/home/me/src/repo%d/file_%d.go", i%20, i), int64(1000 + i*31%50000)})
	}
	for i := 1; i <= 250; i++ {
		files = append(files, sizedRow{fmt.Sprintf("/Small folder/pic_%d.jpg", i), int64(2000000 + i)})
	}
	for i := 1; i <= 10000; i++ {
		versions = append(versions, sizedRow{doc(i), int64(15000 + i*7%2000000)})
	}
	insertSized(b, db, "files", files...)
	insertSized(b, db, "file_versions", versions...)
	if _, err := db.Exec("analyze table `files`, `file_versions`"); err != nil {
		b.Fatal(err)
	}

	for _, c := range []struct{ name, folder string }{
		{"root", "/"},
		{"half", "/mac/"},
		{"45k-files", "/ios/Alonso’s iPhone/Camera Roll/"},
		{"250-files", "/Small folder/"},
		{"7-folders", "/mac/Alonso’s_MacBook_Air/Documents/Project 7/"},
	} {
		b.Run(c.name, func(b *testing.B) {
			for b.Loop() {
				if _, err := d.FolderUsage(c.folder); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	b.Run("listing", func(b *testing.B) {
		for b.Loop() {
			if _, err := d.GetFilesByPath("/", false, false); err != nil {
				b.Fatal(err)
			}
		}
	})
}
