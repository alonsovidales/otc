// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"regexp"
	"strings"
	"testing"
)

// Places on by default: install.sh's schema block (new devices, re-runs,
// the container) and updates/170.sh (the devices installed before) run the
// same statements - the column added on, every library turned on while
// the column still defaults to off, then the default - so a library its
// user turned off after the first run stays off.
func TestPlacesOnIsTheSameInInstallAndRelease170(t *testing.T) {
	block := regexp.MustCompile(`(?s)# Release 170 \(updates/170\.sh\).*?\nSQL\n`).FindString(readFile(t, "../scripts/install.sh"))
	if block == "" {
		t.Fatal("scripts/install.sh has no release 170 block")
	}
	install := sqlLines(block)
	release := sqlLines(readFile(t, "../scripts/updates/170.sh"))
	if strings.Join(install, "\n") != strings.Join(release, "\n") {
		t.Errorf("install.sh runs\n%s\nwant 170.sh's\n%s", strings.Join(install, "\n"), strings.Join(release, "\n"))
	}
	if len(release) != 3 || !strings.Contains(release[1], "places_enabled = 1 WHERE") || !strings.HasSuffix(release[1], "= '0';") ||
		release[2] != "ALTER TABLE settings ALTER COLUMN places_enabled SET DEFAULT 1;" {
		t.Errorf("170.sh no longer turns Places on once, then makes it the default:\n%s", strings.Join(release, "\n"))
	}
}

// Release 170's users loop reads the list strictly, as 169's: the database
// down stops it (and run_release runs it again once), and only an existing
// user's database is turned on, after the primary.
func TestRelease170ReadsTheUsersStrictly(t *testing.T) {
	script := "../scripts/updates/170.sh"
	down := "ERROR 2002 (HY000): Can't connect to local server through socket '/run/mysqld/mysqld.sock' (2)"
	if r := runRelease(t, "170", script, "FAKE_USERS_ERROR="+down); r.rc != "2" {
		t.Errorf("the database down: run_release = %q, want 2\n%s", r.rc, r.output)
	}
	users := strings.Join([]string{goodDB, "--defaults-extra-file=/x", "otc_ffffffffffffffffffffffffffffffff"}, "\n")
	r := runRelease(t, "170", script, "FAKE_USERS="+users, "FAKE_DBS="+goodDB)
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
