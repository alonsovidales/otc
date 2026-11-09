// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"regexp"
	"strings"
	"testing"
)

// People added by hand: install.sh's schema block (new devices, re-runs,
// the container) and updates/179.sh (the devices installed before) make
// the same table, the one db.sql describes.
func TestPeopleByHandIsTheSameInInstallAndRelease179(t *testing.T) {
	block := regexp.MustCompile(`(?s)# Release 179 \(updates/179\.sh\).*?\nSQL\n`).FindString(readFile(t, "../scripts/install.sh"))
	if block == "" {
		t.Fatal("scripts/install.sh has no release 179 block")
	}
	install := sqlLines(block)
	release := sqlLines(readFile(t, "../scripts/updates/179.sh"))
	if strings.Join(install, "\n") != strings.Join(release, "\n") {
		t.Errorf("install.sh runs\n%s\nwant 179.sh's\n%s", strings.Join(install, "\n"), strings.Join(release, "\n"))
	}
	want := "CREATE TABLE IF NOT EXISTS people_by_hand (`hash` VARCHAR(64) NOT NULL, `person_id` VARCHAR(36) NOT NULL, `created` DATETIME NOT NULL, PRIMARY KEY (`person_id`, `hash`), KEY (`hash`)) ENGINE=InnoDB;"
	if len(release) != 1 || release[0] != want {
		t.Errorf("179.sh no longer makes people_by_hand:\n%s", strings.Join(release, "\n"))
	}
	schema := readFile(t, "../db/db.sql")
	table := regexp.MustCompile("(?s)create table people_by_hand\n\\(.*?\n\\) engine=InnoDB;").FindString(schema)
	if table == "" {
		t.Fatal("db.sql has no people_by_hand")
	}
	for _, part := range []string{"`hash` varchar(64) not null", "`person_id` varchar(36) not null", "`created` datetime not null", "primary key (`person_id`, `hash`)", "key (`hash`)"} {
		if !strings.Contains(table, part) {
			t.Errorf("db.sql's people_by_hand has no %s", part)
		}
	}
}

// Release 179's users loop reads the list strictly, as 177's: the database
// down stops it, and only an existing user's database is changed, after
// the primary.
func TestRelease179ReadsTheUsersStrictly(t *testing.T) {
	script := "../scripts/updates/179.sh"
	down := "ERROR 2002 (HY000): Can't connect to local server through socket '/run/mysqld/mysqld.sock' (2)"
	if r := runRelease(t, "179", script, "FAKE_USERS_ERROR="+down); r.rc != "2" {
		t.Errorf("the database down: run_release = %q, want 2\n%s", r.rc, r.output)
	}
	goodDB := "otc_0123456789abcdef0123456789abcdef"
	users := strings.Join([]string{goodDB, "--defaults-extra-file=/x", "otc_ffffffffffffffffffffffffffffffff"}, "\n")
	r := runRelease(t, "179", script, "FAKE_USERS="+users, "FAKE_DBS="+goodDB)
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
