// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/bridge/site"
	"github.com/alonsovidales/otc/bridge/websocket"
	"github.com/alonsovidales/otc/i18n"
)

// siteAPI is an API on the bridge's host "bridge.test", whose static
// directory holds what an earlier deploy left in /var/www: stale copies of
// the pages that moved into the binary, and files that are still served
// from there.
func siteAPI(t *testing.T) *API {
	t.Helper()
	dir := t.TempDir() + "/"
	for name, body := range map[string]string{
		"landing.html":     "STALE landing",
		"disabled.html":    "STALE disabled",
		"unavailable.html": "STALE unavailable",
		"privacy.html":     "the privacy notice",
		"index.html":       "the old web app shell",
		"img/device.jpg":   "a photo",
	} {
		if err := os.MkdirAll(filepath.Dir(dir+name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dir+name, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &API{muxHTTPServer: http.NewServeMux(), websocket: websocket.Init("", nil), staticPath: dir, tld: "bridge.test"}
}

func siteGet(api *API, method, target string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Host = "bridge.test"
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	api.serveSite(rec, req)
	return rec
}

var nonceAttr = regexp.MustCompile(`<(?:script|style) nonce="([A-Za-z0-9_-]+)">`)

// checkPage checks a rendered page's headers: HTML, its language, and a
// policy whose nonce is the one the page's style and scripts carry.
func checkPage(t *testing.T, what string, rec *httptest.ResponseRecorder, status int, lang string) {
	t.Helper()
	body := rec.Body.String()
	if rec.Code != status {
		t.Errorf("%s: status %d, want %d", what, rec.Code, status)
	}
	if strings.Contains(body, "STALE") {
		t.Errorf("%s: served a stale copy from the static directory", what)
	}
	h := rec.Header()
	if !strings.HasPrefix(h.Get("Content-Type"), "text/html") {
		t.Errorf("%s: Content-Type %q", what, h.Get("Content-Type"))
	}
	if l, _ := site.Language(lang); h.Get("Content-Language") != l.Tag {
		t.Errorf("%s: Content-Language %q, want %q", what, h.Get("Content-Language"), l.Tag)
	}
	m := nonceAttr.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("%s: no style or script with a nonce", what)
	}
	csp := h.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'nonce-"+m[1]+"'") || !strings.Contains(csp, "style-src 'self' 'nonce-"+m[1]+"'") || !strings.Contains(csp, "default-src 'none'") ||
		strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("%s: policy %q doesn't match the page's nonce %q", what, csp, m[1])
	}
	if h.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("%s: no nosniff", what)
	}
}

func TestSiteRootServesEnglishOrRedirects(t *testing.T) {
	api := siteAPI(t)
	for _, al := range []string{"", "en-US,en;q=0.9", "en", "ja-JP", "*", "garbage;;q=x"} {
		if i18n.Match(al) != site.English() {
			t.Fatalf("%q matches %s", al, i18n.Match(al))
		}
		rec := siteGet(api, http.MethodGet, "/", map[string]string{"Accept-Language": al})
		checkPage(t, "/ with "+al, rec, http.StatusOK, site.English())
		if !strings.Contains(rec.Body.String(), "Your photos, your rules.") {
			t.Errorf("/ with %q: not the English landing page", al)
		}
		if rec.Header().Get("Vary") != "Accept-Language" {
			t.Errorf("/ with %q: Vary %q", al, rec.Header().Get("Vary"))
		}
	}
	// Every other language this build carries is a redirect from /, with
	// the query kept (DRAFT builds carry them; a shipping build may not).
	for _, l := range i18n.Languages()[1:] {
		rec := siteGet(api, http.MethodGet, "/?utm=x", map[string]string{"Accept-Language": l.Tag + ",en;q=0.5"})
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/"+l.Code+"/?utm=x" {
			t.Errorf("/ for %s: %d %q", l.Tag, rec.Code, rec.Header().Get("Location"))
		}
		if rec.Header().Get("Vary") != "Accept-Language" {
			t.Errorf("/ for %s: the redirect has no Vary: Accept-Language", l.Tag)
		}
		page := siteGet(api, http.MethodGet, "/"+l.Code+"/", nil)
		checkPage(t, "/"+l.Code+"/", page, http.StatusOK, l.Code)
		if !strings.Contains(page.Body.String(), `<html lang="`+l.Tag+`">`) {
			t.Errorf("/%s/ isn't in %s", l.Code, l.Tag)
		}
	}
}

func TestSiteLanguagePaths(t *testing.T) {
	api := siteAPI(t)
	// /en/ is English without the redirect: a browser preferring another
	// language reaches it from the switcher.
	rec := siteGet(api, http.MethodGet, "/en/", map[string]string{"Accept-Language": "es,fr;q=0.8"})
	checkPage(t, "/en/", rec, http.StatusOK, "en")
	if !strings.Contains(rec.Body.String(), `<link rel="canonical" href="https://bridge.test/">`) {
		t.Error("/en/ doesn't name / as its canonical address")
	}

	redirects := map[string]string{
		"/en":               "/en/",
		"/en?x=1":           "/en/?x=1",
		"/landing":          "/",
		"/landing.html":     "/",
		"/landing.html?a=b": "/?a=b",
	}
	for target, want := range redirects {
		rec := siteGet(api, http.MethodGet, target, nil)
		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != want {
			t.Errorf("%s: %d %q, want 301 %q", target, rec.Code, rec.Header().Get("Location"), want)
		}
	}

	notFound := []string{"/xx/", "/xx", "/zzz/", "/en/privacy", "/en/x/y", "/img/", "/img"}
	if _, ok := site.Language("es"); !ok {
		notFound = append(notFound, "/es/", "/es")
	}
	for _, target := range notFound {
		if rec := siteGet(api, http.MethodGet, target, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", target, rec.Code)
		}
	}

	// What stays in the static directory is still served from there.
	for target, want := range map[string]string{
		"/privacy":        "the privacy notice",
		"/img/device.jpg": "a photo",
	} {
		if rec := siteGet(api, http.MethodGet, target, nil); rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("%s: %d %q, want %q", target, rec.Code, rec.Body.String(), want)
		}
	}

	if rec := siteGet(api, http.MethodPost, "/", nil); rec.Code != http.StatusMethodNotAllowed || strings.Contains(rec.Body.String(), "STALE") {
		t.Errorf("POST /: %d %q", rec.Code, rec.Body.String())
	}
	if rec := siteGet(api, http.MethodHead, "/", nil); rec.Code != http.StatusOK {
		t.Errorf("HEAD /: %d", rec.Code)
	}
}

// The device pages, from the binary: on a device's address (serveOwnPage,
// with its status) and, for a look, on the bridge's host.
func TestSiteDevicePages(t *testing.T) {
	api := siteAPI(t)
	for _, target := range []string{"/disabled", "/disabled.html", "/unavailable", "/unavailable.html"} {
		rec := siteGet(api, http.MethodGet, target, map[string]string{"Accept-Language": "en"})
		checkPage(t, target, rec, http.StatusOK, "en")
		if rec.Header().Get("Vary") != "Accept-Language" {
			t.Errorf("%s: no Vary: Accept-Language", target)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/photos", nil)
	req.Host = "pit.bridge.test"
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Accept-Language", "de-DE")
	rec := httptest.NewRecorder()
	api.serveOwnPage(rec, req, site.Disabled, http.StatusServiceUnavailable)
	checkPage(t, "disabled on a device's address", rec, http.StatusServiceUnavailable, i18n.Match("de-DE"))
	if !strings.Contains(rec.Body.String(), "<h1>") {
		t.Error("the disabled page has no heading")
	}
}
