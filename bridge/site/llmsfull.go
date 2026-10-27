// SPDX-License-Identifier: AGPL-3.0-or-later

package site

import (
	"bytes"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// LLMsFull is /llms-full.txt (llmstxt.org's companion to llms.txt): every
// public page's content in English, in Markdown, one page after another -
// the pages themselves rendered and read back, so it says exactly what
// they say. Only their <main> is taken (not the head, header or footer);
// pictures, scripts, forms, buttons and the wiring drawing are left out,
// and links are absolute. base is the site's origin. The answer only
// changes with the binary, so it is made once per origin.
func LLMsFull(base string) ([]byte, error) {
	if v, ok := llmsFull.Load(base); ok {
		return v.([]byte), nil
	}
	var b strings.Builder
	abs := func(h string) string { return absolute(base, h) }
	b.WriteString("# Off The Cloud\n\n> " + markdown(English(), llmsText.summary, abs) + "\n\n")
	b.WriteString("Every page of the site in English, one after another. The short index is " + base + "/llms.txt.\n")
	for _, p := range localized {
		if !p.public {
			continue
		}
		page, err := Render(p.page, English(), Options{Base: base, Now: time.Now()})
		if err != nil {
			return nil, err
		}
		md, err := pageMarkdown(page, base)
		if err != nil {
			return nil, err
		}
		b.WriteString("\n---\n\nPage: " + base + PagePath(p.page, English()) + "\n\n" + md)
	}
	out := []byte(b.String())
	llmsFull.Store(base, out)
	return out, nil
}

var llmsFull sync.Map // base -> []byte

// absolute is href on the site of base: a path, or a #fragment of the
// landing page, made absolute; anything else as it is.
func absolute(base, href string) string {
	switch {
	case strings.HasPrefix(href, "/"):
		return base + href
	case strings.HasPrefix(href, "#"):
		return base + "/" + href
	}
	return href
}

// pageMarkdown is a rendered page's <main> as Markdown: its h1 a "##"
// heading and each heading below it one level deeper, paragraphs, lists,
// the at-a-glance facts, the comparison tables, and the FAQ's questions as
// mdHeadings over their answers.
func pageMarkdown(page []byte, base string) (string, error) {
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		return "", err
	}
	m := mdWriter{base: base}
	var main *html.Node
	for n := range doc.Descendants() {
		switch {
		case n.DataAtom == atom.Main && main == nil:
			main = n
		case n.DataAtom == atom.Link && attrOf(n, "rel") == "canonical":
			// A link within the page (#faq) is the page's address with it.
			m.self = attrOf(n, "href")
		}
	}
	if main != nil {
		m.block(main)
	}
	return strings.TrimSpace(m.out.String()) + "\n", nil
}

func attrOf(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

// mdWriter writes Markdown, a block at a time.
type mdWriter struct {
	out        strings.Builder
	base, self string
	// written are the paragraphs written so far: one that two of the
	// install block's tabs both show is written once.
	written map[string]bool
}

// skipped are the elements whose content isn't the page's text: what runs
// or styles it, the pictures and the drawing, the contact form and the
// install tabs' buttons (each tab's panel is written), what is hidden from
// screen readers (the decorative emoji), the steps' numbers and the
// downloads' "Your system", which only the visitor's own card shows.
func skipped(n *html.Node) bool {
	switch n.DataAtom {
	case atom.Script, atom.Style, atom.Svg, atom.Img, atom.Picture, atom.Form, atom.Button, atom.Template, atom.Noscript:
		return true
	}
	switch attrOf(n, "class") {
	case "num", "mine-tag":
		return true
	}
	return attrOf(n, "aria-hidden") == "true"
}

// mdHeadings are the Markdown of the page's mdHeadings: its h1 is "##", under
// the file's own title.
var mdHeadings = map[atom.Atom]string{atom.H1: "## ", atom.H2: "### ", atom.H3: "#### ", atom.H4: "##### "}

// mdInlineElems are the elements a sentence runs through.
var mdInlineElems = map[atom.Atom]bool{atom.A: true, atom.Strong: true, atom.B: true, atom.Em: true, atom.I: true,
	atom.Code: true, atom.Span: true, atom.Br: true, atom.Small: true}

// isInline reports whether n is part of a run of text: text, or an inline
// element with no block inside.
func isInline(n *html.Node) bool {
	if n.Type == html.TextNode {
		return true
	}
	if n.Type != html.ElementNode || !mdInlineElems[n.DataAtom] {
		return false
	}
	for c := range n.Descendants() {
		if c.Type == html.ElementNode && !mdInlineElems[c.DataAtom] && !skipped(c) {
			return false
		}
	}
	return true
}

func (m *mdWriter) para(prefix, text string) {
	if text = collapse(text); text != "" && (prefix != "" || !m.written[text]) {
		if m.written == nil {
			m.written = map[string]bool{}
		}
		m.written[text] = true
		m.out.WriteString(prefix + text + "\n\n")
	}
}

// block writes n's content: each run of text and inline elements a
// paragraph, each block element as what it is.
func (m *mdWriter) block(n *html.Node) {
	var run strings.Builder
	flush := func() {
		m.para("", run.String())
		run.Reset()
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && skipped(c) {
			continue
		}
		if isInline(c) {
			m.inline(&run, c)
			continue
		}
		flush()
		if c.Type != html.ElementNode {
			continue
		}
		switch {
		case mdHeadings[c.DataAtom] != "":
			m.para(mdHeadings[c.DataAtom], m.inlineOf(c))
		case c.DataAtom == atom.Summary:
			m.para("#### ", m.inlineOf(c))
		case c.DataAtom == atom.P || c.DataAtom == atom.Figcaption || c.DataAtom == atom.Dd || c.DataAtom == atom.Dt:
			m.para("", m.inlineOf(c))
		case c.DataAtom == atom.Ul || c.DataAtom == atom.Ol:
			m.list(c)
		case c.DataAtom == atom.Dl:
			m.facts(c)
		case c.DataAtom == atom.Table:
			m.table(c)
		case c.DataAtom == atom.Pre:
			m.out.WriteString("```\n" + strings.TrimSpace(textOf(c)) + "\n```\n\n")
		default:
			m.block(c)
		}
	}
	flush()
}

func (m *mdWriter) list(l *html.Node) {
	i := 0
	for li := l.FirstChild; li != nil; li = li.NextSibling {
		if li.DataAtom != atom.Li {
			continue
		}
		i++
		mark := "- "
		if l.DataAtom == atom.Ol {
			mark = strconv.Itoa(i) + ". "
		}
		m.out.WriteString(mark + collapse(m.inlineOf(li)) + "\n")
	}
	m.out.WriteString("\n")
}

// facts writes a <dl> (at a glance) as "- **label**: value" lines.
func (m *mdWriter) facts(dl *html.Node) {
	for n := range dl.Descendants() {
		if n.DataAtom != atom.Dt {
			continue
		}
		value := ""
		for x := n.NextSibling; x != nil; x = x.NextSibling {
			if x.DataAtom == atom.Dd {
				value = collapse(m.inlineOf(x))
				break
			}
		}
		m.out.WriteString("- **" + collapse(m.inlineOf(n)) + "**: " + value + "\n")
	}
	m.out.WriteString("\n")
}

// table writes a comparison table, its first row the header.
func (m *mdWriter) table(t *html.Node) {
	first := true
	for tr := range t.Descendants() {
		if tr.DataAtom != atom.Tr {
			continue
		}
		var cells []string
		for c := tr.FirstChild; c != nil; c = c.NextSibling {
			if c.DataAtom == atom.Th || c.DataAtom == atom.Td {
				cells = append(cells, strings.ReplaceAll(collapse(m.inlineOf(c)), "|", `\|`))
			}
		}
		m.out.WriteString("| " + strings.Join(cells, " | ") + " |\n")
		if first {
			m.out.WriteString("|" + strings.Repeat(" --- |", len(cells)) + "\n")
			first = false
		}
	}
	m.out.WriteString("\n")
}

// inlineOf is n's content as Markdown text.
func (m *mdWriter) inlineOf(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		m.inline(&b, c)
	}
	return b.String()
}

// inline writes n as Markdown text: emphasis, code and links kept, the
// spaces round an emphasis outside its marks.
func (m *mdWriter) inline(b *strings.Builder, n *html.Node) {
	switch {
	case n.Type == html.TextNode:
		b.WriteString(mdEscape(n.Data))
		return
	case n.Type != html.ElementNode || skipped(n):
		return
	}
	wrap := func(open, close, inner string) {
		core := strings.TrimSpace(inner)
		if core == "" {
			b.WriteString(inner)
			return
		}
		i := strings.Index(inner, core)
		b.WriteString(inner[:i] + open + core + close + inner[i+len(core):])
	}
	switch n.DataAtom {
	case atom.Br:
		b.WriteString(" ")
	case atom.Strong, atom.B:
		wrap("**", "**", m.inlineOf(n))
	case atom.Em, atom.I:
		wrap("*", "*", m.inlineOf(n))
	case atom.Code:
		wrap("`", "`", strings.ReplaceAll(textOf(n), "`", "'"))
	case atom.A:
		href := attrOf(n, "href")
		if strings.HasPrefix(href, "#") && m.self != "" {
			href = m.self + href
		}
		wrap("[", "]("+absolute(m.base, href)+")", m.inlineOf(n))
	default:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			m.inline(b, c)
		}
	}
}

// collapse collapses whitespace as a browser shows it.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// textOf is n's text, as it is.
func textOf(n *html.Node) string {
	var b strings.Builder
	for c := range n.Descendants() {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		}
	}
	return b.String()
}
