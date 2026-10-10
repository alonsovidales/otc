// SPDX-License-Identifier: AGPL-3.0-or-later

package i18n

import (
	"bytes"
	"embed"
	"encoding/json"
	"io/fs"
	"sort"
	"strings"
	"sync"

	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// catalogFS is what make i18n writes for the Go programs:
// catalog/<code>/<prefix>.json for every language of languages_gen.go and
// every prefix routed to the device, the bridge or otc-sync. Devices build
// from `git archive`, so the files are committed (English common.json
// always exists: the pattern always matches).
//
//go:embed catalog/*/*.json
var catalogFS embed.FS

// std is the embedded catalog, which every exported function reads.
var std = newBundle(languages, catalogFS)

// draftKey marks a catalog file make i18n DRAFT=1 wrote; it is not a key.
const draftKey = "@draft"

// A bundle is a set of languages and the catalog files holding their text:
// the embedded catalog, or a test's.
type bundle struct {
	langs    []Language
	index    map[string]int // code -> position in langs
	tags     []language.Tag // each language's canonical tag
	matcher  language.Matcher
	printers []*message.Printer
	fsys     fs.FS
	tables   []*table
}

// table is one language's messages, read from its files on first use.
type table struct {
	once sync.Once
	msgs map[string]*entry
}

// entry is one key's text in one language, split into tokens.
type entry struct {
	plural bool
	one    []token // the "one" form of a plural
	other  []token // the "other" form, or the whole text
	rich   bool    // the text has tags
	// args are the declared arguments, in order. Only English declares
	// them; a translation is checked against the English entry.
	args []argDecl
}

type argDecl struct{ name, typ string }

// The argument types (i18n/README.md).
const (
	typText     = "text"
	typUser     = "user"
	typCount    = "count"
	typInt      = "int"
	typBytes    = "bytes"
	typDatetime = "datetime"
	typMsg      = "msg"
)

func knownType(t string) bool {
	switch t {
	case typText, typUser, typCount, typInt, typBytes, typDatetime, typMsg:
		return true
	}
	return false
}

// decl returns the declaration of the named argument.
func (e *entry) decl(name string) (argDecl, bool) {
	for _, a := range e.args {
		if a.name == name {
			return a, true
		}
	}
	return argDecl{}, false
}

func newBundle(langs []Language, fsys fs.FS) *bundle {
	b := &bundle{langs: langs, index: map[string]int{}, fsys: fsys}
	for i, l := range langs {
		b.index[l.Code] = i
		t, err := language.Parse(l.Tag)
		if err != nil {
			t = language.English
		}
		b.tags = append(b.tags, t)
		b.printers = append(b.printers, message.NewPrinter(t))
		b.tables = append(b.tables, &table{})
	}
	b.matcher = language.NewMatcher(b.tags)
	return b
}

// lang returns the position of a language code as a client sent it,
// English (0) for anything else.
func (b *bundle) lang(code string) int {
	if i, ok := b.index[b.normalize(code)]; ok {
		return i
	}
	return 0
}

// table returns language i's messages, reading them the first time.
func (b *bundle) table(i int) *table {
	t := b.tables[i]
	t.once.Do(func() { t.msgs = b.load(i) })
	return t
}

// rawEntry is one key in a catalog file:
//
//	"dev.photos_deleted": {"text": {"one": "...", "other": "..."}, "args": [["name", "user"], ["count", "count"]]}
//
// "args" only in English.
type rawEntry struct {
	Text json.RawMessage `json:"text"`
	Args [][]string      `json:"args"`
}

// load reads language i's catalog files. The generator checked them; what
// doesn't parse anyway is left out (and logged), so that key falls back to
// English, or to the key itself.
func (b *bundle) load(i int) map[string]*entry {
	msgs := map[string]*entry{}
	var en map[string]*entry
	if i != 0 {
		en = b.table(0).msgs
	}
	files, _ := fs.Glob(b.fsys, "catalog/"+b.langs[i].Code+"/*.json")
	sort.Strings(files)
	for _, f := range files {
		data, err := fs.ReadFile(b.fsys, f)
		if err != nil {
			logOnce("unreadable catalog file", f)
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			logOnce("unreadable catalog file", f)
			continue
		}
		keys := make([]string, 0, len(raw))
		for k := range raw {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if key == draftKey || msgs[key] != nil {
				continue
			}
			var r rawEntry
			if err := json.Unmarshal(raw[key], &r); err != nil {
				logOnce("unreadable catalog entry", key)
				continue
			}
			var e *entry
			if i == 0 {
				e = parseEntry(r, nil)
			} else if src := en[key]; src != nil {
				e = parseEntry(r, src)
			}
			if e == nil {
				logOnce("unusable catalog entry in "+b.langs[i].Code, key)
				continue
			}
			msgs[key] = e
		}
	}
	return msgs
}

// parseEntry builds an entry from its catalog form. src is the English
// entry a translation must agree with (nil for English itself, which
// declares the arguments). It returns nil for anything malformed.
func parseEntry(r rawEntry, src *entry) *entry {
	e := &entry{}
	if src == nil {
		seen := map[string]bool{}
		for _, a := range r.Args {
			if len(a) != 2 || !validArgName(a[0]) || !knownType(a[1]) || seen[a[0]] {
				return nil
			}
			seen[a[0]] = true
			e.args = append(e.args, argDecl{a[0], a[1]})
		}
	} else {
		e.args = src.args
	}
	text := bytes.TrimSpace(r.Text)
	var forms []string
	switch {
	case len(text) > 0 && text[0] == '"':
		var s string
		if json.Unmarshal(text, &s) != nil {
			return nil
		}
		forms = []string{s}
	case len(text) > 0 && text[0] == '{':
		var p struct {
			One   *string `json:"one"`
			Other *string `json:"other"`
		}
		if json.Unmarshal(text, &p) != nil || p.One == nil || p.Other == nil {
			return nil
		}
		e.plural = true
		forms = []string{*p.One, *p.Other}
	default:
		return nil
	}
	var toks [][]token
	for _, f := range forms {
		t, ok := tokenize(f)
		if !ok {
			return nil
		}
		for _, tok := range t {
			switch tok.kind {
			case tokArg:
				if _, ok := e.decl(tok.val); !ok {
					return nil
				}
			case tokOpen:
				e.rich = true
			}
		}
		toks = append(toks, t)
	}
	if e.plural {
		e.one, e.other = toks[0], toks[1]
	} else {
		e.other = toks[0]
	}
	if src != nil && (e.plural != src.plural || e.rich != src.rich) {
		return nil
	}
	return e
}

// lookup finds key in language i: the language's entry, or English's (and
// then i is 0: an English text gets English plural rules and numbers).
// en is the English entry, nil for a key the catalog doesn't have.
func (b *bundle) lookup(i int, key string) (int, *entry, *entry) {
	en := b.table(0).msgs[key]
	if en == nil {
		return 0, nil, nil
	}
	if i != 0 {
		if e := b.table(i).msgs[key]; e != nil {
			return i, e, en
		}
	}
	return 0, en, en
}

// render is T: the message as plain text, tags left out.
func (b *bundle) render(lang, key string, args map[string]any) string {
	parts, ok := b.expand(b.lang(lang), key, args)
	if !ok {
		return key
	}
	if len(parts) == 1 {
		return parts[0].Text
	}
	var s strings.Builder
	for _, p := range parts {
		s.WriteString(p.Text)
	}
	return s.String()
}

// parts is Rich.Parts.
func (b *bundle) parts(lang, key string, args map[string]any) []Part {
	parts, ok := b.expand(b.lang(lang), key, args)
	if !ok {
		return []Part{{Text: key}}
	}
	return parts
}

// expand renders key in language i into parts: the text between tags, and
// each tag's content. ok is false when the catalog has no such key.
//
// This is the one place arguments meet a template: the tokens were split
// when the catalog was read, and each argument is written as plain text
// once, never scanned again. An argument that is missing or of the wrong
// type comes out empty (logged by key): never a literal "{name}".
func (b *bundle) expand(i int, key string, args map[string]any) ([]Part, bool) {
	i, e, en := b.lookup(i, key)
	if e == nil {
		logOnce("unknown key", key)
		return nil, false
	}
	toks := e.other
	if e.plural {
		if n, ok := b.count(en, args); ok && b.isOne(i, n) {
			toks = e.one
		}
	}
	parts := []Part{{}}
	var s strings.Builder
	cut := func(tag string) {
		parts[len(parts)-1].Text = s.String()
		s.Reset()
		parts = append(parts, Part{Tag: tag})
	}
	bad := false
	for _, t := range toks {
		switch t.kind {
		case tokLit:
			s.WriteString(t.val)
		case tokArg:
			d, _ := en.decl(t.val)
			v, ok := b.arg(i, d, args[t.val])
			if !ok {
				bad = true
			}
			s.WriteString(v)
		case tokOpen:
			cut(t.val)
		case tokClose:
			cut("")
		}
	}
	parts[len(parts)-1].Text = s.String()
	if bad {
		logOnce("arguments that don't fit", key)
	}
	// Drop the empty plain spans cutting left around tags.
	out := parts[:0]
	for _, p := range parts {
		if p.Tag != "" || p.Text != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		out = append(out, Part{})
	}
	return out, true
}

// count returns the value of the argument that picks the plural form.
func (b *bundle) count(en *entry, args map[string]any) (int64, bool) {
	for _, a := range en.args {
		if a.typ == typCount {
			return toInt(args[a.name])
		}
	}
	return 0, false
}

// arg formats one argument for language i, as its declared type says.
func (b *bundle) arg(i int, d argDecl, v any) (string, bool) {
	switch d.typ {
	case typText:
		s, ok := v.(string)
		return s, ok
	case typUser:
		s, ok := v.(string)
		if !ok {
			return "", false
		}
		return isolate(s), true
	case typMsg:
		s, ok := v.(string)
		if !ok {
			return "", false
		}
		return b.sub(i, s)
	}
	n, ok := toInt(v)
	if !ok {
		return "", false
	}
	switch d.typ {
	case typCount, typInt:
		return b.formatInt(i, n), true
	case typBytes:
		return b.formatBytes(i, n), true
	case typDatetime:
		return b.formatTime(i, unixTime(n)), true
	}
	return "", false
}

// sub renders a msg argument: another key, without arguments or tags, in
// the same language.
func (b *bundle) sub(i int, key string) (string, bool) {
	_, e, en := b.lookup(i, key)
	if e == nil || len(en.args) > 0 || e.rich || e.plural {
		return "", false
	}
	var s strings.Builder
	for _, t := range e.other {
		s.WriteString(t.val)
	}
	return s.String(), true
}
