// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/alonsovidales/otc/bridge/site"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
)

// The bridge's own pages in the visitor's language (docs/i18n.md, "Each
// platform": bridge). They come from the binary (package site), never from
// staticPath: a deploy copies bridge/static over /var/www without deleting
// anything, so a page that moved into the binary would otherwise go on
// being served from the copy a previous deploy left there.
//
// On the bridge's own host the landing page has one address per language,
// and the language is in the path - no cookie:
//
//	/                 English, or 302 to /<code>/ when Accept-Language prefers another language this build carries
//	/<code>/          the page in that language; /en/ is English without that redirect (the switcher's link)
//	/<code>           301 to /<code>/
//	/xx, /xx/         404 for two or three lowercase letters that aren't a carried language
//	/<code>/anything  404: the only other localized pages are
//	/account          the account page: English, or 302 to /<code>/account as / does
//	/<code>/account   the account page in that language; /en/account is English without the redirect
//	/private-social-network, /google-photos-alternative, /google-drive-alternative
//	and the same under /<code>/
//	                  the comparison pages, the same way
//	/xx/account       404 for a language this build doesn't carry (and the comparison pages under /xx/)
//	/landing(.html)   301 to /, the page's old address
//	/account.html     301 to /account, the account page's old address
//	/robots.txt, /sitemap.xml, /llms.txt
//	                  what crawlers read (package site, crawl.go)
//
// The pages and those three files go out gzipped to a client that takes
// it (they are text, about 80 KB for the landing page; fonts and pictures
// are compressed already).
//
// The account page's own links (the Google and Apple sign-ins' ?error= and
// ?complete=, and emails' #verify= and #reset=) come to /account: a
// redirect keeps the query, and the browser keeps the fragment. An email
// in a carried language other than English links /<code>/account instead
// (accounts' accountPage), so the page opens in the email's language.
//
// /disabled and /unavailable show those pages on the bridge's host too
// (Accept-Language), for a look at them. Everything else (the privacy,
// terms and admin pages, fonts, images; a 404 for no such file) is
// serveStatic's. On a device's own address the bridge answers with
// serveOwnPage when it can't proxy: there the path is the device's, so
// those pages follow Accept-Language.

// langSegment is what a language code looks like in a path.
var langSegment = regexp.MustCompile(`^[a-z]{2,3}$`)

// serveSite is the bridge's catch-all handler: the site's pages on its own
// host, and everything else as serveStatic decides. A page someone opened
// is counted, anonymously (visits.go).
func (api *API) serveSite(w http.ResponseWriter, r *http.Request) {
	w, counted := api.countVisit(w, r)
	if r.Host != api.tld || !api.sitePage(w, r) {
		api.serveStatic(w, r)
	}
	counted()
}

// sitePage answers r when its path is one of the site's (the table
// above), and reports whether it did.
func (api *API) sitePage(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	var answer func()
	base := "https://" + api.tld
	switch p {
	case "/":
		answer = func() { api.writeEnglishOrRedirect(w, r, site.Landing) }
	case "/account":
		answer = func() { api.writeEnglishOrRedirect(w, r, site.Account) }
	case "/" + site.SocialSlug:
		answer = func() { api.writeEnglishOrRedirect(w, r, site.Social) }
	case "/" + site.AlternativeSlug:
		answer = func() { api.writeEnglishOrRedirect(w, r, site.Alternative) }
	case "/" + site.DriveSlug:
		answer = func() { api.writeEnglishOrRedirect(w, r, site.Drive) }
	case "/landing", "/landing.html":
		answer = func() { siteRedirect(w, r, "/", http.StatusMovedPermanently) }
	case "/account.html":
		answer = func() { siteRedirect(w, r, "/account", http.StatusMovedPermanently) }
	case "/disabled", "/disabled.html":
		answer = func() { api.writeLocalizedPage(w, r, site.Disabled, http.StatusOK) }
	case "/unavailable", "/unavailable.html":
		answer = func() { api.writeLocalizedPage(w, r, site.Unavailable, http.StatusOK) }
	case "/robots.txt":
		answer = func() { writeCompressed(w, r, "text/plain; charset=utf-8", http.StatusOK, site.Robots(base)) }
	case "/llms.txt":
		answer = func() { writeCompressed(w, r, "text/plain; charset=utf-8", http.StatusOK, site.LLMs(base)) }
	case "/sitemap.xml":
		answer = func() {
			body, err := site.Sitemap(base)
			if err != nil {
				log.Error("site: could not write the sitemap:", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			writeCompressed(w, r, "application/xml; charset=utf-8", http.StatusOK, body)
		}
	default:
		code, rest, slash := strings.Cut(p[1:], "/")
		if !langSegment.MatchString(code) {
			return false
		}
		_, carried := site.Language(code)
		page, localized := site.Localized(rest)
		switch {
		case carried && !slash:
			answer = func() { siteRedirect(w, r, "/"+code+"/", http.StatusMovedPermanently) }
		case carried && localized:
			answer = func() { api.writeSitePage(w, r, page, code, http.StatusOK) }
		case carried, localized:
			answer = func() { http.NotFound(w, r) }
		default:
			// Not a language: /img/device.jpg and the like.
			return false
		}
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return true
	}
	answer()
	return true
}

// writeEnglishOrRedirect answers a localized page's English address (/,
// /account, a comparison page): the page in English, or a 302 to
// the page in the language Accept-Language prefers when that is another
// language this build carries.
func (api *API) writeEnglishOrRedirect(w http.ResponseWriter, r *http.Request, page string) {
	w.Header().Add("Vary", "Accept-Language")
	if lang := i18n.Match(r.Header.Get("Accept-Language")); lang != site.English() {
		siteRedirect(w, r, site.PagePath(page, lang), http.StatusFound)
		return
	}
	api.writeSitePage(w, r, page, site.English(), http.StatusOK)
}

// siteRedirect sends r to path, keeping its query.
func siteRedirect(w http.ResponseWriter, r *http.Request, path string, status int) {
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, path, status)
}

// serveOwnPage writes one of the bridge's own pages (site.Disabled,
// site.Unavailable) with a status of this handler's choosing, for a
// request aimed at a device subdomain that the bridge is answering itself
// rather than proxying. The path belongs to the device, so the language
// comes from Accept-Language.
//
// Only pages a person is meant to read get the HTML body. Everything else
// the browser asks for while the page is failing - a stylesheet, a script,
// a favicon - gets the bare status instead, so an HTML error page never
// arrives claiming to be a .js file (which a browser would refuse, noisily
// and confusingly, on top of whatever actually went wrong).
func (api *API) serveOwnPage(w http.ResponseWriter, r *http.Request, page string, status int) {
	if !strings.Contains(r.Header.Get("Accept"), "text/html") {
		w.WriteHeader(status)
		return
	}
	api.writeLocalizedPage(w, r, page, status)
}

// writeLocalizedPage writes page in the language Accept-Language prefers.
func (api *API) writeLocalizedPage(w http.ResponseWriter, r *http.Request, page string, status int) {
	w.Header().Add("Vary", "Accept-Language")
	api.writeSitePage(w, r, page, i18n.Match(r.Header.Get("Accept-Language")), status)
}

// writeSitePage renders page in lang (a carried code) and writes it with
// status, under a Content-Security-Policy whose nonce only this response's
// inline <style> and <script> elements carry. A page that can't be
// rendered gets the bare status (500 for a page that should have been a
// 200), and a log line naming the page - never the host or the language.
func (api *API) writeSitePage(w http.ResponseWriter, r *http.Request, page, lang string, status int) {
	nonce, err := newNonce()
	var body []byte
	if err == nil {
		body, err = site.Render(page, lang, site.Options{Base: "https://" + api.tld, Nonce: nonce, Now: time.Now()})
	}
	if err != nil {
		log.Error("site: could not render", page, ":", err)
		if status < http.StatusBadRequest {
			status = http.StatusInternalServerError
		}
		w.WriteHeader(status)
		return
	}
	h := w.Header()
	if l, ok := site.Language(lang); ok {
		h.Set("Content-Language", l.Tag)
	}
	h.Set("Content-Security-Policy", sitePolicy(nonce))
	writeCompressed(w, r, "text/html; charset=utf-8", status, body)
}

// writeCompressed writes body with status and its type, gzipped when the
// client takes gzip: a page of text is about a quarter of its size that
// way. Content-Length is what is sent - HEAD gets the same headers as GET,
// without the body - and Vary tells caches the answer depends on
// Accept-Encoding.
func writeCompressed(w http.ResponseWriter, r *http.Request, contentType string, status int, body []byte) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Add("Vary", "Accept-Encoding")
	if acceptsGzip(r.Header.Get("Accept-Encoding")) {
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		if _, err := zw.Write(body); err == nil && zw.Close() == nil {
			h.Set("Content-Encoding", "gzip")
			body = b.Bytes()
		}
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		w.Write(body)
	}
}

// acceptsGzip reports whether an Accept-Encoding header lets the answer be
// gzipped: gzip (or x-gzip) listed without q=0, or "*" without q=0 when
// gzip isn't listed at all.
func acceptsGzip(header string) bool {
	star := false
	for _, part := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(part, ";")
		allowed := true
		for _, p := range strings.Split(params, ";") {
			if k, v, ok := strings.Cut(strings.TrimSpace(p), "="); ok && strings.EqualFold(strings.TrimSpace(k), "q") {
				q, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
				allowed = err == nil && q > 0
			}
		}
		switch name = strings.ToLower(strings.TrimSpace(name)); name {
		case "gzip", "x-gzip":
			return allowed
		case "*":
			star = allowed
		}
	}
	return star
}

// sitePolicy is the pages' Content-Security-Policy: their inline style and
// scripts by nonce, and nothing from anywhere but the bridge itself (the
// self-hosted fonts and their stylesheet, the logo and screenshots, the
// contact form's POST, the account page's calls to /api/account). The JSON
// blocks holding the scripts' text are data, never run.
func sitePolicy(nonce string) string {
	return strings.ReplaceAll(cSitePolicy, "NONCE", "'nonce-"+nonce+"'")
}

const cSitePolicy = "default-src 'none'; script-src NONCE; style-src 'self' NONCE; font-src 'self'; img-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'" // i18n-ignore: a Content-Security-Policy header, not text

// newNonce returns 128 random bits for one response's policy.
func newNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
