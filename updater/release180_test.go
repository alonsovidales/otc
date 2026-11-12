// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"regexp"
	"strings"
	"testing"
)

// Text in photos and Pets: install.sh's schema block (new devices, re-runs,
// the container) and updates/180.sh (the devices installed before) make
// the same columns and tables, the ones db.sql describes.
func TestTextAndPetsAreTheSameInInstallAndRelease180(t *testing.T) {
	block := regexp.MustCompile(`(?s)# Release 180 \(updates/180\.sh\).*?\nSQL\n`).FindString(readFile(t, "../scripts/install.sh"))
	if block == "" {
		t.Fatal("scripts/install.sh has no release 180 block")
	}
	install := sqlLines(block)
	release := sqlLines(readFile(t, "../scripts/updates/180.sh"))
	if strings.Join(install, "\n") != strings.Join(release, "\n") {
		t.Errorf("install.sh runs\n%s\nwant 180.sh's\n%s", strings.Join(install, "\n"), strings.Join(release, "\n"))
	}
	want := []string{
		"ALTER TABLE settings ADD COLUMN IF NOT EXISTS text_in_photos_enabled TINYINT(1) NOT NULL DEFAULT 1;",
		"ALTER TABLE settings ADD COLUMN IF NOT EXISTS pets_enabled TINYINT(1) NOT NULL DEFAULT 1;",
		"ALTER TABLE faces ADD COLUMN IF NOT EXISTS `kind` TINYINT NOT NULL DEFAULT 0;",
		"CREATE TABLE IF NOT EXISTS photo_text (`hash` VARCHAR(64) NOT NULL, `model` VARCHAR(64) NOT NULL, `text` MEDIUMBLOB NULL, `made` DATETIME NOT NULL, PRIMARY KEY (`hash`)) ENGINE=InnoDB;",
		"CREATE TABLE IF NOT EXISTS pet_scans (`hash` VARCHAR(64) NOT NULL, `model` VARCHAR(64) NOT NULL, `made` DATETIME NOT NULL, PRIMARY KEY (`hash`)) ENGINE=InnoDB;",
	}
	if strings.Join(release, "\n") != strings.Join(want, "\n") {
		t.Errorf("180.sh no longer makes what Text in photos and Pets need:\n%s", strings.Join(release, "\n"))
	}
	schema := readFile(t, "../db/db.sql")
	for name, parts := range map[string][]string{
		"photo_text": {"`hash` varchar(64) not null", "`model` varchar(64) not null", "`text` mediumblob default null", "`made` datetime not null", "primary key (`hash`)"},
		"pet_scans":  {"`hash` varchar(64) not null", "`model` varchar(64) not null", "`made` datetime not null", "primary key (`hash`)"},
	} {
		table := regexp.MustCompile("(?s)create table " + name + "\n\\(.*?\n\\) engine=InnoDB;").FindString(schema)
		if table == "" {
			t.Errorf("db.sql has no %s", name)
			continue
		}
		for _, part := range parts {
			if !strings.Contains(table, part) {
				t.Errorf("db.sql's %s has no %s", name, part)
			}
		}
	}
	for _, column := range []string{"`text_in_photos_enabled` tinyint(1) not null default 1", "`pets_enabled` tinyint(1) not null default 1", "`kind` tinyint not null default 0"} {
		if !strings.Contains(schema, column) {
			t.Errorf("db.sql has no %s", column)
		}
	}
}

// Release 180's users loop reads the list strictly, as 179's: the database
// down stops it, and only an existing user's database is changed, after
// the primary.
func TestRelease180ReadsTheUsersStrictly(t *testing.T) {
	script := "../scripts/updates/180.sh"
	down := "ERROR 2002 (HY000): Can't connect to local server through socket '/run/mysqld/mysqld.sock' (2)"
	if r := runRelease(t, "180", script, "FAKE_USERS_ERROR="+down); r.rc != "2" {
		t.Errorf("the database down: run_release = %q, want 2\n%s", r.rc, r.output)
	}
	goodDB := "otc_0123456789abcdef0123456789abcdef"
	users := strings.Join([]string{goodDB, "--defaults-extra-file=/x", "otc_ffffffffffffffffffffffffffffffff"}, "\n")
	r := runRelease(t, "180", script, "FAKE_USERS="+users, "FAKE_DBS="+goodDB)
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
