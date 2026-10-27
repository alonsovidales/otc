// SPDX-License-Identifier: AGPL-3.0-or-later

package accounts

import (
	"net/http"
	"strings"
	"time"

	"github.com/alonsovidales/otc/bridge/site"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
)

// The Google and Apple sign-in's own pages, the site's templates (package
// site: signin.html, continue.html) served from here, at the sign-in's
// addresses. They follow the browser's Accept-Language: the sign-in comes
// back from the provider, not from a page of ours that could say which.

// writePage renders page in lang (a carried code) with o and writes it
// with status, as package api writes the site's pages: under site.Policy
// with this response's nonce (so never in a frame), not sniffed, and not
// kept by the browser - the question page holds a one-time link. It
// writes nothing when the page can't be rendered.
func (a *Accounts) writePage(w http.ResponseWriter, page, lang string, status int, o site.Options) error {
	nonce, err := site.NewNonce()
	if err != nil {
		return err
	}
	o.Base, o.Nonce, o.Now = "https://"+a.tldHost(), nonce, time.Now()
	body, err := site.Render(page, lang, o)
	if err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", site.Policy(nonce))
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	// The question page's Continue is checked by its Referer where a
	// browser sends no Sec-Fetch-Site (sameOriginNavigation); nothing goes
	// to the device's page, which is another origin.
	h.Set("Referrer-Policy", "same-origin")
	h.Add("Vary", "Accept-Language")
	if l, ok := site.Language(lang); ok {
		h.Set("Content-Language", l.Tag)
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
	return nil
}

// pageLanguage is the language of the sign-in's pages for r: the first
// in its Accept-Language this build carries, else English.
func pageLanguage(r *http.Request) string { return i18n.Match(r.Header.Get("Accept-Language")) }

// pageError answers a sign-in step that went wrong with status. A browser
// opening a page gets the site's sign-in page (site.SignInProblem): m, in
// its language, under a heading, with the way back to the account page.
// Anything else - a request that is no page (pageNavigation), a client
// that doesn't take HTML - gets m as plain text, as it always did: the
// first language in Accept-Language this build carries, else English.
func (a *Accounts) pageError(w http.ResponseWriter, r *http.Request, m i18n.Msg, status int) {
	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		lang := pageLanguage(r)
		err := a.writePage(w, site.SignInProblem, lang, status, site.Options{Problem: m.Render(lang)})
		if err == nil {
			return
		}
		log.Error("could not render the sign-in's error page:", err)
	}
	http.Error(w, m.Render(acceptLanguage(r.Header.Get("Accept-Language"))), status)
}
