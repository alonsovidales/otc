// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"bytes"
	"database/sql"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	pb "github.com/alonsovidales/otc/proto/generated"
	"golang.org/x/text/unicode/norm"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The top bar's search (SearchFiles in messages.proto): the files and
// folders whose path contains a text, as a listing shows them.
const (
	SearchFilesDefaultLimit = 20
	SearchFilesMaxLimit     = 50
)

// SearchFilesMaxTextBytes is the longest text SearchFiles looks for, in
// bytes once trimmed: a path has at most 768 characters (`files`.`path`),
// and a character takes at most 12 bytes in any case and normal form
// (U+16126 decomposed), so no spelling of a path is longer. A longer text
// is refused before anything is done with it: a paste of a whole document
// would otherwise be normalised and sent to the database in several full
// copies.
const SearchFilesMaxTextBytes = 16 * 768

// SearchFilesLimit is how many entries SearchFiles gives for the limit a
// client asked for: SearchFilesDefaultLimit when unset, never more than
// SearchFilesMaxLimit.
func SearchFilesLimit(limit int) int {
	if limit <= 0 {
		return SearchFilesDefaultLimit
	}
	return min(limit, SearchFilesMaxLimit)
}

// How a path and the text are compared: searchFold, case- and
// accent-insensitive - "malaga", "MALAGA" and "málaga" all find "Málaga",
// and so do they when a Mac stored the name decomposed ("a" followed by a
// combining accent, NFD).
//
// The database can't do that by itself: a LIKE compares one character
// with one character, so under any collation "malaga" misses a decomposed
// "Málaga". It only picks the rows worth looking at, in one pass over the
// table, cheap enough to run on every key press:
//   - a LIKE of the text under utf8mb4_general_ci, which is case- and
//     accent-insensitive for precomposed letters (the columns are binary,
//     issue #172, so the collation is said in the query),
//   - any path holding a character that is only there in a decomposed
//     name (cDecomposedRe), and
//   - when the text has letters that decompose into something other than
//     an accent - an Arabic hamza, an Indic two-part vowel sign - a LIKE
//     of it decomposed, accents left out (searchDecomposed): a decomposed
//     name in those scripts.
//
// searchFold then decides, on those rows only, which match and how well.
// A leading-% LIKE can't use the index on `path`: a full scan.

// cDecomposedRe finds a path with a character only a decomposed name has:
// a combining accent (U+0300-U+036F), a kana voicing mark or a Hangul
// vowel or final consonant jamo. Other scripts' marks (U+0654, U+0BBE...)
// are left to searchDecomposed: they also stand alone in composed names,
// so they would make every Arabic or Tamil path a candidate.
const cDecomposedRe = "[\u0300-\u036f\u1160-\u11ff\u3099\u309a]"

// searchFold is s as SearchFiles compares it: decomposed, without its
// accents, recomposed, and case-folded rune by rune. "/" stays "/", and
// nothing else becomes one, so a path folds segment by segment.
func searchFold(s string) string {
	if isASCII(s) {
		return strings.ToLower(s)
	}
	return string(appendSearchFold(nil, s))
}

// appendSearchFold appends searchFold(s) to dst, with no string of its
// own: a broad search folds every file name. A name with nothing to
// normalise (Фото_001.jpg) isn't copied either - norm hands a normal
// string back as it is.
func appendSearchFold(dst []byte, s string) []byte {
	if isASCII(s) {
		for i := 0; i < len(s); i++ {
			c := s[i]
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			dst = append(dst, c)
		}
		return dst
	}
	for _, r := range norm.NFC.String(stripAccents(norm.NFD.String(s))) {
		dst = utf8.AppendRune(dst, unicode.ToLower(unicode.ToUpper(r)))
	}
	return dst
}

func isCombiningAccent(r rune) bool { return r >= 0x300 && r <= 0x36f }

// stripAccents is s without its combining accents (U+0300-U+036F).
func stripAccents(s string) string {
	if !strings.ContainsFunc(s, isCombiningAccent) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if !isCombiningAccent(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// searchDecomposed is text as a name a Mac stored decomposed spells it,
// its accents left out: cDecomposedRe finds any name with an accent, and
// without them a name lacking an accent typed is found too. "" when
// nothing else in it decomposes - such a name is spelt as the text, and
// the first LIKE finds it.
func searchDecomposed(text string) string {
	d := stripAccents(norm.NFD.String(text))
	if norm.NFC.IsNormalString(d) {
		return ""
	}
	return d
}

// Search tiers, best first.
const (
	tierNameStarts = iota
	tierNameContains
	tierFolderOnTheWay
)

// searchHit is an entry SearchFiles may give: a file or a folder (dir).
type searchHit struct {
	path  string
	dir   bool
	tier  int
	runes int // the path's length in characters
}

// before is whether h ranks above o: a better tier, then a shorter path,
// then by path.
func (h searchHit) before(o searchHit) bool {
	if h.tier != o.tier {
		return h.tier < o.tier
	}
	if h.runes != o.runes {
		return h.runes < o.runes
	}
	if h.path != o.path {
		return h.path < o.path
	}
	return h.dir && !o.dir
}

// searchRanking keeps the best limit entries among the paths it is shown,
// one path at a time, so a search that matches every file of a large
// library holds no more than those and the folders it has seen.
type searchRanking struct {
	q     []byte // the folded text
	limit int
	best  []searchHit // ranked, at most limit
	// dirs is every folder already looked at with its folders above it:
	// a second file in one of them adds nothing.
	dirs map[string]bool
	// Per path, reused: the folded path and where each of its "/" are,
	// in it and in the path.
	folded         []byte
	fSlash, oSlash []int
	// foldedNames: a folder's name is in many paths; folded once. Only
	// folders: a file's name is in one path, and caching every one would
	// grow with the library on a broad search.
	foldedNames map[string]string
}

func newSearchRanking(foldedText string, limit int) *searchRanking {
	return &searchRanking{q: []byte(foldedText), limit: limit, dirs: map[string]bool{}, foldedNames: map[string]string{}}
}

func (r *searchRanking) offer(h searchHit) {
	n := len(r.best)
	if n == r.limit && !h.before(r.best[n-1]) {
		return
	}
	i := sort.Search(n, func(i int) bool { return h.before(r.best[i]) })
	if n < r.limit {
		r.best = append(r.best, searchHit{})
	}
	copy(r.best[i+1:], r.best[i:len(r.best)-1])
	r.best[i] = h
}

// nameTier ranks a match by the name it is in.
func (r *searchRanking) nameTier(name []byte) int {
	switch {
	case bytes.HasPrefix(name, r.q):
		return tierNameStarts
	case bytes.Contains(name, r.q):
		return tierNameContains
	default:
		return tierFolderOnTheWay
	}
}

// add looks at one path: the file, when its path has the text, and the
// folders on its way that have it - from its own folder up to the first
// one whose path holds the whole text.
func (r *searchRanking) add(path string) {
	r.folded, r.fSlash, r.oSlash = r.folded[:0], r.fSlash[:0], r.oSlash[:0]
	start := 0
	for {
		i := strings.IndexByte(path[start:], '/')
		end := len(path)
		if i >= 0 {
			end = start + i
		}
		seg := path[start:end]
		if i < 0 || isASCII(seg) { // the file's name, or nothing to normalise
			r.folded = appendSearchFold(r.folded, seg)
		} else {
			f, ok := r.foldedNames[seg]
			if !ok {
				f = searchFold(seg)
				r.foldedNames[seg] = f
			}
			r.folded = append(r.folded, f...)
		}
		if i < 0 {
			break
		}
		r.fSlash = append(r.fSlash, len(r.folded))
		r.oSlash = append(r.oSlash, end)
		r.folded = append(r.folded, '/')
		start = end + 1
	}

	at := bytes.Index(r.folded, r.q)
	if at < 0 {
		return // the database's pick, but not a match here
	}
	matchEnd := at + len(r.q)

	nameAt := 0
	if n := len(r.fSlash); n > 0 {
		nameAt = r.fSlash[n-1] + 1
	}
	r.offer(searchHit{path: path, tier: r.nameTier(r.folded[nameAt:]), runes: utf8.RuneCountInString(path)})

	// Folder k is the path up to its k-th "/"; it has the text when the
	// match ends before that "/".
	for k := len(r.oSlash) - 1; k >= 0 && r.fSlash[k] >= matchEnd; k-- {
		dir := path[:r.oSlash[k]]
		if r.dirs[dir] {
			break
		}
		r.dirs[dir] = true
		nameStart := 0
		if k > 0 {
			nameStart = r.fSlash[k-1] + 1
		}
		r.offer(searchHit{path: dir, dir: true, tier: r.nameTier(r.folded[nameStart:r.fSlash[k]]), runes: utf8.RuneCountInString(dir)})
	}
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// SearchFiles is the entries a listing (GetFilesByPath) would show - a
// file as its row, a folder as an inode/directory entry with only its
// path, derived from the paths of the files under it - whose path
// contains text (trimmed; see searchFold), at most SearchFilesLimit(limit)
// of them. Best first: a name that starts with the text, then a name that
// contains it, then the text only in a folder on the way; shorter paths
// first within each, then by path. No text, or one longer than
// SearchFilesMaxTextBytes, no entries.
func (dao *Dao) SearchFiles(text string, limit int) ([]*pb.File, error) {
	text = strings.TrimSpace(text)
	if len(text) > SearchFilesMaxTextBytes {
		return nil, nil
	}
	text = norm.NFC.String(text)
	q := searchFold(text)
	if q == "" {
		return nil, nil
	}
	limit = SearchFilesLimit(limit)

	query := "select `path` from `files` where `path` like ? collate utf8mb4_general_ci or `path` regexp ?"
	args := []any{"%" + likeEscape(text) + "%", cDecomposedRe}
	if d := searchDecomposed(text); d != "" {
		query += " or `path` like ? collate utf8mb4_general_ci"
		args = append(args, "%"+likeEscape(d)+"%")
	}
	rows, err := dao.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ranking := newSearchRanking(q, limit)
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		ranking.add(path)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	var paths []string
	for _, h := range ranking.best {
		if !h.dir {
			paths = append(paths, h.path)
		}
	}
	rowsOf, err := dao.filesByPaths(paths)
	if err != nil {
		return nil, err
	}
	out := make([]*pb.File, 0, len(ranking.best))
	for _, h := range ranking.best {
		if h.dir {
			out = append(out, dirEntry(h.path))
		} else if f := rowsOf[h.path]; f != nil { // gone since: left out
			out = append(out, f)
		}
	}

	return out, nil
}

// filesByPaths is the rows of these paths, as GetFilesByPath reads them.
func (dao *Dao) filesByPaths(paths []string) (map[string]*pb.File, error) {
	files := map[string]*pb.File{}
	if len(paths) == 0 {
		return files, nil
	}
	args := make([]any, len(paths))
	for i, p := range paths {
		args[i] = p
	}
	rows, err := dao.db.Query("select `hash`, `mime`, `created`, `modified`, `path`, `size` from `files` where `path` in (?"+
		strings.Repeat(", ?", len(paths)-1)+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		files[f.Path] = f
	}

	return files, rows.Err()
}

// dirEntry is a folder as a listing shows it: its path, nothing else.
func dirEntry(path string) *pb.File {
	return &pb.File{Path: path, Mime: "inode/directory"}
}

// scanFile reads a `hash`, `mime`, `created`, `modified`, `path`, `size`
// row into a File.
func scanFile(rows *sql.Rows) (*pb.File, error) {
	file := new(pb.File)
	var created, modified time.Time
	var size int64
	if err := rows.Scan(&file.Hash, &file.Mime, &created, &modified, &file.Path, &size); err != nil {
		return nil, err
	}
	file.Created = timestamppb.New(created)
	file.Modified = timestamppb.New(modified)
	SetFileSize(file, size)

	return file, nil
}
