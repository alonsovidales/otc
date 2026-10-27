// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// gunzip is body uncompressed, or the test's end.
func gunzip(t *testing.T, body []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCompressible(t *testing.T) {
	for ct, want := range map[string]bool{
		"text/html; charset=utf-8": true, "text/css; charset=utf-8": true, "text/javascript; charset=utf-8": true,
		"application/javascript": true, "application/json": true, "application/manifest+json": true,
		"image/svg+xml": true, "text/plain; charset=utf-8": true, "TEXT/CSS": true, "application/xml": true,
		"image/png": false, "image/jpeg": false, "image/webp": false, "font/woff2": false, "video/mp4": false,
		"application/zip": false, "application/octet-stream": false, "": false,
	} {
		if got := compressible(ct); got != want {
			t.Errorf("compressible(%q) = %v", ct, got)
		}
	}
}

// The text files of the bridge's static directory go out gzipped to a
// client that takes it, with Vary and the compressed length, and still
// answer If-Modified-Since with a 304; pictures, fonts, a Range request
// and a client that refuses gzip get the file as it is.
func TestStaticTextIsGzipped(t *testing.T) {
	static, err := filepath.Abs(filepath.Join("..", "static"))
	if err != nil {
		t.Fatal(err)
	}
	api := &API{muxHTTPServer: http.NewServeMux(), staticPath: static + "/", tld: "bridge.test"}
	gz := map[string]string{"Accept-Encoding": "gzip, br"}
	for target, file := range map[string]string{
		"/fonts/fonts.css": "fonts/fonts.css",
		"/privacy":         "privacy.html",
		"/terms":           "terms.html",
		"/sw.js":           "sw.js",
		"/admin":           "admin.html",
	} {
		want, err := os.ReadFile(filepath.Join(static, file))
		if err != nil {
			t.Fatal(err)
		}
		rec := siteGet(api, http.MethodGet, target, gz)
		h := rec.Header()
		if rec.Code != http.StatusOK || h.Get("Content-Encoding") != "gzip" || !strings.Contains(strings.Join(h.Values("Vary"), ","), "Accept-Encoding") ||
			h.Get("Content-Length") != strconv.Itoa(rec.Body.Len()) || h.Get("Last-Modified") == "" || h.Get("Cache-Control") == "" {
			t.Errorf("%s: %d, headers %v", target, rec.Code, h)
			continue
		}
		if got := gunzip(t, rec.Body.Bytes()); !bytes.Equal(got, want) {
			t.Errorf("%s: the gzipped body isn't the file", target)
		}
		if ct := h.Get("Content-Type"); (strings.HasSuffix(file, ".html") && !strings.HasPrefix(ct, "text/html")) ||
			(strings.HasSuffix(file, ".css") && !strings.HasPrefix(ct, "text/css")) || (strings.HasSuffix(file, ".js") && !strings.Contains(ct, "javascript")) {
			t.Errorf("%s: Content-Type %q", target, ct)
		}
		head := siteGet(api, http.MethodHead, target, gz)
		if head.Code != http.StatusOK || head.Header().Get("Content-Length") != h.Get("Content-Length") || head.Body.Len() != 0 {
			t.Errorf("HEAD %s: %d %q, %d bytes", target, head.Code, head.Header().Get("Content-Length"), head.Body.Len())
		}
		since := siteGet(api, http.MethodGet, target, map[string]string{"Accept-Encoding": "gzip", "If-Modified-Since": h.Get("Last-Modified")})
		if since.Code != http.StatusNotModified || since.Body.Len() != 0 {
			t.Errorf("%s since its Last-Modified: %d, %d bytes", target, since.Code, since.Body.Len())
		}
		for what, header := range map[string]map[string]string{
			"a Range request":           {"Accept-Encoding": "gzip", "Range": "bytes=0-9"},
			"a client refusing gzip":    {"Accept-Encoding": "gzip;q=0, br"},
			"a client asking for none:": nil,
		} {
			rec := siteGet(api, http.MethodGet, target, header)
			if rec.Header().Get("Content-Encoding") != "" || (header["Range"] == "" && !bytes.Equal(rec.Body.Bytes(), want)) {
				t.Errorf("%s for %s: Content-Encoding %q, %d bytes", target, what, rec.Header().Get("Content-Encoding"), rec.Body.Len())
			}
		}
	}
	for _, target := range []string{"/img/device.jpg", "/fonts/manrope-latin-e310b55a.woff2", "/favicon.ico", "/googlebc3c0c0b52da3eb6.html"} {
		rec := siteGet(api, http.MethodGet, target, gz)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "" {
			t.Errorf("%s: %d, Content-Encoding %q", target, rec.Code, rec.Header().Get("Content-Encoding"))
		}
	}
}

// A picture asked for with its version (?v=) is kept 30 days; without it,
// a day; the rest as before.
func TestVersionedPicturesAreCachedLonger(t *testing.T) {
	static, err := filepath.Abs(filepath.Join("..", "static"))
	if err != nil {
		t.Fatal(err)
	}
	api := &API{muxHTTPServer: http.NewServeMux(), staticPath: static + "/", tld: "bridge.test"}
	for target, want := range map[string]string{
		"/img/web-1.jpg?v=2":      "public, max-age=2592000",
		"/img/web-1-900.webp?v=1": "public, max-age=2592000",
		"/img/web-1.jpg":          "public, max-age=86400",
		"/logo.png?v=3":           "public, max-age=2592000",
		"/fonts/fonts.css?v=1":    "public, max-age=86400",
		"/sw.js?v=1":              "public, max-age=86400",
		"/privacy?v=1":            "no-cache",
		"/fonts/unbounded-latin-d7f07f8a.woff2?v=1": "public, max-age=31536000, immutable",
	} {
		rec := siteGet(api, http.MethodGet, target, nil)
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != want {
			t.Errorf("%s: %d, Cache-Control %q, want %q", target, rec.Code, rec.Header().Get("Cache-Control"), want)
		}
	}
}

// The cache of gzipped bodies keeps within its budget, the least recently
// used going first, and never keeps a body over its entry limit.
func TestGzipCacheBudget(t *testing.T) {
	c := newGzipCache(100)
	c.put("a", make([]byte, 40))
	c.put("b", make([]byte, 40))
	if _, ok := c.get("a"); !ok { // a is now the most recently used
		t.Fatal("a not kept")
	}
	c.put("c", make([]byte, 40)) // over 100: b goes
	if _, ok := c.get("b"); ok {
		t.Error("b kept past the budget")
	}
	for _, k := range []string{"a", "c"} {
		if _, ok := c.get(k); !ok {
			t.Errorf("%s dropped", k)
		}
	}
	if c.size != 80 || c.order.Len() != 2 || len(c.items) != 2 {
		t.Errorf("size %d, %d in order, %d items", c.size, c.order.Len(), len(c.items))
	}
	c.put("huge", make([]byte, 101))
	if _, ok := c.get("huge"); ok || c.size != 80 {
		t.Error("a body over the whole budget was kept")
	}
	big := newGzipCache(cGzipCacheBytes)
	big.put("entry", make([]byte, cGzipCacheEntry+1))
	if _, ok := big.get("entry"); ok {
		t.Error("a body over the entry limit was kept")
	}
}
