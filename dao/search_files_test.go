// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/DATA-DOG/go-sqlmock"
	"golang.org/x/text/unicode/norm"
)

// One text whatever its case, accents or normal form: a Mac may store a
// name decomposed (NFD), with the accent as a character of its own.
func TestSearchFold(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"IMG_001.JPG", "img_001.jpg"},
		{"Málaga", "malaga"},
		{"Ma\u0301laga", "malaga"}, // decomposed
		{"MÁLAGA", "malaga"},
		{"España", "espana"},
		{"Ölçek", "olcek"},
		{"ΣΊΣΥΦΟΣ", "σισυφοσ"},
		{"σίσυφος", "σισυφοσ"}, // final sigma
		{"Straße", "straße"},
		{"\u304b\u3099", "\u304c"}, // a decomposed kana keeps its voicing: ka+mark is ga
		{"\u1100\u1161", "\uac00"}, // decomposed Hangul jamo are the syllable
		{"\u0301", ""},
		{"a/B/ć", "a/b/c"},
		{norm.NFD.String("أحمد"), "أحمد"}, // a decomposed hamza is kept: alef+hamza is أ
		{norm.NFD.String("தொலைபேசி"), "தொலைபேசி"},
		{"ФОТО_001.JPG", "фото_001.jpg"},
		{"x\u0301", "x"}, // an accent on nothing it composes with
	} {
		if got := searchFold(c.in); got != c.want {
			t.Errorf("searchFold(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// What the database is asked for on top of the composed text: the text
// decomposed, accents left out, only when something other than an accent
// decomposes.
func TestSearchDecomposed(t *testing.T) {
	nfd := norm.NFD.String
	for _, c := range []struct{ in, want string }{
		{"malaga", ""},
		{"Málaga", ""}, // the accent: cDecomposedRe and the collation
		{"Фото", ""},
		{"أحمد", nfd("أحمد")},
		{"إسبانيا", nfd("إسبانيا")},
		{"தொலைபேசி", nfd("தொலைபேசி")},
		{"বোন", nfd("বোন")},
		{"Ahméd أحمد", "Ahmed " + nfd("أحمد")},
		{"\uac00", "\u1100\u1161"},
	} {
		if got := searchDecomposed(c.in); got != c.want {
			t.Errorf("searchDecomposed(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func searchRanked(q string, limit int, paths ...string) []string {
	r := newSearchRanking(searchFold(q), limit)
	for _, p := range paths {
		r.add(p)
	}
	var out []string
	for _, h := range r.best {
		if h.dir {
			out = append(out, h.path+"/")
		} else {
			out = append(out, h.path)
		}
	}
	return out
}

// The ranking on its own: which folders a file brings, and in what order
// everything comes. Folders are shown here with a trailing "/".
func TestSearchRanking(t *testing.T) {
	library := []string{
		"/Fotos/Málaga 2019/IMG_1.jpg",
		"/Fotos/Málaga 2019/Playa/IMG_2.jpg",
		"/Docs/malaga-guide.pdf",
		"/Docs/Viaje a Málaga.pdf",
		"/MALAGA.txt",
		"/Viajes/Costa de Ma\u0301laga/2019/a.jpg", // decomposed, and a folder holding only a folder
		"/Docs/notes.txt",                          // the database's pick, not a match
	}
	want := []string{
		// The name starts with the text, shorter first.
		"/MALAGA.txt",
		"/Fotos/Málaga 2019/",
		"/Docs/malaga-guide.pdf",
		// The name contains it; same length, by path.
		"/Docs/Viaje a Málaga.pdf",
		"/Viajes/Costa de Ma\u0301laga/",
		// Only a folder on the way has it.
		"/Fotos/Málaga 2019/Playa/",
		"/Fotos/Málaga 2019/IMG_1.jpg",
		"/Viajes/Costa de Ma\u0301laga/2019/",
		"/Fotos/Málaga 2019/Playa/IMG_2.jpg",
		"/Viajes/Costa de Ma\u0301laga/2019/a.jpg",
	}
	for _, q := range []string{"malaga", "MÁLAGA", "Ma\u0301laga"} {
		if got := searchRanked(q, 20, library...); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%q:\n got %q\nwant %q", q, got, want)
		}
	}
	// The order the rows come in doesn't matter, nor a cut.
	reversed := make([]string, len(library))
	for i, p := range library {
		reversed[len(library)-1-i] = p
	}
	if got := searchRanked("malaga", 4, reversed...); strings.Join(got, "|") != strings.Join(want[:4], "|") {
		t.Errorf("limit 4, reversed rows: %q", got)
	}

	for _, c := range []struct {
		name, q string
		paths   []string
		want    []string
	}{
		{"a text across a slash", "laga 2019/play", library,
			[]string{"/Fotos/Málaga 2019/Playa/", "/Fotos/Málaga 2019/Playa/IMG_2.jpg"}},
		{"a text ending in a slash is in no folder's path", "2019/", library,
			[]string{"/Fotos/Málaga 2019/Playa/", "/Fotos/Málaga 2019/IMG_1.jpg", "/Fotos/Málaga 2019/Playa/IMG_2.jpg", "/Viajes/Costa de Ma\u0301laga/2019/a.jpg"}},
		{"a text from the root", "/docs", library,
			[]string{"/Docs/", "/Docs/notes.txt", "/Docs/malaga-guide.pdf", "/Docs/Viaje a Málaga.pdf"}},
		{"wildcards are characters", "100%_", []string{"/a/100%_done.txt", "/a/100ab done.txt", "/b%/x"},
			[]string{"/a/100%_done.txt"}},
		{"a folder and a file of the same name, folder first", "x", []string{"/a/x", "/a/x/y"},
			[]string{"/a/x/", "/a/x", "/a/x/y"}},
		{"a path without a leading slash", "pic", []string{"pics/a.jpg"},
			[]string{"pics/", "pics/a.jpg"}},
		{"no match", "zzz", library, nil},
	} {
		if got := searchRanked(c.q, 20, c.paths...); strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

// A broad search over many files with non-ASCII names keeps only the
// best entries and the names of folders: none of a file's.
func TestSearchRankingKeepsNoFileNames(t *testing.T) {
	r := newSearchRanking(searchFold("a"), 20)
	folders := map[string]bool{}
	for i := range 30000 {
		folder := fmt.Sprintf("Viaje %d a Málaga", i%50)
		folders[folder] = true
		r.add(fmt.Sprintf("/Fotos/%s/Café_%07d.jpg", folder, i))
	}
	if len(r.best) != 20 || r.best[0].path != "/Fotos/Viaje 0 a Málaga" || !r.best[0].dir {
		t.Errorf("ranking: %d entries, first %v", len(r.best), r.best[0])
	}
	if len(r.foldedNames) != len(folders) {
		t.Errorf("%d folded names kept, want the %d folders'", len(r.foldedNames), len(folders))
	}
}

func TestSearchFilesLimit(t *testing.T) {
	for _, c := range []struct{ in, want int }{{0, 20}, {-1, 20}, {1, 1}, {20, 20}, {49, 49}, {50, 50}, {51, 50}, {1 << 30, 50}} {
		if got := SearchFilesLimit(c.in); got != c.want {
			t.Errorf("SearchFilesLimit(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// No text, nothing asked of the database.
func TestSearchFilesEmptyQueryRunsNothing(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	for _, q := range []string{"", "  \t ", "\u0301"} {
		if files, err := d.SearchFiles(q, 0); err != nil || files != nil {
			t.Errorf("%q: %v, %v", q, files, err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A text longer than any path can be is refused before it is normalised
// or sent to the database; trimmed first, one that fits is asked for.
func TestSearchFilesRefusesALongerTextThanAnyPath(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	for _, q := range []string{strings.Repeat("a", SearchFilesMaxTextBytes+1), strings.Repeat("é", 1<<20)} {
		if files, err := d.SearchFiles(q, 0); err != nil || files != nil {
			t.Errorf("%d bytes: %v, %v", len(q), files, err)
		}
	}
	longest := strings.Repeat("a", SearchFilesMaxTextBytes)
	mock.ExpectQuery(searchPrefilter).WithArgs("%"+longest+"%", cDecomposedRe).WillReturnRows(pathRows())
	if _, err := d.SearchFiles("  "+longest+"\n", 0); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// The cap leaves room for any path: 768 characters, each in any case and
// normal form.
func TestSearchFilesMaxTextBytesFitsAnyPath(t *testing.T) {
	widest := 0
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if !utf8.ValidRune(r) {
			continue
		}
		for _, c := range []rune{r, unicode.ToUpper(r), unicode.ToLower(r), unicode.ToTitle(r)} {
			for _, f := range []norm.Form{norm.NFC, norm.NFD} {
				widest = max(widest, len(f.String(string(c))))
			}
		}
	}
	if 768*widest > SearchFilesMaxTextBytes {
		t.Errorf("a character takes up to %d bytes: %d for a path, over the cap %d", widest, 768*widest, SearchFilesMaxTextBytes)
	}
}

const searchPrefilter = "select `path` from `files` where `path` like \\? collate utf8mb4_general_ci or `path` regexp \\?$"

func pathRows(paths ...string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"path"})
	for _, p := range paths {
		rows.AddRow(p)
	}
	return rows
}

func searchArgs(paths ...string) []driver.Value {
	out := make([]driver.Value, len(paths))
	for i, p := range paths {
		out[i] = p
	}
	return out
}

// The database is asked for the text as typed (trimmed, composed), its
// LIKE wildcards escaped, and for any decomposed name.
func TestSearchFilesAsksForTheTextEscaped(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(searchPrefilter).WithArgs(`%50\%\_Off\\x%`, cDecomposedRe).
		WillReturnRows(pathRows("/a/50%_off\\x.txt", "/a/50ab_offx.txt"))
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` in \\(\\?\\)$").
		WithArgs("/a/50%_off\\x.txt").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).
			AddRow("h", "text/plain", time.Now(), time.Now(), "/a/50%_off\\x.txt", int64(3)))
	mock.ExpectQuery(searchPrefilter).WithArgs("%Málaga%", cDecomposedRe).WillReturnRows(pathRows())

	d := NewWithDB(db)
	files, err := d.SearchFiles("  50%_Off\\x  ", 0)
	if err != nil || len(files) != 1 || files[0].Path != "/a/50%_off\\x.txt" {
		t.Fatalf("got %v, %v", files, err)
	}
	// Typed decomposed, asked composed: the collation finds it in a
	// composed name.
	if _, err := d.SearchFiles("Ma\u0301laga", 0); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A text with a letter that decomposes into something other than an
// accent is also asked for decomposed, for a name a Mac stored that way;
// cDecomposedRe can't find those (TestSearchFilesDecomposedScriptsMySQL).
func TestSearchFilesAsksForDecomposedScripts(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	ahmed, decomposed := "أحمد", norm.NFD.String("أحمد")
	stored := "/Mac/Fotos_1 " + decomposed + ".jpg"
	// Typed composed or pasted decomposed, the same question.
	for _, c := range []struct{ q, composed, decomposed string }{
		{"Fotos_1 " + ahmed, `%Fotos\_1 ` + ahmed + `%`, `%Fotos\_1 ` + decomposed + `%`},
		{" fotos_1 " + decomposed + "\t", `%fotos\_1 ` + ahmed + `%`, `%fotos\_1 ` + decomposed + `%`},
	} {
		mock.ExpectQuery("select `path` from `files` where `path` like ? collate utf8mb4_general_ci or `path` regexp ? or `path` like ? collate utf8mb4_general_ci").
			WithArgs(c.composed, cDecomposedRe, c.decomposed).WillReturnRows(pathRows(stored))
		mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` in (?)").
			WithArgs(stored).
			WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).
				AddRow("h", "image/jpeg", time.Now(), time.Now(), stored, int64(1)))
		files, err := d.SearchFiles(c.q, 0)
		if err != nil || len(files) != 1 || files[0].Path != stored {
			t.Errorf("%q: %v, %v", c.q, files, err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// Files come back as GetFilesByPath reads them, folders as its folder
// entries, in rank order; a file deleted between the two queries is left
// out.
func TestSearchFilesReadsTheRankedFiles(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 10, 7, 9, 30, 0, 0, time.UTC)
	mock.ExpectQuery(searchPrefilter).WillReturnRows(pathRows(
		"/Fotos/Málaga/IMG_1.jpg", "/Big/malaga.mov", "/Docs/Viaje a Málaga.pdf", "/Fotos/Málaga/IMG_2.jpg"))
	mock.ExpectQuery("from `files` where `path` in \\(\\?, \\?, \\?, \\?\\)$").
		WithArgs(searchArgs("/Big/malaga.mov", "/Docs/Viaje a Málaga.pdf", "/Fotos/Málaga/IMG_1.jpg", "/Fotos/Málaga/IMG_2.jpg")...).
		WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).
			AddRow("h2", "image/jpeg", at, at, "/Fotos/Málaga/IMG_2.jpg", int64(10)).
			AddRow("hm", "video/quicktime", at, at, "/Big/malaga.mov", threeGiB).
			AddRow("hd", "application/pdf", at, at, "/Docs/Viaje a Málaga.pdf", int64(20)))

	files, err := NewWithDB(db).SearchFiles("malaga", 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.Path)
	}
	want := []string{"/Fotos/Málaga", "/Big/malaga.mov", "/Docs/Viaje a Málaga.pdf", "/Fotos/Málaga/IMG_2.jpg"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", got, want)
	}
	if dir := files[0]; dir.Mime != "inode/directory" || dir.Hash != "" || dir.Created != nil || dir.Size64 != 0 {
		t.Errorf("a folder is its path and mime only, as listed: %v", dir)
	}
	if mov := files[1]; mov.Hash != "hm" || mov.Mime != "video/quicktime" || mov.Size64 != threeGiB || mov.Size != threeGiBWrapped ||
		!mov.Created.AsTime().Equal(at) || !mov.Modified.AsTime().Equal(at) {
		t.Errorf("a file is its row, as listed: %v", mov)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// Never more than the limit, 50 at most, whatever matches.
func TestSearchFilesCapsTheLimit(t *testing.T) {
	var paths []string
	for i := range 60 {
		paths = append(paths, fmt.Sprintf("/Bulk/match-%02d.txt", i))
	}
	for _, c := range []struct{ limit, want int }{{0, 20}, {7, 7}, {500, 50}} {
		t.Run(fmt.Sprint(c.limit), func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectQuery(searchPrefilter).WillReturnRows(pathRows(paths...))
			rows := sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"})
			for _, p := range paths[:c.want] {
				rows.AddRow("h", "text/plain", time.Now(), time.Now(), p, int64(1))
			}
			mock.ExpectQuery("where `path` in \\(\\?" + strings.Repeat(", \\?", c.want-1) + "\\)$").
				WithArgs(searchArgs(paths[:c.want]...)...).WillReturnRows(rows)
			files, err := NewWithDB(db).SearchFiles("match-", c.limit)
			if err != nil || len(files) != c.want || files[0].Path != "/Bulk/match-00.txt" {
				t.Fatalf("got %d entries, %v", len(files), err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Error(err)
			}
		})
	}
}

// Every row of files is searched, as every row is listed: the only
// condition is the text.
func TestSearchFilesReadsOnlyFilesByText(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("select `path` from `files` where `path` like ? collate utf8mb4_general_ci or `path` regexp ?").
		WithArgs("%x%", cDecomposedRe).WillReturnRows(pathRows())
	if files, err := NewWithDB(db).SearchFiles("x", 0); err != nil || len(files) != 0 {
		t.Fatalf("got %v, %v", files, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// CountVersionsOf asks only for the paths given, none at all for none.
func TestCountVersionsOf(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := NewWithDB(db)
	if got, err := d.CountVersionsOf(nil); err != nil || len(got) != 0 {
		t.Fatalf("no paths: %v, %v", got, err)
	}
	mock.ExpectQuery("select `path`, count\\(\\*\\) from `file_versions` where `path` in \\(\\?, \\?\\) group by `path`").
		WithArgs("/a", "/b").WillReturnRows(sqlmock.NewRows([]string{"path", "n"}).AddRow("/a", 3))
	got, err := d.CountVersionsOf([]string{"/a", "/b"})
	if err != nil || len(got) != 1 || got["/a"] != 3 {
		t.Fatalf("got %v, %v", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
