// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"regexp"
	"strings"
	"testing"
)

// Compress: install.sh's schema block (new devices, re-runs, the
// container) and updates/177.sh (the devices installed before) add the
// same column, the one db.sql describes.
func TestOriginalRemovedIsTheSameInInstallAndRelease177(t *testing.T) {
	block := regexp.MustCompile(`(?s)# Release 177 \(updates/177\.sh\).*?\nSQL\n`).FindString(readFile(t, "../scripts/install.sh"))
	if block == "" {
		t.Fatal("scripts/install.sh has no release 177 block")
	}
	install := sqlLines(block)
	release := sqlLines(readFile(t, "../scripts/updates/177.sh"))
	if strings.Join(install, "\n") != strings.Join(release, "\n") {
		t.Errorf("install.sh runs\n%s\nwant 177.sh's\n%s", strings.Join(install, "\n"), strings.Join(release, "\n"))
	}
	want := "ALTER TABLE edited_content ADD COLUMN IF NOT EXISTS `original_removed` TINYINT(1) NOT NULL DEFAULT 0;"
	if len(release) != 1 || release[0] != want {
		t.Errorf("177.sh no longer adds original_removed:\n%s", strings.Join(release, "\n"))
	}
	if !strings.Contains(readFile(t, "../db/db.sql"), "`original_removed` tinyint(1) not null default 0") {
		t.Error("db.sql's edited_content has no original_removed")
	}
}

// Release 177's users loop reads the list strictly, as 174's: the database
// down stops it, and only an existing user's database is changed, after
// the primary.
func TestRelease177ReadsTheUsersStrictly(t *testing.T) {
	script := "../scripts/updates/177.sh"
	down := "ERROR 2002 (HY000): Can't connect to local server through socket '/run/mysqld/mysqld.sock' (2)"
	if r := runRelease(t, "177", script, "FAKE_USERS_ERROR="+down); r.rc != "2" {
		t.Errorf("the database down: run_release = %q, want 2\n%s", r.rc, r.output)
	}
	goodDB := "otc_0123456789abcdef0123456789abcdef"
	users := strings.Join([]string{goodDB, "--defaults-extra-file=/x", "otc_ffffffffffffffffffffffffffffffff"}, "\n")
	r := runRelease(t, "177", script, "FAKE_USERS="+users, "FAKE_DBS="+goodDB)
	if r.rc != "0" {
		t.Fatalf("run_release = %q, want 0\n%s", r.rc, r.output)
	}
	var sqlTo []string
	for _, call := range r.calls {
		if len(call) == 2 && call[1] == "<stdin>" {
			sqlTo = append(sqlTo, call[0])
		}
	}
	if want := "otc " + goodDB; strings.Join(sqlTo, " ") != want {
		t.Errorf("the SQL went to %v, want %s\n%s", sqlTo, want, r.output)
	}
}
