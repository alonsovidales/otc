// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"bytes"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
)

// pushArgNames are the only arguments a push may carry: Apple and Google
// can read push payloads, so a push says who (a friend's name), which
// release or which disk port, and nothing else (push/push.go's rule).
var pushArgNames = []string{"name", "version", "port"}

// Check validates the whole catalog - every rule in docs/i18n.md - and
// returns the diagnostics graded and filtered by opts.
func (c *Catalog) Check(opts CheckOptions) []Diagnostic {
	var ds []Diagnostic
	add := func(d Diagnostic) { ds = append(ds, d) }
	ds = append(ds, c.loadDiags...)
	c.checkFiles(add)
	c.checkNames(add)
	split := c.splitRoots()
	for _, k := range c.keys {
		c.checkEntry(c.entries[k], split, add)
	}
	c.checkStored(add)
	for _, l := range c.languages {
		if !l.IsSource() {
			c.checkLanguage(l, add)
		}
	}
	return grade(ds, opts, c.languages)
}

// NonCanonical returns an error for every source file that isn't in its
// canonical form (make i18n rewrites them; -check refuses them).
func (c *Catalog) NonCanonical(opts CheckOptions) []Diagnostic {
	var ds []Diagnostic
	for _, f := range c.files {
		if !f.Broken && !bytes.Equal(f.Canonical(), f.Raw) {
			ds = append(ds, Diagnostic{File: f.Path, Lang: f.Lang, Prefix: f.Prefix, Msg: "not in canonical form (sorted keys, fixed layout, reviews with their fingerprint): run make i18n"})
		}
	}
	return grade(ds, opts, c.languages)
}

// goRendered reports whether Go renders a root's keys as plain text: those
// may not have tags. site.* is the exception, rendered through
// html/template's converter.
func (c *Catalog) goRendered(root string) bool {
	return root != "site" && c.Serves(ConsumerGo, root)
}

// goOnly reports whether only Go shows a root (the stored-data argument
// types are allowed there).
func (c *Catalog) goOnly(root string) bool {
	cs := c.routes[root]
	return len(cs) == 1 && cs[0] == ConsumerGo
}

// expectedPrefix returns the file a key belongs in: its root, or its first
// two segments when the root is split into several files.
func expectedPrefix(key string, split map[string]bool) string {
	segs := strings.Split(key, ".")
	if split[segs[0]] && len(segs) > 2 {
		return segs[0] + "." + segs[1]
	}
	return segs[0]
}

// splitRoots returns the roots whose English sources are split by area.
func (c *Catalog) splitRoots() map[string]bool {
	split := map[string]bool{}
	for _, f := range c.files {
		if f.IsSource() && strings.Contains(f.Prefix, ".") {
			split[rootOf(f.Prefix)] = true
		}
	}
	return split
}

func (c *Catalog) checkFiles(add func(Diagnostic)) {
	whole := map[string]string{}
	split := map[string]string{}
	for _, f := range c.files {
		if !f.IsSource() {
			continue
		}
		root := rootOf(f.Prefix)
		if _, ok := c.routes[root]; !ok {
			add(Diagnostic{File: f.Path, Prefix: f.Prefix, Msg: fmt.Sprintf("root %q has no route in %s", root, LanguagesFile)})
		}
		if f.Prefix == root {
			whole[root] = f.Path
		} else if _, seen := split[root]; !seen {
			split[root] = f.Path
		}
	}
	for root, p := range whole {
		if q, ok := split[root]; ok {
			add(Diagnostic{File: p, Prefix: root, Msg: fmt.Sprintf("root %q is split into files by area (%s), so it can't also have %s: move these keys into area files", root, q, p)})
		}
	}
}

// checkNames refuses keys or prefixes that two platforms' renamings would
// turn into the same identifier.
func (c *Catalog) checkNames(add func(Diagnostic)) {
	type renaming struct {
		what string
		fn   func(string) string
	}
	renamings := []renaming{{"Android", AndroidName}, {"Swift/Kotlin", CamelName}, {"Go", GoName}}
	collide := func(names []string, report func(name, other string, how []string)) {
		pairs := map[[2]string][]string{}
		var order [][2]string
		for _, r := range renamings {
			seen := map[string]string{}
			for _, n := range names {
				id := r.fn(n)
				if other, ok := seen[id]; ok {
					p := [2]string{n, other}
					if _, known := pairs[p]; !known {
						order = append(order, p)
					}
					pairs[p] = append(pairs[p], r.what+" "+id)
					continue
				}
				seen[id] = n
			}
		}
		for _, p := range order {
			report(p[0], p[1], pairs[p])
		}
	}
	collide(c.keys, func(key, other string, how []string) {
		e := c.entries[key]
		add(Diagnostic{File: e.File, Line: e.Line, Key: key, Prefix: e.Prefix, Msg: fmt.Sprintf("collides with %s after renaming (%s)", other, strings.Join(how, ", "))})
	})
	collide(c.Prefixes(), func(p, other string, how []string) {
		add(Diagnostic{File: path.Join(StringsDir, SourceCode, p+".json"), Prefix: p, Msg: fmt.Sprintf("prefix collides with %s after renaming (%s)", other, strings.Join(how, ", "))})
	})
}

func (c *Catalog) checkEntry(e *Entry, split map[string]bool, add func(Diagnostic)) {
	bad := func(format string, a ...any) {
		add(Diagnostic{File: e.File, Line: e.Line, Key: e.Key, Prefix: e.Prefix, Msg: fmt.Sprintf(format, a...)})
	}
	if !KeyPattern.MatchString(e.Key) {
		bad("key must match %s", KeyPattern)
		return
	}
	if len(e.Key) > MaxKeyLen {
		bad("key is longer than %d characters", MaxKeyLen)
	}
	if want := expectedPrefix(e.Key, split); want != e.Prefix {
		bad("belongs in %s/%s/%s.json", StringsDir, SourceCode, want)
	}
	if strings.TrimSpace(e.Note) == "" {
		bad("\"note\" is required: tell translators where and how the text is shown")
	}

	// Arguments.
	names := map[string]bool{}
	camel := map[string]string{}
	counts := 0
	for _, a := range e.Args {
		switch {
		case !argNamePattern.MatchString(a.Name):
			bad("argument %q: names are lowercase words joined by underscores", a.Name)
		case names[a.Name]:
			bad("argument %q is declared twice", a.Name)
		case len(ReservedIn(a.Name, c.routes[e.Root])) > 0:
			bad("argument %q is a reserved word in %s; pick another name", a.Name, strings.Join(ReservedIn(a.Name, c.routes[e.Root]), " and "))
		case camel[ArgName(a.Name)] != "":
			bad("arguments %q and %q get the same name in generated code (%s)", camel[ArgName(a.Name)], a.Name, ArgName(a.Name))
		}
		names[a.Name] = true
		camel[ArgName(a.Name)] = a.Name
		switch {
		case !argTypes[a.Type]:
			bad("argument %q: unknown type %q (text, user, count, int; bytes, datetime, msg for Go-rendered keys)", a.Name, a.Type)
		case goOnlyArgTypes[a.Type] && !c.goOnly(e.Root):
			bad("argument %q: type %q only exists for keys only Go renders (roots routed to \"go\" alone)", a.Name, a.Type)
		case a.Type == ArgCount:
			counts++
		}
		if e.Root == "push" && !slices.Contains(pushArgNames, a.Name) {
			bad("argument %q: a push may only carry {name}, {version} and {port} (Apple and Google read push payloads)", a.Name)
		}
	}
	switch {
	case counts > 1:
		bad("at most one argument may be a count")
	case e.Text.Plural && counts == 0:
		bad("a plural text needs a count argument, which picks the form")
	case !e.Text.Plural && counts == 1:
		bad("a count argument picks a plural form: make the text {\"one\": ..., \"other\": ...} or use type int")
	}

	// Tags.
	rich := map[string]bool{}
	for _, t := range e.Rich {
		switch {
		case !tagNamePattern.MatchString(t):
			bad("rich tag %q: tag names are lowercase letters and digits", t)
		case rich[t]:
			bad("rich tag %q is listed twice", t)
		}
		rich[t] = true
	}
	if len(e.Rich) > 0 && c.goRendered(e.Root) {
		bad("%s.* keys are rendered by Go as plain text and can't have tags", e.Root)
	}

	// The forms.
	used := map[string]bool{}
	tokOK := true
	en, _ := c.Language(SourceCode)
	for _, f := range e.Text.Forms() {
		label := formLabel(e.Text, f.Name)
		for _, p := range contentProblems(f.Text) {
			bad("%s%s", label, p)
		}
		toks, err := Tokenize(f.Text)
		if err != nil {
			bad("%s%v", label, err)
			tokOK = false
			continue
		}
		c.checkPlaceholders(e, en, f, toks, bad)
		for _, t := range toks {
			if t.Kind != OpenTag {
				continue
			}
			used[t.Value] = true
			if !rich[t.Value] {
				bad("%stag <%s> is not listed in \"rich\"", label, t.Value)
			}
		}
		if e.Max > 0 && TextLength(f.Text) > e.Max {
			bad("%sis %d characters, more than \"max\" %d", label, TextLength(f.Text), e.Max)
		}
	}
	for _, t := range e.Rich {
		if tokOK && !used[t] {
			bad("rich tag %q is listed but the text never uses it", t)
		}
	}

	// Review.
	switch {
	case e.Review != "" && e.Review != ReviewRequired:
		bad("\"review\" can only be %q", ReviewRequired)
	case e.Review == "" && e.Reviewed != "":
		bad("\"reviewed\" is only for keys with \"review\": %q", ReviewRequired)
	case e.Review == ReviewRequired:
		c.checkReview(e.Reviewed, func(class Class, msg string) {
			add(Diagnostic{Class: class, File: e.File, Line: e.Line, Key: e.Key, Lang: SourceCode, Prefix: e.Prefix, Msg: msg})
		}, e.Text)
	}
}

// formLabel names a form in messages about a plural text.
func formLabel(t Text, form string) string {
	if !t.Plural {
		return ""
	}
	return form + ": "
}

// checkPlaceholders checks one form's placeholders against the declared
// arguments: every placeholder is declared, and every argument appears in
// every form, except that "one" may leave the count out in a language
// whose "one" means exactly 1.
func (c *Catalog) checkPlaceholders(e *Entry, lang Language, f Form, toks []Token, bad func(string, ...any)) {
	label := formLabel(e.Text, f.Name)
	present := map[string]bool{}
	for _, t := range toks {
		if t.Kind != Placeholder {
			continue
		}
		present[t.Value] = true
		if e.ArgIndex(t.Value) < 0 {
			bad("%s{%s} is not a declared argument", label, t.Value)
		}
	}
	for _, a := range e.Args {
		if present[a.Name] {
			continue
		}
		if a.Type == ArgCount && f.Name == FormOne {
			if lang.OneMeansOne() {
				continue
			}
			bad("%s%s uses \"one\" for numbers other than 1 too (0 in French), so it must show {%s}", label, lang.Code, a.Name)
			continue
		}
		bad("%sargument {%s} is declared but not used", label, a.Name)
	}
}

// checkReview reports a missing, malformed or stale review.
func (c *Catalog) checkReview(reviewed string, report func(Class, string), texts ...Text) {
	if reviewed == "" {
		report(ClassPending, "needs a review (\"reviewed\": \"<who> <YYYY-MM-DD>\", then make i18n)")
		return
	}
	r, err := ParseReview(reviewed)
	switch {
	case err != nil:
		report(ClassError, "\"reviewed\": "+err.Error())
	case r.Fingerprint == "":
		report(ClassError, "\"reviewed\" has no fingerprint yet: run make i18n to record what was reviewed")
	case r.Fingerprint != Fingerprint(texts...):
		report(ClassPending, fmt.Sprintf("the review by %s on %s is stale: the text changed since", r.Who, r.Date.Format("2006-01-02")))
	}
}

func (c *Catalog) checkLanguage(l Language, add func(Diagnostic)) {
	byKey := c.trans[l.Code]
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		c.checkTranslation(l, byKey[k], add)
	}
	for _, k := range c.keys {
		e := c.entries[k]
		if e.Translate && byKey[k] == nil {
			add(Diagnostic{Class: ClassPending, File: path.Join(StringsDir, l.Code, e.Prefix+".json"), Key: k, Lang: l.Code, Prefix: e.Prefix, Msg: "not translated"})
		}
	}
}

func (c *Catalog) checkTranslation(l Language, t *Translation, add func(Diagnostic)) {
	e := c.entries[t.Key]
	prefix := strings.TrimSuffix(path.Base(t.File), ".json")
	report := func(class Class, msg string) {
		add(Diagnostic{Class: class, File: t.File, Line: t.Line, Key: t.Key, Lang: l.Code, Prefix: prefix, Msg: msg})
	}
	bad := func(format string, a ...any) { report(ClassError, fmt.Sprintf(format, a...)) }
	if e == nil {
		bad("there is no English entry with this key: delete the translation")
		return
	}
	if e.Prefix != prefix {
		bad("belongs in %s/%s/%s.json, like its English", StringsDir, l.Code, e.Prefix)
	}
	// What a form may contain holds even for a stale translation.
	tokOK := true
	for _, f := range t.Text.Forms() {
		label := formLabel(t.Text, f.Name)
		for _, p := range contentProblems(f.Text) {
			bad("%s%s", label, p)
		}
		if _, err := Tokenize(f.Text); err != nil {
			bad("%s%v", label, err)
			tokOK = false
		}
	}
	if !e.Translate {
		if !t.Text.Equal(e.Text) || !t.EN.Equal(e.Text) {
			bad("\"translate\" is false: the text stays the English (or leave the key out)")
		}
		return
	}
	if t.Reviewed != "" && e.Review != ReviewRequired {
		bad("\"reviewed\" is only for keys with \"review\": %q", ReviewRequired)
	}
	if !t.EN.Equal(e.Text) {
		report(ClassPending, "stale: translated from older English (the English is used until it is translated again)")
		return
	}
	if !tokOK {
		return
	}

	enForms := e.Text.Forms()
	for _, f := range t.Text.Forms() {
		label := formLabel(t.Text, f.Name)
		toks, _ := Tokenize(f.Text)
		c.checkPlaceholders(e, l, f, toks, bad)
		enForm := e.Text.Form(f.Name)
		if got, want := sortedTags(f.Text), sortedTags(enForm); !slices.Equal(got, want) {
			bad("%stags %v differ from the English %v", label, got, want)
		}
		for _, link := range linksIn(f.Text) {
			if !slices.ContainsFunc(enForms, func(ef Form) bool { return strings.Contains(ef.Text, link) }) {
				bad("%s%q is not in the English: links, addresses and domains come from code", label, link)
			}
		}
		if e.Max > 0 && TextLength(f.Text) > e.Max {
			bad("%sis %d characters, more than \"max\" %d", label, TextLength(f.Text), e.Max)
		}
		for _, term := range c.never {
			if containsWord(enForm, term) && !strings.Contains(f.Text, term) {
				bad("%s%q is never translated and must appear as it is", label, term)
			}
		}
		if g := c.glossary[l.Code]; g != nil {
			for _, term := range g.Terms {
				if term.Enforce && containsWordFold(enForm, term.EN) && !containsFold(f.Text, term.Text) {
					report(ClassGlossary, fmt.Sprintf("%sthe glossary translates %q as %q", label, term.EN, term.Text))
				}
			}
			for _, fb := range g.Forbidden {
				if containsWordFold(f.Text, fb.Text) {
					msg := fmt.Sprintf("%sthe glossary says not to use %q", label, fb.Text)
					if fb.Note != "" {
						msg += " (" + fb.Note + ")"
					}
					report(ClassGlossary, msg)
				}
			}
		}
	}
	if e.Review == ReviewRequired {
		c.checkReview(t.Reviewed, report, t.Text, t.EN)
	}
}

func sortedTags(s string) []string {
	tags := Tags(s)
	sort.Strings(tags)
	return tags
}
