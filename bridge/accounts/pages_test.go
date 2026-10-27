// SPDX-License-Identifier: AGPL-3.0-or-later

package accounts

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/bridge/limits"
	"github.com/alonsovidales/otc/bridge/site"
	"github.com/alonsovidales/otc/i18n"
)

// To a client that isn't opening a page (no text/html in its Accept), the
// sign-in's errors are the plain text http.Error wrote before the catalog,
// whatever languages the browser asks for, as long as this build carries
// English only.
func TestSignInPagesInEnglishAreUnchanged(t *testing.T) {
	a, _ := testAccounts(t)
	for _, header := range []string{"", "en-GB,en;q=0.9", "es-ES,es;q=0.9", "de, en;q=0.5", "ja", "zz-ZZ;q=x", strings.Repeat("a", 2000)} {
		if code := acceptLanguage(header); code != "" && code != "en" {
			continue // a build carrying that language
		}
		for _, c := range []struct {
			m      i18n.Msg
			status int
			want   string
		}{
			{i18n.ApiPageProviderUnknown(), http.StatusNotFound, "that sign-in provider is not configured on this bridge"},
			{i18n.ApiPageInvalidReturn(), http.StatusBadRequest, "that return address is not allowed"},
			{i18n.ApiPageChallengeRequired(), http.StatusBadRequest, "an app sign-in needs a valid challenge"},
			{i18n.ApiPageNotNavigation(), http.StatusBadRequest, "a sign-in starts from a link, not from within another page"},
			{i18n.ApiPageTooManyStarts(), http.StatusTooManyRequests, "too many sign-in attempts from this address, try again in a minute"},
			{i18n.ApiPageStartUnavailable(), http.StatusInternalServerError, "could not start the sign-in"},
			{i18n.ApiPageBadCallback(), http.StatusBadRequest, "bad callback"},
			{i18n.ApiErrorSigninUnavailable(), http.StatusInternalServerError, "could not sign in right now"},
			{i18n.ApiPageOpenFromAccount(), http.StatusForbidden, "open this from your account page"},
		} {
			r := httptest.NewRequest("GET", "/account/auth/google/start", nil)
			if header != "" {
				r.Header.Set("Accept-Language", header)
			}
			got, want := httptest.NewRecorder(), httptest.NewRecorder()
			a.pageError(got, r, c.m, c.status)
			http.Error(want, c.want, c.status)
			if got.Code != want.Code || got.Body.String() != want.Body.String() || got.Header().Get("Content-Type") != want.Header().Get("Content-Type") {
				t.Errorf("%s with %q: %d %q %q, want %d %q", c.m.Key, header, got.Code, got.Body.String(), got.Header().Get("Content-Type"), want.Code, want.Body.String())
			}
		}
	}

	// Through a handler: a provider this bridge doesn't have.
	a.providers = map[string]*provider{}
	r := httptest.NewRequest("GET", "/account/auth/github/start", nil)
	r.SetPathValue("provider", "github")
	r.Header.Set("Accept-Language", "ja-JP") // a language no build carries
	w := httptest.NewRecorder()
	a.OAuthStart(w, r)
	if w.Code != http.StatusNotFound || w.Body.String() != "that sign-in provider is not configured on this bridge\n" {
		t.Errorf("unknown provider: %d %q", w.Code, w.Body.String())
	}
}

// A browser opening a sign-in step that can't go on gets the site's page
// for it, in its language: what went wrong under a heading, the way back
// to the account page in that language, under the site's policy - never in
// a frame, never kept - with the step's status.
func TestSignInErrorsAreTheSitesPage(t *testing.T) {
	a, _ := testAccounts(t)
	a.tld = "off-the.cloud"
	nonce := regexp.MustCompile(`<style nonce="([A-Za-z0-9_-]+)">`)
	for _, l := range i18n.Languages() {
		r := httptest.NewRequest("GET", "/account/auth/google/start?return=https%3A%2F%2Fevil.com%2F", nil)
		r.SetPathValue("provider", "google")
		r.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
		r.Header.Set("Accept-Language", l.Tag+",en;q=0.5")
		newFakeIdP(t, a)
		w := httptest.NewRecorder()
		a.OAuthStart(w, r)
		body, h := w.Body.String(), w.Header()
		if w.Code != http.StatusBadRequest || !strings.HasPrefix(h.Get("Content-Type"), "text/html") || h.Get("Content-Language") != l.Tag {
			t.Errorf("%s: %d %q %q", l.Code, w.Code, h.Get("Content-Type"), h.Get("Content-Language"))
		}
		for _, want := range []string{
			`<html lang="` + l.Tag + `">`,
			"<h1>" + template.HTMLEscapeString(i18n.T(l.Code, "site.signin.error.heading", nil)) + "</h1>",
			`<p class="problem">` + template.HTMLEscapeString(i18n.ApiPageInvalidReturn().Render(l.Code)) + "</p>",
			`<a class="btn" href="` + site.AccountPath(l.Code) + `">` + template.HTMLEscapeString(i18n.T(l.Code, "site.signin.error.back", nil)) + "</a>",
			`<meta name="robots" content="noindex">`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: the page has no %s", l.Code, want)
			}
		}
		m := nonce.FindStringSubmatch(body)
		if csp := h.Get("Content-Security-Policy"); m == nil || csp != site.Policy(m[1]) || !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("%s: policy %q for the page's nonce %v", l.Code, csp, m)
		}
		if h.Get("X-Frame-Options") != "DENY" || h.Get("Cache-Control") != "no-store" || h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: headers %v", l.Code, h)
		}
	}

	// The starts' own limit says when to come back, as the page does.
	a.oauthStarts = limits.NewRate(1/60.0, 1)
	a.oauthStarts.Allow("203.0.113.9") // the address's one start this minute, used
	r := httptest.NewRequest("GET", "/account/auth/google/start", nil)
	r.SetPathValue("provider", "google")
	r.RemoteAddr = "203.0.113.9:4000"
	r.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	a.OAuthStart(w, r)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" || !strings.Contains(w.Body.String(), `<p class="problem">too many sign-in attempts`) {
		t.Errorf("too many starts: %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
}
