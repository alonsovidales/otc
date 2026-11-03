// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Release 168 (issue #206, Images' month counts kept as the library
// changes): updates/168.sh (devices installed before), install.sh's schema
// migrations (new devices, re-runs and the container) and db.sql (a fresh
// database, a new user's) make the same tables and columns.
func TestMonthCountsSchemaIsTheSameInInstallRelease168AndDBSQL(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	var release string
	for _, m := range regexp.MustCompile(`(?s)<<'SQL'\n(.*?)\nSQL\n`).FindAllStringSubmatch(read("../scripts/updates/168.sh"), -1) {
		release += m[1] + "\n"
	}
	install := read("../scripts/install.sh")
	block := regexp.MustCompile(`(?s)# >>> schema-migrations.*# <<< schema-migrations`).FindString(install)
	if block == "" {
		t.Fatal("scripts/install.sh has no schema-migrations block")
	}
	statements := 0
	for _, line := range strings.Split(release, "\n") {
		if line = strings.TrimSpace(line); !strings.Contains(line, "photo_month") {
			continue
		}
		statements++
		if !strings.Contains(block, "\n    "+line+"\n") {
			t.Errorf("install.sh's schema migrations lack %q", line)
		}
	}
	if statements != 4 {
		t.Errorf("168.sh has %d statements for the month counts, want 4", statements)
	}
	schema := read("../db/db.sql")
	for table, columns := range map[string][]string{
		"photo_months":       {"month", "images", "media"},
		"photo_month_hashes": {"hash", "images", "media"},
		"settings":           {"photo_months_for", "photo_months_run"},
	} {
		def := regexp.MustCompile(`(?s)create table ` + table + `\n\(\n(.*?)\n\)`).FindStringSubmatch(schema)
		if def == nil {
			t.Errorf("db.sql has no table %s", table)
			continue
		}
		for _, c := range columns {
			if !strings.Contains(def[1], "`"+c+"`") {
				t.Errorf("db.sql's %s has no column %s", table, c)
			}
			if !strings.Contains(release, c) {
				t.Errorf("168.sh doesn't name %s.%s", table, c)
			}
		}
	}
}
