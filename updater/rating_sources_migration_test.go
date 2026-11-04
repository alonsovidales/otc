// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Release 169 (each rated link's visitors' own ratings): its section of
// updates/169.sh (devices installed before), install.sh's schema
// migrations (new devices, re-runs and the container, which runs that block
// as it is) and db.sql (a fresh database, a new user's) make the same
// table.
func TestRatingSourcesSchemaIsTheSameInInstallRelease169AndDBSQL(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	release := read("../scripts/updates/169.sh")
	section := regexp.MustCompile(`(?s)# --- dev-api: each rated link's visitors' own ratings -+\n.*?<<'SQL'\n(.*?)\nSQL\n`).FindStringSubmatch(release)
	if section == nil {
		t.Fatal("scripts/updates/169.sh has no SQL block in its rating sources section")
	}
	install := read("../scripts/install.sh")
	block := regexp.MustCompile(`(?s)# >>> schema-migrations.*# <<< schema-migrations`).FindString(install)
	if block == "" {
		t.Fatal("scripts/install.sh has no schema-migrations block")
	}
	var statements int
	for _, line := range strings.Split(section[1], "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		statements++
		if !strings.Contains(block, "\n    "+line+"\n") {
			t.Errorf("install.sh's schema migrations lack %q", line)
		}
	}
	if statements != 1 {
		t.Errorf("169.sh's rating sources section has %d statements, want 1", statements)
	}

	schema := read("../db/db.sql")
	def := regexp.MustCompile("(?s)create table photo_rating_sources\n\\(\n(.*?)\n\\)").FindStringSubmatch(schema)
	if def == nil {
		t.Fatal("db.sql has no table photo_rating_sources")
	}
	for _, c := range []string{"group_id", "hash", "source", "stars", "comment", "link_comment", "updated"} {
		if !strings.Contains(def[1], "`"+c+"`") {
			t.Errorf("db.sql's photo_rating_sources has no column %s", c)
		}
		if !strings.Contains(section[1], "`"+c+"`") {
			t.Errorf("169.sh doesn't name photo_rating_sources.%s", c)
		}
	}
	for _, want := range []string{"datetime(6)", "primary key (`group_id`, `hash`, `source`)"} {
		if !strings.Contains(strings.ToLower(def[1]), want) || !strings.Contains(strings.ToLower(section[1]), want) {
			t.Errorf("db.sql and 169.sh don't both have %s", want)
		}
	}
}
