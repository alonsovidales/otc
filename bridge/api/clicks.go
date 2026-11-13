// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"strings"

	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/bridge/site"
)

// Download clicks by source (/privacy, "Visit counts"): the site's
// download buttons and store links go to /get/<target> on the site's own
// host, answered here with a 302 to that target's file or store page
// (site.DownloadURL) - only the targets the site lists, each to its own
// destination: an unknown one is the site's 404, and nothing in the
// request can name another destination. A person's click (personVisit: a
// GET, no prefetch, no robot) is counted as visits are, into site_visits
// under the page /get/<target> with the day and the source; robots and
// prefetches are sent on without being counted. Nothing about who clicked
// is kept.
//
// The source is the click's own (visitSource), else the one its link
// carried (?s=, site.SourceParam): the pages write where their visit came
// from into their links to the site's pages and downloads (linkSource,
// site.Options.Source), so a click on our own page - whose Referer is the
// site itself - still counts under the website or the link of ours the
// visitor came from, however many of our pages they went through.

// serveDownload answers /get/<target>.
func (api *API) serveDownload(w http.ResponseWriter, r *http.Request, target string) {
	src := linkSource(r, api.tld, "")
	dest, ok := site.DownloadURL(target, src)
	if !ok {
		api.notFound(w, r, "")
		return
	}
	if api.recordVisit != nil && personVisit(r) {
		api.recordVisit(dao.ClickPagePrefix+target, src)
	}
	// Asked for again at each click, so that each one is counted.
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, dest, http.StatusFound)
}

// linkSource is where the visit r is came from, for the links of the page
// answering it (page, one of package site's; "" for a download): the
// visit's own source (visitSource), else the one a link of ours carried
// when it is one visitSource could have given (carriedSource). On the
// account page, as for its visits, its own Referer - a sign-in coming
// back, an email's link - is no source; a carried one is. None on a
// device's address, whose visitors aren't the site's, and none for a
// robot, whose pages keep their plain links: no addresses that follow a
// crawler's way through the site.
func linkSource(r *http.Request, tld, page string) string {
	if r.Host != tld || robot(r) {
		return ""
	}
	src := visitSource(r, tld)
	if page == site.Account && !strings.HasPrefix(src, "ref:") {
		src = ""
	}
	if src == "" {
		src = carriedSource(r.URL.Query().Get(site.SourceParam), tld)
	}
	return src
}

// carriedSource is v, a source one of our links carried, when it is one
// visitSource gives - "ref:" and a link's name as cVisitRef takes it, or
// a website's name as referrerSite keeps it - and "" for anything else:
// anyone can write an address.
func carriedSource(v, tld string) string {
	if name, ok := strings.CutPrefix(v, "ref:"); ok {
		if cVisitRef.MatchString(name) {
			return v
		}
		return ""
	}
	if v != "" && referrerSite("https://"+v+"/", tld) == v {
		return v
	}
	return ""
}
