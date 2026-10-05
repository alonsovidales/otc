// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestParseKinds(t *testing.T) {
	k := parseKinds(strings.NewReader("# header\n84\tmajor\t1.0\n85\tminor\t1.1\n86\tcritical\t2.0\n87\tweird\t2.1\nbad line\n"))
	if k[84].kind != KindMajor || k[84].label != "1.0" || k[86].kind != KindCritical || k[87].kind != KindMinor {
		t.Fatalf("parsed %+v", k)
	}
}

// The alert is the most important pending kind, pointing at the newest
// version; only minor ones pending is no alert at all.
func TestAlertFor(t *testing.T) {
	info := &Info{LatestVersion: 87, LatestLabel: "2.1", Pending: []Release{
		{Version: 85, Kind: KindMinor}, {Version: 86, Kind: KindCritical, Description: "Fixes the bridge"}, {Version: 87, Kind: KindMajor},
	}}
	a := alertFor(info)
	if a == nil || a.Level != KindCritical || a.Version != "2.1" || a.Summary != "Fixes the bridge" {
		t.Fatalf("alert %+v", a)
	}
	if alertFor(&Info{Pending: []Release{{Version: 85, Kind: KindMinor}}}) != nil {
		t.Fatal("a minor update raised an alert")
	}
}

// Only the release key's signature counts.
func TestKindsSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	body := []byte("86\tcritical\t2.0\n")
	sig := ed25519.Sign(priv, body)
	if !ed25519.Verify(pub, body, sig) {
		t.Fatal("own signature")
	}
	if ed25519.Verify(pub, []byte("86\tcritical\t9.0\n"), sig) {
		t.Fatal("an altered file verified")
	}
	if _, err := releaseKey(); err != nil {
		t.Fatalf("the embedded release key doesn't parse: %v", err)
	}
}

// The manifest and the release kinds are trusted only signed: a valid
// signature passes, an altered body or a malformed signature doesn't.
func TestVerifySigned(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	body := []byte("92\t-\tabc\tA release\tdef\n")
	sig := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, body)) + "\n")
	if err := verifySigned(pub, body, sig); err != nil {
		t.Fatalf("a signed body was refused: %v", err)
	}
	if err := verifySigned(pub, []byte("92\t-\tabc\tPhishing text\tdef\n"), sig); !errors.Is(err, errNotSigned) {
		t.Fatalf("an altered body: %v", err)
	}
	if err := verifySigned(pub, body, []byte("not base64!")); !errors.Is(err, errNotSigned) {
		t.Fatalf("a malformed signature: %v", err)
	}
}

// What is committed verifies with the key devices embed - the same check
// Check now makes against main.
func TestCommittedManifestsAreSigned(t *testing.T) {
	pub, err := parseReleaseKey([]byte(cEmbeddedReleaseKey))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"VERSIONS", "RELEASES"} {
		body, err := os.ReadFile("../scripts/updates/" + name)
		if err != nil {
			t.Fatal(err)
		}
		sig, err := os.ReadFile("../scripts/updates/" + name + ".sig")
		if err != nil {
			t.Fatal(err)
		}
		if err := verifySigned(pub, body, sig); err != nil {
			t.Errorf("scripts/updates/%s: %v", name, err)
		}
	}
}
