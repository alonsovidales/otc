// SPDX-License-Identifier: AGPL-3.0-or-later

package scan

import (
	"bytes"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// The bridge's pages and the wizard's page: text nodes, text attributes and
// the text their scripts write. Inline markup (<a>, <strong>, <code>, ...)
// doesn't split a sentence, so a paragraph with links counts once, like the
// one catalog entry it becomes; a block element ends it. Text inside <code>,
// <kbd>, <samp> and <pre> (commands, identifiers) doesn't count.

var inlineTags = map[atom.Atom]bool{
	atom.A: true, atom.Abbr: true, atom.B: true, atom.Bdi: true, atom.Bdo: true, atom.Br: true, atom.Cite: true,
	atom.Code: true, atom.Data: true, atom.Dfn: true, atom.Em: true, atom.I: true, atom.Kbd: true, atom.Mark: true,
	atom.Q: true, atom.S: true, atom.Samp: true, atom.Small: true, atom.Span: true, atom.Strong: true, atom.Sub: true,
	atom.Sup: true, atom.Time: true, atom.U: true, atom.Var: true, atom.Wbr: true, atom.Img: true,
}

var codeTags = map[atom.Atom]bool{atom.Code: true, atom.Kbd: true, atom.Samp: true, atom.Pre: true, atom.Var: true}

// textAttrs are the attributes a person reads.
var textAttrs = boolSet("title", "alt", "placeholder", "aria-label", "aria-description", "aria-placeholder",
	"aria-roledescription", "aria-valuetext", "label", "data-tip", "data-title", "data-tooltip")

var metaText = boolSet("description", "og:title", "og:description", "og:site_name", "twitter:title",
	"twitter:description", "application-name", "apple-mobile-web-app-title")

// lineIndex turns byte offsets into 1-based lines.
type lineIndex []int

func newLineIndex(src []byte) lineIndex {
	idx := lineIndex{0}
	for i, c := range src {
		if c == '\n' {
			idx = append(idx, i+1)
		}
	}
	return idx
}

func (li lineIndex) line(off int) int {
	return sort.Search(len(li), func(i int) bool { return li[i] > off })
}

// htmlScan counts a page, or a fragment of one that a script writes.
type htmlScan struct {
	s      *scanner
	rel    string
	lineOf func(off int) int // the file's line of a byte of src
}

// scan counts src; scripts says whether <script> bodies are lexed (a
// fragment's never are).
func (h *htmlScan) scan(src []byte, scripts bool) {
	z := html.NewTokenizer(bytes.NewReader(src))
	off := 0
	var run strings.Builder
	runStart, runEnd := -1, -1
	codeDepth := 0
	var inScript, inStyle bool
	scriptType := ""
	flush := func() {
		if runStart >= 0 {
			text := run.String()
			if h.s.textRun(text) {
				h.s.add(h.rel, h.lineOf(runStart), h.lineOf(runEnd), "text", text)
			}
		}
		run.Reset()
		runStart, runEnd = -1, -1
	}
	for {
		tt := z.Next()
		raw := z.Raw()
		start := off
		off += len(raw)
		switch tt {
		case html.ErrorToken:
			flush()
			return
		case html.TextToken:
			switch {
			case inScript:
				if scripts {
					h.script(string(raw), h.lineOf(start), scriptType)
				}
				continue
			case inStyle:
				continue
			case codeDepth > 0:
				run.WriteString(" " + hole + " ")
				continue
			}
			text := html.UnescapeString(string(raw))
			if strings.TrimSpace(text) == "" {
				if strings.Contains(text, "\n") {
					flush() // elements on lines of their own: separate pieces
				} else {
					run.WriteString(" ")
				}
				continue
			}
			lead := len(raw) - len(bytes.TrimLeft(raw, " \t\r\n"))
			if runStart < 0 {
				runStart = start + lead
			}
			runEnd = start + len(bytes.TrimRight(raw, " \t\r\n")) - 1
			run.WriteString(text)
		case html.StartTagToken, html.SelfClosingTagToken, html.EndTagToken:
			name, hasAttr := z.TagName()
			a := atom.Lookup(name)
			if tt != html.EndTagToken {
				attrs := map[string]string{}
				for hasAttr {
					var k, v []byte
					k, v, hasAttr = z.TagAttr()
					attrs[string(k)] = string(v)
				}
				h.attrs(a, attrs, raw, start)
				if a == atom.Script && tt == html.StartTagToken {
					inScript = true
					scriptType = strings.ToLower(attrs["type"])
					flush()
					continue
				}
				if a == atom.Style && tt == html.StartTagToken {
					inStyle = true
					flush()
					continue
				}
			} else {
				if a == atom.Script {
					inScript = false
					continue
				}
				if a == atom.Style {
					inStyle = false
					continue
				}
			}
			if codeTags[a] {
				if tt == html.StartTagToken {
					codeDepth++
				} else if tt == html.EndTagToken && codeDepth > 0 {
					codeDepth--
				}
			}
			if !inlineTags[a] {
				flush() // a block element ends the sentence
			}
		default:
			flush() // comments, doctype
		}
	}
}

// attrs counts a tag's text attributes.
func (h *htmlScan) attrs(a atom.Atom, attrs map[string]string, raw []byte, start int) {
	at := func(name string) int {
		if i := bytes.Index(raw, []byte(name+"=")); i >= 0 {
			return h.lineOf(start + i)
		}
		return h.lineOf(start)
	}
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v := attrs[k]; textAttrs[k] && h.s.looseText(v) {
			h.s.add(h.rel, at(k), at(k), "attr:"+k, v)
		}
	}
	if a == atom.Meta && metaText[strings.ToLower(attrs["name"]+attrs["property"])] && h.s.looseText(attrs["content"]) {
		h.s.add(h.rel, at("content"), at("content"), "attr:content", attrs["content"])
	}
	if a == atom.Input {
		switch strings.ToLower(attrs["type"]) {
		case "button", "submit", "reset":
			if h.s.looseText(attrs["value"]) {
				h.s.add(h.rel, at("value"), at("value"), "attr:value", attrs["value"])
			}
		}
	}
}

// tagLike: a literal that holds markup is a fragment of the page.
var tagLike = regexp.MustCompile(`<(?:[a-zA-Z][a-zA-Z0-9-]*(?:\s[^<>]*)?/?|/[a-zA-Z][a-zA-Z0-9-]*\s*)>`)

// hasMarkup reports whether a literal holds an HTML tag ("<b>", "</p>", "<a
// href=…>"), not just angle brackets ("<subdomain>", "a < b").
func hasMarkup(s string) bool {
	for _, m := range tagLike.FindAllString(s, -1) {
		name := strings.TrimLeft(strings.TrimPrefix(m, "<"), "/")
		if i := strings.IndexAny(name, " \t\n/>"); i >= 0 {
			name = name[:i]
		}
		if atom.Lookup([]byte(strings.ToLower(name))) != 0 {
			return true
		}
	}
	return false
}

// The sinks of the pages' scripts.
var (
	jsSinkCalls  = boolSet("alert", "confirm", "prompt", "banner", "msg", "setMsg", "showMsg", "show", "flash", "toast", "say", "status", "setStatus", "fail", "note", "setNote", "notify", "info", "err", "ok", "warn", "message", "setText", "showError", "showStatus")
	jsDenyCalls  = boolSet("querySelector", "querySelectorAll", "getElementById", "getElementsByClassName", "addEventListener", "removeEventListener", "fetch", "getAttribute", "setAttribute", "removeAttribute", "hasAttribute", "createElement", "startsWith", "endsWith", "includes", "indexOf", "lastIndexOf", "split", "replace", "replaceAll", "match", "matchAll", "test", "getItem", "setItem", "removeItem", "parse", "stringify", "encodeURIComponent", "decodeURIComponent", "get", "has", "set", "append", "delete", "URL", "URLSearchParams", "$", "$$", "closest", "matches", "add", "remove", "toggle", "contains", "postMessage", "open", "importKey", "digest", "toLocaleDateString", "toLocaleString", "toLocaleTimeString", "localeCompare", "DateTimeFormat", "NumberFormat", "PluralRules", "Intl", "padStart", "padEnd", "join", "log", "error", "warn", "debug", "assign", "pushState", "replaceState", "dispatchEvent", "Event", "CustomEvent", "Error", "TextEncoder", "TextDecoder", "BigInt", "Number", "String", "charCodeAt", "fromCharCode", "atob", "btoa", "setProperty", "getPropertyValue", "send_json", "api", "post", "call", "invoke")
	jsDenyRecv   = boolSet("console", "localStorage", "sessionStorage", "classList", "dataset", "style", "params", "headers", "JSON", "Math", "crypto", "subtle", "location", "history", "navigator", "document", "window", "Object", "Array", "Reflect", "Promise")
	jsTextProps  = boolSet("textContent", "innerText", "innerHTML", "placeholder", "alt", "title", "label", "ariaLabel", "outerHTML")
	jsFallbackOp = boolSet("||", "??")
)

// script judges the literals of a <script> body starting on firstLine.
func (h *htmlScan) script(src string, firstLine int, typ string) {
	json := strings.Contains(typ, "json")
	if typ != "" && !json && !strings.Contains(typ, "javascript") && typ != "module" {
		return // a template or data block of another kind
	}
	toks := lexJS(src, firstLine)
	type frame struct {
		open, callee, recv string
	}
	var stack []frame
	tok := func(i int) jsTok {
		if i < 0 || i >= len(toks) {
			return jsTok{kind: tPunct}
		}
		return toks[i]
	}
	for i, t := range toks {
		switch {
		case t.kind == tPunct && (t.text == "(" || t.text == "[" || t.text == "{"):
			f := frame{open: t.text}
			if p := tok(i - 1); t.text == "(" && p.kind == tIdent && !jsRegexAfter[p.text] && p.text != "if" && p.text != "while" && p.text != "for" && p.text != "switch" && p.text != "function" && p.text != "catch" {
				f.callee = p.text
				if q := tok(i - 2); q.kind == tPunct && (q.text == "." || q.text == "?.") {
					f.recv = tok(i - 3).text
				}
			}
			stack = append(stack, f)
			continue
		case t.kind == tPunct && (t.text == ")" || t.text == "]" || t.text == "}"):
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			continue
		case t.kind != tString:
			continue
		}
		var f frame
		if len(stack) > 0 {
			f = stack[len(stack)-1]
		}
		prev, next := tok(i-1), tok(i+1)
		if json {
			if !(next.kind == tPunct && next.text == ":") && h.s.looseText(t.text) {
				h.s.add(h.rel, t.line, t.endLine, "json", t.text)
			}
			continue
		}
		// keys, comparisons, case labels
		if prev.kind == tPunct && (prev.text == "===" || prev.text == "!==" || prev.text == "==" || prev.text == "!=") ||
			next.kind == tPunct && (next.text == "===" || next.text == "!==" || next.text == "==" || next.text == "!=") ||
			prev.kind == tIdent && (prev.text == "case" || prev.text == "in") ||
			next.kind == tPunct && next.text == ":" && f.open == "{" && !(prev.kind == tPunct && prev.text == "?") ||
			next.kind == tIdent && next.text == "in" {
			continue
		}
		if hasMarkup(t.text) {
			// a fragment of the page: its text nodes and attributes
			lines := t.lines
			frag := &htmlScan{s: h.s, rel: h.rel, lineOf: func(off int) int {
				switch {
				case off >= 0 && off < len(lines):
					return lines[off]
				case len(lines) > 0 && off >= len(lines):
					return lines[len(lines)-1]
				}
				return t.line
			}}
			frag.scan([]byte(t.text), false)
			continue
		}
		if jsDenyRecv[f.recv] || jsDenyCalls[f.callee] && !jsSinkCalls[f.callee] {
			continue
		}
		if f.open == "(" && jsSinkCalls[f.callee] {
			if h.s.looseText(t.text) {
				h.s.add(h.rel, t.line, t.endLine, "sink:"+f.callee, t.text)
			}
			continue
		}
		if prev.kind == tPunct && prev.text == ":" && f.open == "{" && (tok(i-3).text == "{" || tok(i-3).text == ",") {
			if k := tok(i - 2); k.kind == tIdent || k.kind == tString {
				if textProp.MatchString(k.text) && !notTextProp.MatchString(k.text) && h.s.looseText(t.text) {
					h.s.add(h.rel, t.line, t.endLine, "prop:"+k.text, t.text)
					continue
				}
			}
		}
		if prev.kind == tPunct && (prev.text == "=" || prev.text == "+=") {
			if k := tok(i - 2); k.kind == tIdent && (jsTextProps[k.text] || textProp.MatchString(k.text) && !notTextProp.MatchString(k.text)) {
				if h.s.looseText(t.text) {
					h.s.add(h.rel, t.line, t.endLine, "assign:"+k.text, t.text)
				}
				continue
			}
		}
		if prev.kind == tPunct && jsFallbackOp[prev.text] && h.s.looseText(t.text) {
			h.s.add(h.rel, t.line, t.endLine, "fallback", t.text)
			continue
		}
		if h.s.prose(t.text, strict) {
			h.s.add(h.rel, t.line, t.endLine, "prose", t.text)
		}
	}
}

// scanPage counts a whole HTML file.
func (s *scanner) scanPage(rel string) error {
	src, err := s.source(rel)
	if err != nil {
		return err
	}
	s.scanHTML(rel, src, newLineIndex(src).line)
	return nil
}

// scanHTML counts an HTML document whose bytes map to file lines by lineOf.
func (s *scanner) scanHTML(rel string, src []byte, lineOf func(int) int) {
	h := &htmlScan{s: s, rel: rel, lineOf: lineOf}
	h.scan(src, true)
}

// bridgePages are the pages people read. admin.html is the operator's,
// privacy.html and terms.html stay English (legal text), index.html is a
// stale copy of the web app's shell.
var bridgePages = []string{
	"bridge/static/landing.html",
	"bridge/static/account.html",
	"bridge/static/disabled.html",
	"bridge/static/unavailable.html",
}

func scanPages(s *scanner) error {
	for _, rel := range bridgePages {
		if !s.exists(rel) {
			continue
		}
		if err := s.scanPage(rel); err != nil {
			return err
		}
	}
	return nil
}
