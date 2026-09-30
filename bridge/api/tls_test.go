// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Issue #161: plain HTTP only sends people to HTTPS.
func TestPort80OnlyRedirects(t *testing.T) {
	served := false
	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { served = true })
	h := redirectToHTTPS("/healthy", mux)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "http://cala.off-the.cloud:80/admin/api/login?x=1", nil))
	if w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "https://cala.off-the.cloud/admin/api/login?x=1" {
		t.Fatalf("got %d %q", w.Code, w.Header().Get("Location"))
	}
	if served {
		t.Fatal("the site answered over plain HTTP")
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://off-the.cloud/healthy", nil))
	if !served {
		t.Error("the health check didn't reach the mux")
	}
}

func TestHSTS(t *testing.T) {
	w := httptest.NewRecorder()
	withHSTS(http.NotFoundHandler()).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Header().Get("Strict-Transport-Security") != cHSTS {
		t.Fatalf("no HSTS: %q", w.Header().Get("Strict-Transport-Security"))
	}
}

func writeCert(t *testing.T, dir, cn string) (string, string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	kder, _ := x509.MarshalECPrivateKey(key)
	cp, kp := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	os.WriteFile(cp, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(kp, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600)
	return cp, kp
}

// A renewed certificate is picked up without a restart.
func TestCertificateIsReloadedWhenRenewed(t *testing.T) {
	dir := t.TempDir()
	cp, kp := writeCert(t, dir, "old")
	c := &certReloader{certFile: cp, keyFile: kp}
	first, err := c.get(nil)
	if err != nil {
		t.Fatal(err)
	}
	writeCert(t, dir, "new")
	later := time.Now().Add(time.Minute)
	os.Chtimes(cp, later, later)
	c.checked = time.Time{} // as if the minute had passed
	second, err := c.get(nil)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("the renewed certificate was not loaded")
	}
	leaf, _ := x509.ParseCertificate(second.Certificate[0])
	if leaf.Subject.CommonName != "new" {
		t.Errorf("serving %q", leaf.Subject.CommonName)
	}
}
