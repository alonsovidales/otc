// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"crypto/ed25519"
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
