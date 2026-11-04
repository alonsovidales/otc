// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"os"
	"regexp"
	"testing"
)

// Release 161: install.sh (new devices) and updates/corrected/161.sh (the
// devices installed before, in place of the released 161.sh) set up the
// same security updates and IPv4-options filter, from the same function -
// all of it, to its last line (the nft table's own closing brace sits at the
// start of a line inside it).
func TestOSSecurityIsTheSameInInstallAndRelease161(t *testing.T) {
	fn := regexp.MustCompile(`(?ms)^otc_os_security\(\) \{\n.*?\n    return "\$rc"\n\}\n`)
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		m := fn.Find(b)
		if m == nil {
			t.Fatalf("%s has no otc_os_security", path)
		}
		return string(m)
	}
	if read("../scripts/install.sh") != read("../scripts/updates/corrected/161.sh") {
		t.Error("otc_os_security differs between scripts/install.sh and scripts/updates/corrected/161.sh")
	}
}
