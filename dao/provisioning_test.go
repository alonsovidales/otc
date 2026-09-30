// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import "testing"

// Issue #172: only names and passwords this device generates reach the
// SQL that can't bind them.
func TestCheckGenerated(t *testing.T) {
	good := "otc_0123456789abcdef0123456789abcdef"
	if err := checkGenerated(good, good, "a1b2c3"); err != nil {
		t.Fatalf("a generated name was refused: %v", err)
	}
	for _, bad := range []string{"otc_x`; drop database otc; --", "otc", "mysql", "otc_0123456789ABCDEF0123456789abcdef"} {
		if checkGenerated(bad, good, "") == nil || checkGenerated(good, bad, "") == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if checkGenerated(good, good, "x' or '1'='1") == nil {
		t.Error("a password with a quote was accepted")
	}
}
