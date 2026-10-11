// SPDX-License-Identifier: AGPL-3.0-or-later

// Package site renders the bridge's public pages in the visitor's
// language (docs/i18n.md, "Each platform": bridge): the landing page, at
// / in English and /<code>/ in every other language, and the two pages a
// device's own address shows when the bridge answers for it (disabled,
// unavailable).
//
// The pages are html/template files embedded in the binary (pages/), so a
// deploy never serves a stale copy left in the static directory, and their
// text comes from the site.* keys of the catalog (i18n/strings/en/site.*).
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
	Disabled    = "disabled.html"
	Unavailable = "unavailable.html"
)

//go:embed pages/*.html
var pagesFS embed.FS

// Options are what a page needs besides its language.
type Options struct {
	// Base is the site's own origin, "https://off-the.cloud": the landing
	// page's canonical and hreflang links are absolute.
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
func Path(code string) string {
	if code == English() {
		return "/"
	}
	return "/" + code + "/"
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
	if err := t.ExecuteTemplate(&b, page, pageData(l, o)); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// data is what the pages read.
type data struct {
	Lang  string // the language's code
	Tag   string // its BCP 47 tag, for <html lang> and hreflang
	Nonce string
	Year  string
	// Canonical and Alternates are the landing page's own address and its
	// addresses in every carried language (hreflang, plus x-default: /).
	Canonical  string
	Alternates []alternate
	// Switcher links to the page in every carried language, this one
	// marked as current; empty while English is the only one.
	Switcher []choice
}

type alternate struct{ Lang, Href string }

type choice struct {
	Name    string // the language's own name
	Lang    string // its tag
	Href    string
	Current bool
}

func pageData(l i18n.Language, o Options) data {
	d := data{
		Lang:      l.Code,
		Tag:       l.Tag,
		Nonce:     o.Nonce,
		Year:      strconv.Itoa(o.Now.Year()),
		Canonical: o.Base + Path(l.Code),
	}
	langs := i18n.Languages()
	for _, other := range langs {
		d.Alternates = append(d.Alternates, alternate{other.Tag, o.Base + Path(other.Code)})
	}
	d.Alternates = append(d.Alternates, alternate{"x-default", o.Base + Path(English())})
	if len(langs) > 1 {
		for _, other := range langs {
			// English's own link is /en/, which shows English without
			// the Accept-Language redirect of /: a visitor whose browser
			// prefers Spanish could never reach English through /.
			d.Switcher = append(d.Switcher, choice{other.Name, other.Tag, "/" + other.Code + "/", other.Code == l.Code})
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
// all the same comes out as plain text.
var tags = map[string]span{
	"b":        {El: "strong"},
	"em":       {El: "em"},
	"code":     {El: "code"},
	"account":  {El: "a", Href: "/account"},
	"signup":   {El: "a", Href: "/account?signup=1"},
	"privacy":  {El: "a", Href: "/privacy"},
	"install":  {El: "a", Href: "#install"},
	"contact":  {El: "a", Href: "#contact"},
	"mail":     {El: "a", Href: "mailto:info@off-the.cloud"},
	"github":   {El: "a", Href: repo},
	"readme":   {El: "a", Href: repo + "#readme"},
	"release":  {El: "a", Href: repo + "/releases/tag/desktop"},
	"sha":      {El: "a", Href: releases + "image/off-the-cloud-rpi-lite-arm64.img.xz.sha256"},
	"imager":   {El: "a", Href: "https://www.raspberrypi.com/software/"},
	"winarm":   {El: "a", Href: releases + "desktop/otc-sync-windows-arm64.exe", Download: true},
	"linuxarm": {El: "a", Href: releases + "desktop/otc-sync-linux-arm64", Download: true},
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
func funcs(code string) template.FuncMap {
	return template.FuncMap{
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
	}
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
