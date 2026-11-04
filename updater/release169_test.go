// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"regexp"
	"strings"
	"testing"
)

// TCP's window on a connection that sat idle: install.sh (new devices) and
// updates/169.sh (the devices installed before) write the same sysctl file,
// from the same function.
func TestNetworkTuningIsTheSameInInstallAndRelease169(t *testing.T) {
	fn := regexp.MustCompile(`(?ms)^otc_network_tuning\(\) \{\n.*?^\}\n`)
	install := fn.FindString(readFile(t, "../scripts/install.sh"))
	release := fn.FindString(readFile(t, "../scripts/updates/169.sh"))
	if install == "" || release == "" {
		t.Fatal("otc_network_tuning is missing from scripts/install.sh or scripts/updates/169.sh")
	}
	if install != release {
		t.Error("otc_network_tuning differs between scripts/install.sh and scripts/updates/169.sh")
	}
	if !strings.Contains(install, "net.ipv4.tcp_slow_start_after_idle = 0") {
		t.Error("otc_network_tuning no longer keeps TCP's window on idle connections")
	}
}

// Release 169 adds release 168's month counts again where they are
// missing: exactly 168's statements, so the schema it leaves is 168's.
func TestRelease169MonthCountsAreRelease168s(t *testing.T) {
	var months []string
	for _, line := range sqlLines(readFile(t, "../scripts/updates/168.sh")) {
		if strings.Contains(line, "photo_month") {
			months = append(months, line)
		}
	}
	if len(months) != 4 {
		t.Fatalf("168.sh has %d month-count statements, want 4", len(months))
	}
	if got := sqlLines(readFile(t, "../scripts/updates/169.sh")); strings.Join(got, "\n") != strings.Join(months, "\n") {
		t.Errorf("169.sh's statements are\n%s\nwant 168's\n%s", strings.Join(got, "\n"), strings.Join(months, "\n"))
	}
}

// Release 169's own users loop reads the list strictly: the database down
// stops it (and run_release runs it again once), a missing users table is
// nothing to loop over, and only an existing user's database gets 168's
// month counts again. OTC_CONTAINER=1 leaves out its system part, so the
// test never writes to /etc.
func TestRelease169ReadsTheUsersStrictly(t *testing.T) {
	script := "../scripts/updates/169.sh"
	down := "ERROR 2002 (HY000): Can't connect to local server through socket '/run/mysqld/mysqld.sock' (2)"
	if r := runRelease(t, "169", script, "OTC_CONTAINER=1", "FAKE_USERS_ERROR="+down); r.rc != "2" {
		t.Errorf("the database down: run_release = %q, want 2\n%s", r.rc, r.output)
	}
	users := strings.Join([]string{goodDB, "--defaults-extra-file=/x", "otc_ffffffffffffffffffffffffffffffff"}, "\n")
	r := runRelease(t, "169", script, "OTC_CONTAINER=1", "FAKE_USERS="+users, "FAKE_DBS="+goodDB)
	if r.rc != "0" {
		t.Fatalf("run_release = %q, want 0\n%s", r.rc, r.output)
	}
	var sqlTo []string
	for _, call := range r.calls {
		if len(call) == 2 && call[1] == "<stdin>" {
			sqlTo = append(sqlTo, call[0])
		}
	}
	if strings.Join(sqlTo, " ") != "otc "+goodDB {
		t.Errorf("the month counts went to %v, want otc and %s\n%s", sqlTo, goodDB, r.output)
	}
	if !strings.Contains(r.output, "in a container") {
		t.Errorf("the system part ran under OTC_CONTAINER=1\n%s", r.output)
	}
}
