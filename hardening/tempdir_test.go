// SPDX-License-Identifier: AGPL-3.0-or-later

package hardening

import "testing"

func TestTempBaseCandidateSkipsAnInheritedOtcDir(t *testing.T) {
	for in, want := range map[string]string{
		"/tmp/otc-1234":        "/tmp",     // a child inheriting the primary's TMPDIR
		"/dev/shm/otc-99":      "/dev/shm", // the same, on /dev/shm
		"/tmp":                 "/tmp",
		"/var/tmp/otc-scratch": "/var/tmp/otc-scratch", // not a pid: an operator's choice
		"/run/user/1000":       "/run/user/1000",
	} {
		if got := tempBaseCandidate(in); got != want {
			t.Errorf("tempBaseCandidate(%q) = %q, want %q", in, got, want)
		}
	}
}
