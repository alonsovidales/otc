// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/bridge/site"
	"github.com/alonsovidales/otc/i18n"
)

// On the bridge's own host an address that names no file is a 404, never
// the web app's shell an older deploy left in the static directory (a
// 200 that search engines index as another copy of the site).
func TestApexUnknownPathsAreNotFound(t *testing.T) {
	api := siteAPI(t)
	for _, target := range []string{"/nope", "/robots", "/some/client/route", "/index.html", "/index",
		"/foo.xyz", "/img/missing.jpg", "/google-photos-alternative/", "/assets/index-AAAAAAAA.js", "/fonts/"} {
		rec := siteGet(api, http.MethodGet, target, nil)
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "web app shell") {
			t.Errorf("%s: %d %q, want 404", target, rec.Code, rec.Body.String())
		}
	}
	// The same once the stale shell is gone.
	if err := os.Remove(api.staticPath + "index.html"); err != nil {
		t.Fatal(err)
	}
	if rec := siteGet(api, http.MethodGet, "/nope", nil); rec.Code != http.StatusNotFound {
		t.Errorf("/nope without an index.html: %d", rec.Code)
	}
}

// Every file of bridge/static is still served on the bridge's own host,
// as it is (the privacy, terms and admin pages at their addresses without
// .html too), with the caching its kind allows: a font named by its
// content for good, a page never without asking, the rest a day.
func TestApexServesEveryStaticFile(t *testing.T) {
	static, err := filepath.Abs(filepath.Join("..", "static"))
	if err != nil {
		t.Fatal(err)
	}
	api := &API{muxHTTPServer: http.NewServeMux(), staticPath: static + "/", tld: "bridge.test"}
	served := 0
	err = filepath.WalkDir(static, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, static))
		// .gitignore isn't the site's; index.html is the old web app's
		// shell, which git no longer tracks and the bridge never serves.
		if strings.HasPrefix(filepath.Base(rel), ".") || rel == "/index.html" {
			return nil
		}
		want, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		targets := []string{rel}
		if strings.HasSuffix(rel, ".html") && !strings.Contains(rel[1:], "/") {
			targets = append(targets, strings.TrimSuffix(rel, ".html"))
		}
		for _, target := range targets {
			rec := siteGet(api, http.MethodGet, target, nil)
			if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), want) {
				t.Errorf("%s: %d, %d bytes, want 200 with %d", target, rec.Code, rec.Body.Len(), len(want))
			}
			cc := "public, max-age=86400"
			switch {
			case strings.HasSuffix(rel, ".woff2"):
				cc = "public, max-age=31536000, immutable"
			case strings.HasSuffix(rel, ".html"):
				cc = "no-cache"
			}
			if got := rec.Header().Get("Cache-Control"); got != cc {
				t.Errorf("%s: Cache-Control %q, want %q", target, got, cc)
			}
			if rec.Header().Get("Last-Modified") == "" {
				t.Errorf("%s: no Last-Modified", target)
			}
			served++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range []string{"/privacy", "/terms", "/admin"} {
		if rec := siteGet(api, http.MethodGet, page, nil); rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Errorf("%s: %d %q", page, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	if served < 20 {
		t.Errorf("only %d files served", served)
	}
}

// A font is cached for good only because its name changes with its
// content: the name ends with the first 8 hex digits of its SHA-256.
func TestFontNamesCarryTheirHash(t *testing.T) {
	fonts, err := filepath.Glob(filepath.Join("..", "static", "fonts", "*.woff2"))
	if err != nil || len(fonts) == 0 {
		t.Fatalf("no fonts: %v", err)
	}
	for _, f := range fonts {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		if want := "-" + hex.EncodeToString(sum[:])[:8] + ".woff2"; !strings.HasSuffix(f, want) || !cFontPath.MatchString("/fonts/"+filepath.Base(f)) {
			t.Errorf("%s: its name doesn't end with its hash, %s", f, want)
		}
	}
}

// robots.txt, sitemap.xml and llms.txt: their types, what they say, HEAD
// like GET, and GET and HEAD only.
func TestCrawlerFiles(t *testing.T) {
	api := siteAPI(t)
	base := "https://bridge.test"
	sitemap, err := site.Sitemap(base)
	if err != nil {
		t.Fatal(err)
	}
	for target, want := range map[string]struct {
		ct   string
		body []byte
	}{
		"/robots.txt":  {"text/plain; charset=utf-8", site.Robots(base)},
		"/sitemap.xml": {"application/xml; charset=utf-8", sitemap},
		"/llms.txt":    {"text/plain; charset=utf-8", site.LLMs(base)},
	} {
		rec := siteGet(api, http.MethodGet, target, nil)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != want.ct || !bytes.Equal(rec.Body.Bytes(), want.body) {
			t.Errorf("%s: %d %q\n%s", target, rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
		}
		if rec.Header().Get("X-Robots-Tag") != "" {
			t.Errorf("%s: X-Robots-Tag on the bridge's own host", target)
		}
		head := siteGet(api, http.MethodHead, target, nil)
		if head.Code != http.StatusOK || head.Header().Get("Content-Length") != rec.Header().Get("Content-Length") || head.Body.Len() != 0 {
			t.Errorf("HEAD %s: %d, Content-Length %q (GET %q), %d bytes", target, head.Code,
				head.Header().Get("Content-Length"), rec.Header().Get("Content-Length"), head.Body.Len())
		}
		if post := siteGet(api, http.MethodPost, target, nil); post.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s: %d", target, post.Code)
		}
	}
	robots := siteGet(api, http.MethodGet, "/robots.txt", nil).Body.String()
	if robots != "User-agent: *\nDisallow: /api/\n\nSitemap: https://bridge.test/sitemap.xml\n" || strings.Contains(robots, "/admin") {
		t.Errorf("robots.txt:\n%s", robots)
	}
	var set struct {
		URLs []struct {
			Loc string `xml:"loc"`
		} `xml:"url"`
	}
	if err := xml.Unmarshal(siteGet(api, http.MethodGet, "/sitemap.xml", nil).Body.Bytes(), &set); err != nil || len(set.URLs) != 4*len(i18n.Languages())+2 {
		t.Errorf("sitemap: %v, %d addresses", err, len(set.URLs))
	}
	if llms := siteGet(api, http.MethodGet, "/llms.txt", nil).Body.String(); !strings.HasPrefix(llms, "# Off The Cloud\n\n> ") || !strings.Contains(llms, "## Questions") {
		t.Errorf("llms.txt:\n%s", llms)
	}
}

func TestAcceptsGzip(t *testing.T) {
	for header, want := range map[string]bool{
		"gzip": true, "GZIP": true, "x-gzip": true, "gzip, deflate, br, zstd": true, "br;q=1.0, gzip;q=0.5": true,
		"*": true, "br, *;q=0.1": true,
		"": false, "identity": false, "deflate, br": false, "gzip;q=0": false, "gzip; q=0.000": false,
		"*;q=0": false, "gzip;q=0, *": false, "gzip;q=x": false,
	} {
		if got := acceptsGzip(header); got != want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", header, got, want)
		}
	}
}

var nonceValue = regexp.MustCompile(`nonce="[^"]*"`)

// The pages and the crawlers' files go out gzipped to a client that takes
// it, with the compressed length and Vary: Accept-Encoding; HEAD answers
// the same headers without a body.
func TestSiteAnswersAreGzipped(t *testing.T) {
	api := siteAPI(t)
	gz := map[string]string{"Accept-Encoding": "gzip, deflate, br"}
	for _, target := range []string{"/", "/es/", "/account", "/private-social-network", "/google-photos-alternative", "/google-drive-alternative", "/robots.txt", "/sitemap.xml", "/llms.txt", "/unavailable"} {
		plain := siteGet(api, http.MethodGet, target, nil)
		rec := siteGet(api, http.MethodGet, target, gz)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" || plain.Header().Get("Content-Encoding") != "" {
			t.Errorf("%s: %d, Content-Encoding %q (plain %q)", target, rec.Code, rec.Header().Get("Content-Encoding"), plain.Header().Get("Content-Encoding"))
			continue
		}
		if !strings.Contains(strings.Join(rec.Header().Values("Vary"), ","), "Accept-Encoding") {
			t.Errorf("%s: Vary %q", target, rec.Header().Values("Vary"))
		}
		if rec.Header().Get("Content-Length") != strconv.Itoa(rec.Body.Len()) {
			t.Errorf("%s: Content-Length %q for %d bytes", target, rec.Header().Get("Content-Length"), rec.Body.Len())
		}
		zr, err := gzip.NewReader(rec.Body)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(zr)
		if err != nil {
			t.Fatal(err)
		}
		// The same answer, but for the nonce each response draws.
		if !bytes.Equal(nonceValue.ReplaceAll(got, nil), nonceValue.ReplaceAll(plain.Body.Bytes(), nil)) {
			t.Errorf("%s: the gzipped answer isn't the plain one", target)
		}
		if strings.HasSuffix(target, "/") && rec.Body.Len()*3 > len(got) {
			t.Errorf("%s: %d bytes gzipped from %d", target, rec.Body.Len(), len(got))
		}
		head := siteGet(api, http.MethodHead, target, gz)
		if head.Code != http.StatusOK || head.Header().Get("Content-Encoding") != "gzip" || head.Header().Get("Content-Length") == "" || head.Body.Len() != 0 {
			t.Errorf("HEAD %s: %d %q %q, %d bytes", target, head.Code, head.Header().Get("Content-Encoding"), head.Header().Get("Content-Length"), head.Body.Len())
		}
	}
	// A client that refuses gzip gets the page as it is.
	if rec := siteGet(api, http.MethodGet, "/", map[string]string{"Accept-Encoding": "gzip;q=0, br"}); rec.Header().Get("Content-Encoding") != "" {
		t.Error("gzip sent to a client that refused it")
	}
}

// The comparison pages, at /<slug> and /<code>/<slug>, chosen like the
// landing page's language; any other /<code>/<x> is still a 404.
func TestSiteComparisonPages(t *testing.T) {
	api := siteAPI(t)
	for _, slug := range []string{site.SocialSlug, site.AlternativeSlug, site.DriveSlug} {
		for _, al := range []string{"", "en-US,en;q=0.9", "ja-JP"} {
			rec := siteGet(api, http.MethodGet, "/"+slug, map[string]string{"Accept-Language": al})
			checkPage(t, "/"+slug+" with "+al, rec, http.StatusOK, site.English())
			body := rec.Body.String()
			if !strings.Contains(body, `<link rel="canonical" href="https://bridge.test/`+slug+`">`) || rec.Header().Get("Vary") != "Accept-Language" {
				t.Errorf("/%s with %q: not the English page, or Vary %q", slug, al, rec.Header().Get("Vary"))
			}
		}
		for _, l := range i18n.Languages()[1:] {
			rec := siteGet(api, http.MethodGet, "/"+slug+"?utm=x", map[string]string{"Accept-Language": l.Tag + ",en;q=0.5"})
			if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/"+l.Code+"/"+slug+"?utm=x" || rec.Header().Get("Vary") != "Accept-Language" {
				t.Errorf("/%s for %s: %d %q", slug, l.Tag, rec.Code, rec.Header().Get("Location"))
			}
			page := siteGet(api, http.MethodGet, "/"+l.Code+"/"+slug, nil)
			checkPage(t, "/"+l.Code+"/"+slug, page, http.StatusOK, l.Code)
			if !strings.Contains(page.Body.String(), `<html lang="`+l.Tag+`">`) || !strings.Contains(page.Body.String(), `href="/`+l.Code+`/#faq"`) {
				t.Errorf("/%s/%s isn't in %s, or its header doesn't link /%s/#faq", l.Code, slug, l.Tag, l.Code)
			}
		}
		rec := siteGet(api, http.MethodGet, "/en/"+slug, map[string]string{"Accept-Language": "es,fr;q=0.8"})
		checkPage(t, "/en/"+slug, rec, http.StatusOK, "en")
		if !strings.Contains(rec.Body.String(), `<link rel="canonical" href="https://bridge.test/`+slug+`">`) {
			t.Errorf("/en/%s doesn't name /%s as its canonical address", slug, slug)
		}
		for _, target := range []string{"/xx/" + slug, "/en/" + slug + "/", "/es/" + slug + "/x",
			"/es/google-photos", "/es/alternative", "/es/private-social", "/en/x"} {
			if rec := siteGet(api, http.MethodGet, target, nil); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "STALE") {
				t.Errorf("%s: %d, want 404", target, rec.Code)
			}
		}
		if rec := siteGet(api, http.MethodPost, "/"+slug, nil); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST /%s: %d", slug, rec.Code)
		}
		if rec := siteGet(api, http.MethodHead, "/es/"+slug, nil); rec.Code != http.StatusOK || rec.Body.Len() != 0 {
			t.Errorf("HEAD /es/%s: %d", slug, rec.Code)
		}
	}
}

// www.<tld> is a permanent redirect to the bridge's own host, path and
// query kept, whatever the method; nothing else is touched.
func TestWWWGoesToTheApex(t *testing.T) {
	api := &API{tld: "bridge.test"}
	reached := false
	h := api.wwwToApex(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
		for _, host := range []string{"www.bridge.test", "WWW.Bridge.Test"} {
			req := httptest.NewRequest(method, "/es/google-photos-alternative?utm=a&b=%2F", nil)
			req.Host = host
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "https://bridge.test/es/google-photos-alternative?utm=a&b=%2F" {
				t.Errorf("%s %s: %d %q", method, host, rec.Code, rec.Header().Get("Location"))
			}
		}
	}
	if reached {
		t.Error("a www request reached the site")
	}
	for _, host := range []string{"bridge.test", "pit.bridge.test", "wwwx.bridge.test"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = host
		reached = false
		h.ServeHTTP(httptest.NewRecorder(), req)
		if !reached {
			t.Errorf("%s didn't reach the site", host)
		}
	}
}

// Every answer for a device's address says noindex - its web app, the
// disabled and unavailable pages, anything else - and none for the
// bridge's own host.
func TestDeviceHostsAreNoindex(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	api := siteAPI(t)
	api.dao = dao.NewWithDB(db)
	device := fakeStaticDevice(map[string]string{"/": "text/html; charset=utf-8"})
	api.forwardOneOff = func(domain string, frame []byte) ([]byte, error) {
		if domain == "gone.bridge.test" {
			return nil, errors.New("no connection")
		}
		return device(domain, frame)
	}
	api.registerAPIs()
	get := func(host, target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Host = host
		req.Header.Set("Accept", "text/html")
		rec := httptest.NewRecorder()
		api.handler().ServeHTTP(rec, req)
		return rec
	}
	disabled := func(host string, is bool) {
		mock.ExpectQuery("select `disabled` from `devices` where `domain` = \\?").WithArgs(host).
			WillReturnRows(sqlmock.NewRows([]string{"disabled"}).AddRow(is))
	}
	disabled("pit.bridge.test", false)
	disabled("demo.bridge.test", false)
	disabled("off.bridge.test", true)
	disabled("gone.bridge.test", false)
	for _, c := range []struct {
		host, target string
		status       int
	}{
		{"pit.bridge.test", "/", http.StatusOK},
		{"demo.bridge.test", "/", http.StatusOK},
		{"off.bridge.test", "/", http.StatusServiceUnavailable},
		{"gone.bridge.test", "/", http.StatusServiceUnavailable},
		{"pit.bridge.test", cHealtyPath, http.StatusOK},
	} {
		rec := get(c.host, c.target)
		if rec.Code != c.status || rec.Header().Get("X-Robots-Tag") != "noindex" {
			t.Errorf("%s%s: %d, X-Robots-Tag %q", c.host, c.target, rec.Code, rec.Header().Get("X-Robots-Tag"))
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
	for _, target := range []string{"/", "/es/", "/privacy", "/robots.txt", "/img/device.jpg", "/nope"} {
		if rec := get("bridge.test", target); rec.Header().Get("X-Robots-Tag") != "" {
			t.Errorf("bridge.test%s: X-Robots-Tag %q", target, rec.Header().Get("X-Robots-Tag"))
		}
	}
}
