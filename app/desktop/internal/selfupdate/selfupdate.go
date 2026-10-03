// SPDX-License-Identifier: AGPL-3.0-or-later

// Package selfupdate keeps otc-sync current: it reads the desktop release
// manifest (desktop.json in the "desktop" GitHub release), checks its
// signature with the project's release key - the same Ed25519 key device
// updates are signed with (issue #160) - and replaces this program with the
// build for this platform when a newer one is published, after checking its
// SHA-256 against the signed manifest. The macOS app updates itself the
// same way (Sparkle), from the same release.
package selfupdate

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ManifestURL is the signed desktop manifest; DESKTOP_MANIFEST overrides it
// (tests, forks).
var ManifestURL = "https://github.com/alonsovidales/otc/releases/download/desktop/desktop.json"

const releaseKey = `-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAtVgLIKBzcqMNM2nUnK9xfgpqWrLTuZsk8ylhyI0BK9g=
-----END PUBLIC KEY-----`

// Manifest is desktop.json.
type Manifest struct {
	Version string          `json:"version"`
	Notes   string          `json:"notes"`
	Files   map[string]File `json:"files"`
}

// File is one platform's build.
type File struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// Update is a newer build for this platform.
type Update struct {
	Version string
	Notes   string
	File    File
}

// Platform is this build's key in the manifest ("linux-amd64", ...).
func Platform() string { return runtime.GOOS + "-" + runtime.GOARCH }

var client = &http.Client{Timeout: 30 * time.Second}

func get(url string, limit int64) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func publicKey() (ed25519.PublicKey, error) {
	block, _ := pem.Decode([]byte(releaseKey))
	if block == nil {
		return nil, errors.New("no release key")
	}
	k, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	pub, ok := k.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("the release key is not Ed25519")
	}
	return pub, nil
}

// Fetch downloads the manifest and checks its signature.
func Fetch() (*Manifest, error) {
	url := ManifestURL
	if u := os.Getenv("DESKTOP_MANIFEST"); u != "" {
		url = u
	}
	body, err := get(url, 1<<20)
	if err != nil {
		return nil, err
	}
	sigB64, err := get(url+".sig", 4096)
	if err != nil {
		return nil, err
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigB64)))
	if err != nil {
		return nil, fmt.Errorf("the manifest's signature is malformed: %w", err)
	}
	pub, err := publicKey()
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(pub, body, sig) {
		return nil, errors.New("the desktop manifest is not signed with the release key")
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// Check answers the update for this platform newer than current, nil when
// there is none. A development build ("dev") never updates itself.
func Check(current string) (*Update, error) {
	if current == "" || current == "dev" {
		return nil, nil
	}
	m, err := Fetch()
	if err != nil {
		return nil, err
	}
	f, ok := m.Files[Platform()]
	if !ok || Compare(m.Version, current) <= 0 {
		return nil, nil
	}
	return &Update{Version: m.Version, Notes: m.Notes, File: f}, nil
}

// Compare orders dotted versions ("1.10.0" > "1.9.2"); a leading "v" and
// anything after a "-" are ignored.
func Compare(a, b string) int {
	pa, pb := parts(a), parts(b)
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func parts(v string) []int {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v = v[:i]
	}
	var out []int
	for _, p := range strings.Split(v, ".") {
		n, _ := strconv.Atoi(p)
		out = append(out, n)
	}
	return out
}

// Executable is this program's own file, links resolved.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// Apply downloads u, checks it against the signed hash and puts it in this
// program's place. The running program keeps going until restarted: on
// Windows the file in use is renamed aside (<exe>.old, removed by Cleanup
// at the next start), elsewhere the new file is renamed over it.
func Apply(u *Update) error {
	exe, err := Executable()
	if err != nil {
		return err
	}
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".otc-sync-update-*")
	if err != nil {
		if runtime.GOOS != "windows" {
			// Installed system-wide (/usr/local/bin): only root may replace it.
			return fmt.Errorf("can't replace %s (%w) - run `sudo otc-sync update`, or install otc-sync in ~/.local/bin so it can update itself", exe, err)
		}
		return fmt.Errorf("can't write next to %s - download the new version from off-the.cloud instead: %w", exe, err)
	}
	defer os.Remove(tmp.Name())
	resp, err := (&http.Client{Timeout: 15 * time.Minute}).Get(u.File.URL)
	if err != nil {
		tmp.Close()
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		tmp.Close()
		return fmt.Errorf("downloading %s: %s", u.Version, resp.Status)
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, 512<<20)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, u.File.SHA256) {
		return fmt.Errorf("the download of %s does not match its signed hash", u.Version)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		old := exe + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			return err
		}
		if err := os.Rename(tmp.Name(), exe); err != nil {
			_ = os.Rename(old, exe)
			return err
		}
		return nil
	}
	return os.Rename(tmp.Name(), exe)
}

// Cleanup removes what a Windows update left behind.
func Cleanup() {
	if exe, err := Executable(); err == nil {
		_ = os.Remove(exe + ".old")
	}
}
