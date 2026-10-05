// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alonsovidales/otc/log"
)

// Issue #183: a release is minor, major (security, stability, durability)
// or critical (breaks compatibility with the bridge or the apps if not
// installed, or a serious security fix), and has a "major.minor" label.
// Both live in scripts/updates/RELEASES (version<TAB>kind<TAB>label),
// signed like the manifest (RELEASES.sig) - in a file of their own because
// the update runners on devices read VERSIONS by position.

const (
	KindMinor    = "minor"
	KindMajor    = "major"
	KindCritical = "critical"

	cReleaseKeyPath = "/etc/otc/release-signing.pub"
	// The release key (scripts/release-signing.pub), for a device that
	// doesn't have the file yet.
	cEmbeddedReleaseKey = `-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAtVgLIKBzcqMNM2nUnK9xfgpqWrLTuZsk8ylhyI0BK9g=
-----END PUBLIC KEY-----`
)

type releaseMeta struct{ kind, label string }

func kindsURL() string { return repoRaw() + "/scripts/updates/RELEASES" }

func releaseKey() (ed25519.PublicKey, error) {
	raw, err := os.ReadFile(cReleaseKeyPath)
	if err != nil {
		raw = []byte(cEmbeddedReleaseKey)
	}
	return parseReleaseKey(raw)
}

func parseReleaseKey(raw []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("no PEM key")
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

// cMaxBody bounds what a fetch reads: the signed files are a few KB.
const cMaxBody = 4 << 20

func fetchBody(url string) ([]byte, error) {
	client := &http.Client{Timeout: cManifestTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, cMaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > cMaxBody {
		return nil, fmt.Errorf("%s: larger than %d bytes", url, cMaxBody)
	}
	return body, nil
}

// errNotSigned: the file came, but not with a valid release-key signature
// (an altered file - or, for a few minutes after a release, the CDN serving
// a new file next to its old signature).
var errNotSigned = errors.New("not signed with the release key")

// verifySigned checks body against a base64 Ed25519 signature.
func verifySigned(pub ed25519.PublicKey, body, sigB64 []byte) error {
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigB64)))
	if err != nil {
		return fmt.Errorf("%w (malformed signature: %v)", errNotSigned, err)
	}
	if !ed25519.Verify(pub, body, sig) {
		return errNotSigned
	}
	return nil
}

// fetchSigned fetches url and url.sig, and returns the body only when it
// carries the release key's signature - the rule the root runner and the
// installers apply to everything they read from the repository.
func fetchSigned(url string) ([]byte, error) {
	body, err := fetchBody(url)
	if err != nil {
		return nil, err
	}
	sigB64, err := fetchBody(url + ".sig")
	if err != nil {
		return nil, err
	}
	pub, err := releaseKey()
	if err != nil {
		return nil, err
	}
	if err := verifySigned(pub, body, sigB64); err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	return body, nil
}

// fetchKinds reads RELEASES and checks its signature: an unsigned or
// altered file could raise a fake "critical update" banner in every app,
// so it is trusted only signed.
func fetchKinds() (map[int]releaseMeta, error) {
	body, err := fetchSigned(kindsURL())
	if err != nil {
		return nil, err
	}
	return parseKinds(bytes.NewReader(body)), nil
}

func parseKinds(r io.Reader) map[int]releaseMeta {
	out := map[int]releaseMeta{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 3 {
			continue
		}
		v, err := strconv.Atoi(strings.TrimSpace(f[0]))
		if err != nil {
			continue
		}
		kind := strings.TrimSpace(f[1])
		if kind != KindMajor && kind != KindCritical {
			kind = KindMinor
		}
		out[v] = releaseMeta{kind: kind, label: strings.TrimSpace(f[2])}
	}
	return out
}

// rank orders kinds: critical > major > minor.
func rank(kind string) int {
	switch kind {
	case KindCritical:
		return 2
	case KindMajor:
		return 1
	}
	return 0
}

// Alert is the most important update this device is missing, nil when
// everything pending is minor (or nothing is).
type Alert struct {
	Level   string // KindMajor or KindCritical
	Version string // the label it updates to
	Target  int
	Summary string
}

func alertFor(info *Info) *Alert {
	var best *Release
	for i := range info.Pending {
		r := &info.Pending[i]
		if rank(r.Kind) > 0 && (best == nil || rank(r.Kind) >= rank(best.Kind)) {
			best = r
		}
	}
	if best == nil {
		return nil
	}
	label := info.LatestLabel
	if label == "" {
		label = strconv.Itoa(info.LatestVersion)
	}
	return &Alert{Level: best.Kind, Version: label, Target: info.LatestVersion, Summary: best.Description}
}

var (
	alertMu sync.RWMutex
	alert   *Alert
)

// CurrentAlert is the last background check's alert (nil: none).
func CurrentAlert() *Alert {
	alertMu.RLock()
	defer alertMu.RUnlock()
	return alert
}

// cWatchEvery is how often a device checks for updates by itself, and
// cWatchRetry how soon it tries again after a check that failed or could
// not verify the kinds (doubling up to cWatchEvery).
var (
	cWatchEvery = 6 * time.Hour
	cWatchRetry = 5 * time.Minute
)

// nextAlert decides the alert after a check, and whether the check settled
// it (ok) or should be retried soon. A failed check, or kinds that couldn't
// be verified (a brief RELEASES outage, the CDN serving it out of step with
// its signature), says nothing about what is pending: the alert already
// known stays - while its release is still pending - instead of a passing
// glitch clearing a critical banner for hours.
func nextAlert(prev *Alert, info *Info, err error) (*Alert, bool) {
	if err != nil {
		return prev, false
	}
	if !info.KindsVerified {
		if prev != nil && prev.Target > info.CurrentVersion && len(info.Pending) > 0 {
			return prev, false
		}
		return nil, false
	}
	return alertFor(info), true
}

// Watch checks for updates a minute after start and then every
// cWatchEvery, keeping CurrentAlert up to date and calling notify once for
// each major or critical update found - so an owner who never opens
// Settings still hears about it. A check that didn't settle it (no network
// yet after a power cut, say) is retried within minutes, not hours.
func Watch(notify func(*Alert)) {
	time.Sleep(time.Minute)
	notified := map[int]bool{}
	retry := cWatchRetry
	for {
		info, err := Check()
		if err != nil {
			log.Debug("background update check failed:", err)
		}
		a, ok := nextAlert(CurrentAlert(), info, err)
		alertMu.Lock()
		alert = a
		alertMu.Unlock()
		if ok && a != nil && !notified[a.Target] && notify != nil {
			notified[a.Target] = true
			notify(a)
		}
		if ok {
			retry = cWatchRetry
			time.Sleep(cWatchEvery)
			continue
		}
		time.Sleep(retry)
		retry = min(retry*2, cWatchEvery)
	}
}
