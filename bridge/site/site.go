// SPDX-License-Identifier: AGPL-3.0-or-later

// Package site renders the bridge's public pages in the visitor's
// language (docs/i18n.md, "Each platform": bridge): the landing page, at
// / in English and /<code>/ in every other language, the account page, at
// /account and /<code>/account, the three comparison pages, at
// /private-social-network, /google-photos-alternative and
// /google-drive-alternative (and under /<code>/), and the
// two pages a device's own address shows when the bridge answers for it
// (disabled, unavailable). It also writes what crawlers read about the
// site (crawl.go: robots.txt, sitemap.xml, llms.txt).
//
// The pages are html/template files embedded in the binary (pages/), so a
// deploy never serves a stale copy left in the static directory, and their
// text comes from the site.* keys of the catalog (i18n/strings/en/site.*).
// The landing and comparison pages share their head, header and footer -
// the {{define}} blocks of pages/common.html; head.go builds what the head
// says about the page (Open Graph, JSON-LD) - and their style's first part,
// pages/site.css (the comparison pages add pages/compare.css).
// The rendering contract holds throughout: every text is a plain string
// that html/template escapes for wherever it stands, and a rich key's
// spans become elements through the fixed tags table below, whose links
// always come from code. Nothing here makes template.HTML (or JS, CSS,
// URL) out of translated text.
//
// A page asks for its text by key:
//
//	<h2>{{t "site.landing.faq.title"}}</h2>
//	<p>{{template "spans" (rich "site.landing.faq.free.p2")}}</p>
//	<p>{{t "site.landing.footer.copyright" "year" .Year}}</p>
//
// A page whose script writes text (the account page) gets it in a JSON
// block, each key with its placeholders left for the script to fill in:
//
//	<script type="application/json" id="account-text">{{jstexts
//	  "signIn" "site.account.signin.title"
//	  "hello" (jsform "site.account.home.hello" "name" "user")}}</script>
//
// TestPageKeys checks that every key a page names exists, with the
// arguments it declares, that t never gets a rich key nor rich a plain
// one, and that every site.* key is on some page.
package site

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alonsovidales/otc/i18n"
)

// The pages, by template name.
const (
	Landing     = "landing.html"
	Social      = "social.html"
	Alternative = "alternative.html"
	Drive       = "drive.html"
	Disabled    = "disabled.html"
	Unavailable = "unavailable.html"
	Account     = "account.html"
	NotFound    = "notfound.html"
)

// The comparison pages' addresses after / (English) or /<code>/: one
// ASCII slug in every language. Social compares the private social network
// with Instagram, Facebook and the like, Alternative the photos with
// Google Photos and iCloud, Drive the files with Google Drive and Dropbox.
const (
	SocialSlug      = "private-social-network"
	AlternativeSlug = "google-photos-alternative"
	DriveSlug       = "google-drive-alternative"
)

// localized are the pages with an address in every carried language, by
// the slug that follows / (English) or /<code>/ ("" for the landing page);
// public ones are listed in the sitemap.
var localized = []struct {
	page, slug string
	public     bool
}{
	{Landing, "", true},
	{Account, "account", false},
	{Social, SocialSlug, true},
	{Alternative, AlternativeSlug, true},
	{Drive, DriveSlug, true},
}

// Localized returns the page whose address in a language is
// /<code>/<slug> ("" for the landing page), if there is one.
func Localized(slug string) (page string, ok bool) {
	for _, p := range localized {
		if p.slug == slug {
			return p.page, true
		}
	}
	return "", false
}

// slugOf is where page lives after / or /<code>/; the landing page's ""
// for a page without localized addresses (disabled, unavailable).
func slugOf(page string) string {
	for _, p := range localized {
		if p.page == page {
			return p.slug
		}
	}
	return ""
}

//go:embed pages/*.html pages/site.css pages/compare.css
var pagesFS embed.FS

// Options are what a page needs besides its language.
type Options struct {
	// Base is the site's own origin, "https://off-the.cloud": the pages'
	// canonical, hreflang and Open Graph links are absolute.
	Base string
	// Nonce is this response's Content-Security-Policy nonce, carried by
	// the page's inline <style> and <script> elements.
	Nonce string
	// Now is when the page is shown (the footer's year).
	Now time.Time
}

// Language returns the language of code when this build carries it:
// code must be exactly a carried code ("es", not "ES" or "es-ES").
func Language(code string) (i18n.Language, bool) {
	if i18n.Normalize(code) != code {
		return i18n.Language{}, false
	}
	for _, l := range i18n.Languages() {
		if l.Code == code {
			return l, true
		}
	}
	return i18n.Language{}, false
}

// English is the code of the source language, first among the carried
// ones: it lives at / itself.
func English() string { return i18n.Languages()[0].Code }

// Path is where the landing page lives in a language: / for English,
// /<code>/ for the others.
func Path(code string) string { return PagePath(Landing, code) }

// AccountPath is where the account page lives in a language: /account for
// English, /<code>/account for the others.
func AccountPath(code string) string { return PagePath(Account, code) }

// PagePath is where a localized page lives in a language: /<slug> for
// English, /<code>/<slug> for the others.
func PagePath(page, code string) string {
	if code == English() {
		return "/" + slugOf(page)
	}
	return "/" + code + "/" + slugOf(page)
}

// switcherPath is where the language switcher links to page in a
// language: /<code>/<slug>, /en/... for English too, which shows English
// without the Accept-Language redirect of its own address - a visitor
// whose browser prefers Spanish could never reach English that way.
func switcherPath(page, code string) string { return "/" + code + "/" + slugOf(page) }

// alternates are page's addresses in every carried language, absolute,
// for hreflang: each language's tag, then x-default, the English one. A
// tag with a region (pt-PT) also names its page for the language alone
// (pt), unless a carried language has that tag: the Portuguese page is
// the one for every Portuguese reader, in Brazil too.
func alternates(page, base string) []alternate {
	langs := i18n.Languages()
	tags := map[string]bool{}
	for _, l := range langs {
		tags[l.Tag] = true
	}
	var out []alternate
	for _, l := range langs {
		href := base + PagePath(page, l.Code)
		out = append(out, alternate{l.Tag, href})
		if lang, _, region := strings.Cut(l.Tag, "-"); region && !tags[lang] {
			out = append(out, alternate{lang, href})
		}
	}
	return append(out, alternate{"x-default", base + PagePath(page, English())})
}

// Render returns page in lang. lang is a code this build carries; anything
// else renders English.
func Render(page, lang string, o Options) ([]byte, error) {
	l, ok := Language(lang)
	if !ok {
		l, _ = Language(English())
	}
	t, err := templates(l.Code)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := t.ExecuteTemplate(&b, page, pageData(page, l, o)); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// data is what the pages read.
type data struct {
	Page  string // the page's template name
	Lang  string // the language's code
	Tag   string // its BCP 47 tag, for <html lang> and hreflang
	Nonce string
	Year  string
	// Base is Options.Base, for the absolute addresses of the head.
	Base string
	// Home is the landing page in this language (/, /<code>/), where the
	// other pages' links home go.
	Home string
	// Account is the account page in this language (/account,
	// /<code>/account), where the header's sign-in and sign-up go.
	Account string
	// Sections is what the shared header's and footer's links to the
	// landing page's sections (#download, #faq, #contact...) start with:
	// "" on the landing page itself, Home on the others.
	Sections string
	// Compare are the comparison pages in this language
	// (/private-social-network, /<code>/private-social-network...), where
	// the landing page's and the footer's links to them go.
	Compare comparePages
	// Canonical and Alternates are the page's own address and its
	// addresses in every carried language (hreflang, plus x-default: the
	// English one): the landing page's, the account page's or the
	// comparison page's.
	Canonical  string
	Alternates []alternate
	// Switcher links to the page in every carried language, this one
	// marked as current; empty while English is the only one.
	Switcher []choice
}

type alternate struct{ Lang, Href string }

// comparePages are the comparison pages' addresses in one language.
type comparePages struct{ Social, Photos, Drive string }

type choice struct {
	Name    string // the language's own name
	Lang    string // its tag
	Href    string
	Current bool
}

func pageData(page string, l i18n.Language, o Options) data {
	d := data{
		Page:    page,
		Lang:    l.Code,
		Tag:     l.Tag,
		Nonce:   o.Nonce,
		Year:    strconv.Itoa(o.Now.Year()),
		Base:    o.Base,
		Home:    Path(l.Code),
		Account: AccountPath(l.Code),
		Compare: comparePages{
			Social: PagePath(Social, l.Code),
			Photos: PagePath(Alternative, l.Code),
			Drive:  PagePath(Drive, l.Code),
		},
		Canonical:  o.Base + PagePath(page, l.Code),
		Alternates: alternates(page, o.Base),
	}
	if page != Landing {
		d.Sections = d.Home
	}
	if langs := i18n.Languages(); len(langs) > 1 {
		for _, other := range langs {
			d.Switcher = append(d.Switcher, choice{other.Name, other.Tag, switcherPath(page, other.Code), other.Code == l.Code})
		}
	}
	return d
}

// sets are the parsed pages, one set per language: each set's functions
// render in its language.
var sets sync.Map // code -> *set

type set struct {
	once sync.Once
	t    *template.Template
	err  error
}

// templates returns the pages for a carried language code, parsed the
// first time.
func templates(code string) (*template.Template, error) {
	v, _ := sets.LoadOrStore(code, new(set))
	s := v.(*set)
	s.once.Do(func() { s.t, s.err = parsePages(code) })
	return s.t, s.err
}

func parsePages(code string) (*template.Template, error) {
	t, err := template.New("spans").Funcs(funcs(code)).Parse(spansTemplate)
	if err != nil {
		return nil, err
	}
	// The public pages' shared style: a plain CSS file, so it reads as one
	// (and the text scanner, i18n/scan, doesn't take it for page text).
	css, err := pagesFS.ReadFile("pages/site.css")
	if err != nil {
		return nil, err
	}
	if _, err := t.New("site-style").Parse(string(css)); err != nil {
		return nil, err
	}
	// What the comparison pages add to it.
	compare, err := pagesFS.ReadFile("pages/compare.css")
	if err != nil {
		return nil, err
	}
	if _, err := t.New("compare-style").Parse(string(compare)); err != nil {
		return nil, err
	}
	return t.ParseFS(pagesFS, "pages/*.html")
}

// spansTemplate writes a rich text: each span as the element its tag
// stands for, its text escaped by html/template like any other.
const spansTemplate = `{{define "spans"}}{{range .}}` +
	`{{if eq .El "strong"}}<strong>{{.Text}}</strong>` +
	`{{else if eq .El "em"}}<em>{{.Text}}</em>` +
	`{{else if eq .El "code"}}<code>{{.Text}}</code>` +
	`{{else if eq .El "a"}}<a href="{{.Href}}"{{if .Download}} download{{end}}>{{.Text}}</a>` +
	`{{else}}{{.Text}}{{end}}{{end}}{{end}}`

// span is one span of a rich text, as the page writes it.
type span struct {
	Text     string
	El       string // "" (plain text), "strong", "em", "code" or "a"
	Href     string // for "a": always from tags, never from the catalog
	Download bool   // for "a": a download link
}

// The download links of the landing page.
const (
	releases = "https://github.com/alonsovidales/otc/releases/download/"
	repo     = "https://github.com/alonsovidales/otc"
)

// tags are what the tags of the site.* keys become. A translation has the
// English's tags (the checker refuses anything else), and TestPageKeys
// checks that every tag an English site.* key uses is here; a tag missing
// all the same comes out as plain text. The links to the account page are
// its English address: a page in another language links its own
// (localHref).
var tags = map[string]span{
	"b":        {El: "strong"},
	"em":       {El: "em"},
	"code":     {El: "code"},
	"account":  {El: "a", Href: "/account"},
	"signup":   {El: "a", Href: "/account?signup=1"},
	"privacy":  {El: "a", Href: "/privacy"},
	"install":  {El: "a", Href: "#install"},
	"download": {El: "a", Href: "#download"},
	"contact":  {El: "a", Href: "#contact"},
	"demo":     {El: "a", Href: demo},
	"mail":     {El: "a", Href: "mailto:" + contactEmail},
	"github":   {El: "a", Href: repo},
	"readme":   {El: "a", Href: repo + "#readme"},
	"release":  {El: "a", Href: desktopRelease},
	"sha":      {El: "a", Href: releases + "image/off-the-cloud-rpi-lite-arm64.img.xz.sha256"},
	"imager":   {El: "a", Href: "https://www.raspberrypi.com/software/"},
	"winarm":   {El: "a", Href: releases + "desktop/otc-sync-windows-arm64.exe", Download: true},
	"linuxarm": {El: "a", Href: releases + "desktop/otc-sync-linux-arm64", Download: true},
}

// localHref is a tag's link on a page in code: the account page's English
// address (/account, /account?signup=1) becomes the page's own language's
// (/es/account?signup=1), so following it never lands on the English page
// or goes through a redirect.
func localHref(href, code string) string {
	if rest, ok := strings.CutPrefix(href, AccountPath(English())); ok && (rest == "" || rest[0] == '?') {
		return AccountPath(code) + rest
	}
	return href
}

// line is one line of a text drawn in the SVG, which doesn't wrap: Y is
// its baseline.
type line struct {
	Y    int
	Text string
}

// funcs are the template functions of one language's pages.
//
//	t "key" ["arg" value]...     the key's text (a plain key)
//	rich "key" ["arg" value]...  its spans, for {{template "spans" ...}}
//	lines "key" y dy             its lines (the text may hold line breaks), y, y+dy, ...
//	texts "name" "key"...        name -> text, for a page script's JSON block
//	series "key" "arg" from to   the text with arg = from...to, for a script counting down
//	jstexts "name" key...        name -> text with its placeholders, for a script that fills them in (below)
//	jsform "key" ["arg" "type"]... one key's text for jstexts, its arguments named with their types
//	head . title description     the shared head's data (head.go), for {{template "head" ...}}
func funcs(code string) template.FuncMap {
	return template.FuncMap{
		"head": newHead,
		"t": func(key string, args ...any) (string, error) {
			m, err := argMap(args)
			if err != nil {
				return "", fmt.Errorf("%s: %w", key, err)
			}
			return i18n.T(code, key, m), nil
		},
		"rich": func(key string, args ...any) ([]span, error) {
			m, err := argMap(args)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			var out []span
			for _, p := range (i18n.Rich{Key: key, Args: m}).Parts(code) {
				s := span{Text: p.Text}
				if p.Tag != "" {
					if el, ok := tags[p.Tag]; ok {
						s = el
						s.Text = p.Text
						s.Href = localHref(el.Href, code)
					}
				}
				out = append(out, s)
			}
			return out, nil
		},
		"lines": func(key string, y, dy int) []line {
			var out []line
			for i, l := range strings.Split(i18n.T(code, key, nil), "\n") {
				out = append(out, line{y + i*dy, l})
			}
			return out
		},
		"texts": func(pairs ...string) (map[string]string, error) {
			if len(pairs)%2 != 0 {
				return nil, fmt.Errorf("texts: %d arguments, want name/key pairs", len(pairs))
			}
			m := map[string]string{}
			for i := 0; i < len(pairs); i += 2 {
				m[pairs[i]] = i18n.T(code, pairs[i+1], nil)
			}
			return m, nil
		},
		"series": func(key, arg string, from, to int) []string {
			var out []string
			for n := from; n <= to; n++ {
				out = append(out, i18n.T(code, key, map[string]any{arg: n}))
			}
			return out
		},
		"jstexts": func(pairs ...any) (map[string]any, error) {
			if len(pairs)%2 != 0 {
				return nil, fmt.Errorf("jstexts: %d arguments, want name/key pairs", len(pairs))
			}
			m := map[string]any{}
			for i := 0; i < len(pairs); i += 2 {
				name, ok := pairs[i].(string)
				if !ok {
					return nil, fmt.Errorf("jstexts: argument %d is not a name", i)
				}
				switch v := pairs[i+1].(type) {
				case string: // a key without arguments
					f, err := scriptForm(code, v, nil)
					if err != nil {
						return nil, err
					}
					m[name] = f.text
				case scriptText:
					m[name] = v.text
				default:
					return nil, fmt.Errorf("jstexts: %s is neither a key nor a jsform", name)
				}
			}
			return m, nil
		},
		"jsform": func(key string, args ...string) (scriptText, error) {
			return scriptForm(code, key, args)
		},
	}
}

// scriptText is one key's text as a page's script gets it (jstexts): a
// string for a plain key; for a rich one its spans, [tag, text] pairs with
// "" for plain text, so the script never looks for markup in the text.
// The placeholders are left in, {name}, for the script to fill in as
// plain text; a user argument's comes between its isolation marks (FSI
// {name} PDI), as the script must keep it.
type scriptText struct{ text any }

// Stand-ins for the arguments while a key renders: private-use
// characters, which the checker refuses in the catalog's text, around a
// text argument's name, and for a number one no text holds.
const (
	standInOpen        = "\ue000"
	standInClose       = "\ue001"
	standInInt   int64 = 987_654_321_000
)

// scriptForm renders key in code with each argument left as its
// placeholder. args are name/type pairs: "text" or "user" (a string), or
// "int" (a number, which the script writes in the page's language). The
// catalog renders the key with a stand-in for each argument, and each
// stand-in, as rendered, becomes the placeholder again - so a translation
// missing (English in its place, with English numbers) or an argument
// isolated works the same. A key that doesn't render back with every
// placeholder is an error, as is one whose arguments aren't exactly these.
func scriptForm(code, key string, args []string) (scriptText, error) {
	if len(args)%2 != 0 {
		return scriptText{}, fmt.Errorf("%s: %d arguments, want name/type pairs", key, len(args))
	}
	vals := map[string]any{}
	var swap []string
	var names []string
	for i := 0; i < len(args); i += 2 {
		name, typ := args[i], args[i+1]
		ph := "{" + name + "}"
		switch typ {
		case "text", "user":
			s := standInOpen + name + standInClose
			vals[name] = s
			swap = append(swap, s, ph)
		case "int":
			n := standInInt + int64(i/2)
			vals[name] = n
			swap = append(swap, i18n.FormatInt(code, n), ph, i18n.FormatInt(English(), n), ph)
		default:
			return scriptText{}, fmt.Errorf("%s: argument %s is a %q; a script fills in text, user and int arguments", key, name, typ)
		}
		names = append(names, ph)
	}
	if err := (i18n.Msg{Key: key, Args: vals}).Check(); err != nil {
		return scriptText{}, fmt.Errorf("%s: %w", key, err)
	}
	back := strings.NewReplacer(swap...)
	var spans [][2]string
	var all strings.Builder
	rich := false
	for _, p := range (i18n.Rich{Key: key, Args: vals}).Parts(code) {
		text := back.Replace(p.Text)
		spans = append(spans, [2]string{p.Tag, text})
		all.WriteString(text)
		rich = rich || p.Tag != ""
	}
	whole := all.String()
	for _, ph := range names {
		if !strings.Contains(whole, ph) {
			return scriptText{}, fmt.Errorf("%s: %s didn't render back", key, ph)
		}
	}
	if strings.ContainsAny(whole, standInOpen+standInClose) {
		return scriptText{}, fmt.Errorf("%s: a stand-in left in the text", key)
	}
	if !rich {
		return scriptText{whole}, nil
	}
	return scriptText{spans}, nil
}

// argMap turns a page's "name", value, ... into a key's arguments.
func argMap(args []any) (map[string]any, error) {
	if len(args) == 0 {
		return nil, nil
	}
	if len(args)%2 != 0 {
		return nil, fmt.Errorf("%d arguments, want name/value pairs", len(args))
	}
	m := make(map[string]any, len(args)/2)
	for i := 0; i < len(args); i += 2 {
		name, ok := args[i].(string)
		if !ok {
			return nil, fmt.Errorf("argument %d is not a name", i)
		}
		m[name] = args[i+1]
	}
	return m, nil
}
