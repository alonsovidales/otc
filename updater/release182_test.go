// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"regexp"
	"strings"
	"testing"
)

// Pets off by default: install.sh's schema block (new devices, re-runs,
// the container) and updates/182.sh (the devices installed before) run the
// same statements - a library where Pets found no pet turned off while the
// column still defaults to on, then the default - so a library its user
// turned on after the first run stays on. The block comes after 180's,
// which makes the column and faces.kind it reads.
func TestPetsOffIsTheSameInInstallAndRelease182(t *testing.T) {
	install := readFile(t, "../scripts/install.sh")
	block := regexp.MustCompile(`(?s)# Release 182 \(updates/182\.sh\).*?\nSQL\n`).FindString(install)
	if block == "" {
		t.Fatal("scripts/install.sh has no release 182 block")
	}
	if strings.Index(install, "# Release 180 (updates/180.sh)") > strings.Index(install, block) {
		t.Error("install.sh's release 182 block runs before 180's, which makes what it reads")
	}
	got := sqlLines(block)
	release := sqlLines(readFile(t, "../scripts/updates/182.sh"))
	if strings.Join(got, "\n") != strings.Join(release, "\n") {
		t.Errorf("install.sh runs\n%s\nwant 182.sh's\n%s", strings.Join(got, "\n"), strings.Join(release, "\n"))
	}
	if len(release) != 2 || !strings.HasPrefix(release[0], "UPDATE settings SET pets_enabled = 0 WHERE") ||
		!strings.HasSuffix(release[0], "= '1' AND NOT EXISTS (SELECT 1 FROM faces WHERE kind <> 0);") ||
		release[1] != "ALTER TABLE settings ALTER COLUMN pets_enabled SET DEFAULT 0;" {
		t.Errorf("182.sh no longer turns Pets off once where it found no pet, then makes it the default:\n%s", strings.Join(release, "\n"))
	}
}

// Release 182's users loop reads the list strictly, as 180's: the database
// down stops it (and run_release runs it again once), and only an existing
// user's database is changed, after the primary.
func TestRelease182ReadsTheUsersStrictly(t *testing.T) {
	script := "../scripts/updates/182.sh"
	down := "ERROR 2002 (HY000): Can't connect to local server through socket '/run/mysqld/mysqld.sock' (2)"
	if r := runRelease(t, "182", script, "FAKE_USERS_ERROR="+down); r.rc != "2" {
		t.Errorf("the database down: run_release = %q, want 2\n%s", r.rc, r.output)
	}
	users := strings.Join([]string{goodDB, "--defaults-extra-file=/x", "otc_ffffffffffffffffffffffffffffffff"}, "\n")
	r := runRelease(t, "182", script, "FAKE_USERS="+users, "FAKE_DBS="+goodDB)
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
