// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/alonsovidales/otc/bridge/site"
)

// Anonymous visit counts for the public site (/privacy, "Visit counts"),
// so the owner can tell which pages and links bring people, and nothing
// more. A visit is a person's browser opening one of the site's pages: a
// GET to the site's own host (www. is redirected to it before this), not a
// prefetch, not a robot, answered 200 with a page people read - the
// landing and comparison pages in every language, the account page (one
// page, whatever its language and with nothing about who), the privacy
// and terms pages. Never a device's address, /api/, /admin, /media, a
// font, picture, stylesheet or script, sw.js, a verification file, a
// redirect, a 304 or a 404.
//
// What is counted is the day (UTC), the page and where the visit came
// from: a ref= or utm_source= on one of our own links, else the name of
// the website whose link was followed (the Referer's host, nothing else
// of it; never for the account page), else nothing. No address, cookie, browser or account is kept;
// the User-Agent is only looked at, to leave robots out. The counts are
// added up in memory and written in batches (dao.RecordSiteVisit).

// cVisitRobots are in robots' User-Agents, matched in lower case: search
// engines, link previews, uptime checks, scripts. facebookexternalhit,
// WhatsApp, Mastodon (every server fetches a link posted there) and
// Bluesky's Cardyb fetch a link's preview under names without "bot".
var cVisitRobots = []string{
	"bot", "crawl", "spider", "slurp", "preview", "fetch", "curl", "wget", "python", "go-http", "headless", "lighthouse", "monitor",
	"facebookexternalhit", "whatsapp", "mastodon", "cardyb",
}

const (
	// cVisitPageMax is site_visits.page's length; a longer name is
	// counted as "other".
	cVisitPageMax = 64
	// cVisitHostMax is how much of a referring website's name is kept
	// (site_visits.source's length).
	cVisitHostMax = 100
)

// cVisitRef is the value of a link's ref= or utm_source=, lowercased.
var cVisitRef = regexp.MustCompile(`^[a-z0-9._-]{1,40}$`)

// cVisitHost is a host name as a Referer carries it (IDNs in punycode).
var cVisitHost = regexp.MustCompile(`^[a-z0-9._-]+$`)

// countVisit wraps w when r may be a visit to one of the site's pages,
// and returns what to call once the answer is written: it counts the
// visit when the page was answered 200.
func (api *API) countVisit(w http.ResponseWriter, r *http.Request) (http.ResponseWriter, func()) {
	if api.recordVisit == nil || r.Host != api.tld || !personVisit(r) {
		return w, func() {}
	}
	page, ok := visitPage(r.URL.Path)
	if !ok {
		return w, func() {}
	}
	sw := &statusWriter{ResponseWriter: w}
	return sw, func() {
		if sw.status != http.StatusOK {
			return
		}
		src := visitSource(r, api.tld)
		// The account page is mostly opened by people who already have an
		// account: back from Google's or Apple's sign-in (the redirect to
		// it keeps their Referer) or from a link in our emails (the mail
		// service's, which can be someone's own domain). Neither is a way
		// people find Off The Cloud, so only one of our links counts there.
		if page == "/account" && !strings.HasPrefix(src, "ref:") {
			src = ""
		}
		api.recordVisit(page, src)
	}
}

// personVisit reports whether r can be a person opening a page: a GET,
// not a prefetch (a browser fetching a page in case it is opened next),
// and not a robot.
func personVisit(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	for _, h := range []string{"Sec-Purpose", "Purpose"} {
		if strings.Contains(strings.ToLower(r.Header.Get(h)), "prefetch") {
			return false
		}
	}
	ua := strings.ToLower(r.UserAgent())
	if ua == "" {
		return false
	}
	for _, robot := range cVisitRobots {
		if strings.Contains(ua, robot) {
			return false
		}
	}
	return true
}

// visitPage is the name a visit to path is counted under - the page's
// address with its language ("/", "/es/", "/private-social-network",
// "/fr/google-drive-alternative"; "/account" for the account page in any
// language; "/privacy" and "/terms" with or without .html) - and whether
// path is one of the pages counted at all.
func visitPage(path string) (string, bool) {
	switch path {
	case "/privacy", "/privacy.html":
		return "/privacy", true
	case "/terms", "/terms.html":
		return "/terms", true
	}
	code, rest, slash := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	var page string
	var ok bool
	if !slash {
		// English: /, /account, /<slug>.
		page, ok = site.Localized(code)
	} else if _, carried := site.Language(code); carried {
		page, ok = site.Localized(rest)
	}
	switch {
	case !ok:
		return "", false
	case page == site.Account:
		return "/account", true
	case len(path) > cVisitPageMax:
		return "other", true
	}
	return path, true
}

// visitSource is where a visit came from: "ref:<name>" for one of our own
// links (?ref= or ?utm_source=, letters, digits, ".", "_" and "-" only),
// else the name of the website a Referer says it came from, else "".
func visitSource(r *http.Request, tld string) string {
	q := r.URL.Query()
	for _, param := range []string{"ref", "utm_source"} {
		if v := strings.ToLower(q.Get(param)); cVisitRef.MatchString(v) {
			return "ref:" + v
		}
	}
	return referrerSite(r.Header.Get("Referer"), tld)
}

// referrerSite is the name of the website a Referer header names: its
// host only, lowercased, without "www.", at most cVisitHostMax characters.
// "" for none, for the site itself - a device's address too, whose name
// would say whose device it was - for an IP address, and for anything that
// isn't a host name.
func referrerSite(referer, tld string) string {
	u, err := url.Parse(referer)
	if err != nil {
		return ""
	}
	host := strings.TrimPrefix(strings.TrimSuffix(strings.ToLower(u.Hostname()), "."), "www.")
	if host == "" || !cVisitHost.MatchString(host) || net.ParseIP(host) != nil {
		return ""
	}
	// A test bridge's tld carries its port ("bridge.test:8081").
	if h, _, err := net.SplitHostPort(tld); err == nil {
		tld = h
	}
	tld = strings.ToLower(tld)
	if host == tld || strings.HasSuffix(host, "."+tld) {
		return ""
	}
	return host[:min(len(host), cVisitHostMax)]
}

// statusWriter notes the status an answer was written with.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	// 1xx are interim: the answer's own status comes after.
	if s.status == 0 && code >= 200 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }
