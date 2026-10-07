// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/go-sql-driver/mysql"
	"golang.org/x/text/unicode/norm"
	"google.golang.org/protobuf/proto"
)

// searchTestDB is a connection to OTC_TEST_MYSQL_DSN's database with a
// temporary `files` table shaped as db.sql's - nothing in that database
// is read or changed - and the device DSN's parseTime and
// interpolateParams. One connection: a temporary table is the
// connection's own.
func searchTestDB(tb testing.TB) *sql.DB {
	tb.Helper()
	dsn := os.Getenv("OTC_TEST_MYSQL_DSN")
	if dsn == "" {
		tb.Skip("OTC_TEST_MYSQL_DSN not set")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		tb.Fatal(err)
	}
	cfg.ParseTime = true
	cfg.InterpolateParams = true
	conn, err := mysql.NewConnector(cfg)
	if err != nil {
		tb.Fatal(err)
	}
	db := sql.OpenDB(conn)
	tb.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec("create temporary table `files` (" +
		"`hash` varchar(64) not null, `mime` varchar(150) not null, `created` datetime not null, `modified` datetime not null, " +
		"`path` varchar(768) character set utf8mb4 collate utf8mb4_bin not null, `size` bigint not null, `cloud_id` varchar(255) null, " +
		"key (`hash`), unique (`path`), index using btree (`created`), index using btree (`modified`), index using btree (`size`), index using btree (`cloud_id`)" +
		") engine=InnoDB"); err != nil {
		tb.Fatal(err)
	}

	return db
}

// insertSearchRows adds a row per path, mime by extension, many at once.
func insertSearchRows(tb testing.TB, db *sql.DB, paths []string) {
	tb.Helper()
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	for start := 0; start < len(paths); start += 1000 {
		batch := paths[start:min(start+1000, len(paths))]
		var args []any
		for i, p := range batch {
			mime := "application/octet-stream"
			switch {
			case strings.HasSuffix(p, ".jpg"), strings.HasSuffix(p, ".HEIC"):
				mime = "image/jpeg"
			case strings.HasSuffix(p, ".pdf"):
				mime = "application/pdf"
			case strings.HasSuffix(p, ".txt"):
				mime = "text/plain"
			}
			args = append(args, fmt.Sprintf("%064x", start+i), mime, at, at, p, int64(1000+start+i))
		}
		q := "insert into `files` (`hash`, `mime`, `created`, `modified`, `path`, `size`) values " +
			strings.TrimSuffix(strings.Repeat("(?, ?, ?, ?, ?, ?), ", len(batch)), ", ")
		if _, err := db.Exec(q, args...); err != nil {
			tb.Fatal(err)
		}
	}
}

func searchPaths(files []*pb.File) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Path
	}
	return out
}

// Against a real MariaDB/MySQL, what sqlmock can't check: the ranking,
// case and accents, the escaping, the folders and that each entry is
// what a listing shows.
func TestSearchFilesMySQL(t *testing.T) {
	db := searchTestDB(t)
	insertSearchRows(t, db, []string{
		"/Fotos/Málaga 2019/IMG_1.jpg",
		"/Fotos/Málaga 2019/Playa/IMG_2.jpg",
		"/Docs/malaga-guide.pdf",
		"/Docs/Viaje a Málaga.pdf",
		"/MALAGA.txt",
		"/Viajes/Costa de Málaga/2019/a.jpg", // a folder with only a folder in it
		"/Docs/100%_done.txt",
		"/Docs/100ab done.txt",
		"/Other/notes.txt",
	})
	d := NewWithDB(db)

	want := []string{
		// The name starts with the text, shorter first.
		"/MALAGA.txt",
		"/Fotos/Málaga 2019",
		"/Docs/malaga-guide.pdf",
		// The name contains it.
		"/Viajes/Costa de Málaga",
		"/Docs/Viaje a Málaga.pdf",
		// Only a folder on the way has it; same length, by path.
		"/Fotos/Málaga 2019/Playa",
		"/Fotos/Málaga 2019/IMG_1.jpg",
		"/Viajes/Costa de Málaga/2019",
		"/Fotos/Málaga 2019/Playa/IMG_2.jpg",
		"/Viajes/Costa de Málaga/2019/a.jpg",
	}
	for _, q := range []string{"malaga", "MÁLAGA", "  Málaga\t"} {
		got, err := d.SearchFiles(q, 0)
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		if g := searchPaths(got); strings.Join(g, "|") != strings.Join(want, "|") {
			t.Errorf("%q:\n got %q\nwant %q", q, g, want)
		}
	}

	if got, err := d.SearchFiles("malaga", 4); err != nil || strings.Join(searchPaths(got), "|") != strings.Join(want[:4], "|") {
		t.Errorf("limit 4: %q, %v", searchPaths(got), err)
	}

	// LIKE's wildcards in the text are the characters themselves.
	if got, err := d.SearchFiles("100%_", 0); err != nil || strings.Join(searchPaths(got), "|") != "/Docs/100%_done.txt" {
		t.Errorf("100%%_: %q, %v", searchPaths(got), err)
	}
	if got, err := d.SearchFiles("100", 0); err != nil || len(got) != 2 {
		t.Errorf("100: %q, %v", searchPaths(got), err)
	}

	// A text across a "/" finds the folder that has it and what is in it.
	if got, err := d.SearchFiles("laga 2019/play", 0); err != nil ||
		strings.Join(searchPaths(got), "|") != "/Fotos/Málaga 2019/Playa|/Fotos/Málaga 2019/Playa/IMG_2.jpg" {
		t.Errorf("across a slash: %q, %v", searchPaths(got), err)
	}

	if got, err := d.SearchFiles("nothing like it", 0); err != nil || len(got) != 0 {
		t.Errorf("no match: %q, %v", searchPaths(got), err)
	}

	// Every entry is the one a listing of its folder shows.
	got, err := d.SearchFiles("malaga", 0)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]*pb.File{}
	for _, dir := range []string{"/", "/Docs/", "/Fotos/", "/Fotos/Málaga 2019/", "/Fotos/Málaga 2019/Playa/", "/Viajes/", "/Viajes/Costa de Málaga/", "/Viajes/Costa de Málaga/2019/"} {
		entries, err := d.GetFilesByPath(dir, false, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			listed[e.Path] = e
		}
	}
	for _, f := range got {
		if l, ok := listed[f.Path]; !ok || !proto.Equal(f, l) {
			t.Errorf("%s: search gives %v, its listing %v", f.Path, f, l)
		}
	}
}

// The limit: 20 when unset, 50 at most.
func TestSearchFilesLimitMySQL(t *testing.T) {
	db := searchTestDB(t)
	var paths []string
	for i := range 60 {
		paths = append(paths, fmt.Sprintf("/Bulk/match-%02d.txt", i))
	}
	insertSearchRows(t, db, paths)
	d := NewWithDB(db)
	for _, c := range []struct{ limit, want int }{{0, 20}, {-3, 20}, {7, 7}, {50, 50}, {500, 50}} {
		got, err := d.SearchFiles("match-", c.limit)
		if err != nil || len(got) != c.want {
			t.Errorf("limit %d: %d entries, %v; want %d", c.limit, len(got), err, c.want)
		}
	}
}

// 50,000 rows: a library's worth of photos, documents and folders. Run
// with -bench SearchFilesMySQL and OTC_TEST_MYSQL_DSN set.
func BenchmarkSearchFilesMySQL(b *testing.B) {
	db := searchTestDB(b)
	var paths []string
	cities := []string{"Málaga", "Córdoba", "Lisboa", "Amsterdam", "Zürich"}
	for i := range 40000 {
		paths = append(paths, fmt.Sprintf("/mac/Alonso’s_MacBook_Air/Pictures/%d/%02d/IMG_%05d.HEIC", 2015+i%10, 1+i%12, i))
	}
	for i := range 8000 {
		paths = append(paths, fmt.Sprintf("/Documents/Proyecto %d/Informe %d.pdf", i%200, i))
	}
	for i := range 2000 {
		paths = append(paths, fmt.Sprintf("/Fotos/Viaje a %s %d/DSC_%04d.jpg", cities[i%len(cities)], 2010+i%15, i))
	}
	insertSearchRows(b, db, paths)
	d := NewWithDB(db)
	for _, q := range []string{"img_123", "malaga", "informe 99", "a", "2019", "zzzz"} {
		b.Run(q, func(b *testing.B) {
			for b.Loop() {
				if _, err := d.SearchFiles(q, 0); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// 300,000 photos whose names aren't ASCII: every name is normalised in Go
// on a broad search ("a", "о" match them all), and none is kept. Run with
// -bench SearchFilesNonASCIINamesMySQL and OTC_TEST_MYSQL_DSN set.
func BenchmarkSearchFilesNonASCIINamesMySQL(b *testing.B) {
	db := searchTestDB(b)
	var paths []string
	for i := range 300000 {
		paths = append(paths, fmt.Sprintf("/mac/Alonso’s_MacBook_Air/Pictures/%d/%02d/Фото_%06d.HEIC", 2015+i%10, 1+i%12, i))
	}
	insertSearchRows(b, db, paths)
	d := NewWithDB(db)
	for _, q := range []string{"a", "о", "фото_12345"} {
		b.Run(q, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := d.SearchFiles(q, 0); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// A name a Mac stored decomposed (NFD) is found by the text typed either
// way, as a composed one is - which no LIKE does by itself.
func TestSearchFilesDecomposedNamesMySQL(t *testing.T) {
	db := searchTestDB(t)
	insertSearchRows(t, db, []string{
		"/Mac/Ma\u0301laga/foto.jpg", // decomposed
		"/Fotos/Málaga/foto.jpg",
		"/Fotos/\u304b\u3099\u3063\u3053\u3046.jpg", // decomposed kana: gakkou
		"/Docs/Майя.txt",
		"/Docs/other.txt",
	})
	d := NewWithDB(db)
	malaga := []string{"/Mac/Ma\u0301laga", "/Fotos/Málaga", "/Mac/Ma\u0301laga/foto.jpg", "/Fotos/Málaga/foto.jpg"}
	for _, c := range []struct {
		q    string
		want []string
	}{
		{"malaga", malaga},
		{"Málaga", malaga},
		{"MA\u0301LAGA", malaga},
		{"\u304c\u3063\u3053\u3046", []string{"/Fotos/\u304b\u3099\u3063\u3053\u3046.jpg"}},
		{"\u304b\u3063\u3053\u3046", nil}, // without the voicing it is another word
		{"майя", []string{"/Docs/Майя.txt"}},
	} {
		got, err := d.SearchFiles(c.q, 0)
		if err != nil {
			t.Fatalf("%q: %v", c.q, err)
		}
		if g := searchPaths(got); strings.Join(g, "|") != strings.Join(c.want, "|") {
			t.Errorf("%q:\n got %q\nwant %q", c.q, g, c.want)
		}
	}
}

// Scripts whose decomposed marks aren't accents: an Arabic hamza (أ is ا
// and U+0654), a Tamil or Bengali two-part vowel sign. Stored decomposed,
// such a name is found by the text typed composed, pasted decomposed, or
// in another case or with accents where it has Latin letters too.
func TestSearchFilesDecomposedScriptsMySQL(t *testing.T) {
	db := searchTestDB(t)
	nfd := norm.NFD.String
	ahmed, spain, phone, sister := "أحمد", "إسبانيا", "தொலைபேசி", "বোন"
	for _, s := range []string{ahmed, spain, phone, sister} {
		if nfd(s) == s || strings.ContainsFunc(nfd(s), func(r rune) bool { return r >= 0x300 && r <= 0x36f }) {
			t.Fatalf("%q: not a decomposable name without accents", s)
		}
	}
	insertSearchRows(t, db, []string{
		"/Mac/" + nfd(ahmed) + ".jpg",
		"/Fotos/" + ahmed + ".jpg",
		"/Mac/" + nfd(spain) + "/mapa.pdf",
		"/Mac/" + nfd(phone) + ".txt",
		"/Docs/" + phone + ".txt",
		"/Mac/" + nfd(sister) + ".jpg",
		"/Mac/Fotos de Ahmed " + nfd(ahmed) + "/a.jpg",
		"/Docs/other.txt",
	})
	d := NewWithDB(db)
	ahmeds := []string{"/Mac/" + nfd(ahmed) + ".jpg", "/Fotos/" + ahmed + ".jpg"}
	phones := []string{"/Docs/" + phone + ".txt", "/Mac/" + nfd(phone) + ".txt"}
	withName := []string{"/Mac/Fotos de Ahmed " + nfd(ahmed), "/Mac/Fotos de Ahmed " + nfd(ahmed) + "/a.jpg"}
	for _, c := range []struct {
		q    string
		want []string
	}{
		{ahmed, append(ahmeds, withName...)},
		{nfd(ahmed), append(ahmeds, withName...)},
		{spain, []string{"/Mac/" + nfd(spain), "/Mac/" + nfd(spain) + "/mapa.pdf"}},
		{nfd(spain), []string{"/Mac/" + nfd(spain), "/Mac/" + nfd(spain) + "/mapa.pdf"}},
		{phone, phones},
		{nfd(phone), phones},
		{sister, []string{"/Mac/" + nfd(sister) + ".jpg"}},
		{"FOTOS DE AHMED " + ahmed, withName},
		{"de Ahméd " + ahmed, withName}, // an accent the name hasn't
	} {
		got, err := d.SearchFiles(c.q, 0)
		if err != nil {
			t.Fatalf("%q: %v", c.q, err)
		}
		if g := searchPaths(got); strings.Join(g, "|") != strings.Join(c.want, "|") {
			t.Errorf("%q:\n got %q\nwant %q", c.q, g, c.want)
		}
	}
}

// The longest text that can be in a path is still asked for, decomposed:
// a 768-character name, every character of which decomposes.
func TestSearchFilesLongestTextMySQL(t *testing.T) {
	db := searchTestDB(t)
	name := "/" + strings.Repeat("가", 767)
	insertSearchRows(t, db, []string{name})
	d := NewWithDB(db)
	for _, q := range []string{name, norm.NFD.String(name)} {
		if got, err := d.SearchFiles(q, 0); err != nil || strings.Join(searchPaths(got), "|") != name {
			t.Errorf("%d bytes: %d entries, %v", len(q), len(got), err)
		}
	}
}
