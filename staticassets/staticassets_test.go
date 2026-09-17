// SPDX-License-Identifier: AGPL-3.0-or-later

package staticassets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveServesExistingFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello world"), 0644); err != nil {
		t.Fatalf("writing fixture file: %v", err)
	}

	path, err := Resolve(dir+"/", "/hello.txt")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading resolved path: %v", err)
	}
	if string(content) != "hello world" {
		t.Errorf("expected %q, got %q", "hello world", content)
	}
}

func TestResolveAppendsHTMLForExtensionLessPaths(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "social.html"), []byte("<html>social</html>"), 0644); err != nil {
		t.Fatalf("writing fixture file: %v", err)
	}

	path, err := Resolve(dir+"/", "/social")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if filepath.Base(path) != "social.html" {
		t.Errorf("expected the client-side route to resolve to social.html, got %q", path)
	}
}

func TestResolveDoesNotAppendHTMLForFilesWithExtensions(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "logo.png"), []byte("fake-png-bytes"), 0644); err != nil {
		t.Fatalf("writing fixture file: %v", err)
	}

	path, err := Resolve(dir+"/", "/logo.png")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if filepath.Base(path) != "logo.png" {
		t.Errorf("expected the actual asset to be resolved untouched, got %q", path)
	}
}

func TestResolveFallsBackToIndexForUnknownPaths(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>spa shell</html>"), 0644); err != nil {
		t.Fatalf("writing fixture file: %v", err)
	}

	// Neither a real file nor something the ".html" guess would match -
	// same bucket as a client-side route with no exact fixture here, a
	// captive-portal probe path, or a plain typo.
	path, err := Resolve(dir+"/", "/this/does/not/exist.xyz")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if filepath.Base(path) != "index.html" {
		t.Errorf("expected the SPA shell fallback, got %q", path)
	}
}

func TestResolveRejectsPathTraversal(t *testing.T) {
	outsideDir := t.TempDir()
	secret := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(secret, []byte("do not leak me"), 0644); err != nil {
		t.Fatalf("writing fixture file: %v", err)
	}

	staticDir := t.TempDir()

	if _, err := Resolve(staticDir+"/", "/../"+filepath.Base(outsideDir)+"/secret.txt"); err != ErrInvalidPath {
		t.Fatalf("expected ErrInvalidPath for a traversal attempt, got %v", err)
	}
}

func TestResolveRejectsNonExistentRoot(t *testing.T) {
	if _, err := Resolve("/this/does/not/exist-at-all/", "/index.html"); err != ErrInvalidPath {
		t.Fatalf("expected ErrInvalidPath when even the index.html fallback doesn't exist, got %v", err)
	}
}
