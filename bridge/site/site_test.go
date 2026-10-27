// SPDX-License-Identifier: AGPL-3.0-or-later

package site

import (
	"bytes"
	"encoding/json"
	"flag"
	"html/template"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"text/template/parse"
	"time"

	"github.com/alonsovidales/otc/i18n"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// pages are every page the site renders.
var pages = []string{Landing, Social, Alternative, Drive, Pi, Disabled, Unavailable, Account, NotFound}

// publicPages are the pages in the sitemap, in its order (localized's):
// the landing page, the comparison pages, the Raspberry Pi page.
var publicPages = []string{Landing, Social, Alternative, Drive, Pi}

// shared is the file of definitions the public pages share: no page.
const shared = "common.html"

var testOptions = Options{
	Base:  "https://off-the.cloud",
	Nonce: "bm9uY2Utbm9uY2Utbm9uY2U=",
	Now:   time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC),
}

// englishEntry is what the tests read of an English site.* entry.
type englishEntry struct {
	Text json.RawMessage `json:"text"`
	Args [][]string      `json:"args"`
	Rich []string        `json:"rich"`
}

// siteEntries reads every English site.* entry from the catalog's source.
func siteEntries(t *testing.T) map[string]englishEntry {
	t.Helper()
	return englishEntries(t, "site.*.json")
}

// englishEntries reads the English string files matching pattern.
func englishEntries(t *testing.T, pattern string) map[string]englishEntry {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "i18n", "strings", "en", pattern))
	if err != nil || len(files) == 0 {
		t.Fatalf("no %s string files: %v", pattern, err)
	}
	out := map[string]englishEntry{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]englishEntry
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// use is one key a page asks for: which function, and the argument names
// it passes.
type use struct {
	fn, key string
	args    []string
	types   []string // jsform: each argument's type, as the page names it
}

// pageUses walks the parsed pages, and the definitions they call
// (common.html's head, header, footer...), for every call of the text
// functions.
func pageUses(t *testing.T) []use {
	t.Helper()
	tmpl, err := templates(English())
	if err != nil {
		t.Fatal(err)
	}
	var uses []use
	var walk func(n parse.Node)
	var called map[string]bool // the definitions one page's walk went into
	call := func(args []parse.Node) {
		if len(args) == 0 {
			return
		}
		id, ok := args[0].(*parse.IdentifierNode)
		if !ok {
			return
		}
		str := func(n parse.Node) string {
			if s, ok := n.(*parse.StringNode); ok {
				return s.Text
			}
			t.Errorf("%s: an argument that isn't a string literal: %s", id.Ident, n)
			return ""
		}
		rest := args[1:]
		switch id.Ident {
		case "t", "rich":
			if len(rest) == 0 {
				t.Errorf("%s without a key", id.Ident)
				return
			}
			u := use{fn: id.Ident, key: str(rest[0])}
			for i := 1; i < len(rest); i += 2 {
				u.args = append(u.args, str(rest[i]))
			}
			uses = append(uses, u)
		case "lines":
			uses = append(uses, use{fn: "t", key: str(rest[0])})
		case "series":
			uses = append(uses, use{fn: "t", key: str(rest[0]), args: []string{str(rest[1])}})
		case "texts":
			for i := 1; i < len(rest); i += 2 {
				uses = append(uses, use{fn: "t", key: str(rest[i])})
			}
		case "jstexts":
			// name/value pairs: a value is a key without arguments, or a
			// jsform, which the walk finds on its own.
			for i := 1; i < len(rest); i += 2 {
				if s, ok := rest[i].(*parse.StringNode); ok {
					uses = append(uses, use{fn: "jsform", key: s.Text})
				}
			}
		case "jsform":
			if len(rest) == 0 {
				t.Errorf("jsform without a key")
				return
			}
			u := use{fn: "jsform", key: str(rest[0])}
			for i := 1; i+1 < len(rest); i += 2 {
				u.args = append(u.args, str(rest[i]))
				u.types = append(u.types, str(rest[i+1]))
			}
			uses = append(uses, u)
		}
	}
	walk = func(n parse.Node) {
		switch n := n.(type) {
		case *parse.ListNode:
			if n == nil {
				return
			}
			for _, c := range n.Nodes {
				walk(c)
			}
		case *parse.ActionNode:
			walk(n.Pipe)
		case *parse.PipeNode:
			if n == nil {
				return
			}
			for _, c := range n.Cmds {
				walk(c)
			}
		case *parse.CommandNode:
			call(n.Args)
			for _, a := range n.Args {
				walk(a)
			}
		case *parse.IfNode:
			walk(n.Pipe)
			walk(n.List)
			walk(n.ElseList)
		case *parse.RangeNode:
			walk(n.Pipe)
			walk(n.List)
			walk(n.ElseList)
		case *parse.WithNode:
			walk(n.Pipe)
			walk(n.List)
			walk(n.ElseList)
		case *parse.TemplateNode:
			walk(n.Pipe)
			if def := tmpl.Lookup(n.Name); def != nil && def.Tree != nil && !called[n.Name] {
				called[n.Name] = true
				walk(def.Tree.Root)
			}
		}
	}
	for _, name := range pages {
		pt := tmpl.Lookup(name)
		if pt == nil {
			t.Fatalf("no page %s", name)
		}
		called = map[string]bool{}
		walk(pt.Tree.Root)
	}
	return uses
}

// Every key a page names exists with exactly the arguments the page
// passes; t gets only plain keys and rich only rich ones; every tag of a
// rich key is in the tags table; and every site.* key is used somewhere.
// The contact form's errors are the API's own api.error.* keys, the texts
// the endpoint answers with.
func TestPageKeys(t *testing.T) {
	entries := siteEntries(t)
	known := englishEntries(t, "api.json")
	for k, v := range entries {
		known[k] = v
	}
	used := map[string]bool{}
	for _, u := range pageUses(t) {
		e, ok := known[u.key]
		if !ok {
			t.Errorf("%s %q: no such site.* or api.* key", u.fn, u.key)
			continue
		}
		used[u.key] = true
		var declared []string
		for _, a := range e.Args {
			declared = append(declared, a[0])
		}
		got := slices.Clone(u.args)
		sort.Strings(got)
		sort.Strings(declared)
		if !slices.Equal(got, declared) {
			t.Errorf("%s %q: the page passes %v, the key declares %v", u.fn, u.key, u.args, declared)
		}
		// A script's text (jsform) may be plain or rich: the script
		// gets spans for a rich one. Its arguments' types are the key's.
		if rich := len(e.Rich) > 0; u.fn != "jsform" && rich != (u.fn == "rich") {
			t.Errorf("%s %q: rich key %v", u.fn, u.key, rich)
		}
		for i, name := range u.args {
			for _, a := range e.Args {
				if a[0] == name && i < len(u.types) && u.types[i] != a[1] {
					t.Errorf("%s %q: the page says %s is a %s, the key a %s", u.fn, u.key, name, u.types[i], a[1])
				}
			}
		}
		for _, tag := range e.Rich {
			if u.fn == "jsform" {
				// The account page's script makes these elements itself.
				if _, ok := accountTags[tag]; !ok {
					t.Errorf("%q: tag <%s> isn't one the account page's script makes", u.key, tag)
				}
			} else if _, ok := tags[tag]; !ok {
				t.Errorf("%q: tag <%s> isn't in the tags table", u.key, tag)
			}
		}
	}
	for k := range entries {
		if !used[k] {
			t.Errorf("%s: no page shows it", k)
		}
	}
}

// Every page renders in every language the build carries, carrying the
// nonce on each inline <style> and executable <script>, and its language.
func TestRenderEveryLanguage(t *testing.T) {
	for _, l := range i18n.Languages() {
		for _, page := range pages {
			out, err := Render(page, l.Code, testOptions)
			if err != nil {
				t.Fatalf("%s in %s: %v", page, l.Code, err)
			}
			s := string(out)
			if !strings.Contains(s, `<html lang="`+l.Tag+`">`) {
				t.Errorf("%s in %s: no <html lang=%q>", page, l.Code, l.Tag)
			}
			doc, err := html.Parse(bytes.NewReader(out))
			if err != nil {
				t.Fatal(err)
			}
			for n := range doc.Descendants() {
				if n.Type != html.ElementNode || (n.DataAtom != atom.Script && n.DataAtom != atom.Style) {
					continue
				}
				if typ := attr(n, "type"); typ == "application/json" || typ == "application/ld+json" {
					continue // data, never run
				}
				if attr(n, "nonce") != testOptions.Nonce {
					t.Errorf("%s in %s: a <%s> without the nonce", page, l.Code, n.Data)
				}
			}
			if strings.Contains(s, "{{") || leftKey.MatchString(s) {
				t.Errorf("%s in %s: a template action or a key left in the output", page, l.Code)
			}
		}
	}
}

// leftKey is a key rendered as itself: what a key the catalog lacks
// becomes.
var leftKey = regexp.MustCompile(`\bsite\.[a-z0-9]+\.[a-z0-9_.]+`)

func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

// An unknown language renders English; an unknown page is an error.
func TestRenderFallbacks(t *testing.T) {
	en, err := Render(Landing, English(), testOptions)
	if err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"", "xx", "EN", "en-GB", "../en"} {
		got, err := Render(Landing, lang, testOptions)
		if err != nil || !bytes.Equal(got, en) {
			t.Errorf("Render(%q) = %d bytes, %v; want the English page", lang, len(got), err)
		}
	}
	if _, err := Render("privacy.html", English(), testOptions); err == nil {
		t.Error("an unknown page rendered")
	}
}

// The landing page names its address in every carried language (hreflang,
// with x-default: /) and its canonical address; the language switcher
// shows only once a second language ships.
func TestLandingAlternates(t *testing.T) {
	langs := i18n.Languages()
	out, err := Render(Landing, English(), testOptions)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, `<link rel="canonical" href="https://off-the.cloud/">`) {
		t.Error("no canonical link to /")
	}
	for _, l := range langs {
		want := `<link rel="alternate" hreflang="` + l.Tag + `" href="https://off-the.cloud` + Path(l.Code) + `">`
		if !strings.Contains(s, want) {
			t.Errorf("missing %s", want)
		}
	}
	if !strings.Contains(s, `<link rel="alternate" hreflang="x-default" href="https://off-the.cloud/">`) {
		t.Error("no x-default alternate")
	}
	if got := strings.Contains(s, `class="lang-switch"`); got != (len(langs) > 1) {
		t.Errorf("language switcher shown %v with %d languages", got, len(langs))
	}
	if len(langs) > 1 {
		// Each language by its own name, at /<code>/ (English too: / would
		// redirect a browser that prefers another language), this one
		// marked as the current page.
		for _, l := range langs {
			link := `<a href="/` + l.Code + `/" lang="` + l.Tag + `" hreflang="` + l.Tag + `"`
			if l.Code == English() {
				link += ` aria-current="page"`
			}
			if !strings.Contains(s, link+">"+template.HTMLEscapeString(l.Name)+"</a>") {
				t.Errorf("switcher: no %s>%s</a>", link, l.Name)
			}
		}
	}
	if Path(English()) != "/" || Path("es") != "/es/" {
		t.Errorf("Path: %q %q", Path(English()), Path("es"))
	}
}

// What the pages' scripts write is English unchanged: the same words the
// scripts held before they moved into the catalog.
func TestScriptTextsUnchanged(t *testing.T) {
	out, err := Render(Landing, English(), testOptions)
	if err != nil {
		t.Fatal(err)
	}
	var texts map[string]string
	if err := json.Unmarshal([]byte(jsonBlock(t, out, "site-text")), &texts); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"copy":       "Copy",
		"copied":     "Copied!",
		"copyFailed": "Select & copy manually",
		"send":       "Send message",
		"sending":    "Sending…",
		"sent":       "Thanks — we'll get back to you soon.",
		"sendFailed": "Something went wrong sending your message.",
	}
	for k, v := range want {
		if texts[k] != v {
			t.Errorf("site-text %s = %q, want %q", k, texts[k], v)
		}
	}
	if len(texts) != len(want) {
		t.Errorf("site-text has %d texts, want %d", len(texts), len(want))
	}

	// The contact form showed the bridge's English text for each error;
	// the page's table has the same words for each code the contact
	// endpoint (and the origin check before it) answers with.
	var errs map[string]string
	if err := json.Unmarshal([]byte(jsonBlock(t, out, "contact-errors")), &errs); err != nil {
		t.Fatal(err)
	}
	wantErrs := map[string]string{
		"invalid_body":      "invalid request body",
		"missing_fields":    "name, email and message are required",
		"invalid_email":     "that doesn't look like a valid email address",
		"field_too_long":    "one of the fields is too long",
		"too_many_messages": "please wait a moment before sending another message",
		"internal_error":    "internal error, please try again",
		"cross_origin":      "not allowed from this page",
	}
	for k, v := range wantErrs {
		if errs[k] != v {
			t.Errorf("contact-errors %s = %q, want %q", k, errs[k], v)
		}
	}
	if len(errs) != len(wantErrs) {
		t.Errorf("contact-errors has %d texts, want %d", len(errs), len(wantErrs))
	}

	out, err = Render(Unavailable, English(), testOptions)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	if err := json.Unmarshal([]byte(jsonBlock(t, out, "countdown-text")), &lines); err != nil {
		t.Fatal(err)
	}
	// The page started at 30 and showed 'Checking again in ' + seconds + 's'.
	if len(lines) != 31 {
		t.Fatalf("%d countdown lines, want 31", len(lines))
	}
	for s := 1; s <= 30; s++ {
		if want := "Checking again in " + strconv.Itoa(s) + "s"; lines[s] != want {
			t.Errorf("countdown[%d] = %q, want %q", s, lines[s], want)
		}
	}
}

// jsonBlock returns the content of the <script type="application/json">
// with the given id.
func jsonBlock(t *testing.T, page []byte, id string) string {
	t.Helper()
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		t.Fatal(err)
	}
	for n := range doc.Descendants() {
		if n.DataAtom == atom.Script && attr(n, "id") == id {
			if attr(n, "type") != "application/json" {
				t.Fatalf("#%s is %q, not a JSON block", id, attr(n, "type"))
			}
			if n.FirstChild == nil {
				return ""
			}
			return n.FirstChild.Data
		}
	}
	t.Fatalf("no #%s", id)
	return ""
}

// updateGoldens rewrites testdata/*.golden from the English pages as they
// render now: go test ./bridge/site -run TestEnglishUnchanged -update. Only
// for a change to what a page says or links to that is meant, reviewed in
// the golden's diff.
var updateGoldens = flag.Bool("update", false, "rewrite testdata/*.golden from the English pages")

// The English pages read exactly as the static pages they replaced: their
// text and text attributes, their links and images, line by line
// (testdata/*.golden were extracted from bridge/static/landing.html,
// disabled.html, unavailable.html and account.html before they moved into
// the binary, with the year the footer's script filled in; since then the
// Android and iPhone cards link to their stores instead of "Coming soon",
// and the landing page has gained the comparison page's links, its at a
// glance list and more questions). The comparison page had no static page
// before it: its golden is what it said when it was written. The account
// page writes the rest of its text from its script:
// TestAccountScriptTextsUnchanged.
func TestEnglishUnchanged(t *testing.T) {
	for _, page := range pages {
		out, err := Render(page, English(), testOptions)
		if err != nil {
			t.Fatal(err)
		}
		golden := filepath.Join("testdata", strings.TrimSuffix(page, ".html")+".golden")
		got := visibleText(t, out)
		if *updateGoldens {
			if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
			for i := 0; i < len(gl) || i < len(wl); i++ {
				var g, w string
				if i < len(gl) {
					g = gl[i]
				}
				if i < len(wl) {
					w = wl[i]
				}
				if g != w {
					t.Errorf("%s line %d:\n  got  %q\n  want %q", page, i+1, g, w)
					break
				}
			}
		}
	}
}

// inlineElems don't break a line of visibleText.
var inlineElems = map[atom.Atom]bool{atom.A: true, atom.Strong: true, atom.Em: true, atom.Code: true,
	atom.Span: true, atom.B: true, atom.I: true, atom.Br: true}

var spaces = regexp.MustCompile(`\s+`)

// visibleText is what a page shows and says: one line per block of text,
// whitespace collapsed (as the browser renders it), then each text
// attribute, link and image source. Scripts, styles, comments and the
// head's links (canonical, hreflang: new) are left out; the footer's year
// span is filled in as its script did.
func visibleText(t *testing.T, page []byte) string {
	t.Helper()
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	var cur strings.Builder
	flush := func() {
		if l := strings.TrimSpace(spaces.ReplaceAllString(cur.String(), " ")); l != "" {
			lines = append(lines, l)
		}
		cur.Reset()
	}
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			cur.WriteString(n.Data)
			return
		case html.CommentNode:
			return
		case html.ElementNode:
			switch {
			case n.DataAtom == atom.Script, n.DataAtom == atom.Style:
				return
			case n.DataAtom == atom.Nav && attr(n, "class") == "lang-switch":
				return // new: the language switcher, once a second language ships
			}
			block := !inlineElems[n.DataAtom]
			if block {
				flush()
			}
			for _, a := range n.Attr {
				switch {
				case a.Key == "alt" || a.Key == "aria-label" || a.Key == "placeholder" || a.Key == "title" || a.Key == "lang":
					lines = append(lines, "@"+n.Data+"."+a.Key+"="+a.Val)
				case a.Key == "content" && attr(n, "name") == "description":
					lines = append(lines, "@meta.description="+a.Val)
				case a.Key == "href" && n.DataAtom == atom.A, a.Key == "src" && n.DataAtom == atom.Img:
					lines = append(lines, "@"+n.Data+"."+a.Key+"="+a.Val)
				case a.Key == "download" && n.DataAtom == atom.A:
					lines = append(lines, "@a.download")
				}
			}
			if n.DataAtom == atom.Span && attr(n, "id") == "y" && n.FirstChild == nil {
				cur.WriteString(strconv.Itoa(testOptions.Now.Year()))
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
			if block {
				flush()
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	flush()
	return strings.Join(lines, "\n") + "\n"
}

// The rendering contract: whatever a translation or an argument holds is
// text, escaped for where it stands - never markup, and a link's address
// never comes from the catalog.
func TestHostileTextIsInert(t *testing.T) {
	const hostile = `</script><img src=x onerror=alert(1)>{{.}}"'&<b>`
	tmpl, err := template.New("spans").Funcs(funcs(English())).Parse(spansTemplate)
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err = tmpl.Parse(`{{define "page"}}<p title="{{.Attr}}">{{template "spans" .Spans}}</p>` +
		`<p>{{t "site.landing.install.step_signin" "address" .Attr}}</p>` +
		`<script type="application/json">{{.JSON}}</script>{{end}}`)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	err = tmpl.ExecuteTemplate(&b, "page", map[string]any{
		"Attr":  hostile,
		"Spans": []span{{Text: hostile}, {Text: hostile, El: "a", Href: tags["readme"].Href}, {Text: hostile, El: "strong"}},
		"JSON":  map[string]string{"x": hostile},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, bad := range []string{"<img", "</script><", "<b>", `"'&`} {
		if strings.Contains(out, bad) {
			t.Errorf("%q came out as markup:\n%s", bad, out)
		}
	}
	doc, err := html.Parse(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for n := range doc.Descendants() {
		if n.Type == html.TextNode && n.Parent.DataAtom != atom.Script {
			texts = append(texts, n.Data)
		}
		if n.DataAtom == atom.Img {
			t.Error("an <img> element came out of the text")
		}
	}
	if !slices.Contains(texts, hostile) {
		t.Errorf("the text doesn't read back as written: %q", texts)
	}
	var m map[string]string
	start := strings.Index(out, `<script type="application/json">`) + len(`<script type="application/json">`)
	end := strings.Index(out[start:], "</script>")
	if err := json.Unmarshal([]byte(out[start:start+end]), &m); err != nil || m["x"] != hostile {
		t.Errorf("the JSON block doesn't read back: %v %q", err, m["x"])
	}
}

// Every tag's link is fixed here, and the pages exist where the bridge
// looks for them.
func TestTagsAndPages(t *testing.T) {
	for name, s := range tags {
		if s.El == "a" && s.Href == "" || s.El != "a" && (s.Href != "" || s.Download) {
			t.Errorf("tag %s: %+v", name, s)
		}
	}
	names, err := fs.Glob(pagesFS, "pages/*.html")
	want := []string{"pages/" + shared}
	for _, p := range pages {
		want = append(want, "pages/"+p)
	}
	sort.Strings(want)
	if err != nil || !slices.Equal(names, want) {
		t.Errorf("embedded pages %v, %v; want %v", names, err, want)
	}
}

// The install block's tabs, in every language: the computer app first and
// selected, then the image and the script, each tab controlling its own
// panel (the ARIA tabs pattern: only the selected tab in the tab order, the
// other panels hidden); and the computer app tab's buttons are the
// Download section's own links.
func TestInstallTabs(t *testing.T) {
	for _, l := range i18n.Languages() {
		out, err := Render(Landing, l.Code, testOptions)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := html.Parse(bytes.NewReader(out))
		if err != nil {
			t.Fatal(err)
		}
		byID := map[string]*html.Node{}
		var tabs []*html.Node
		installApps := map[string]string{}
		downloads := map[string]string{}
		for n := range doc.Descendants() {
			if n.Type != html.ElementNode {
				continue
			}
			if id := attr(n, "id"); id != "" {
				byID[id] = n
			}
			if attr(n, "role") == "tab" {
				tabs = append(tabs, n)
			}
			if n.DataAtom == atom.A && attr(n, "data-os") != "" {
				installApps[attr(n, "data-os")] = attr(n, "href")
			}
			if n.DataAtom == atom.Div && attr(n, "class") == "dl-card" && attr(n, "data-os") != "" {
				for c := range n.Descendants() {
					if c.DataAtom == atom.A && strings.HasPrefix(attr(c, "class"), "btn") {
						downloads[attr(n, "data-os")] = attr(c, "href")
						break
					}
				}
			}
		}
		var ids []string
		for i, tab := range tabs {
			ids = append(ids, attr(tab, "id"))
			selected := i == 0
			if got := attr(tab, "aria-selected"); got != strconv.FormatBool(selected) {
				t.Errorf("%s: tab %s aria-selected=%q", l.Code, attr(tab, "id"), got)
			}
			if got, want := attr(tab, "tabindex"), map[bool]string{true: "", false: "-1"}[selected]; got != want {
				t.Errorf("%s: tab %s tabindex=%q, want %q", l.Code, attr(tab, "id"), got, want)
			}
			panel := byID[attr(tab, "aria-controls")]
			if panel == nil || attr(panel, "role") != "tabpanel" || attr(panel, "aria-labelledby") != attr(tab, "id") {
				t.Errorf("%s: tab %s controls no panel labelled by it", l.Code, attr(tab, "id"))
				continue
			}
			if on := slices.Contains(strings.Fields(attr(panel, "class")), "on"); on != selected {
				t.Errorf("%s: panel %s shown=%v", l.Code, attr(panel, "id"), on)
			}
		}
		if want := []string{"tab-app", "tab-image", "tab-script"}; !slices.Equal(ids, want) {
			t.Errorf("%s: tabs %v, want %v", l.Code, ids, want)
		}
		for _, sys := range []string{"mac", "windows", "linux"} {
			if installApps[sys] == "" || installApps[sys] != downloads[sys] {
				t.Errorf("%s: the computer app tab's %s button links to %q, the Download section to %q", l.Code, sys, installApps[sys], downloads[sys])
			}
		}
		if len(installApps) != 3 {
			t.Errorf("%s: %d buttons in the computer app tab, want 3", l.Code, len(installApps))
		}
	}
}

// A page links the account page in its own language - the header's sign-in
// and sign-up, and the texts' account and sign-up links - never the
// English address, which would answer with a redirect or the wrong
// language.
func TestAccountLinksInPageLanguage(t *testing.T) {
	for _, page := range []string{Landing, Social, Alternative, Drive, Pi, NotFound} {
		for _, l := range i18n.Languages() {
			out, err := Render(page, l.Code, testOptions)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := html.Parse(bytes.NewReader(out))
			if err != nil {
				t.Fatal(err)
			}
			account := AccountPath(l.Code)
			signIn, signUp := 0, 0
			for n := range doc.Descendants() {
				href := attr(n, "href")
				if n.DataAtom != atom.A || !strings.Contains(href, "account") {
					continue
				}
				switch href {
				case account:
					signIn++
				case account + "?signup=1":
					signUp++
				default:
					t.Errorf("%s in %s: a link to %s", page, l.Code, href)
				}
			}
			if signIn == 0 || signUp == 0 {
				t.Errorf("%s in %s: %d links to %s, %d to sign up", page, l.Code, signIn, account, signUp)
			}
		}
	}
	if got := localHref("/account?signup=1", "es"); got != "/es/account?signup=1" {
		t.Errorf("localHref: %q", got)
	}
	if got := localHref("/accounts", "es"); got != "/accounts" {
		t.Errorf("localHref: %q", got)
	}
}

// Below the width its links need, the header has a Menu (a <details>, no
// script) with the same links, and on a phone the sign-up too.
func TestHeaderMenu(t *testing.T) {
	for _, l := range i18n.Languages() {
		out, err := Render(Social, l.Code, testOptions)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := html.Parse(bytes.NewReader(out))
		if err != nil {
			t.Fatal(err)
		}
		links := func(class string) (hrefs []string, label string) {
			for n := range doc.Descendants() {
				if !strings.Contains(" "+attr(n, "class")+" ", " "+class+" ") {
					continue
				}
				for c := range n.Descendants() {
					if c.DataAtom == atom.A {
						hrefs = append(hrefs, attr(c, "href"))
					}
					if c.DataAtom == atom.Span && attr(c, "class") == "vh" && c.FirstChild != nil {
						label = c.FirstChild.Data
					}
				}
				return hrefs, label
			}
			return nil, ""
		}
		bar, _ := links("navlinks")
		menu, label := links("nav-menu")
		if len(bar) != 6 || !slices.Equal(menu, append(slices.Clone(bar), AccountPath(l.Code)+"?signup=1")) {
			t.Errorf("%s: the bar links %v, the Menu %v", l.Code, bar, menu)
		}
		if label != i18n.T(l.Code, "site.landing.nav.menu", nil) {
			t.Errorf("%s: the Menu is named %q", l.Code, label)
		}
	}
}

// The page for an address the site doesn't have: in every language, kept
// out of search results, titled and headed as such, with the header and
// footer of the other pages and its way back to the home page in its
// language.
func TestNotFoundPage(t *testing.T) {
	for _, l := range i18n.Languages() {
		out, err := Render(NotFound, l.Code, testOptions)
		if err != nil {
			t.Fatal(err)
		}
		s := string(out)
		for _, want := range []string{
			`<meta name="robots" content="noindex">`,
			"<title>" + template.HTMLEscapeString(i18n.T(l.Code, "site.notfound.meta.title", nil)) + "</title>",
			"<h1>" + template.HTMLEscapeString(i18n.T(l.Code, "site.notfound.heading", nil)) + "</h1>",
			`<a class="btn ember" href="` + Path(l.Code) + `">`,
			`href="` + Path(l.Code) + `#faq"`,
			`class="lang-switch"`,
		} {
			if !strings.Contains(s, want) {
				t.Errorf("%s: the page has no %s", l.Code, want)
			}
		}
		if strings.Contains(s, `rel="canonical"`) || strings.Contains(s, `hreflang="x-default"`) {
			t.Errorf("%s: the page names a canonical or alternate address", l.Code)
		}
	}
}
