// SPDX-License-Identifier: AGPL-3.0-or-later

package selfupdate

import "testing"

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"1.10.0", "1.9.2", 1}, {"v1.2", "1.2.0", 0}, {"1.2.0-3-gabc", "1.2.0", 0}, {"1.2", "1.3", -1}} {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	if _, err := publicKey(); err != nil {
		t.Fatalf("embedded key: %v", err)
	}
}
