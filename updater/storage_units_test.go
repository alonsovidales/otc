// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"os"
	"regexp"
	"testing"
)

// Issue #207: install.sh (new devices) and updates/166.sh (the devices
// installed before) install the same otc-degraded.service and the same
// raid-watch.service ordering, from the same function.
func TestStorageUnitsAreTheSameInInstallAndRelease166(t *testing.T) {
	fn := regexp.MustCompile(`(?ms)^otc_storage_units\(\) \{\n.*?^\}\n`)
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		m := fn.Find(b)
		if m == nil {
			t.Fatalf("%s has no otc_storage_units", path)
		}
		return string(m)
	}
	if read("../scripts/install.sh") != read("../scripts/updates/166.sh") {
		t.Error("otc_storage_units differs between scripts/install.sh and scripts/updates/166.sh")
	}
}
