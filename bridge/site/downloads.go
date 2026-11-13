// SPDX-License-Identifier: AGPL-3.0-or-later

package site

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// Download clicks by source (/privacy, "Visit counts"). The pages'
// download buttons and store links point at the site's own
// /get/<target>, which bridge/api answers with a redirect to the target's
// file or store page, counting a person's click under where the visit
// came from (api/clicks.go). Only the targets below exist there, each
// with its destination written here, so /get/ never sends anyone anywhere
// else. The checksum, the source code and GitHub's pages stay direct
// links.
//
// Where the visit came from goes along with the visitor: a page rendered
// for a visit from somewhere (Options.Source) writes it into its links to
// the site's pages and downloads as ?s=<source>, so the page opened next
// carries it on, and a download is counted under it wherever it is
// clicked. No cookie, storage or script: only the links of the page in
// hand.

// DownloadPrefix starts every download link's address: /get/pi-image.
const DownloadPrefix = "/get/"

// SourceParam is the query parameter the links carry the source in. It is
// neither of the ones visits are counted by (ref=, utm_source=), so a page
// opened from one of the site's links still counts as a visit from the
// site itself.
const SourceParam = "s"

// downloads are the download links' targets and where each one goes: the
// Raspberry Pi image, otc-sync for each system the site links, and the
// stores - the App Store's one record twice, for the Mac's card and the
// iPhone's, to tell which of them was clicked.
var downloads = map[string]string{
	"pi-image":    releases + "image/off-the-cloud-rpi-lite-arm64.img.xz",
	"windows":     releases + "desktop/otc-sync-windows-amd64.exe",
	"windows-arm": releases + "desktop/otc-sync-windows-arm64.exe",
	"linux":       releases + "desktop/otc-sync-linux-amd64",
	"linux-arm":   releases + "desktop/otc-sync-linux-arm64",
	"mac":         appStore,
	"ios":         appStore,
	"android":     googlePlay,
}

// appStoreProvider is the App Store Connect provider token (pt=) that
// App Analytics needs to count the App Store's downloads by campaign
// (ct=, the source's name). Empty until the project's token is filled in:
// the App Store's links carry no campaign until then. Once it is set, the
// privacy notice's "Visit counts" names the App Store beside Google Play.
const appStoreProvider = ""

// cAppStoreCampaignMax is the longest campaign token (ct=) the App Store
// takes.
const cAppStoreCampaignMax = 40

// cPlayMedium is what the Google Play links say the source's medium is:
// the site.
const cPlayMedium = "off-the.cloud"

// DownloadURL is where /get/<target> sends a visit from source (as
// Options.Source has it; "" for none), and false for a target that isn't
// one of the site's. A store's link tells the store the source, so its own
// figures show where its installs came from too: Google Play's referrer=
// (whose utm_source and utm_medium Play Console's acquisition reports
// read), and the App Store's ct= once appStoreProvider is set.
func DownloadURL(target, source string) (string, bool) {
	dest, ok := downloads[target]
	if !ok {
		return "", false
	}
	name := strings.TrimPrefix(source, "ref:")
	switch {
	case name == "":
	case dest == googlePlay:
		dest = playLink(name)
	case dest == appStore:
		dest = appStoreLink(appStoreProvider, name)
	}
	return dest, true
}

// playLink is the Google Play listing telling Play that the visit came
// from name: a link's name or a website's.
func playLink(name string) string {
	return googlePlay + "&referrer=" + url.QueryEscape("utm_source="+name+"&utm_medium="+cPlayMedium)
}

// appStoreLink is the App Store listing with name as the campaign of the
// provider token, cut to the App Store's length; the listing as it is
// without a token.
func appStoreLink(token, name string) string {
	if token == "" {
		return appStore
	}
	return appStore + "?pt=" + url.QueryEscape(token) + "&ct=" + url.QueryEscape(name[:min(len(name), cAppStoreCampaignMax)]) + "&mt=8"
}

// carry is href, a link to one of the site's pages or downloads, with
// source in its query (SourceParam), ahead of any #fragment; href as it
// is without a source.
func carry(href, source string) string {
	if source == "" {
		return href
	}
	path, fragment, hasFragment := strings.Cut(href, "#")
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	path += sep + SourceParam + "=" + url.QueryEscape(source)
	if hasFragment {
		path += "#" + fragment
	}
	return path
}

// Download is a page's link to one of the downloads, carrying the page's
// source: href="{{.Download "pi-image"}}". A target that isn't one is an
// error, and the page doesn't render.
func (d data) Download(target string) (string, error) {
	if _, ok := downloads[target]; !ok {
		return "", fmt.Errorf("no download %q", target)
	}
	return carry(DownloadPrefix+target, d.source), nil
}

// Carry is a rich text's spans with their links to the site's pages and
// downloads (the tags that say Carries) carrying the page's source, for a
// text with such a link: {{template "spans" (.Carry (rich "key"))}}.
func (d data) Carry(spans []span) []span {
	if d.source == "" {
		return spans
	}
	out := slices.Clone(spans)
	for i := range out {
		if out[i].Carries {
			out[i].Href = carry(out[i].Href, d.source)
		}
	}
	return out
}
