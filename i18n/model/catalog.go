// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Catalog is everything under i18n/ that the generator reads. Load builds
// it, Check validates it, and emitters read it through the methods in this
// file once Check has found no errors.
type Catalog struct {
	root      string
	languages []Language
	routes    map[string][]string
	files     []*SourceFile

	entries map[string]*Entry                  // English, by key
	keys    []string                           // sorted
	trans   map[string]map[string]*Translation // language -> key -> translation

	glossary  map[string]*Glossary
	never     []string
	neverFile bool
	stored    map[string][]Arg // the record in StoredFile
	storedSet bool             // StoredFile exists

	loadDiags []Diagnostic
}

// Load reads the catalog under root (the repository root). It fails only
// when languages.json can't be read; every other problem becomes a
// diagnostic that Check returns.
func Load(root string) (*Catalog, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(LanguagesFile)))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", LanguagesFile, err)
	}
	c := &Catalog{
		root:     root,
		entries:  map[string]*Entry{},
		trans:    map[string]map[string]*Translation{},
		glossary: map[string]*Glossary{},
		stored:   map[string][]Arg{},
	}
	var ds []Diagnostic
	c.languages, c.routes, ds = loadLanguages(data, LanguagesFile)
	c.loadDiags = append(c.loadDiags, ds...)
	c.loadGlossaries()
	c.loadStrings()
	c.loadStored()
	return c, nil
}

func (c *Catalog) diag(file string, line int, key, lang, prefix, format string, a ...any) {
	c.loadDiags = append(c.loadDiags, Diagnostic{File: file, Line: line, Key: key, Lang: lang, Prefix: prefix, Msg: fmt.Sprintf(format, a...)})
}

// readDir lists a directory under the root, without hidden files (Finder's
// .DS_Store). A missing directory is empty.
func (c *Catalog) readDir(rel string) ([]fs.DirEntry, error) {
	des, err := os.ReadDir(filepath.Join(c.root, filepath.FromSlash(rel)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	var out []fs.DirEntry
	for _, de := range des {
		if !strings.HasPrefix(de.Name(), ".") {
			out = append(out, de)
		}
	}
	return out, err
}

func (c *Catalog) readFile(rel string) ([]byte, error) {
	return os.ReadFile(filepath.Join(c.root, filepath.FromSlash(rel)))
}

func (c *Catalog) loadGlossaries() {
	des, err := c.readDir(GlossaryDir)
	if err != nil {
		c.diag(GlossaryDir, 0, "", "", "", "%v", err)
	}
	for _, de := range des {
		rel := path.Join(GlossaryDir, de.Name())
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".json") || rel == NeverFile {
			continue
		}
		data, err := c.readFile(rel)
		if err != nil {
			c.diag(rel, 0, "", "", "", "%v", err)
			continue
		}
		code := strings.TrimSuffix(de.Name(), ".json")
		if l, ok := c.Language(code); !ok || l.IsSource() {
			c.diag(rel, 0, "", "", "", "glossary for %q, which is not a translated language in %s", code, LanguagesFile)
			continue
		}
		g, ds := parseGlossary(data, rel, code)
		c.loadDiags = append(c.loadDiags, ds...)
		if g != nil {
			c.glossary[code] = g
		}
	}
	data, err := c.readFile(NeverFile)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		c.never = DefaultNever
	case err != nil:
		c.diag(NeverFile, 0, "", "", "", "%v", err)
	default:
		never, ds := parseNever(data, NeverFile)
		c.loadDiags = append(c.loadDiags, ds...)
		c.never, c.neverFile = never, true
	}
}

func (c *Catalog) loadStrings() {
	langDirs, err := c.readDir(StringsDir)
	if err != nil {
		c.diag(StringsDir, 0, "", "", "", "%v", err)
	}
	// English first: translation files are checked against its prefixes.
	slices.SortFunc(langDirs, func(a, b fs.DirEntry) int {
		if (a.Name() == SourceCode) != (b.Name() == SourceCode) {
			if a.Name() == SourceCode {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name(), b.Name())
	})
	enPrefixes := map[string]bool{}
	for _, ld := range langDirs {
		code := ld.Name()
		dir := path.Join(StringsDir, code)
		if !ld.IsDir() {
			c.diag(dir, 0, "", "", "", "only language directories belong in %s", StringsDir)
			continue
		}
		if _, ok := c.Language(code); !ok {
			c.diag(dir, 0, "", code, "", "%q is not a language in %s", code, LanguagesFile)
			continue
		}
		des, err := c.readDir(dir)
		if err != nil {
			c.diag(dir, 0, "", code, "", "%v", err)
		}
		for _, de := range des {
			rel := path.Join(dir, de.Name())
			prefix := strings.TrimSuffix(de.Name(), ".json")
			switch {
			case de.IsDir() || !strings.HasSuffix(de.Name(), ".json"):
				c.diag(rel, 0, "", code, "", "only <prefix>.json files belong in %s", dir)
				continue
			case !prefixPattern.MatchString(prefix):
				c.diag(rel, 0, "", code, "", "%q is not a prefix: a root (\"common\") or a root and one segment (\"app.photos\")", prefix)
				continue
			case code != SourceCode && !enPrefixes[prefix]:
				c.diag(rel, 0, "", code, prefix, "there is no English %s/%s/%s.json for these translations", StringsDir, SourceCode, prefix)
				continue
			}
			data, err := c.readFile(rel)
			if err != nil {
				c.diag(rel, 0, "", code, prefix, "%v", err)
				continue
			}
			f, ds := parseSourceFile(data, rel, code, prefix)
			c.loadDiags = append(c.loadDiags, ds...)
			c.files = append(c.files, f)
			if code == SourceCode {
				enPrefixes[prefix] = true
				for _, e := range f.Entries {
					if prev, dup := c.entries[e.Key]; dup {
						c.diag(rel, e.Line, e.Key, code, prefix, "also defined in %s:%d", prev.File, prev.Line)
						continue
					}
					c.entries[e.Key] = e
				}
				continue
			}
			if c.trans[code] == nil {
				c.trans[code] = map[string]*Translation{}
			}
			for _, t := range f.Translations {
				if prev, dup := c.trans[code][t.Key]; dup {
					c.diag(rel, t.Line, t.Key, code, prefix, "also translated in %s:%d", prev.File, prev.Line)
					continue
				}
				c.trans[code][t.Key] = t
			}
		}
	}
	for k := range c.entries {
		c.keys = append(c.keys, k)
	}
	sort.Strings(c.keys)
	sort.Slice(c.files, func(i, j int) bool { return c.files[i].Path < c.files[j].Path })
}

// Root is the repository root the catalog was loaded from.
func (c *Catalog) Root() string { return c.root }

// Files returns the source files, sorted by path.
func (c *Catalog) Files() []*SourceFile { return c.files }

// AllLanguages returns every language in languages.json, in its order
// (English first).
func (c *Catalog) AllLanguages() []Language { return slices.Clone(c.languages) }

// OutputLanguages returns the languages generated files contain: the
// shipping ones, and with draft also every draft language (or, when only
// is not empty, the draft languages it names) followed by the
// pseudo-locale. English comes first.
func (c *Catalog) OutputLanguages(draft bool, only []string) []Language {
	var out []Language
	for _, l := range c.languages {
		if l.Status == Shipping || (draft && (len(only) == 0 || slices.Contains(only, l.Code))) {
			out = append(out, l)
		}
	}
	if draft {
		out = append(out, PseudoLanguage)
	}
	return out
}

// Language returns a language by code (the pseudo-locale included).
func (c *Catalog) Language(code string) (Language, bool) {
	if code == PseudoLanguage.Code {
		return PseudoLanguage, true
	}
	for _, l := range c.languages {
		if l.Code == code {
			return l, true
		}
	}
	return Language{}, false
}

// Routes returns a copy of the root -> consumers table.
func (c *Catalog) Routes() map[string][]string {
	out := map[string][]string{}
	for r, cs := range c.routes {
		out[r] = slices.Clone(cs)
	}
	return out
}

// ConsumersOf returns the consumers of a root, sorted.
func (c *Catalog) ConsumersOf(root string) []string { return slices.Clone(c.routes[root]) }

// Serves reports whether consumer shows the keys of root.
func (c *Catalog) Serves(consumer, root string) bool {
	return slices.Contains(c.routes[root], consumer)
}

// Prefixes returns every prefix that has an English source file, sorted.
func (c *Catalog) Prefixes() []string {
	var out []string
	for _, f := range c.files {
		if f.IsSource() {
			out = append(out, f.Prefix)
		}
	}
	return out
}

// PrefixesFor returns the prefixes whose keys consumer shows, sorted.
func (c *Catalog) PrefixesFor(consumer string) []string {
	var out []string
	for _, p := range c.Prefixes() {
		if c.Serves(consumer, rootOf(p)) {
			out = append(out, p)
		}
	}
	return out
}

// Entries returns every English entry, sorted by key.
func (c *Catalog) Entries() []*Entry {
	out := make([]*Entry, 0, len(c.keys))
	for _, k := range c.keys {
		out = append(out, c.entries[k])
	}
	return out
}

// EntriesFor returns the entries consumer shows, sorted by key.
func (c *Catalog) EntriesFor(consumer string) []*Entry {
	var out []*Entry
	for _, k := range c.keys {
		if e := c.entries[k]; c.Serves(consumer, e.Root) {
			out = append(out, e)
		}
	}
	return out
}

// EntriesIn returns the entries of one prefix, sorted by key.
func (c *Catalog) EntriesIn(prefix string) []*Entry {
	var out []*Entry
	for _, k := range c.keys {
		if e := c.entries[k]; e.Prefix == prefix {
			out = append(out, e)
		}
	}
	return out
}

// Entry returns the English entry of a key, or nil.
func (c *Catalog) Entry(key string) *Entry { return c.entries[key] }

// Translation returns a language's translation entry of a key, or nil -
// whatever its state; Resolve says whether to use it.
func (c *Catalog) Translation(lang, key string) *Translation { return c.trans[lang][key] }

// Glossary returns a language's glossary, or nil.
func (c *Catalog) Glossary(code string) *Glossary { return c.glossary[code] }

// Never returns the never-translate terms.
func (c *Catalog) Never() []string { return slices.Clone(c.never) }

// State says where a resolved text comes from.
type State int

const (
	// StateSource: English itself - the language is English, or the key
	// has "translate": false.
	StateSource State = iota
	// StateTranslated: a current translation.
	StateTranslated
	// StateUnreviewed: a current translation of a review-required key
	// whose review is missing or stale. The translation is used (the
	// release check refuses it in a shipping language).
	StateUnreviewed
	// StateMissing: no translation; the English is used.
	StateMissing
	// StateStale: the translation was made from older English; the
	// current English is used.
	StateStale
	// StatePseudo: the pseudo-locale's text, made from the English.
	StatePseudo
)

func (s State) String() string {
	return [...]string{"source", "translated", "unreviewed", "missing", "stale", "pseudo"}[s]
}

// Resolved is a key's text in one language, with English filled in where
// there is no usable translation.
type Resolved struct {
	*Entry
	Lang Language
	// Text is what the language shows: the translation, or the English.
	Text  Text
	State State
	// Translation is the translation entry when there is one, used or not
	// (a stale one is kept here for tools that show what changed).
	Translation *Translation
}

// Fallback reports whether the English stands in for a translation.
func (r Resolved) Fallback() bool { return r.State == StateMissing || r.State == StateStale }

// Resolve returns a key's text in lang (ok false for an unknown key).
func (c *Catalog) Resolve(lang Language, key string) (Resolved, bool) {
	e := c.entries[key]
	if e == nil {
		return Resolved{}, false
	}
	r := Resolved{Entry: e, Lang: lang, Text: e.Text, State: StateSource}
	switch {
	case !e.Translate || lang.IsSource():
	case lang.Pseudo:
		r.Text, r.State = PseudoText(e.Text), StatePseudo
	default:
		t := c.trans[lang.Code][key]
		r.Translation = t
		switch {
		case t == nil:
			r.State = StateMissing
		case !t.EN.Equal(e.Text):
			r.State = StateStale
		case e.Review == ReviewRequired && !reviewCurrent(t.Reviewed, t.Text, t.EN):
			r.Text, r.State = t.Text, StateUnreviewed
		default:
			r.Text, r.State = t.Text, StateTranslated
		}
	}
	return r, true
}

// Messages returns every key of a prefix resolved in lang, sorted by key.
func (c *Catalog) Messages(lang Language, prefix string) []Resolved {
	var out []Resolved
	for _, e := range c.EntriesIn(prefix) {
		r, _ := c.Resolve(lang, e.Key)
		out = append(out, r)
	}
	return out
}

// Coverage counts a language's keys by state.
func (c *Catalog) Coverage(lang Language) map[State]int {
	out := map[State]int{}
	for _, k := range c.keys {
		r, _ := c.Resolve(lang, k)
		out[r.State]++
	}
	return out
}
