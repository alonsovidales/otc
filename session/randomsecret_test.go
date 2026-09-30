// SPDX-License-Identifier: AGPL-3.0-or-later

package session

import (
	"encoding/hex"
	"testing"
)

// Issue #157: 32 random bytes, hex - not a UUID.
func TestRandomSecret(t *testing.T) {
	a, b := RandomSecret(), RandomSecret()
	if raw, err := hex.DecodeString(a); err != nil || len(raw) != 32 {
		t.Fatalf("not 32 hex-encoded bytes: %q", a)
	}
	if a == b {
		t.Fatal("two secrets were the same")
	}
}
