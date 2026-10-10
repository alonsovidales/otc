// SPDX-License-Identifier: AGPL-3.0-or-later

package scan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/alonsovidales/otc/i18n/model"
)

// The lexers write {} where a literal interpolates code ("\(n) photos",
// "${n} photos", `${n} photos`): the text tests below see only the words.
const hole = "{}"

// formatVerb matches printf-style verbs (Go, Swift's String(format:),
// Kotlin's format, Python's %): "%d", "%1$@", "%[2]s", "%.1f", "%lld", "%%".
var formatVerb = regexp.MustCompile(`%(?:\d+\$)?(?:\[\d+\])?[-+# 0']*(?:\d+|\*)?(?:\.(?:\d+|\*))?(?:ll|l|hh|h|q|z|j|t)?[a-zA-Z@%]`)

// bare removes interpolations and format verbs.
func bare(s string) string {
	s = strings.ReplaceAll(s, hole, " ")
	return formatVerb.ReplaceAllString(s, " ")
}

// words returns the runs of letters in s once interpolations and format
// verbs are gone.
func words(s string) []string {
	return strings.FieldsFunc(bare(s), func(r rune) bool { return !unicode.IsLetter(r) })
}

// hasLetters reports whether s has a run of two or more letters: the least a
// text a person reads has ("OK", "Delete", "of").
func hasLetters(s string) bool {
	for _, w := range words(s) {
		if len([]rune(w)) >= 2 {
			return true
		}
	}
	return false
}

var (
	urlLike   = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://|^mailto:|^www\.`)
	camelLike = regexp.MustCompile(`^[a-z][a-z0-9]*[A-Z]`)
	keyLike   = regexp.MustCompile(`^[a-z0-9]+(?:[._:/-][a-z0-9]+)*$`)
	constLike = regexp.MustCompile(`^[A-Z0-9]+(?:_[A-Z0-9]+)+$`)
	cssUnit   = regexp.MustCompile(`\d(?:px|rem|em|ms|vh|vw|vmin|vmax|deg|fr|pt|dp|sp)\b`)
	cssFunc   = regexp.MustCompile(`\b(?:rgba?|hsla?|var|calc|translate[XYZ3d]*|scale[XY]?|rotate|url|cubic-bezier|linear-gradient|radial-gradient|minmax|repeat|clamp)\(`)
	sqlLike   = regexp.MustCompile(`(?i)^\s*(?:select\s.+\sfrom\s|insert\s+(?:ignore\s+)?into\s|update\s+\S+\s+set\s|delete\s+from\s|create\s+(?:table|index|database|unique)\s|alter\s+table\s|drop\s+(?:table|index|database)\s|replace\s+into\s|show\s+(?:tables|columns|databases)\b|set\s+names\b|with\s+\w+\s+as\s*\()`)
	dateChars = regexp.MustCompile(`^[dMyHhmsSaEeLzZGQwWkKuUX'\s.,:/-]+$`)
	datePairs = regexp.MustCompile(`dd|MM|yy|HH|hh|mm|ss`)
	classList = regexp.MustCompile(`^(?:[a-z0-9]+(?:[-:/][a-z0-9.]+)*)(?:\s+[a-z0-9]+(?:[-:/][a-z0-9.]+)*)*$`)
	sentence  = regexp.MustCompile(`[.!?…:;,—–]`)
)

// identLike reports whether a single-token literal is a key, an identifier,
// a path, a URL or a constant rather than a word for a person: "photos",
// "otc-setup", "inode/directory", "faceRecognitionEnabled", "OTC_KEY".
// A capitalised word ("Delete", "OK") is not.
func identLike(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return true
	}
	if urlLike.MatchString(t) {
		return true
	}
	if strings.ContainsFunc(t, unicode.IsSpace) {
		return false
	}
	if keyLike.MatchString(t) || camelLike.MatchString(t) || constLike.MatchString(t) {
		return true
	}
	if strings.HasPrefix(t, "#") || strings.HasPrefix(t, ".") || strings.HasPrefix(t, "/") ||
		strings.HasPrefix(t, "-") || strings.HasPrefix(t, "@") || strings.HasPrefix(t, "$") {
		return true
	}
	if strings.ContainsAny(t, "_/\\=&{}[]<>|`") {
		return true
	}
	// a.b or name.ext, but not "Done." or "Wait…"
	if i := strings.IndexByte(t, '.'); i > 0 && i < len(t)-1 {
		return true
	}
	return false
}

// codeish reports whether a literal with spaces is code rather than text: a
// CSS value, a class list, SQL, an expression.
func codeish(s string) bool {
	b := strings.TrimSpace(bare(s))
	switch {
	case sqlLike.MatchString(b), cssUnit.MatchString(b), cssFunc.MatchString(b), isDateFormat(b):
		return true
	case strings.Contains(b, "=>"), strings.Contains(b, "&&"), strings.Contains(b, "||"),
		strings.Contains(b, "=="), strings.Contains(b, "!="), strings.Contains(b, "::"):
		return true
	case strings.Count(b, ";") > 1, strings.Contains(b, "{") && strings.Contains(b, "}"):
		return true
	}
	// a class list: lower-case tokens, at least half of them with a hyphen,
	// colon, slash or digit ("flex items-center gap-2", "btn btn-primary");
	// "could not start the sign-in" is a sentence
	if classList.MatchString(b) {
		fields := strings.Fields(b)
		marked := 0
		for _, f := range fields {
			if strings.ContainsAny(f, "-:/0123456789") {
				marked++
			}
		}
		return marked*2 >= len(fields)
	}
	return false
}

// isDateFormat: "dd-MM-yyyy HH:mm", "yyyy.MM.dd" - two field pairs and only
// format letters and separators.
func isDateFormat(s string) bool {
	return dateChars.MatchString(s) && len(datePairs.FindAllString(s, -1)) >= 2
}

// proseLevel says how much a literal must read like a sentence.
type proseLevel int

const (
	// loose: two words are enough, lower case included ("could not be
	// read", "not found"): Go errors in user-facing packages.
	loose proseLevel = iota
	// strict: two words starting with a capital or carrying sentence
	// punctuation, or three words: literals in app code, where two
	// lower-case words are more often a key or a class list.
	strict
)

// isProse reports whether s reads like text for a person.
func isProse(s string, level proseLevel) bool {
	if !hasLetters(s) || codeish(s) {
		return false
	}
	var fields []string
	for _, f := range strings.Fields(bare(s)) {
		if strings.ContainsFunc(f, unicode.IsLetter) {
			fields = append(fields, f)
		}
	}
	if len(fields) < 2 {
		return false
	}
	long := 0
	for _, f := range fields {
		if identLike(f) && strings.ContainsAny(f, "_/\\=") {
			return false // a path or an expression among words
		}
		n := 0
		for _, r := range f {
			if unicode.IsLetter(r) {
				n++
			}
		}
		if n >= 2 {
			long++
		}
	}
	if long < 1 {
		return false
	}
	if level == loose {
		return true
	}
	first := []rune(strings.TrimSpace(bare(s)))
	for len(first) > 0 && !unicode.IsLetter(first[0]) && !unicode.IsDigit(first[0]) {
		first = first[1:]
	}
	upper := len(first) > 0 && unicode.IsUpper(first[0])
	return upper || sentence.MatchString(bare(s)) || len(fields) >= 3
}

// looseText is the test for a literal in a place that shows text (a Text
// view, a JSX attribute, a reply's error): any word a person reads, but not
// a key or an identifier.
func (s *scanner) looseText(text string) bool {
	if !hasLetters(text) || s.never.only(text) {
		return false
	}
	if !strings.ContainsFunc(strings.TrimSpace(bare(text)), unicode.IsSpace) && identLike(text) {
		return false
	}
	return !codeish(text)
}

// textRun is the test for the text of an element (an HTML or JSX text node
// with its inline markup): any word, unless every word is code
// ("DEVICE_UUID={} BRIDGE_SECRET={}", "http://otc.local:8080").
func (s *scanner) textRun(text string) bool {
	if !hasLetters(text) || s.never.only(text) {
		return false
	}
	for _, f := range strings.Fields(bare(text)) {
		if strings.ContainsFunc(f, unicode.IsLetter) && !codeToken(f) {
			return true
		}
	}
	return false
}

// codeToken: an identifier, a path, an assignment or a URL among words.
func codeToken(f string) bool {
	f = strings.Trim(f, "\"'“”‘’()[],;:")
	if f == "" || !identLike(f) {
		return false
	}
	return strings.ContainsAny(f, "_=/\\{}<>|$@") || constLike.MatchString(f) || camelLike.MatchString(f) ||
		urlLike.MatchString(f) || strings.Contains(strings.Trim(f, "."), ".")
}

// prose is the test for a literal anywhere else.
func (s *scanner) prose(text string, level proseLevel) bool {
	return isProse(text, level) && !s.never.only(text)
}

// neverTerms are the words never translated (i18n/glossary/never.json): a
// literal made only of them ("Off The Cloud", "Tailscale") is not counted.
type neverTerms struct{ re *regexp.Regexp }

func loadNever(root string) *neverTerms {
	terms := model.DefaultNever
	if data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(model.NeverFile))); err == nil {
		var f struct {
			Never []string `json:"never"`
		}
		if json.Unmarshal(data, &f) == nil && len(f.Never) > 0 {
			terms = f.Never
		}
	}
	return newNeverTerms(terms)
}

func newNeverTerms(terms []string) *neverTerms {
	sorted := append([]string(nil), terms...)
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
	var alts []string
	for _, t := range sorted {
		if t = strings.TrimSpace(t); t != "" {
			alts = append(alts, regexp.QuoteMeta(t))
		}
	}
	if len(alts) == 0 {
		return &neverTerms{}
	}
	return &neverTerms{re: regexp.MustCompile(`(?:^|[^\p{L}\p{N}])(?:` + strings.Join(alts, "|") + `)(?:[^\p{L}\p{N}]|$)`)}
}

// only reports whether s has no words left once the never-translated terms
// are taken out.
func (n *neverTerms) only(s string) bool {
	if n == nil || n.re == nil {
		return false
	}
	// twice: adjacent terms share the separator the first pass consumed
	rest := n.re.ReplaceAllString(n.re.ReplaceAllString(s, " "), " ")
	return !hasLetters(rest)
}
