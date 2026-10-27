// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func newTestAPI(t *testing.T, staticPath string) *API {
	t.Helper()
	api := &API{
		muxHTTPServer: http.NewServeMux(),
		staticPath:    staticPath,
	}
	api.muxHTTPServer.HandleFunc(cHealtyPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("OK"))
	})
	api.muxHTTPServer.HandleFunc("/", api.serveStatic)
	return api
}

func TestHealthcheck(t *testing.T) {
	api := newTestAPI(t, t.TempDir()+"/")

	req := httptest.NewRequest(http.MethodGet, cHealtyPath, nil)
	rec := httptest.NewRecorder()
	api.muxHTTPServer.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
	if rec.Body.String() != "OK" {
		t.Errorf("expected body %q, got %q", "OK", rec.Body.String())
	}
}

func TestServeStaticServesExistingFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello world"), 0644); err != nil {
		t.Fatalf("writing fixture file: %v", err)
	}
	api := newTestAPI(t, dir+"/")

	req := httptest.NewRequest(http.MethodGet, "/hello.txt", nil)
	rec := httptest.NewRecorder()
	api.serveStatic(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "hello world" {
		t.Errorf("expected body %q, got %q", "hello world", rec.Body.String())
	}
}

func TestServeStaticAppendsHTMLForExtensionLessPaths(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "social.html"), []byte("<html>social</html>"), 0644); err != nil {
		t.Fatalf("writing fixture file: %v", err)
	}
	api := newTestAPI(t, dir+"/")

	req := httptest.NewRequest(http.MethodGet, "/social", nil)
	rec := httptest.NewRecorder()
	api.serveStatic(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "<html>social</html>" {
		t.Errorf("expected the client-side route to resolve to social.html, got %q", rec.Body.String())
	}
}

func TestServeStaticDoesNotAppendHTMLForFilesWithExtensions(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "logo.png"), []byte("fake-png-bytes"), 0644); err != nil {
		t.Fatalf("writing fixture file: %v", err)
	}
	api := newTestAPI(t, dir+"/")

	req := httptest.NewRequest(http.MethodGet, "/logo.png", nil)
	rec := httptest.NewRecorder()
	api.serveStatic(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "fake-png-bytes" {
		t.Errorf("expected the actual asset file to be served untouched, got %q", rec.Body.String())
	}
}

func TestServeStaticRejectsPathTraversal(t *testing.T) {
	// A directory the static handler must never be able to reach.
	outsideDir := t.TempDir()
	secret := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(secret, []byte("do not leak me"), 0644); err != nil {
		t.Fatalf("writing fixture file: %v", err)
	}

	staticDir := t.TempDir()
	api := newTestAPI(t, staticDir+"/")

	// Build the request path/URL directly rather than via httptest.NewRequest
	// (which would clean the path) to exercise serveStatic's own ".." guard
	// exactly as net/http.ServeMux would hand it a raw, uncleaned path.
	req := &http.Request{
		Method: http.MethodGet,
		URL:    &url.URL{Path: "/../" + filepath.Base(outsideDir) + "/secret.txt"},
	}
	rec := httptest.NewRecorder()
	api.serveStatic(rec, req)

	if got := rec.Body.String(); got == "do not leak me" {
		t.Fatal("path traversal was not blocked: secret file content was served")
	}
	// Issue #95: serveStatic's own path resolution moved into
	// staticassets.Resolve (shared with the bridge's ReqGetStaticAsset
	// RPC) - a rejected path now answers with a real 404 rather than a
	// silent empty 200.
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for a rejected path, got %d", rec.Code)
	}
}

// The page is always asked again, so an open tab or a reload after a device
// update gets the new web app; Vite's hashed files are kept for good; a
// missing hashed file answered with the SPA's index.html is never kept.
func TestServeStaticCacheHeaders(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"index.html":                "<html>spa shell</html>",
		"social.html":               "<html>social</html>",
		"logo.png":                  "fake-png-bytes",
		"assets/index-Bx3_k9-Q.js":  "console.log(1)",
		"assets/index-D4fGh2Jk.css": "body{}",
		"assets/plain.js":           "console.log(2)",
	} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatalf("making fixture dir: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatalf("writing fixture file: %v", err)
		}
	}
	api := newTestAPI(t, dir+"/")

	const immutable = "public, max-age=31536000, immutable"
	for _, tc := range []struct {
		path, body, cacheControl string
	}{
		{"/", "<html>spa shell</html>", "no-cache"},
		{"/some/client/route", "<html>spa shell</html>", "no-cache"},
		{"/generate_204", "<html>spa shell</html>", "no-cache"},
		{"/social", "<html>social</html>", "no-cache"},
		{"/assets/index-Bx3_k9-Q.js", "console.log(1)", immutable},
		{"/assets/index-D4fGh2Jk.css", "body{}", immutable},
		// Shaped like a hashed file but not there (an older or newer
		// build's): the SPA's index.html, which must not be kept.
		{"/assets/index-Zz9Yy8Xx.js", "<html>spa shell</html>", "no-cache"},
		{"/assets/plain.js", "console.log(2)", ""},
		{"/logo.png", "fake-png-bytes", ""},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		api.muxHTTPServer.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("%s: expected status 200, got %d", tc.path, rec.Code)
			continue
		}
		if got := rec.Body.String(); got != tc.body {
			t.Errorf("%s: expected body %q, got %q", tc.path, tc.body, got)
		}
		if got := rec.Header().Get("Cache-Control"); got != tc.cacheControl {
			t.Errorf("%s: expected Cache-Control %q, got %q", tc.path, tc.cacheControl, got)
		}
	}

	// The page's revalidation: unchanged, a 304 that still says no-cache.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	api.muxHTTPServer.ServeHTTP(rec, req)
	etag, lastModified := rec.Header().Get("ETag"), rec.Header().Get("Last-Modified")
	if etag == "" || lastModified == "" {
		t.Fatalf("expected the page to carry ETag and Last-Modified, got %q and %q", etag, lastModified)
	}
	revalidate := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("If-None-Match", etag)
		req.Header.Set("If-Modified-Since", lastModified)
		rec := httptest.NewRecorder()
		api.muxHTTPServer.ServeHTTP(rec, req)
		return rec
	}
	rec = revalidate()
	if rec.Code != http.StatusNotModified {
		t.Errorf("expected 304 for an unchanged page, got %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("expected the 304 to say no-cache, got %q", got)
	}

	// A new build's page with the old one's file time (a release unpacks
	// its bundle with the times it was built with): served anew.
	index := filepath.Join(dir, "index.html")
	info, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(index, []byte("<html>new build</html>"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(index, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	rec = revalidate()
	if rec.Code != http.StatusOK || rec.Body.String() != "<html>new build</html>" {
		t.Errorf("expected the new page, got %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("ETag"); got == etag || got == "" {
		t.Errorf("expected a new ETag, got %q (was %q)", got, etag)
	}
}

// The app's files are never sniffed into another type nor framed by
// another site (F23 of the 2026-10 review): every answer of the static
// handler says so, the page and the SPA's fallback for an unknown path
// included.
func TestServeStaticSecurityHeaders(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"index.html":               "<html>spa shell</html>",
		"assets/index-Bx3_k9-Q.js": "console.log(1)",
	} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatalf("making fixture dir: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatalf("writing fixture file: %v", err)
		}
	}
	api := newTestAPI(t, dir+"/")
	for _, path := range []string{"/", "/shared", "/assets/index-Bx3_k9-Q.js"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		api.muxHTTPServer.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected status 200, got %d", path, rec.Code)
		}
		for header, want := range map[string]string{
			"X-Content-Type-Options":  "nosniff",
			"Content-Security-Policy": "frame-ancestors 'none'",
			"X-Frame-Options":         "DENY",
		} {
			if got := rec.Header().Get(header); got != want {
				t.Errorf("%s: %s = %q, want %q", path, header, got, want)
			}
		}
	}
}
