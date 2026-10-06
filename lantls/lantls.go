// SPDX-License-Identifier: AGPL-3.0-or-later

// Package lantls is the device's half of issue #190: the TLS identity the
// apps pin to reach this device on the home network instead of through the
// bridge, and the addresses they reach it at. No CA can vouch for a
// private address, so the certificate is self-signed and an app accepts it
// only by the SHA-256 of its DER bytes, a pin it learnt over its signed-in
// bridge session (GetLocalEndpoint).
package lantls

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// cDir is the identity's folder in the instance's storage path: on the
	// RAID, so a re-image that keeps the data keeps the pin the apps hold.
	cDir      = ".lan-tls"
	cKeyFile  = "key.pem"
	cCertFile = "cert.pem"
	cName     = "otc-lan"
	// cPortOffset puts the TLS port beside the HTTP one: 8080 gets 8443,
	// and a child instance on 8081 gets 8444.
	cPortOffset = 363
)

// Identity is the key and certificate the home-network listener serves.
type Identity struct {
	Cert tls.Certificate
	// Pin is the SHA-256 of the certificate's DER bytes.
	Pin []byte
}

// LoadOrCreate loads the identity kept under storagePath, creating it on
// the first start. One that exists is never replaced, since the apps hold
// its pin: a pair that can't be read is an error (no listener), not a
// reason to make a new one. Removing the folder by hand makes a new pair
// at the next start; the apps then stay on the bridge until their next
// sign-in there hands them the new pin.
func LoadOrCreate(storagePath string) (*Identity, error) {
	if storagePath == "" {
		return nil, errors.New("no storage path to keep the key in")
	}
	dir := filepath.Join(storagePath, cDir)
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		if err := create(storagePath, dir, time.Now()); err != nil {
			return nil, fmt.Errorf("creating the key pair: %w", err)
		}
	} else if err != nil {
		return nil, err
	}
	return load(dir)
}

func load(dir string) (*Identity, error) {
	certPEM, err := os.ReadFile(filepath.Join(dir, cCertFile))
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, cKeyFile))
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("the key pair in %s: %w", dir, err)
	}
	sum := sha256.Sum256(cert.Certificate[0])
	return &Identity{Cert: cert, Pin: sum[:]}, nil
}

// create writes a new pair into a temporary folder and renames it into
// place, so a start that dies half-way leaves no pair rather than half of
// one.
func create(storagePath, dir string, now time.Time) error {
	keyPEM, certPEM, err := newPair(now)
	if err != nil {
		return err
	}
	tmp := dir + ".new"
	// Left by a start that died before the rename: never served.
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	if err := os.Mkdir(tmp, 0o700); err != nil {
		return err
	}
	if err := writeSynced(filepath.Join(tmp, cKeyFile), keyPEM); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	if err := writeSynced(filepath.Join(tmp, cCertFile), certPEM); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	syncDir(tmp)
	if err := os.Rename(tmp, dir); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	syncDir(storagePath)
	return nil
}

func newPair(now time.Time) (keyPEM, certPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cName},
		DNSNames:     []string{cName},
		// An hour back, for a clock a little behind at the first start
		// (the Pi has no clock of its own). The apps check the pin, not
		// the dates.
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(100, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

func writeSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// syncDir is best effort: the pair is complete either way, this only
// makes the rename survive a power cut right after it.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}

// Port is the home-network TLS port: [otc-api] lan-tls-port, or the HTTP
// port + 363 when that is unset.
func Port(configured string, httpPort int) (int, error) {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		if httpPort <= 0 || httpPort+cPortOffset > 65535 {
			return 0, fmt.Errorf("no lan-tls-port, and HTTP port %d has none beside it", httpPort)
		}
		return httpPort + cPortOffset, nil
	}
	p, err := strconv.Atoi(configured)
	if err != nil || p <= 0 || p > 65535 {
		return 0, fmt.Errorf("lan-tls-port %q is not a port", configured)
	}
	return p, nil
}

// Endpoint is the listener GetLocalEndpoint describes, while it serves.
type Endpoint struct {
	port    int
	pin     []byte
	stopped atomic.Bool
}

// NewEndpoint describes a listener serving on port with the certificate
// whose pin is pin.
func NewEndpoint(port int, pin []byte) *Endpoint {
	return &Endpoint{port: port, pin: bytes.Clone(pin)}
}

// Serving is false for a nil Endpoint (no listener was started) and once
// Stop was called.
func (e *Endpoint) Serving() bool { return e != nil && !e.stopped.Load() }

// Stop marks the listener as gone.
func (e *Endpoint) Stop() { e.stopped.Store(true) }

func (e *Endpoint) Port() int { return e.port }

func (e *Endpoint) Pin() []byte { return bytes.Clone(e.pin) }
