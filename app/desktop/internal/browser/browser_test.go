// SPDX-License-Identifier: AGPL-3.0-or-later

package browser

import "testing"

// Only http and https reach the desktop's opener; these fail before it.
func TestOpenRefusesOtherAddresses(t *testing.T) {
	for _, addr := range []string{"", "file:///etc/passwd", "javascript:alert(1)", "smb://nas/share", "https://", "-a https://x/", "cala.off-the.cloud"} {
		if err := Open(addr); err == nil {
			t.Errorf("Open(%q) = nil, want an error", addr)
		}
	}
}
