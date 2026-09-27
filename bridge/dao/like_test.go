// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import "testing"

// Issue #143: the admin search box's text matches literally - a % or _
// typed into it is not a LIKE wildcard.
func TestLikeArgEscapesWildcards(t *testing.T) {
	cases := map[string]string{
		"pit":      "%pit%",
		"50%":      `%50\%%`,
		"a_b":      `%a\_b%`,
		`back\sl`:  `%back\\sl%`,
		"ana@x.es": "%ana@x.es%",
	}
	for in, want := range cases {
		if got := likeArg(in); got != want {
			t.Errorf("likeArg(%q) = %q, want %q", in, got, want)
		}
	}
}
