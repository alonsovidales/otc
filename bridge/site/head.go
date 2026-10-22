// SPDX-License-Identifier: AGPL-3.0-or-later

package site

import (
	"fmt"

	"github.com/alonsovidales/otc/i18n"
)

// What the public pages' head says about them besides their title and
// description, for search engines, assistants and share previews: Open
// Graph and Twitter tags, and a JSON-LD graph of the project (the
// Organization, the WebSite, the software and its apps, its source).
//
// Everything in it is either written here (names, addresses, sizes) or
// the page's own title and description, which the page names by key (the
// software's description is the landing page's on every page):
//
//	{{template "head" (head . (t "site.landing.meta.title") (t "site.landing.meta.description"))}}
//
// The graph is a map that html/template writes into a
// <script type="application/ld+json"> as JSON, escaping "<" like any
// script value: never template.JS, never text marshalled by hand. A data
// block is never run, so it needs no nonce. It states only what the pages
// state: no ratings, reviews, download counts or version numbers.

// The addresses the head and llms.txt name besides the site's own.
const (
	appStore       = "https://apps.apple.com/app/id6814658734" // iPhone, iPad and Mac: one App Store record
	googlePlay     = "https://play.google.com/store/apps/details?id=cloud.offthe.otc"
	desktopRelease = repo + "/releases/tag/desktop" // otc-sync for Windows and Linux
	demo           = "https://demo.off-the.cloud/"
	factSheet      = repo + "/blob/main/docs/press/fact-sheet.md"
	hackster       = "https://www.hackster.io/vidalesmiguelez/off-the-cloud-a-raspberry-pi-5-that-replaces-google-photos-8f79ea"
	founderGitHub  = "https://github.com/alonsovidales"
	agpl           = "https://www.gnu.org/licenses/agpl-3.0.html"
	contactEmail   = "info@off-the.cloud"
)

// The pictures the head names, under the site's base. The share picture
// is a 1200x630 crop of the hero's photo (docs/promo/pi-photographer-desk.jpg)
// in bridge/static/img; the screenshots are the landing page's own, with
// the ?v= its <img> elements give them (TestStructuredData checks they are).
const (
	ogImage       = "/img/og-1200x630.jpg"
	ogImageWidth  = 1200
	ogImageHeight = 630
	logo          = "/logo.png"
	logoSize      = 192
)

var screenshots = []string{"/img/web-1.jpg?v=2", "/img/mobile-1.jpg?v=2", "/img/macos-sync.jpg?v=2"}

// ogLocales are the carried languages as Open Graph names them (language
// and territory). A language added to the catalog needs its entry here:
// TestOGLocales fails until it has one.
var ogLocales = map[string]string{
	"en": "en_GB",
	"es": "es_ES",
	"fr": "fr_FR",
	"de": "de_DE",
	"it": "it_IT",
	"pt": "pt_PT",
	"nl": "nl_NL",
}

// pageHead is what common.html's "head" reads: the page's data, its title
// and description, and what the head says about the page from them.
type pageHead struct {
	data
	Title, Description string
	// OGLocale is the page's language as Open Graph names it, and
	// OGAlternates the other carried languages.
	OGLocale     string
	OGAlternates []string
	// OGImage is the share picture's absolute address.
	OGImage                     string
	OGImageWidth, OGImageHeight int
	// LD is the page's JSON-LD graph.
	LD map[string]any
}

// newHead is the "head" template function.
func newHead(d data, title, description string) (pageHead, error) {
	h := pageHead{data: d, Title: title, Description: description,
		OGImage: d.Base + ogImage, OGImageWidth: ogImageWidth, OGImageHeight: ogImageHeight}
	for _, l := range i18n.Languages() {
		loc, ok := ogLocales[l.Code]
		if !ok {
			return pageHead{}, fmt.Errorf("no Open Graph locale for %s", l.Code)
		}
		if l.Code == d.Lang {
			h.OGLocale = loc
		} else {
			h.OGAlternates = append(h.OGAlternates, loc)
		}
	}
	h.LD = structuredData(d, title, description)
	return h, nil
}

// softwareSummary is the key of the software's description in the
// JSON-LD: the landing page's own description, on every page, so that the
// nodes every page defines read the same wherever a crawler meets them.
const softwareSummary = "site.landing.meta.description"

// structuredData is the page's JSON-LD graph. Every node has the same @id
// in every language; the WebPage is the page's own (its canonical address
// and language). Every public page defines the Organization, the WebSite
// and the software (siteNodes: the same nodes on each, in the page's
// language) besides its WebPage; the landing page adds the apps and the
// source code.
func structuredData(d data, title, description string) map[string]any {
	page := map[string]any{
		"@type":       "WebPage",
		"@id":         d.Canonical + "#webpage",
		"url":         d.Canonical,
		"name":        title,
		"description": description,
		"inLanguage":  d.Tag,
		"isPartOf":    ldRef(d, "website"),
		"about":       ldRef(d, "software"),
		"publisher":   ldRef(d, "organization"),
		"primaryImageOfPage": map[string]any{
			"@type": "ImageObject", "url": d.Base + ogImage, "width": ogImageWidth, "height": ogImageHeight,
		},
	}
	organization, website, software := siteNodes(d)
	graph := []any{organization, website, page, software}
	if d.Page == Landing {
		app := func(typ, name, appName, os, url string) map[string]any {
			return ldNode(d, typ, name, map[string]any{
				"name":                appName,
				"operatingSystem":     os,
				"applicationCategory": "MultimediaApplication",
				"url":                 url,
				"offers":              freeOffer(),
				"isPartOf":            ldRef(d, "software"),
				"publisher":           ldRef(d, "organization"),
			})
		}
		desktop := app("SoftwareApplication", "app-desktop", "otc-sync", "Windows, Linux", desktopRelease)
		desktop["downloadUrl"] = desktopRelease
		graph = append(graph,
			// The App Store record has the iPhone, iPad and Mac apps (the
			// iOS target builds for both device families).
			app("MobileApplication", "app-ios", "Off The Cloud", "iOS, iPadOS", appStore),
			app("MobileApplication", "app-android", "Off The Cloud", "Android", googlePlay),
			app("SoftwareApplication", "app-mac", "Off The Cloud", "macOS", appStore),
			desktop,
			ldNode(d, "SoftwareSourceCode", "source", map[string]any{
				"name":                "Off The Cloud",
				"codeRepository":      repo,
				"license":             agpl,
				"programmingLanguage": "Go",
				"targetProduct":       ldRef(d, "software"),
			}),
		)
	}
	return map[string]any{"@context": "https://schema.org", "@graph": graph}
}

// siteNodes are the Organization, the WebSite and the software, as every
// public page defines them in its language.
func siteNodes(d data) (organization, website, software map[string]any) {
	var tags []string
	for _, l := range i18n.Languages() {
		tags = append(tags, l.Tag)
	}
	var shots []string
	for _, s := range screenshots {
		shots = append(shots, d.Base+s)
	}
	organization = ldNode(d, "Organization", "organization", map[string]any{
		"name":          "Off The Cloud",
		"alternateName": "OTC",
		"url":           d.Base + "/",
		"logo":          map[string]any{"@type": "ImageObject", "url": d.Base + logo, "width": logoSize, "height": logoSize},
		"email":         contactEmail,
		"founder":       map[string]any{"@type": "Person", "name": "Alonso Vidales", "sameAs": founderGitHub},
		"sameAs":        []string{repo, hackster},
	})
	website = ldNode(d, "WebSite", "website", map[string]any{
		"name":          "Off The Cloud",
		"alternateName": []string{"OTC", "off-the.cloud"},
		"url":           d.Base + "/",
		"inLanguage":    tags,
		"publisher":     ldRef(d, "organization"),
	})
	software = ldNode(d, "SoftwareApplication", "software", map[string]any{
		"name":                "Off The Cloud",
		"url":                 d.Base + "/",
		"description":         i18n.T(d.Lang, softwareSummary, nil),
		"applicationCategory": "MultimediaApplication",
		"operatingSystem":     "Raspberry Pi OS, Debian, Ubuntu",
		"isAccessibleForFree": true,
		"license":             agpl,
		"offers":              freeOffer(),
		"screenshot":          shots,
		"publisher":           ldRef(d, "organization"),
	})
	return organization, website, software
}

// ldNode is a node of the site's own (its @id under the site's base).
func ldNode(d data, typ, name string, fields map[string]any) map[string]any {
	fields["@type"] = typ
	fields["@id"] = d.Base + "/#" + name
	return fields
}

// ldRef names one of the site's own nodes by its @id.
func ldRef(d data, name string) map[string]any { return map[string]any{"@id": d.Base + "/#" + name} }

// freeOffer is what the software and its apps cost.
func freeOffer() map[string]any {
	return map[string]any{"@type": "Offer", "price": "0", "priceCurrency": "EUR"}
}
