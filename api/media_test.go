// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/mediastream"
)

type mediaTestKeys struct{ aead cipher.AEAD }

func (k mediaTestKeys) AEAD() cipher.AEAD                { return k.aead }
func (k mediaTestKeys) Decrypt(b []byte) ([]byte, error) { return nil, errors.New("not used") }

// newMediaServer stores plain as an encrypted library file (as the device
// keeps every file) and mints a token for it under path and mime.
func newMediaServer(t *testing.T, plain []byte, path, mimeType string) (*mediastream.Server, string) {
	t.Helper()
	dir := t.TempDir()
	block, _ := aes.NewCipher(make([]byte, 32))
	aead, _ := cipher.NewGCM(block)
	keys := mediaTestKeys{aead}
	if err := blobstore.WriteBytes(filepath.Join(dir, "h1"), keys, plain); err != nil {
		t.Fatal(err)
	}
	sv := mediastream.NewServer(mediastream.NewStore(), dir, t.TempDir())
	token, _, err := sv.Store().Issue(mediastream.Resource{
		Kind: mediastream.KindLibraryFile, Path: path, Hash: "h1", Mime: mimeType, Size: int64(len(plain)), Keys: keys,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sv, token
}

func getMedia(sv *mediastream.Server, token, query, rng string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/media/"+token+query, nil)
	req.SetPathValue("token", token)
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	rec := httptest.NewRecorder()
	serveMediaFrom(rec, req, sv)
	return rec
}

// Playing is untouched: the type it was stored with, inline, ranges.
func TestServeMediaPlaysInline(t *testing.T) {
	plain := bytes.Repeat([]byte("0123456789"), 1000)
	sv, token := newMediaServer(t, plain, "/Videos/Boda.mp4", "video/mp4")

	rec := getMedia(sv, token, "", "")
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), plain) {
		t.Fatalf("GET: %d, %d bytes", rec.Code, rec.Body.Len())
	}
	if got := rec.Header().Get("Content-Type"); got != "video/mp4" {
		t.Errorf("Content-Type %q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); got != "" {
		t.Errorf("a stream to play has Content-Disposition %q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options %q", got)
	}

	rec = getMedia(sv, token, "", "bytes=10-19")
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "0123456789" {
		t.Errorf("range: %d %q", rec.Code, rec.Body.String())
	}
}

// ?download=1 saves the same bytes under the file's own name, written so
// that any name - accents, quotes, spaces, a percent sign - comes back
// exactly, with an ASCII stand-in for clients without filename*.
func TestServeMediaDownloadIsAnAttachmentNamedAfterTheFile(t *testing.T) {
	plain := bytes.Repeat([]byte{1, 2, 3}, 5000)
	for _, tc := range []struct{ path, ascii string }{
		{"/Videos/IMG_4001.MP4", "IMG_4001.MP4"},
		{`/Videos/Cumpleaños "Leo" 100% 2024.mov`, `Cumplea_os _Leo_ 100_ 2024.mov`},
		{"/Fotos/日本 旅行.mp4", "__ __.mp4"},
		{"/a/b;c=d, e\\f.mp4", "b;c=d, e_f.mp4"},
	} {
		sv, token := newMediaServer(t, plain, tc.path, "video/quicktime")
		rec := getMedia(sv, token, "?download=1", "")
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), plain) {
			t.Fatalf("%s: GET %d, %d bytes", tc.path, rec.Code, rec.Body.Len())
		}
		cd := rec.Header().Get("Content-Disposition")
		disp, params, err := mime.ParseMediaType(cd)
		if err != nil || disp != "attachment" {
			t.Fatalf("%s: Content-Disposition %q: %v", tc.path, cd, err)
		}
		if want := filepath.Base(tc.path); params["filename"] != want {
			t.Errorf("%s: filename* decodes to %q, want %q (%s)", tc.path, params["filename"], want, cd)
		}
		if !strings.Contains(cd, `filename="`+tc.ascii+`"`) {
			t.Errorf("%s: no ASCII filename %q in %q", tc.path, tc.ascii, cd)
		}
		if got := rec.Header().Get("Content-Type"); got != "video/quicktime" {
			t.Errorf("%s: Content-Type %q", tc.path, got)
		}
	}
}

// A browser resuming a download asks for the rest: still the attachment,
// still a 206 of just that span.
func TestServeMediaDownloadKeepsRanges(t *testing.T) {
	plain := bytes.Repeat([]byte("abcdefghij"), 100)
	sv, token := newMediaServer(t, plain, "/v.mp4", "video/mp4")
	rec := getMedia(sv, token, "?download=1", "bytes=990-")
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "abcdefghij" {
		t.Fatalf("range: %d %q", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment;") {
		t.Errorf("Content-Disposition %q", rec.Header().Get("Content-Disposition"))
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 990-999/1000" {
		t.Errorf("Content-Range %q", got)
	}
}

// Whatever a token was minted for, /media never shows a page or an image
// that can run script in the device's origin, and never lets the browser
// guess: anything but media, and any type that isn't exactly one, is
// bytes to save.
func TestServeMediaNeverRendersAnythingButMedia(t *testing.T) {
	html := []byte("<html><script>alert(1)</script></html>")
	for _, m := range []string{"text/html", "image/svg+xml", "application/xhtml+xml", "", "video/mp4,text/html", "video/mp4;,text/html", "video/mp4; x=\"", "text/plain"} {
		sv, token := newMediaServer(t, html, "/x.mp4", m)
		rec := getMedia(sv, token, "", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%q: %d", m, rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
			t.Errorf("%q served as %q", m, got)
		}
		if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") {
			t.Errorf("%q: Content-Disposition %q", m, got)
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%q: X-Content-Type-Options %q", m, got)
		}
	}
}

// A post's media has no path: an attachment with no name, and the
// browser names it (the web app's link says what).
func TestServeMediaDownloadWithoutAName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pubhash"), []byte("video bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	sv := mediastream.NewServer(mediastream.NewStore(), t.TempDir(), dir)
	token, _, _ := sv.Store().Issue(mediastream.Resource{Kind: mediastream.KindPublicationMedia, Hash: "pubhash", Mime: "video/mp4"})
	rec := getMedia(sv, token, "?download=1", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "video bytes" {
		t.Fatalf("GET: %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Disposition"); got != "attachment" {
		t.Errorf("Content-Disposition %q", got)
	}
}

// The token is still the only credential: an unknown one is a flat 404
// with nothing about a file in it, download or not.
func TestServeMediaUnknownTokenIsNotFound(t *testing.T) {
	sv, _ := newMediaServer(t, []byte("x"), "/secret name.mp4", "video/mp4")
	for _, q := range []string{"", "?download=1"} {
		rec := getMedia(sv, "nope", q, "")
		if rec.Code != http.StatusNotFound {
			t.Errorf("%q: %d", q, rec.Code)
		}
		if cd := rec.Header().Get("Content-Disposition"); cd != "" {
			t.Errorf("%q: Content-Disposition %q", q, cd)
		}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/media/x", nil)
	req.SetPathValue("token", "x")
	serveMediaFrom(rec, req, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("no media server: %d", rec.Code)
	}
}
