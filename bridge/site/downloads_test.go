// SPDX-License-Identifier: AGPL-3.0-or-later

package site

import (
	"bytes"
	"maps"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/i18n"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// The download links' targets, each to its one destination: the files of
// the image and desktop releases, and the stores' listings. Nothing else
// is a target, and no destination is anywhere but those.
func TestDownloadTargets(t *testing.T) {
	want := map[string]string{
		"pi-image":    "https://github.com/alonsovidales/otc/releases/download/image/off-the-cloud-rpi-lite-arm64.img.xz",
		"windows":     "https://github.com/alonsovidales/otc/releases/download/desktop/otc-sync-windows-amd64.exe",
		"windows-arm": "https://github.com/alonsovidales/otc/releases/download/desktop/otc-sync-windows-arm64.exe",
		"linux":       "https://github.com/alonsovidales/otc/releases/download/desktop/otc-sync-linux-amd64",
		"linux-arm":   "https://github.com/alonsovidales/otc/releases/download/desktop/otc-sync-linux-arm64",
		"mac":         "https://apps.apple.com/app/id6814658734",
		"ios":         "https://apps.apple.com/app/id6814658734",
		"android":     "https://play.google.com/store/apps/details?id=cloud.offthe.otc",
	}
	if !maps.Equal(downloads, want) {
		t.Errorf("targets %v, want %v", downloads, want)
	}
	for target, dest := range want {
		if got, ok := DownloadURL(target, ""); !ok || got != dest {
			t.Errorf("%s: %q, %v; want %q", target, got, ok, dest)
		}
		// The site's /get/ addresses fit site_visits' page column.
		if len(DownloadPrefix+target) > 64 {
			t.Errorf("%s: too long a page to count under", target)
		}
	}
	for _, target := range []string{"", "PI-IMAGE", "pi-image/", "pi-image?x=1", "../admin", "pi-image.sha256",
		"https://evil.example/", "//evil.example", "evil.example", "source"} {
		if got, ok := DownloadURL(target, "ref:launch"); ok || got != "" {
			t.Errorf("%q: %q, %v", target, got, ok)
		}
	}
}

// A store's link says where the visit came from: Google Play's in its
// referrer= (utm_source, utm_medium), the App Store's in ct= under the
// provider token, once there is one. The files' links never change.
func TestDownloadURLTellsTheStores(t *testing.T) {
	cases := []struct{ target, source, want string }{
		{"android", "", googlePlay},
		{"android", "ref:reddit-rpi", googlePlay + "&referrer=utm_source%3Dreddit-rpi%26utm_medium%3Doff-the.cloud"},
		{"android", "news.ycombinator.com", googlePlay + "&referrer=utm_source%3Dnews.ycombinator.com%26utm_medium%3Doff-the.cloud"},
		{"pi-image", "ref:reddit-rpi", releases + "image/off-the-cloud-rpi-lite-arm64.img.xz"},
		{"windows", "reddit.com", releases + "desktop/otc-sync-windows-amd64.exe"},
	}
	if appStoreProvider == "" {
		// No token yet: the App Store's link as it is.
		cases = append(cases,
			struct{ target, source, want string }{"ios", "ref:reddit-rpi", appStore},
			struct{ target, source, want string }{"mac", "reddit.com", appStore})
	}
	for _, c := range cases {
		if got, ok := DownloadURL(c.target, c.source); !ok || got != c.want {
			t.Errorf("%s from %q: %q, want %q", c.target, c.source, got, c.want)
		}
	}
	// What Play Console reads out of the referrer.
	u, err := url.Parse(playLink("reddit-rpi"))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := url.ParseQuery(u.Query().Get("referrer"))
	if err != nil || u.Query().Get("id") != "cloud.offthe.otc" || ref.Get("utm_source") != "reddit-rpi" || ref.Get("utm_medium") != "off-the.cloud" {
		t.Errorf("Play's link %s: referrer %v, %v", u, ref, err)
	}

	// With a token: pt, ct (at most 40 characters) and mt=8.
	if got, want := appStoreLink("123456", "reddit-rpi"), appStore+"?pt=123456&ct=reddit-rpi&mt=8"; got != want {
		t.Errorf("App Store: %q, want %q", got, want)
	}
	long := strings.Repeat("a", 60) + ".example"
	if got, want := appStoreLink("123456", long), appStore+"?pt=123456&ct="+long[:40]+"&mt=8"; got != want {
		t.Errorf("App Store, a long name: %q, want %q", got, want)
	}
	if got := appStoreLink("", "reddit-rpi"); got != appStore {
		t.Errorf("App Store without a token: %q", got)
	}
}

// A link carries its source in its query, before any fragment; without
// one it is as it was.
func TestCarry(t *testing.T) {
	for _, c := range []struct{ href, source, want string }{
		{"/", "", "/"},
		{"/#install", "", "/#install"},
		{"/", "ref:reddit-rpi", "/?s=ref%3Areddit-rpi"},
		{"/es/#install", "reddit.com", "/es/?s=reddit.com#install"},
		{"/account?signup=1", "ref:launch", "/account?signup=1&s=ref%3Alaunch"},
		{"/get/pi-image", "ref:launch", "/get/pi-image?s=ref%3Alaunch"},
	} {
		if got := carry(c.href, c.source); got != c.want {
			t.Errorf("carry(%q, %q) = %q, want %q", c.href, c.source, got, c.want)
		}
	}
}

// Every tag linking one of the site's pages or downloads carries the
// page's source - all but the privacy notice, a static page that carries
// nothing on - and a tag's download is one of the targets.
func TestTagsCarryTheSource(t *testing.T) {
	for name, s := range tags {
		if s.Carries && s.El != "a" {
			t.Errorf("tag %s carries a source but isn't a link", name)
		}
		own := strings.HasPrefix(s.Href, "/") && !strings.HasPrefix(s.Href, "//")
		if want := own && s.Href != "/privacy"; s.Carries != want {
			t.Errorf("tag %s (%s): Carries %v, want %v", name, s.Href, s.Carries, want)
		}
		if target, ok := strings.CutPrefix(s.Href, DownloadPrefix); ok {
			if _, known := downloads[target]; !known {
				t.Errorf("tag %s: no download %q", name, target)
			}
		}
	}
}

// pageLinks are a rendered page's <a> addresses, and the addresses its
// head gives (canonical, hreflang) with its JSON-LD.
func pageLinks(t *testing.T, page []byte) (links, head []string) {
	t.Helper()
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		t.Fatal(err)
	}
	for n := range doc.Descendants() {
		switch {
		case n.DataAtom == atom.A:
			links = append(links, attr(n, "href"))
		case n.DataAtom == atom.Link:
			head = append(head, attr(n, "href"))
		case n.DataAtom == atom.Meta && attr(n, "property") == "og:url":
			head = append(head, attr(n, "content"))
		case n.DataAtom == atom.Script && attr(n, "type") == "application/ld+json" && n.FirstChild != nil:
			head = append(head, n.FirstChild.Data)
		}
	}
	return links, head
}

// The landing page links every download through /get/, plainly when the
// visit came from nowhere, and links no release file or store directly
// but the checksum: the links it had are the targets.
func TestLandingLinksTheDownloads(t *testing.T) {
	for _, l := range i18n.Languages() {
		out, err := Render(Landing, l.Code, testOptions)
		if err != nil {
			t.Fatal(err)
		}
		links, _ := pageLinks(t, out)
		linked := map[string]bool{}
		for _, href := range links {
			if target, ok := strings.CutPrefix(href, DownloadPrefix); ok {
				if _, known := downloads[target]; !known {
					t.Errorf("%s: a link to %s", l.Code, href)
				}
				linked[target] = true
				continue
			}
			direct := strings.HasPrefix(href, releases) && !strings.HasSuffix(href, ".sha256") ||
				strings.HasPrefix(href, appStore) || strings.HasPrefix(href, googlePlay)
			if direct {
				t.Errorf("%s: %s is linked directly, not through %s", l.Code, href, DownloadPrefix)
			}
		}
		if got := slices.Sorted(maps.Keys(linked)); !slices.Equal(got, slices.Sorted(maps.Keys(downloads))) {
			t.Errorf("%s: links the downloads %v, want every one: %v", l.Code, got, slices.Sorted(maps.Keys(downloads)))
		}
	}
}

// A page rendered with a source writes it into every link to the site's
// pages and downloads - the header's, the footer's, the language
// switcher's, the texts' - in every language, and nowhere else: not in its
// canonical, hreflang or Open Graph address, nor its JSON-LD; an in-page
// #section stays as it is, and so do the static privacy and terms pages.
// Without one, no link has it. These are the pages the site's host
// renders with links home (bridge/accounts gives the sign-in's own two no
// source; the device pages have no links).
func TestPagesCarryTheSource(t *testing.T) {
	const source = "ref:reddit-rpi"
	carried := SourceParam + "=" + url.QueryEscape(source)
	o := testOptions
	o.Source = source
	for _, page := range []string{Landing, Social, Alternative, Drive, Pi, Proofing, Account, NotFound} {
		for _, l := range i18n.Languages() {
			what := page + " in " + l.Code
			out, err := Render(page, l.Code, o)
			if err != nil {
				t.Fatal(err)
			}
			links, head := pageLinks(t, out)
			own := 0
			for _, href := range links {
				switch {
				case !strings.HasPrefix(href, "/") || strings.HasPrefix(href, "//"):
					if strings.Contains(href, carried) {
						t.Errorf("%s: %s carries the source off the site", what, href)
					}
				case href == "/privacy" || href == "/terms":
				default:
					own++
					u, err := url.Parse(href)
					if err != nil || u.Query().Get(SourceParam) != source {
						t.Errorf("%s: %s doesn't carry the source", what, href)
					}
				}
			}
			if own == 0 {
				t.Errorf("%s: no link to the site's own pages", what)
			}
			for _, h := range head {
				if strings.Contains(h, "reddit-rpi") {
					t.Errorf("%s: the head names the source: %.120s", what, h)
				}
			}

			plain, err := Render(page, l.Code, testOptions)
			if err != nil {
				t.Fatal(err)
			}
			links, _ = pageLinks(t, plain)
			for _, href := range links {
				if u, err := url.Parse(href); err != nil || u.Query().Has(SourceParam) {
					t.Errorf("%s without a source: %s", what, href)
				}
			}
		}
	}
	// The landing page's sections are on the page itself: its header's
	// #install stays, and the downloads carry it.
	out, err := Render(Landing, English(), o)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{`href="#install"`, `href="/get/pi-image?s=ref%3areddit-rpi"`, `href="/get/windows-arm?s=ref%3areddit-rpi" download`,
		`href="/account?signup=1&amp;s=ref%3areddit-rpi"`} {
		if !strings.Contains(strings.ToLower(s), strings.ToLower(want)) {
			t.Errorf("the landing page has no %s", want)
		}
	}
	// The other pages' way to the landing page's install section.
	out, err = Render(Pi, English(), o)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(string(out)), `href="/?s=ref%3areddit-rpi#install"`) {
		t.Error("the Raspberry Pi page's links to #install don't carry the source")
	}
}

// A page asking for a download that isn't one doesn't render.
func TestUnknownDownloadIsAnError(t *testing.T) {
	if _, err := (data{}).Download("nope"); err == nil {
		t.Error("an unknown download rendered")
	}
	if got, err := (data{source: "reddit.com"}).Download("android"); err != nil || got != "/get/android?s=reddit.com" {
		t.Errorf("%q, %v", got, err)
	}
}
