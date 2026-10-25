// SPDX-License-Identifier: AGPL-3.0-or-later

package site

import (
	"bytes"
	"encoding/xml"
	"strings"

	"github.com/alonsovidales/otc/i18n"
)

// What crawlers read about the site besides its pages, on the bridge's own
// host: /robots.txt, /sitemap.xml and /llms.txt (bridge/api serves them).
// They are built from the same tables as the pages (the carried languages,
// localized) and, for llms.txt, from the catalog when asked, so a new
// language, a new public page or a reworded text shows up in them by
// itself.

// Robots is /robots.txt: everything may be crawled but the API, and the
// sitemap is at base. Nothing else is kept out: /admin and /account say
// noindex in their pages, which a crawler kept out would never read, and
// device addresses (<name>.<tld>) send X-Robots-Tag: noindex.
func Robots(base string) []byte {
	return []byte("User-agent: *\nDisallow: /api/\n\nSitemap: " + base + "/sitemap.xml\n")
}

// englishOnly are the site's pages that exist in English only (legal
// text, bridge/static), for the sitemap.
var englishOnly = []string{"/privacy", "/terms"}

// The sitemap's XML (sitemaps.org, with Google's xhtml:link for the
// language versions).
type urlSet struct {
	XMLName xml.Name     `xml:"urlset"`
	NS      string       `xml:"xmlns,attr"`
	XHTML   string       `xml:"xmlns:xhtml,attr"`
	URLs    []sitemapURL `xml:"url"`
}

type sitemapURL struct {
	Loc   string        `xml:"loc"`
	Links []sitemapLink `xml:"xhtml:link"`
}

type sitemapLink struct {
	Rel      string `xml:"rel,attr"`
	Hreflang string `xml:"hreflang,attr"`
	Href     string `xml:"href,attr"`
}

// Sitemap is /sitemap.xml: every public page (landing, comparisons) in
// every carried language, each listing all of that page's language
// versions and x-default, as the pages' own <link rel="alternate"> do;
// then the English-only pages. No /en/ addresses (their canonical is the
// English one at the root), nothing that isn't meant for search (the
// account page), and no lastmod, which nothing here would keep true.
func Sitemap(base string) ([]byte, error) {
	set := urlSet{NS: "http://www.sitemaps.org/schemas/sitemap/0.9", XHTML: "http://www.w3.org/1999/xhtml"}
	for _, p := range localized {
		if !p.public {
			continue
		}
		var links []sitemapLink
		for _, a := range alternates(p.page, base) {
			links = append(links, sitemapLink{"alternate", a.Lang, a.Href})
		}
		for _, l := range i18n.Languages() {
			set.URLs = append(set.URLs, sitemapURL{Loc: base + PagePath(p.page, l.Code), Links: links})
		}
	}
	for _, path := range englishOnly {
		set.URLs = append(set.URLs, sitemapURL{Loc: base + path})
	}
	var b bytes.Buffer
	b.WriteString(xml.Header)
	enc := xml.NewEncoder(&b)
	enc.Indent("", "  ")
	if err := enc.Encode(set); err != nil {
		return nil, err
	}
	b.WriteString("\n")
	return b.Bytes(), nil
}

// llmsLink is one line of llms.txt's links: a name (English, or nameKey's
// text), where it goes (an address, or a path on the site), and what it
// is, from the catalog.
type llmsLink struct {
	name, nameKey, href, key, more string
}

// llmsText is everything /llms.txt says, in order: the summary and the
// lead from the catalog, the landing page's at-a-glance facts, the links
// with their descriptions, and the FAQ's questions (TestLLMsKeys checks the
// facts and the questions are the landing page's, in its order). Add a key
// here to say more. The names are English, as is the whole file, like the
// privacy notice: it is read by language models, which the English page
// serves best.
var llmsText = struct {
	summary, lead string
	// glance are the at-a-glance rows, each the keys
	// site.landing.glance.<row>.label and .value.
	glance    []string
	links     []llmsLink
	questions []string
}{
	summary: "site.landing.meta.description",
	lead:    "site.landing.hero.lead",
	glance:  []string{"what", "social", "files", "instead", "hardware", "storage", "apps", "cost", "privacy", "licence", "languages"},
	links: []llmsLink{
		{name: "Website", href: "/", key: "site.landing.what.lead"},
		// The demo signs in by itself ("Try the demo"); its password is
		// public, published by the demo device itself (docs/demo-device.md).
		{name: "Live demo", href: demo, key: "site.landing.hero.demo_note", more: "Its public password is `try-the-demo`."},
		{name: "App Store: iPhone, iPad and Mac", href: appStore, key: "site.landing.phone.ios_text"},
		{name: "Google Play: Android", href: googlePlay, key: "site.landing.phone.android_text"},
		{name: "otc-sync for Windows and Linux", href: desktopRelease, key: "site.landing.desktop.lead"},
		{name: "Source code", href: repo},
		{name: "Press fact sheet", href: factSheet},
		{nameKey: "site.social.link", href: "/" + SocialSlug, key: "site.social.meta.description"},
		{nameKey: "site.alt.link", href: "/" + AlternativeSlug, key: "site.alt.meta.description"},
		{nameKey: "site.drive.link", href: "/" + DriveSlug, key: "site.drive.meta.description"},
		{name: "Privacy notice", href: "/privacy"},
		{name: "Terms of use", href: "/terms"},
	},
	questions: []string{
		"site.landing.faq.social_how.q",
		"site.landing.faq.friends_device.q",
		"site.landing.faq.vs_instagram.q",
		"site.landing.faq.vs_drive.q",
		"site.landing.faq.vs_cloud.q",
		"site.landing.faq.vs_selfhosted.q",
		"site.landing.faq.hardware.q",
		"site.landing.faq.image.q",
		"site.landing.faq.other_hw.q",
		"site.landing.faq.from_icloud.q",
		"site.landing.faq.from_google.q",
		"site.landing.faq.raw.q",
		"site.landing.faq.lights.q",
		"site.landing.faq.family.q",
		"site.landing.faq.storage.q",
		"site.landing.faq.wifi.q",
		"site.landing.faq.pi_dies.q",
		"site.landing.faq.backup.q",
		"site.landing.faq.password.q",
		"site.landing.faq.protect.q",
		"site.landing.faq.bridge.q",
		"site.landing.faq.free.q",
		"site.landing.faq.speed.q",
		"site.landing.faq.private.q",
		"site.landing.faq.third_parties.q",
		"site.landing.faq.ai.q",
		"site.landing.faq.open_source.q",
		"site.landing.faq.social.q",
	},
}

// glanceKeys are the label and value keys of an at-a-glance row.
func glanceKeys(row string) (label, value string) {
	return "site.landing.glance." + row + ".label", "site.landing.glance." + row + ".value"
}

// LLMs is /llms.txt (llmstxt.org): the site in Markdown for language
// models, written from the English catalog when asked, so it never says
// something the page doesn't. base is the site's origin.
func LLMs(base string) []byte {
	lang := English()
	// The tags table's links are the landing page's: "/account" and
	// "#install" are on the site.
	abs := func(href string) string {
		switch {
		case strings.HasPrefix(href, "/"):
			return base + href
		case strings.HasPrefix(href, "#"):
			return base + "/" + href
		}
		return href
	}
	text := func(key string) string { return markdown(lang, key, abs) }
	var b strings.Builder
	b.WriteString("# Off The Cloud\n\n")
	b.WriteString("> " + text(llmsText.summary) + "\n\n")
	b.WriteString(text(llmsText.lead) + "\n\n")
	b.WriteString("## At a glance\n\n")
	for _, row := range llmsText.glance {
		label, value := glanceKeys(row)
		b.WriteString("- " + text(label) + ": " + text(value) + "\n")
	}
	b.WriteString("\n## Links\n\n")
	for _, l := range llmsText.links {
		name := mdEscape(l.name)
		if l.nameKey != "" {
			name = text(l.nameKey)
		}
		b.WriteString("- [" + name + "](" + abs(l.href) + ")")
		var about []string
		if l.key != "" {
			about = append(about, text(l.key))
		}
		if l.more != "" {
			about = append(about, l.more)
		}
		if len(about) > 0 {
			b.WriteString(": " + strings.Join(about, " "))
		}
		b.WriteString("\n")
		if l.href == "/" {
			// The page in every other language this build carries.
			var others []string
			for _, other := range i18n.Languages() {
				if other.Code != lang {
					others = append(others, "["+mdEscape(other.Name)+"]("+base+Path(other.Code)+")")
				}
			}
			if len(others) > 0 {
				b.WriteString("- Also in " + strings.Join(others, ", ") + "\n")
			}
		}
	}
	b.WriteString("\n## Questions\n\nAnswered on the site's FAQ.\n\n")
	for _, key := range llmsText.questions {
		b.WriteString("- [" + text(key) + "](" + base + "/#faq)\n")
	}
	return []byte(b.String())
}

// markdown is key's text in lang as Markdown: plain text escaped, and a
// rich key's spans as emphasis, code or links (the tags table's, made
// absolute by abs).
func markdown(lang, key string, abs func(string) string) string {
	var b strings.Builder
	for _, p := range (i18n.Rich{Key: key}).Parts(lang) {
		s, tagged := tags[p.Tag]
		switch {
		case p.Tag == "" || !tagged:
			b.WriteString(mdEscape(p.Text))
		case s.El == "strong":
			b.WriteString("**" + mdEscape(p.Text) + "**")
		case s.El == "em":
			b.WriteString("*" + mdEscape(p.Text) + "*")
		case s.El == "code":
			b.WriteString("`" + strings.ReplaceAll(p.Text, "`", "'") + "`")
		case s.El == "a":
			b.WriteString("[" + mdEscape(p.Text) + "](" + abs(s.Href) + ")")
		default:
			b.WriteString(mdEscape(p.Text))
		}
	}
	return b.String()
}

// mdEscape keeps text from reading as Markdown.
var mdEscape = strings.NewReplacer(`\`, `\\`, "`", "\\`", "*", `\*`, "_", `\_`, "[", `\[`, "]", `\]`, "<", `\<`).Replace
