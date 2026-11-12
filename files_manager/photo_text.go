// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"encoding/json"
	"errors"
	"image"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	textreader "github.com/alonsovidales/otc/text_reader"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Text in photos (release 180): the text in each photo - signs, documents,
// screenshots, number plates - read by a text model on the device
// (text_reader: PP-OCRv6 small, vision_models.go) while the user has it on
// (settings.text_in_photos_enabled, on by default): by the slow lane after
// Smart search, from the photo itself, and in the background for what the
// device held before (BackfillVision, from each photo's original). Kept
// sealed with the user's key (photo_text), as Smart search's vectors are:
// the text of a document, an ID or an address is as private as the photo.
//
// A search's words find the photos whose text holds them (textMatches),
// case and accents aside, ahead of Smart search's: the whole words, as
// typed, first; a part of a word only for a word of cTextMinPart letters
// or more ("04-R" finds GTS-04-R, "dog" doesn't find "hotdog"). The texts
// are decrypted into memory at the first search and dropped after
// cTextIndexIdle without one. Info shows a photo's text (PhotoText).
//
// The switch works as Smart search's: every write holds mu for reading and
// checks the switch under it (textWrite); turning it off holds it for
// writing and deletes every text in the same transaction, so none lands
// after.

const (
	// cTextIndexIdle: the decrypted texts are dropped after this long
	// without a search.
	cTextIndexIdle = 15 * time.Minute
	// cTextMinPart: a search's word shorter than this (letters and digits)
	// is matched only as a whole word of a photo's text; a longer one also
	// as part of one.
	cTextMinPart = 4
	// cMaxPhotoText is the most of a photo's text kept, in bytes - a page
	// of a book is about 3 KB.
	cMaxPhotoText = 64 << 10
	// cMaxTextLines is the most lines of a photo kept.
	cMaxTextLines = 2000
)

// A text match's rank in a text search's list: over any cosine (Smart
// search's are 1 at most), so they come first - the words as typed, whole,
// then as part of a word; then every word somewhere, whole, then partly.
const (
	cTextRankPhrase     = 2.6
	cTextRankPhrasePart = 2.4
	cTextRankWords      = 2.2
	cTextRankWordsPart  = 2.0
	// cTextRank: a list's row ranked at least this is a text match.
	cTextRank = cTextRankWordsPart
)

// textState is Text in photos' switch and the decrypted texts.
type textState struct {
	mu     sync.RWMutex
	loadMu sync.Mutex
	loaded atomic.Bool
	on     atomic.Bool
	turn   atomic.Uint64
	index  textIndex
}

// loadTexts reads the switch from the database once. A database before
// the release that added it reads as off; an error reading it is off for
// now and read again next time.
func (mg *Manager) loadTexts() {
	if mg == nil || mg.dao == nil {
		return
	}
	s := &mg.texts
	if s.loaded.Load() {
		return
	}
	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	if s.loaded.Load() {
		return
	}
	on, err := mg.dao.TextInPhotosEnabled()
	if err != nil && !errors.Is(err, dao.ErrSchemaBehind) {
		log.Error("text in photos: could not read whether it is on, off for now:", err)
		return
	}
	s.mu.Lock()
	if !s.loaded.Load() {
		s.on.Store(on)
		s.loaded.Store(true)
	}
	s.mu.Unlock()
}

// TextInPhotosEnabled is whether Text in photos is on for this library.
func (mg *Manager) TextInPhotosEnabled() bool {
	if mg == nil {
		return false
	}
	mg.loadTexts()
	return mg.texts.on.Load()
}

// SetTextInPhotosEnabled turns Text in photos on or off for this library.
// Off deletes every text before it returns and drops the decrypted ones;
// on reads what the device holds in the background with ses's key, once
// the models are there (BackfillVision, which waits for their download).
func (mg *Manager) SetTextInPhotosEnabled(ses *session.Session, on bool) error {
	mg.loadTexts()
	s := &mg.texts
	s.mu.Lock()
	if err := mg.dao.SetTextInPhotosEnabled(on); err != nil {
		s.mu.Unlock()
		return err
	}
	s.on.Store(on)
	s.loaded.Store(true)
	s.turn.Add(1)
	s.mu.Unlock()
	s.index.drop()
	mg.wantVision()
	if on {
		log.Info("text in photos: on, reading the photos")
		visionBackfill(mg, ses)
	} else {
		log.Info("text in photos: off, every text deleted")
	}
	return nil
}

// textWrite runs write while Text in photos is on and still in turn, under
// the switch's read lock, and is whether it ran.
func (mg *Manager) textWrite(turn uint64, write func() error) (bool, error) {
	mg.loadTexts()
	s := &mg.texts
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.on.Load() || s.turn.Load() != turn {
		return false, nil
	}
	return true, write()
}

// storedLine is a line of a photo's text as photo_text keeps it, sealed:
// its text and its corners in the photo's pixels (top-left, top-right,
// bottom-right, bottom-left), for showing it on the photo one day.
type storedLine struct {
	T string   `json:"t"`
	Q [8]int32 `json:"q"`
}

// encodeLines is lines as photo_text keeps them (before sealing), cut at
// cMaxTextLines lines and cMaxPhotoText bytes of text.
func encodeLines(lines []textreader.Line) []byte {
	out := make([]storedLine, 0, min(len(lines), cMaxTextLines))
	size := 0
	for _, l := range lines {
		t := strings.TrimSpace(l.Text)
		if t == "" {
			continue
		}
		if len(out) == cMaxTextLines || size+len(t) > cMaxPhotoText {
			break
		}
		var q [8]int32
		for i, v := range l.Quad {
			if !math.IsNaN(float64(v)) {
				q[i] = int32(min(max(v, 0), math.MaxInt32))
			}
		}
		out = append(out, storedLine{T: t, Q: q})
		size += len(t)
	}
	b, _ := json.Marshal(out)
	return b
}

// decodeLines is the text of a photo_text row: its lines.
func decodeLines(b []byte) ([]storedLine, error) {
	var lines []storedLine
	err := json.Unmarshal(b, &lines)
	return lines, err
}

// joinLines is the lines of a photo's text, a line each.
func joinLines(lines []storedLine) string {
	var sb strings.Builder
	for i, l := range lines {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(l.T)
	}
	return sb.String()
}

// storeText reads the text of a photo the slow lane is analysing, or the
// background pass, from img - the photo itself, decoded upright - while
// Text in photos is on and its model is here. Read once per content: a
// second pass of the same content leaves it. A failure leaves no row: the
// next start's background pass reads it.
func (mg *Manager) storeText(ses *session.Session, file *pb.File, img image.Image) {
	if ses == nil || img == nil || !strings.HasPrefix(file.Mime, "image") || !mg.TextInPhotosEnabled() || mg.visionModel() == nil {
		return
	}
	if _, read, err := mg.dao.PhotoText(file.Hash, textreader.Model); err != nil || read {
		if err != nil && !errors.Is(err, dao.ErrSchemaBehind) {
			log.Error("text in photos: could not check whether", file.Hash, "was read:", err)
		}
		return
	}
	turn := mg.texts.turn.Load()
	started := time.Now()
	lines, err := mg.readText(img)
	if err != nil {
		log.Debug("text in photos: could not read", file.Hash, ":", err)
		return
	}
	if _, err := mg.keepText(ses, file.Hash, lines, turn); err != nil && !errors.Is(err, dao.ErrSchemaBehind) {
		log.Error("text in photos: could not keep the text of", file.Hash, ":", err)
		return
	}
	log.Debug("text in photos:", len(lines), "line(s) in", file.Hash, "in", time.Since(started).Round(time.Millisecond))
}

// keepText keeps hash's lines (none: read, without text), in turn. Whether
// they were kept.
func (mg *Manager) keepText(ses *session.Session, hash string, lines []textreader.Line, turn uint64) (bool, error) {
	var sealed []byte
	var stored []storedLine
	if len(lines) > 0 {
		plain := encodeLines(lines)
		if stored, _ = decodeLines(plain); len(stored) > 0 {
			sealed = ses.Encrypt(plain)
		}
	}
	return mg.textWrite(turn, func() error {
		if err := mg.dao.SetPhotoText(hash, textreader.Model, sealed); err != nil {
			return err
		}
		if len(stored) > 0 {
			mg.texts.index.add(hash, joinLines(stored))
		}
		return nil
	})
}

// PhotoText is the text read in the photo with this content hash, a line
// of it a line (FileExifInfo.text): "" when none was found, it isn't read
// yet, it can't be decrypted, or Text in photos is off.
func (mg *Manager) PhotoText(ses *session.Session, hash string) string {
	if mg == nil || ses == nil || hash == "" || !mg.TextInPhotosEnabled() {
		return ""
	}
	sealed, read, err := mg.dao.PhotoText(hash, textreader.Model)
	if err != nil || !read || sealed == nil {
		if err != nil && !errors.Is(err, dao.ErrSchemaBehind) {
			log.Error("text in photos: could not read the text of", hash, ":", err)
		}
		return ""
	}
	plain, err := ses.Decrypt(sealed)
	if err != nil {
		log.Error("text in photos: could not decrypt the text of", hash, ":", err)
		return ""
	}
	lines, err := decodeLines(plain)
	if err != nil {
		return ""
	}
	return joinLines(lines)
}

// forgetText forgets the text of content nothing uses any more, or kept out
// of Images.
func (mg *Manager) forgetText(hashes ...string) {
	if len(hashes) == 0 {
		return
	}
	if err := mg.dao.DelPhotoTexts(hashes...); err != nil {
		log.Error("could not forget the text of", len(hashes), "content(s):", err)
	}
	for _, h := range hashes {
		mg.texts.index.remove(h)
	}
}

// ---- the search ------------------------------------------------------------

// textIndex is the photos' texts, decrypted, folded for matching: loaded
// at the first search after a start or after cTextIndexIdle without one.
type textIndex struct {
	mu      sync.Mutex
	loaded  bool
	texts   map[string]foldedText
	lastUse time.Time
}

// foldedText is a photo's text as searched: folded (foldText), and that
// with only its letters and digits (for "gts04r" to find GTS-04-R).
type foldedText struct{ text, compact string }

func newFolded(text string) foldedText {
	f := foldText(text)
	return foldedText{text: f, compact: compactText(f)}
}

func (x *textIndex) drop() {
	x.mu.Lock()
	x.loaded, x.texts = false, nil
	x.mu.Unlock()
}

// add puts a photo's text in the index, when it is loaded (otherwise the
// load reads it).
func (x *textIndex) add(hash, text string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.loaded {
		x.texts[hash] = newFolded(text)
	}
}

func (x *textIndex) remove(hash string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.loaded {
		delete(x.texts, hash)
	}
}

// withTexts runs fn on the index, loading it first with ses's key.
func (mg *Manager) withTexts(ses *session.Session, fn func(texts map[string]foldedText)) error {
	x := &mg.texts.index
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.loaded && time.Since(x.lastUse) > cTextIndexIdle {
		x.loaded, x.texts = false, nil
	}
	if !x.loaded {
		if ses == nil {
			return errors.New("no key to read the texts with") // i18n-ignore: internal, logged
		}
		started := time.Now()
		texts := map[string]foldedText{}
		bad := 0
		err := mg.dao.EachPhotoText(textreader.Model, func(hash string, sealed []byte) error {
			plain, err := ses.Decrypt(sealed)
			if err != nil {
				bad++
				return nil
			}
			lines, err := decodeLines(plain)
			if err != nil {
				bad++
				return nil
			}
			texts[hash] = newFolded(joinLines(lines))
			return nil
		})
		if err != nil && !errors.Is(err, dao.ErrSchemaBehind) {
			return err
		}
		x.texts, x.loaded = texts, true
		log.Info("text in photos: read", len(texts), "text(s) in", time.Since(started).Round(time.Millisecond), "-", bad, "unreadable")
	}
	x.lastUse = time.Now()
	fn(x.texts)
	return nil
}

// textMatches is the photos whose text matches a search's text, best first
// (ties by hash), each with its rank (cTextRank or more): nil while Text in
// photos is off or nothing matches.
func (mg *Manager) textMatches(ses *session.Session, text string) []dao.ListedHash {
	if !mg.TextInPhotosEnabled() {
		return nil
	}
	q := newFolded(text)
	if q.text == "" {
		return nil
	}
	var hits []dao.ListedHash
	err := mg.withTexts(ses, func(texts map[string]foldedText) {
		for hash, t := range texts {
			if r, ok := textRank(t, q); ok {
				hits = append(hits, dao.ListedHash{Hash: hash, Rank: [2]float64{r}})
			}
		}
	})
	if err != nil {
		log.Error("text in photos: could not read the texts to search them:", err)
		return nil
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Rank[0] != hits[j].Rank[0] {
			return hits[i].Rank[0] > hits[j].Rank[0]
		}
		return hits[i].Hash < hits[j].Hash
	})
	return hits
}

// foldText is s as it is matched: lower case, accents taken off (é is e,
// ñ is n, ß is ss), every run of spaces a single space, trimmed.
func foldText(s string) string {
	s, _, _ = transform.String(transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC), s)
	s = foldExtra.Replace(strings.ToLower(s))
	return strings.Join(strings.Fields(s), " ")
}

// foldExtra is what NFD doesn't take apart.
var foldExtra = strings.NewReplacer("ß", "ss", "æ", "ae", "œ", "oe", "ø", "o", "ł", "l", "đ", "d", "ð", "d", "þ", "th", "ı", "i")

// compactText is a folded text's letters and digits alone.
func compactText(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// wordRune is whether r is part of a word.
func wordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// unspaced is whether r is of a script written without spaces between
// words (Chinese, Japanese, Thai...), where any part may be a word.
func unspaced(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Thai, unicode.Lao, unicode.Khmer, unicode.Myanmar)
}

// partOK is whether a search's word may match part of a word.
func partOK(w string) bool {
	n := 0
	for _, r := range w {
		if unspaced(r) {
			return true
		}
		if wordRune(r) {
			n++
		}
	}
	return n >= cTextMinPart
}

// find says whether w is in text as a whole word (whole) or only as part
// of one (part).
func find(text, w string) (whole, part bool) {
	for from := 0; from <= len(text); {
		i := strings.Index(text[from:], w)
		if i < 0 {
			return false, part
		}
		i += from
		end := i + len(w)
		before, _ := utf8.DecodeLastRuneInString(text[:i])
		after, _ := utf8.DecodeRuneInString(text[end:])
		first, _ := utf8.DecodeRuneInString(w)
		last, _ := utf8.DecodeLastRuneInString(w)
		if (i == 0 || !wordRune(before) || !wordRune(first)) && (end == len(text) || !wordRune(after) || !wordRune(last)) {
			return true, true
		}
		part = true
		_, size := utf8.DecodeRuneInString(text[i:])
		from = i + max(size, 1)
	}
	return false, part
}

// textRank is how a photo's text t matches a search's q (both folded), and
// whether it does at all.
func textRank(t, q foldedText) (float64, bool) {
	if whole, part := find(t.text, q.text); whole {
		return cTextRankPhrase, true
	} else if part && partOK(q.text) {
		return cTextRankPhrasePart, true
	}
	words := strings.Fields(q.text)
	if len(words) > 1 {
		all, allWhole := true, true
		for _, w := range words {
			whole, part := find(t.text, w)
			if !whole {
				allWhole = false
				if !part || !partOK(w) {
					all = false
					break
				}
			}
		}
		if all && allWhole {
			return cTextRankWords, true
		}
		if all {
			return cTextRankWordsPart, true
		}
	}
	// Punctuation and spaces aside: "gts04r" or "gts 04 r" for GTS-04-R.
	if utf8.RuneCountInString(q.compact) >= cTextMinPart && strings.Contains(t.compact, q.compact) {
		return cTextRankWordsPart, true
	}
	return 0, false
}

// withTextMatches is list (a Smart search's ranked hashes, best first, or
// nil) with the text matches hits ahead of it, each photo once.
func withTextMatches(hits, list []dao.ListedHash) []dao.ListedHash {
	if len(hits) == 0 {
		return list
	}
	seen := make(map[string]bool, len(hits))
	out := make([]dao.ListedHash, 0, len(hits)+len(list))
	for _, h := range hits {
		seen[h.Hash] = true
		out = append(out, h)
	}
	for _, l := range list {
		if !seen[l.Hash] {
			out = append(out, l)
		}
	}
	return out
}
