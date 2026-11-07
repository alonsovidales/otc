// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"regexp"
	"strings"
	"testing"
)

// Photo edits: install.sh's schema block (new devices, re-runs, the
// container) and updates/174.sh (the devices installed before) make the
// same table, the one db.sql describes.
func TestEditedContentIsTheSameInInstallAndRelease174(t *testing.T) {
	block := regexp.MustCompile(`(?s)# Release 174 \(updates/174\.sh\).*?\nSQL\n`).FindString(readFile(t, "../scripts/install.sh"))
	if block == "" {
		t.Fatal("scripts/install.sh has no release 174 block")
	}
	install := sqlLines(block)
	release := sqlLines(readFile(t, "../scripts/updates/174.sh"))
	if strings.Join(install, "\n") != strings.Join(release, "\n") {
		t.Errorf("install.sh runs\n%s\nwant 174.sh's\n%s", strings.Join(install, "\n"), strings.Join(release, "\n"))
	}
	if len(release) != 1 || !strings.HasPrefix(release[0], "CREATE TABLE IF NOT EXISTS edited_content (") {
		t.Errorf("174.sh no longer makes edited_content:\n%s", strings.Join(release, "\n"))
	}
	schema := readFile(t, "../db/db.sql")
	for _, col := range []string{"`hash`", "`original_hash`", "`original_mime`", "`original_size`", "`original_ext`", "`edited`"} {
		if !strings.Contains(release[0], col) {
			t.Errorf("174.sh's edited_content has no %s", col)
		}
	}
	if !strings.Contains(schema, "create table edited_content") {
		t.Error("db.sql has no edited_content")
	}
}

// Release 174's users loop reads the list strictly, as 170's: the database
// down stops it, and only an existing user's database is changed, after
// the primary.
func TestRelease174ReadsTheUsersStrictly(t *testing.T) {
	script := "../scripts/updates/174.sh"
	down := "ERROR 2002 (HY000): Can't connect to local server through socket '/run/mysqld/mysqld.sock' (2)"
	if r := runRelease(t, "174", script, "FAKE_USERS_ERROR="+down); r.rc != "2" {
		t.Errorf("the database down: run_release = %q, want 2\n%s", r.rc, r.output)
	}
	goodDB := "otc_0123456789abcdef0123456789abcdef"
	users := strings.Join([]string{goodDB, "--defaults-extra-file=/x", "otc_ffffffffffffffffffffffffffffffff"}, "\n")
	r := runRelease(t, "174", script, "FAKE_USERS="+users, "FAKE_DBS="+goodDB)
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
