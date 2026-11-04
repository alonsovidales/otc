// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/alonsovidales/otc/container"
	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
)

// The NoNewPrivs line of /proc/<pid>/status decides; a file that can't be
// read (not Linux) or has no such line says no.
func TestNoNewPrivileges(t *testing.T) {
	dir := t.TempDir()
	for i, c := range []struct {
		status string
		want   bool
	}{
		{"Name:\totc\nUmask:\t0022\nNoNewPrivs:\t1\nSeccomp:\t2\n", true},
		{"Name:\tbash\nNoNewPrivs:\t0\n", false},
		{"Name:\told kernel\n", false},
		{"NoNewPrivs:\t1", true},
	} {
		path := filepath.Join(dir, "status")
		if err := os.WriteFile(path, []byte(c.status), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := noNewPrivileges(path); got != c.want {
			t.Errorf("case %d: noNewPrivileges = %v, want %v", i, got, c.want)
		}
	}
	if noNewPrivileges(filepath.Join(dir, "missing")) {
		t.Error("an unreadable status file read as NoNewPrivileges")
	}
}

// A device with no root runner falls back to sudo, which can't run under
// NoNewPrivileges (every unit install.sh and Makefile.pi wrote) or without
// a sudo rule: Update says the device has no updater, where it used to
// report nothing at all and start nothing.
func TestApplyWithoutAWayToRootSaysSo(t *testing.T) {
	if _, err := os.Stat(cRootRunner); err == nil {
		t.Skip("this machine has the root runner: Apply would really ask for an update")
	}
	if container.Enabled() {
		t.Skip("in a container Apply never runs anything")
	}
	if CurrentStatus().State == "running" {
		t.Skip("an update is running here")
	}
	savedStatus, savedSudo := procSelfStatus, sudoWorks
	t.Cleanup(func() { procSelfStatus, sudoWorks = savedStatus, savedSudo })
	dir := t.TempDir()
	write := func(noNewPrivs string) {
		t.Helper()
		procSelfStatus = filepath.Join(dir, "status")
		if err := os.WriteFile(procSelfStatus, []byte("Name:\totc\nNoNewPrivs:\t"+noNewPrivs+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sudoAsked := false

	// NoNewPrivileges: sudo isn't even tried.
	write("1")
	sudoWorks = func() bool { sudoAsked = true; return true }
	assertNoUpdater(t, Apply())
	if sudoAsked {
		t.Error("sudo was tried under NoNewPrivileges")
	}

	// Privileges can be gained, but sudo won't run without a password.
	write("0")
	sudoWorks = func() bool { sudoAsked = true; return false }
	assertNoUpdater(t, Apply())
	if !sudoAsked {
		t.Error("sudo was never asked")
	}
}

func assertNoUpdater(t *testing.T, err error) {
	t.Helper()
	var e *devtext.Error
	if !errors.As(err, &e) {
		t.Fatalf("Apply = %v, want the no-updater error", err)
	}
	if got, want := devtext.Text("en", err), devtext.Text("en", devtext.New(i18n.DevUpdateNoUpdater())); got != want {
		t.Errorf("Apply's error is %q, want %q", got, want)
	}
}
