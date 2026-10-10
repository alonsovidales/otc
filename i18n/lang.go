// SPDX-License-Identifier: AGPL-3.0-or-later

package i18n

import (
	"slices"
	"strings"

	"golang.org/x/text/language"
)

// Language is one language this build carries (i18n/languages.json).
type Language struct {
	// Code is what is stored and sent on the wire ("pt").
	Code string
	// Tag is the BCP 47 tag behind plural rules and number formats
	// ("pt-PT": "pt" alone would mean Brazilian rules).
	Tag string
	// Name is the language's own name for itself, for a picker.
	Name string
}

// Languages returns the languages this build carries, English first: the
// shipping ones (a make i18n DRAFT=1 build adds the drafts and the
// pseudo-locale).
func Languages() []Language { return slices.Clone(std.langs) }

// Normalize turns a language a client sent (ReqEnvelope.lang, a stored
// choice, a ?lang= parameter) into the code of a language this build
// carries, or "" for anything else - render that in English. It accepts
// at most 8 characters of ASCII letters, digits, "-" and "_", any case,
// and keeps the base subtag: "es", "ES", "pt-PT" and "pt_BR" give "es",
// "es", "pt" and "pt". The result is safe to use as a key into the
// catalog, and only as that.
func Normalize(lang string) string { return std.normalize(lang) }

func (b *bundle) normalize(lang string) string {
	if lang == "" || len(lang) > 8 {
		return ""
	}
	for i := 0; i < len(lang); i++ {
		switch c := lang[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return ""
		}
	}
	base := strings.ToLower(lang)
	if i := strings.IndexAny(base, "-_"); i >= 0 {
		base = base[:i]
	}
	if _, ok := b.index[base]; !ok {
		return ""
	}
	return base
}

// ValidCode reports whether s may be stored as a language choice: two or
// three lowercase letters (^[a-z]{2,3}$), so that a device keeps a
// language added after it was released and renders it in English until
// an update brings its text. "" (Automatic) is not a code.
func ValidCode(s string) bool {
	if len(s) < 2 || len(s) > 3 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 'a' || s[i] > 'z' {
			return false
		}
	}
	return true
}

// Match picks the language to show someone who prefers prefs, in order:
// each an Accept-Language header ("es-ES,es;q=0.9,en;q=0.8"), a single tag
// ("pt-BR") or a POSIX locale ("es_ES.UTF-8", or a LANGUAGE list
// "es:en"). A preferred tag matches a language this build carries by base
// language, through x/text's matcher: es-MX gives es, pt-BR pt, nl-BE nl,
// and [ca-ES, es-ES] es. The first preferred tag that matches wins; with
// none, English. The result is always a code this build carries.
func Match(prefs ...string) string { return std.match(prefs) }

// maxPrefLen bounds what Match parses of one preference.
const maxPrefLen = 1024

func (b *bundle) match(prefs []string) string {
	for _, p := range prefs {
		if p = strings.TrimSpace(p); p == "" || len(p) > maxPrefLen {
			continue
		}
		tags, _, err := language.ParseAcceptLanguage(p)
		if err != nil {
			tags = posixTags(p)
		}
		for _, t := range tags {
			if code, ok := b.matchTag(t); ok {
				return code
			}
		}
	}
	return b.langs[0].Code
}

// matchTag matches one preferred tag. x/text's matcher also suggests
// related languages (Galician gets Spanish, Basque Spanish, Afrikaans
// Dutch); the apps match by base language, so the Go programs do too.
func (b *bundle) matchTag(t language.Tag) (string, bool) {
	_, i, conf := b.matcher.Match(t)
	if conf < language.High {
		return "", false
	}
	want, _ := t.Base()
	got, _ := b.tags[i].Base()
	if want != got {
		return "", false
	}
	return b.langs[i].Code, true
}

// posixTags reads POSIX locale names: "es_ES.UTF-8", "pt_BR@euro", and the
// colon-separated lists of LANGUAGE ("es:en"). C and POSIX name no
// language.
func posixTags(s string) []language.Tag {
	var tags []language.Tag
	for _, name := range strings.Split(s, ":") {
		if i := strings.IndexAny(name, ".@"); i >= 0 {
			name = name[:i]
		}
		if name == "" || name == "C" || name == "POSIX" {
			continue
		}
		if t, err := language.Parse(strings.ReplaceAll(name, "_", "-")); err == nil {
			tags = append(tags, t)
		}
	}
	return tags
}
