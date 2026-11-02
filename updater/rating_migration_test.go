// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Release 163 (collection Rating): updates/163.sh (devices installed
// before), install.sh's schema migrations (new devices, re-runs and the
// container, which runs that block as it is) and db.sql (a fresh database,
// a new user's) make the same schema.
func TestRatingSchemaIsTheSameInInstallRelease163AndDBSQL(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	release := read("../scripts/updates/163.sh")
	heredoc := regexp.MustCompile(`(?s)<<'SQL'\n(.*?)\nSQL\n`).FindStringSubmatch(release)
	if heredoc == nil {
		t.Fatal("scripts/updates/163.sh has no SQL block")
	}
	install := read("../scripts/install.sh")
	block := regexp.MustCompile(`(?s)# >>> schema-migrations.*# <<< schema-migrations`).FindString(install)
	if block == "" {
		t.Fatal("scripts/install.sh has no schema-migrations block")
	}
	var statements int
	for _, line := range strings.Split(heredoc[1], "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		statements++
		if !strings.Contains(block, "\n    "+line+"\n") {
			t.Errorf("install.sh's schema migrations lack %q", line)
		}
	}
	if statements != 8 {
		t.Errorf("163.sh has %d statements, want 8", statements)
	}

	schema := read("../db/db.sql")
	for table, columns := range map[string][]string{
		"image_groups":  {"rating", "rating_key"},
		"photo_ratings": {"group_id", "hash", "stars", "comment", "shared_comment", "link", "updated"},
		"shared_links":  {"group_id", "rating", "rating_alerted"},
		"vault":         {"rating_public", "rating_private"},
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
			if !strings.Contains(heredoc[1], c) {
				t.Errorf("163.sh doesn't name %s.%s", table, c)
			}
		}
	}
}
