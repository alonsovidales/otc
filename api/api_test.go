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
	if rec.Code != http.StatusOK {
		// The handler returns early without writing a status, which the
		// ResponseWriter reports as 200; documenting the current behavior
		// here so a change to it is a deliberate, visible one.
		t.Errorf("expected the default 200 with empty body for a rejected path, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("expected an empty body for a rejected path, got %q", rec.Body.String())
	}
}
