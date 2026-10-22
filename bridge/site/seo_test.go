// SPDX-License-Identifier: AGPL-3.0-or-later

package site

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/i18n"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// renderedHead is what a rendered page's head says - its title, meta tags
// by name or property, canonical, hreflang links and JSON-LD block as
// written - and the addresses its links and images go to.
type renderedHead struct {
	title, canonical string
	meta             map[string][]string
	hreflang         map[string]string
	ld               string
	hrefs, imgs      []string
}

func readHead(t *testing.T, page []byte) renderedHead {
	t.Helper()
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		t.Fatal(err)
	}
	h := renderedHead{meta: map[string][]string{}, hreflang: map[string]string{}}
	lds := 0
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode {
			continue
		}
		switch n.DataAtom {
		case atom.Title:
			if n.Parent.DataAtom == atom.Head { // not an SVG's
				h.title = n.FirstChild.Data
			}
		case atom.Meta:
			name := attr(n, "name") + attr(n, "property")
			h.meta[name] = append(h.meta[name], attr(n, "content"))
		case atom.Link:
			switch attr(n, "rel") {
			case "canonical":
				h.canonical = attr(n, "href")
			case "alternate":
				h.hreflang[attr(n, "hreflang")] = attr(n, "href")
			}
		case atom.Script:
			if attr(n, "type") == "application/ld+json" {
				lds++
				h.ld = n.FirstChild.Data
			}
		case atom.A:
			h.hrefs = append(h.hrefs, attr(n, "href"))
		case atom.Img:
			h.imgs = append(h.imgs, attr(n, "src"))
		}
	}
	if lds != 1 {
		t.Fatalf("%d JSON-LD blocks, want 1", lds)
	}
	return h
}

// The public pages' JSON-LD parses in every language, names every node by
// the same @id everywhere, describes the page as its head does, and says
// nothing the pages don't (no ratings, reviews, counts or versions); its
// links are the page's own; and the Open Graph tags agree with the page.
// The comparison page defines the Organization, the WebSite and the
// software exactly as the landing page does in its language, besides its
// own WebPage.
func TestStructuredData(t *testing.T) {
	base := testOptions.Base
	wantIDs := []string{"organization", "website", "software", "app-ios", "app-android", "app-mac", "app-desktop", "source"}
	shared := []string{"organization", "website", "software"}
	landingNodes := map[string]map[string]map[string]any{} // language -> @id -> node
	forbidden := regexp.MustCompile(`(?i)"(aggregateRating|review|reviews|ratingValue|ratingCount|softwareVersion|version|downloadCount|interactionStatistic|dateModified|datePublished)"`)
	var tags []string
	for _, l := range i18n.Languages() {
		tags = append(tags, l.Tag)
	}
	defined := map[string]bool{}
	var refs []string
	for _, page := range publicPages {
		for _, l := range i18n.Languages() {
			out, err := Render(page, l.Code, testOptions)
			if err != nil {
				t.Fatal(err)
			}
			h := readHead(t, out)
			what := page + " in " + l.Code
			canonical := base + PagePath(page, l.Code)
			if strings.ContainsAny(h.ld, "<>") || strings.Contains(string(out), "</script><script type=\"application/ld+json\">") {
				t.Errorf("%s: the JSON-LD isn't escaped: %q", what, h.ld)
			}
			if forbidden.MatchString(h.ld) {
				t.Errorf("%s: the JSON-LD says %s", what, forbidden.FindString(h.ld))
			}
			var ld struct {
				Context string           `json:"@context"`
				Graph   []map[string]any `json:"@graph"`
			}
			if err := json.Unmarshal([]byte(h.ld), &ld); err != nil {
				t.Fatalf("%s: %v\n%s", what, err, h.ld)
			}
			if ld.Context != "https://schema.org" {
				t.Errorf("%s: @context %q", what, ld.Context)
			}
			byID := map[string]map[string]any{}
			var ids []string
			for _, n := range ld.Graph {
				id, _ := n["@id"].(string)
				byID[id] = n
				defined[id] = true
				if id != canonical+"#webpage" {
					ids = append(ids, strings.TrimPrefix(id, base+"/#"))
				}
			}
			collectRefs(ld.Graph, &refs)
			want := wantIDs
			if page != Landing {
				want = shared
			}
			if !slices.Equal(ids, want) {
				t.Errorf("%s: @ids %v, want %v", what, ids, want)
			}
			if page == Landing {
				landingNodes[l.Code] = byID
			} else {
				for _, name := range shared {
					id := base + "/#" + name
					if got, want := byID[id], landingNodes[l.Code][id]; want == nil || !reflect.DeepEqual(got, want) {
						t.Errorf("%s: %s %v,\nthe landing page's %v", what, name, got, want)
					}
				}
			}
			wp := byID[canonical+"#webpage"]
			if wp == nil || wp["@type"] != "WebPage" || wp["url"] != canonical || wp["name"] != h.title ||
				wp["description"] != h.meta["description"][0] || wp["inLanguage"] != l.Tag {
				t.Errorf("%s: WebPage %v, want %s titled %q", what, wp, canonical, h.title)
			}
			if page == Landing {
				if ws := byID[base+"/#website"]; ws["url"] != base+"/" || !slices.Equal(anyStrings(ws["inLanguage"]), tags) {
					t.Errorf("%s: WebSite %v", what, ws)
				}
				if sw := byID[base+"/#software"]; sw["description"] != h.meta["description"][0] {
					t.Errorf("%s: the software's description isn't the page's", what)
				}
				// Every address it gives is one the page links to or shows.
				for _, name := range []string{"app-ios", "app-android", "app-mac", "app-desktop"} {
					if u, _ := byID[base+"/#"+name]["url"].(string); !slices.Contains(h.hrefs, u) {
						t.Errorf("%s: %s's address %q isn't on the page", what, name, u)
					}
				}
				for _, s := range anyStrings(byID[base+"/#software"]["screenshot"]) {
					if !slices.Contains(h.imgs, strings.TrimPrefix(s, base)) {
						t.Errorf("%s: screenshot %s isn't on the page", what, s)
					}
				}
			}

			// Open Graph and Twitter: the page's own title, description,
			// address and language.
			checks := map[string]string{
				"og:url":         canonical,
				"og:title":       h.title,
				"og:description": h.meta["description"][0],
				"og:type":        "website",
				"og:site_name":   "Off The Cloud",
				"og:image":       base + ogImage,
				"og:locale":      ogLocales[l.Code],
				"twitter:card":   "summary_large_image",
				"theme-color":    "#10231f",
			}
			for k, v := range checks {
				if got := h.meta[k]; len(got) != 1 || got[0] != v {
					t.Errorf("%s: %s %q, want %q", what, k, got, v)
				}
			}
			if h.canonical != canonical {
				t.Errorf("%s: canonical %q, want %q", what, h.canonical, canonical)
			}
			if n := len(h.meta["og:locale:alternate"]); n != len(tags)-1 || slices.Contains(h.meta["og:locale:alternate"], ogLocales[l.Code]) {
				t.Errorf("%s: og:locale:alternate %v", what, h.meta["og:locale:alternate"])
			}
		}
	}
	for _, r := range refs {
		if !defined[r] {
			t.Errorf("a reference to %s, which no page defines", r)
		}
	}
}

// collectRefs gathers every {"@id": ...} reference (a node with nothing
// but its @id) in v.
func collectRefs(v any, refs *[]string) {
	switch v := v.(type) {
	case []map[string]any:
		for _, n := range v {
			collectRefs(n, refs)
		}
	case []any:
		for _, n := range v {
			collectRefs(n, refs)
		}
	case map[string]any:
		if id, ok := v["@id"].(string); ok && len(v) == 1 {
			*refs = append(*refs, id)
			return
		}
		for _, n := range v {
			collectRefs(n, refs)
		}
	}
}

func anyStrings(v any) []string {
	var out []string
	if list, ok := v.([]any); ok {
		for _, s := range list {
			if s, ok := s.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// The pictures the head describes are there, at the sizes it gives.
func TestHeadPictures(t *testing.T) {
	for file, size := range map[string][2]int{
		ogImage: {ogImageWidth, ogImageHeight},
		logo:    {logoSize, logoSize},
	} {
		f, err := os.Open(filepath.Join("..", "static", filepath.FromSlash(file)))
		if err != nil {
			t.Fatal(err)
		}
		cfg, _, err := image.DecodeConfig(f)
		f.Close()
		if err != nil || cfg.Width != size[0] || cfg.Height != size[1] {
			t.Errorf("%s: %dx%d %v, want %dx%d", file, cfg.Width, cfg.Height, err, size[0], size[1])
		}
	}
	for _, s := range screenshots {
		if _, err := os.Stat(filepath.Join("..", "static", filepath.FromSlash(strings.Split(s, "?")[0]))); err != nil {
			t.Error(err)
		}
	}
}

// Every carried language has its Open Graph locale: its own language,
// with a territory.
func TestOGLocales(t *testing.T) {
	shape := regexp.MustCompile(`^[a-z]{2,3}_[A-Z]{2}$`)
	for _, l := range i18n.Languages() {
		loc := ogLocales[l.Code]
		if !shape.MatchString(loc) || !strings.HasPrefix(loc, l.Code+"_") {
			t.Errorf("%s: Open Graph locale %q", l.Code, loc)
		}
	}
}

// The sitemap is XML listing every public page in every carried language,
// each with all of its language versions and x-default - the same ones
// the page's own head names - reciprocally; then privacy and terms; never
// an /en/ address or the account page.
func TestSitemap(t *testing.T) {
	base := testOptions.Base
	out, err := Sitemap(base)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out, []byte(xml.Header)) {
		t.Error("no XML declaration")
	}
	var set struct {
		XMLName xml.Name `xml:"http://www.sitemaps.org/schemas/sitemap/0.9 urlset"`
		URLs    []struct {
			Loc   string `xml:"loc"`
			Links []struct {
				Rel      string `xml:"rel,attr"`
				Hreflang string `xml:"hreflang,attr"`
				Href     string `xml:"href,attr"`
			} `xml:"http://www.w3.org/1999/xhtml link"`
		} `xml:"url"`
	}
	if err := xml.Unmarshal(out, &set); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	alts := map[string]map[string]string{} // loc -> hreflang -> href
	var locs []string
	for _, u := range set.URLs {
		locs = append(locs, u.Loc)
		if strings.Contains(u.Loc, "/en/") || strings.Contains(u.Loc, "account") {
			t.Errorf("%s listed", u.Loc)
		}
		m := map[string]string{}
		for _, l := range u.Links {
			if l.Rel != "alternate" {
				t.Errorf("%s: rel %q", u.Loc, l.Rel)
			}
			if _, dup := m[l.Hreflang]; dup {
				t.Errorf("%s: hreflang %s twice", u.Loc, l.Hreflang)
			}
			m[l.Hreflang] = l.Href
		}
		alts[u.Loc] = m
	}
	var want []string
	for _, page := range publicPages {
		for _, l := range i18n.Languages() {
			want = append(want, base+PagePath(page, l.Code))
		}
	}
	want = append(want, base+"/privacy", base+"/terms")
	if !slices.Equal(locs, want) {
		t.Errorf("listed %v\nwant %v", locs, want)
	}
	for _, page := range publicPages {
		for _, l := range i18n.Languages() {
			loc := base + PagePath(page, l.Code)
			m := alts[loc]
			if len(m) != len(i18n.Languages())+1 || m["x-default"] != base+PagePath(page, English()) || m[l.Tag] != loc {
				t.Errorf("%s: alternates %v", loc, m)
			}
			// Reciprocal: every version it names names it back.
			for tag, href := range m {
				if back := alts[href]; back == nil || back[l.Tag] != loc {
					t.Errorf("%s names %s (%s), which doesn't name it back", loc, href, tag)
				}
			}
			// The same as the page's own head.
			page, err := Render(page, l.Code, testOptions)
			if err != nil {
				t.Fatal(err)
			}
			if h := readHead(t, page); !mapsEqual(h.hreflang, m) {
				t.Errorf("%s: the page's hreflang %v, the sitemap's %v", loc, h.hreflang, m)
			}
		}
	}
	for _, path := range []string{"/privacy", "/terms"} {
		if len(alts[base+path]) != 0 {
			t.Errorf("%s has language versions", path)
		}
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestRobots(t *testing.T) {
	want := "User-agent: *\nDisallow: /api/\n\nSitemap: https://off-the.cloud/sitemap.xml\n"
	if got := string(Robots("https://off-the.cloud")); got != want {
		t.Errorf("robots.txt:\n%s\nwant\n%s", got, want)
	}
}

// llms.txt says what the English page says, from the catalog: its summary,
// its lead, the links, and every question of the FAQ.
func TestLLMs(t *testing.T) {
	base := testOptions.Base
	out := string(LLMs(base))
	en := English()
	if !strings.HasPrefix(out, "# Off The Cloud\n\n> "+mdEscape(i18n.T(en, llmsText.summary, nil))+"\n\n"+mdEscape(i18n.T(en, llmsText.lead, nil))+"\n\n## At a glance\n\n") {
		t.Errorf("llms.txt starts:\n%s", out[:min(len(out), 600)])
	}
	for _, want := range []string{
		"- [Website](" + base + "/): ",
		"[Español](" + base + "/es/)",
		"- [Live demo](" + demo + "): " + mdEscape(i18n.T(en, "site.landing.hero.demo_note", nil)),
		"](" + appStore + ")",
		"](" + googlePlay + ")",
		"](" + desktopRelease + ")",
		"- [Source code](" + repo + ")\n",
		"](" + factSheet + ")",
		"- [" + mdEscape(i18n.T(en, "site.social.link", nil)) + "](" + base + "/" + SocialSlug + "): " + mdEscape(i18n.T(en, "site.social.meta.description", nil)) + "\n",
		"- [" + mdEscape(i18n.T(en, "site.alt.link", nil)) + "](" + base + "/" + AlternativeSlug + "): " + mdEscape(i18n.T(en, "site.alt.meta.description", nil)) + "\n",
		"- [" + mdEscape(i18n.T(en, "site.drive.link", nil)) + "](" + base + "/" + DriveSlug + "): " + mdEscape(i18n.T(en, "site.drive.meta.description", nil)) + "\n",
		"](" + base + "/privacy)",
		"](" + base + "/terms)",
		"\n## Questions\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("llms.txt has no %q", want)
		}
	}
	// The at-a-glance facts, as label: value lines, between the lead and
	// the links.
	var glance strings.Builder
	glance.WriteString("\n\n## At a glance\n\n")
	for _, row := range llmsText.glance {
		label, value := glanceKeys(row)
		glance.WriteString("- " + mdEscape(i18n.T(en, label, nil)) + ": " + mdEscape(i18n.T(en, value, nil)) + "\n")
	}
	glance.WriteString("\n## Links\n\n")
	if !strings.Contains(out, glance.String()) {
		t.Errorf("llms.txt has no %q", glance.String())
	}
	for _, key := range llmsText.questions {
		if line := "- [" + mdEscape(i18n.T(en, key, nil)) + "](" + base + "/#faq)\n"; !strings.Contains(out, line) {
			t.Errorf("llms.txt has no %q", line)
		}
	}
	if strings.Contains(out, "site.") || strings.Contains(out, "<") {
		t.Errorf("a key or markup in llms.txt:\n%s", out)
	}
	// A rich text's links come out as Markdown links, absolute.
	abs := func(h string) string {
		if strings.HasPrefix(h, "/") {
			return base + h
		}
		return h
	}
	if got := markdown(en, "site.landing.faq.open_source.a", abs); !strings.Contains(got, "[GitHub]("+repo+")") {
		t.Errorf("markdown: %q", got)
	}
}

// The keys llms.txt reads exist, as plain texts without arguments, and its
// at-a-glance facts and questions are the landing page's, in the page's
// order.
func TestLLMsKeys(t *testing.T) {
	entries := siteEntries(t)
	keys := append([]string{llmsText.summary, llmsText.lead}, llmsText.questions...)
	for _, l := range llmsText.links {
		if l.key != "" {
			keys = append(keys, l.key)
		}
		if l.nameKey != "" {
			keys = append(keys, l.nameKey)
		}
		if (l.name == "") == (l.nameKey == "") {
			t.Errorf("link to %s: a name or a name key, not both or neither", l.href)
		}
	}
	for _, row := range llmsText.glance {
		label, value := glanceKeys(row)
		keys = append(keys, label, value)
	}
	for _, k := range keys {
		e, ok := entries[k]
		if !ok || len(e.Args) > 0 {
			t.Errorf("%s: no such key, or it takes arguments", k)
		}
	}
	var faq, glance []string
	question := regexp.MustCompile(`^site\.landing\.faq\.[a-z_]+\.q$`)
	fact := regexp.MustCompile(`^site\.landing\.glance\.([a-z_]+)\.label$`)
	for _, u := range pageUses(t) {
		if question.MatchString(u.key) && !slices.Contains(faq, u.key) {
			faq = append(faq, u.key)
		}
		if m := fact.FindStringSubmatch(u.key); m != nil && !slices.Contains(glance, m[1]) {
			glance = append(glance, m[1])
		}
	}
	if !slices.Equal(faq, llmsText.questions) {
		t.Errorf("llms.txt's questions %v,\nthe page's %v", llmsText.questions, faq)
	}
	if !slices.Equal(glance, llmsText.glance) {
		t.Errorf("llms.txt's facts %v,\nthe page's %v", llmsText.glance, glance)
	}
}

// comparison is one comparison page: where it lives after / and /<code>/,
// its keys' prefix, its table's rows (by the key of their heading, in
// order) and how many products the table compares, Off The Cloud first.
type comparison struct {
	page, slug, prefix string
	rows               []string
	products           int
}

// The rows of the social network's and the files' tables.
var (
	socialRows = []string{"where", "audience", "ads", "feed", "ai", "backup", "leave", "chat", "cost", "open_source", "setup"}
	driveRows  = []string{"where", "cost", "space", "computers", "phones", "versions", "links", "more", "setup", "open_source"}
)

var comparisons = []comparison{
	{Social, "private-social-network", "site.social.", socialRows, 6},
	{Alternative, "google-photos-alternative", "site.alt.", []string{"where", "quality", "cost", "space",
		"open_source", "search", "files", "social", "family", "links", "apps", "setup"}, 6},
	{Drive, "google-drive-alternative", "site.drive.", driveRows, 7},
}

// Each comparison page lives at its slug in every language, with the
// landing page's header and footer: their links to the landing page's
// sections go to it (/#faq, /es/#faq), and the switcher to the same page
// in each language.
func TestComparisonPages(t *testing.T) {
	langs := i18n.Languages()
	for _, c := range comparisons {
		if PagePath(c.page, English()) != "/"+c.slug || PagePath(c.page, "es") != "/es/"+c.slug {
			t.Errorf("PagePath: %q %q", PagePath(c.page, English()), PagePath(c.page, "es"))
		}
		if p, ok := Localized(c.slug); !ok || p != c.page {
			t.Errorf("Localized(%q) = %q %v", c.slug, p, ok)
		}
		for _, l := range langs {
			out, err := Render(c.page, l.Code, testOptions)
			if err != nil {
				t.Fatal(err)
			}
			what := c.slug + " in " + l.Code
			h := readHead(t, out)
			for _, other := range langs {
				if h.hreflang[other.Tag] != testOptions.Base+PagePath(c.page, other.Code) {
					t.Errorf("%s: hreflang %s = %q", what, other.Tag, h.hreflang[other.Tag])
				}
			}
			home := Path(l.Code)
			for _, section := range []string{"#get-started", "#download", "#what", "#screenshots", "#faq", "#contact"} {
				if !slices.Contains(h.hrefs, home+section) {
					t.Errorf("%s: no link to %s%s", what, home, section)
				}
			}
			for _, href := range h.hrefs {
				if strings.HasPrefix(href, "#") {
					t.Errorf("%s: a link to %s, which isn't on this page", what, href)
				}
			}
			if len(langs) > 1 {
				for _, other := range langs {
					if !slices.Contains(h.hrefs, "/"+other.Code+"/"+c.slug) {
						t.Errorf("%s: the switcher has no /%s/%s", what, other.Code, c.slug)
					}
				}
			}
			if !slices.Contains(h.hrefs, home) {
				t.Errorf("%s: no link home", what)
			}
		}
	}
	// The landing page's own links to its sections stay on the page.
	out, err := Render(Landing, English(), testOptions)
	if err != nil {
		t.Fatal(err)
	}
	if h := readHead(t, out); !slices.Contains(h.hrefs, "#faq") || slices.Contains(h.hrefs, "/#faq") {
		t.Error("the landing page's header links leave the page")
	}
}

// The account page is kept out of search results, and shows the site's
// icons like the public pages.
func TestAccountHead(t *testing.T) {
	out, err := Render(Account, English(), testOptions)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{`<meta name="robots" content="noindex, follow">`, `href="/favicon-192x192.png"`, `rel="apple-touch-icon"`} {
		if !strings.Contains(s, want) {
			t.Errorf("the account page has no %s", want)
		}
	}
}

// Each comparison page's table, in every language: in a box of its own that
// scrolls (a labelled, focusable region) so the page never does; a header
// for each product's column, Off The Cloud's first, and each row - every
// one of the page's rows, in order - a header and a cell per product, Off
// The Cloud's marked as ours.
func TestComparisonTable(t *testing.T) {
	for _, c := range comparisons {
		for _, l := range i18n.Languages() {
			what := c.slug + " in " + l.Code
			out, err := Render(c.page, l.Code, testOptions)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := html.Parse(bytes.NewReader(out))
			if err != nil {
				t.Fatal(err)
			}
			ids := map[string]bool{}
			var tables []*html.Node
			for n := range doc.Descendants() {
				if id := attr(n, "id"); n.Type == html.ElementNode && id != "" {
					ids[id] = true
				}
				if n.DataAtom == atom.Table {
					tables = append(tables, n)
				}
			}
			if len(tables) != 1 {
				t.Fatalf("%s: %d tables, want 1", what, len(tables))
			}
			table := tables[0]
			wrap := table.Parent
			if attr(wrap, "class") != "table-wrap" || attr(wrap, "role") != "region" || attr(wrap, "tabindex") != "0" ||
				!ids[attr(wrap, "aria-labelledby")] || !ids[attr(table, "aria-labelledby")] {
				t.Errorf("%s: the table isn't in a labelled, focusable region of its own", what)
			}
			var rows [][]*html.Node
			for n := range table.Descendants() {
				if n.DataAtom == atom.Tr {
					var cells []*html.Node
					for c := n.FirstChild; c != nil; c = c.NextSibling {
						if c.DataAtom == atom.Th || c.DataAtom == atom.Td {
							cells = append(cells, c)
						}
					}
					rows = append(rows, cells)
				}
			}
			if len(rows) != len(c.rows)+1 || len(rows[0]) != c.products+1 {
				t.Fatalf("%s: %d rows, a header of %d cells; want %d rows", what, len(rows), len(rows[0]), len(c.rows)+1)
			}
			for i, cell := range rows[0][1:] {
				if cell.DataAtom != atom.Th || attr(cell, "scope") != "col" || strings.TrimSpace(cell.FirstChild.Data) == "" {
					t.Errorf("%s: a column without its header", what)
				}
				if us := attr(cell, "class") == "us"; us != (i == 0) || (us && cell.FirstChild.Data != "Off The Cloud") {
					t.Errorf("%s: column %d's header %q, class %q", what, i+1, cell.FirstChild.Data, attr(cell, "class"))
				}
			}
			for i, row := range rows[1:] {
				if len(row) != c.products+1 || row[0].DataAtom != atom.Th || attr(row[0], "scope") != "row" {
					t.Errorf("%s: row %d isn't a header and %d cells", what, i+1, c.products)
					continue
				}
				if want := i18n.T(l.Code, c.prefix+"row."+c.rows[i], nil); row[0].FirstChild == nil || row[0].FirstChild.Data != want {
					t.Errorf("%s: row %d isn't %q", what, i+1, want)
				}
				for j, cell := range row[1:] {
					if cell.DataAtom != atom.Td || cell.FirstChild == nil || strings.TrimSpace(cell.FirstChild.Data) == "" {
						t.Errorf("%s: row %d has an empty cell", what, i+1)
					}
					if us := attr(cell, "class") == "us"; us != (j == 0) {
						t.Errorf("%s: row %d, cell %d has class %q", what, i+1, j+1, attr(cell, "class"))
					}
				}
			}
		}
	}
}

// The landing page links to each comparison page in its own language: from
// at a glance and the footer by the page's short name, and after the FAQ
// answers that compare Off The Cloud with those services by the page's own
// link text. It opens its first question only, the one about the social
// network.
func TestLandingLinksToComparisons(t *testing.T) {
	for _, l := range i18n.Languages() {
		out, err := Render(Landing, l.Code, testOptions)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := html.Parse(bytes.NewReader(out))
		if err != nil {
			t.Fatal(err)
		}
		tr := func(key string) string { return i18n.T(l.Code, key, nil) }
		want := map[string][]string{
			// At a glance, the answer about Instagram and Facebook, the footer.
			PagePath(Social, l.Code): {tr("site.landing.glance.compare_social"), tr("site.social.link"), tr("site.landing.glance.compare_social")},
			// At a glance, the answers about Google Photos and iCloud and
			// about Immich, PhotoPrism and Nextcloud, the footer.
			PagePath(Alternative, l.Code): {tr("site.landing.glance.compare_photos"), tr("site.alt.link"), tr("site.alt.link"), tr("site.landing.glance.compare_photos")},
			// At a glance, the answer about Google Drive, Dropbox and Proton
			// Drive, the footer.
			PagePath(Drive, l.Code): {tr("site.landing.glance.compare_drive"), tr("site.drive.link"), tr("site.landing.glance.compare_drive")},
		}
		got := map[string][]string{}
		for n := range doc.Descendants() {
			if href := attr(n, "href"); n.DataAtom == atom.A && want[href] != nil && n.FirstChild != nil {
				got[href] = append(got[href], n.FirstChild.Data)
			}
		}
		for href, w := range want {
			if !slices.Equal(got[href], w) {
				t.Errorf("%s: the links to %s say %q, want %q", l.Code, href, got[href], w)
			}
		}
		var open []string
		for n := range doc.Descendants() {
			if n.DataAtom == atom.Details {
				if _, ok := attrOK(n, "open"); ok {
					for c := range n.Descendants() {
						if c.DataAtom == atom.Summary {
							open = append(open, c.FirstChild.Data)
						}
					}
				}
			}
		}
		if q := tr("site.landing.faq.social_how.q"); !slices.Equal(open, []string{q}) {
			t.Errorf("%s: open questions %q, want only %q", l.Code, open, q)
		}
	}
}

func attrOK(n *html.Node, name string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val, true
		}
	}
	return "", false
}
