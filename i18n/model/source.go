// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// SourceFile is one file of i18n/strings: the English source of a prefix,
// or one language's translations of it.
type SourceFile struct {
	Path   string // repository-relative, slash-separated
	Lang   string
	Prefix string
	// Entries (English) or Translations (any other language), in file order.
	Entries      []*Entry
	Translations []*Translation
	// Raw is the file as read; Broken is set when it couldn't be read
	// whole (bad JSON, a duplicate key, an unknown field, a wrong type).
	// A broken file is never rewritten.
	Raw    []byte
	Broken bool
}

// IsSource reports whether this is an English source file.
func (f *SourceFile) IsSource() bool { return f.Lang == SourceCode }

// The fields an entry may have, and the order the canonical form writes
// them in.
var (
	entryFields       = []string{"text", "args", "note", "max", "rich", "stored", "review", "reviewed", "translate"}
	translationFields = []string{"text", "en", "reviewed"}
)

// parseSourceFile reads a strings file. Problems that stop an entry from
// being read are returned as diagnostics and mark the file broken; the
// rules about what the entries say are checked later, by Check.
func parseSourceFile(data []byte, path, lang, prefix string) (*SourceFile, []Diagnostic) {
	f := &SourceFile{Path: path, Lang: lang, Prefix: prefix, Raw: data}
	var ds []Diagnostic
	bad := func(line int, key, format string, a ...any) {
		f.Broken = true
		ds = append(ds, Diagnostic{File: path, Line: line, Key: key, Lang: lang, Prefix: prefix, Msg: fmt.Sprintf(format, a...)})
	}
	root, err := parseJSON(data)
	if err != nil {
		je := err.(*jsonError)
		bad(je.line, "", "%s", je.msg)
		return f, ds
	}
	if root.kind != kindObject {
		bad(root.line, "", "a strings file is one object of key -> entry")
		return f, ds
	}
	for _, m := range root.obj {
		key, n := m.key, m.val
		if n.kind != kindObject {
			bad(m.line, key, "an entry must be an object")
			continue
		}
		allowed := entryFields
		if lang != SourceCode {
			allowed = translationFields
		}
		okFields := true
		for _, fm := range n.obj {
			if !slices.Contains(allowed, fm.key) {
				bad(fm.line, key, "unknown field %q (an %s entry has %s)", fm.key, map[bool]string{true: "English", false: "translation"}[lang == SourceCode], strings.Join(allowed, ", "))
				okFields = false
			}
		}
		if !okFields {
			continue
		}
		if lang == SourceCode {
			if e, ok := parseEntry(n, key, path, prefix, m.line, bad); ok {
				f.Entries = append(f.Entries, e)
			}
		} else if t, ok := parseTranslation(n, key, lang, path, m.line, bad); ok {
			f.Translations = append(f.Translations, t)
		}
	}
	return f, ds
}

type badFunc func(line int, key, format string, a ...any)

func parseEntry(n *node, key, path, prefix string, line int, bad badFunc) (*Entry, bool) {
	e := &Entry{Key: key, Root: rootOf(key), Prefix: prefix, Translate: true, File: path, Line: line}
	ok := true
	tn := n.field("text")
	if tn == nil {
		bad(line, key, "\"text\" is required")
		return nil, false
	}
	if e.Text, ok = parseText(tn, key, "text", bad); !ok {
		return nil, false
	}
	for _, m := range n.obj {
		v := m.val
		switch m.key {
		case "args":
			if v.kind != kindArray {
				bad(m.line, key, "\"args\" is an array of [name, type] pairs")
				ok = false
				continue
			}
			for _, a := range v.arr {
				if a.kind != kindArray || len(a.arr) != 2 || a.arr[0].kind != kindString || a.arr[1].kind != kindString {
					bad(a.line, key, "each argument is a [\"name\", \"type\"] pair")
					ok = false
					continue
				}
				e.Args = append(e.Args, Arg{Name: a.arr[0].str, Type: ArgType(a.arr[1].str)})
			}
		case "note":
			e.Note, ok = str(v, m, key, bad, ok)
		case "max":
			i, err := strconv.Atoi(v.str)
			if v.kind != kindNumber || err != nil || i <= 0 {
				bad(m.line, key, "\"max\" must be a positive whole number")
				ok = false
				continue
			}
			e.Max = i
		case "rich":
			if v.kind != kindArray {
				bad(m.line, key, "\"rich\" is an array of tag names")
				ok = false
				continue
			}
			for _, t := range v.arr {
				if t.kind != kindString {
					bad(t.line, key, "\"rich\" is an array of tag names")
					ok = false
					continue
				}
				e.Rich = append(e.Rich, t.str)
			}
		case "stored":
			e.Stored, ok = boolean(v, m, key, bad, ok)
		case "translate":
			e.Translate, ok = boolean(v, m, key, bad, ok)
		case "review":
			e.Review, ok = str(v, m, key, bad, ok)
		case "reviewed":
			e.Reviewed, ok = str(v, m, key, bad, ok)
		}
	}
	return e, ok
}

func parseTranslation(n *node, key, lang, path string, line int, bad badFunc) (*Translation, bool) {
	t := &Translation{Key: key, Lang: lang, File: path, Line: line}
	tn, en := n.field("text"), n.field("en")
	if tn == nil || en == nil {
		bad(line, key, "a translation needs \"text\" and \"en\" (the English it was made from)")
		return nil, false
	}
	var ok1, ok2 bool
	t.Text, ok1 = parseText(tn, key, "text", bad)
	t.EN, ok2 = parseText(en, key, "en", bad)
	if !ok1 || !ok2 {
		return nil, false
	}
	if t.Text.Plural != t.EN.Plural {
		bad(line, key, "\"text\" and \"en\" must both be plural or both not")
		return nil, false
	}
	ok := true
	if r := n.field("reviewed"); r != nil {
		if r.kind != kindString {
			bad(r.line, key, "\"reviewed\" must be a string")
			ok = false
		}
		t.Reviewed = r.str
	}
	return t, ok
}

// parseText reads a string or {"one": ..., "other": ...}.
func parseText(n *node, key, field string, bad badFunc) (Text, bool) {
	switch n.kind {
	case kindString:
		return PlainText(n.str), true
	case kindObject:
		var t Text
		t.Plural = true
		ok := true
		for _, m := range n.obj {
			switch m.key {
			case FormOne, FormOther:
				if m.val.kind != kindString {
					bad(m.line, key, "%s.%s must be a string", field, m.key)
					ok = false
					continue
				}
				if m.key == FormOne {
					t.One = m.val.str
				} else {
					t.Other = m.val.str
				}
			default:
				bad(m.line, key, "plural form %q: plurals are exactly \"one\" and \"other\" (a text for zero is a key of its own, chosen in code)", m.key)
				ok = false
			}
		}
		if n.field(FormOne) == nil || n.field(FormOther) == nil {
			bad(n.line, key, "a plural %q needs both \"one\" and \"other\"", field)
			ok = false
		}
		return t, ok
	}
	bad(n.line, key, "%q must be a string or {\"one\": ..., \"other\": ...}", field)
	return Text{}, false
}

func str(v *node, m member, key string, bad badFunc, ok bool) (string, bool) {
	if v.kind != kindString {
		bad(m.line, key, "%q must be a string", m.key)
		return "", false
	}
	return v.str, ok
}

func boolean(v *node, m member, key string, bad badFunc, ok bool) (bool, bool) {
	if v.kind != kindBool {
		bad(m.line, key, "%q must be true or false", m.key)
		return m.key == "translate", false
	}
	return v.b, ok
}
