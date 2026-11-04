// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"testing"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
)

// withStdin feeds the command line's standard input, for one test.
func withStdin(t *testing.T, text string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(text); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old; _ = r.Close() })
}

func savedPassword(t *testing.T) string {
	t.Helper()
	pw, err := config.LoadPassword()
	if err != nil {
		t.Fatal(err)
	}

	return pw
}

// Pointed at another device, the password kept - the old device's - is
// forgotten before the new address is saved, given a new one or not; the
// same device keeps it, and a new password for it replaces it.
func TestSettingsForgetTheOldDevicesPassword(t *testing.T) {
	withConfigDir(t)
	t.Setenv("OTC_SYNC_NO_KEYRING", "1")
	if err := (&config.Config{Domain: "cala.off-the.cloud"}).Save(); err != nil {
		t.Fatal(err)
	}
	if err := config.SavePassword("cala's"); err != nil {
		t.Fatal(err)
	}
	if err := cmdSettings([]string{"--name", "Cala"}); err != nil { // the same device, respelled
		t.Fatal(err)
	}
	if pw := savedPassword(t); pw != "cala's" {
		t.Fatalf("the same device lost its password: %q", pw)
	}
	if err := cmdSettings([]string{"--name", "pit"}); err != nil {
		t.Fatal(err)
	}
	if pw := savedPassword(t); pw != "" {
		t.Fatalf("pit would be sent %q", pw)
	}
	withStdin(t, "pit's\n")
	if err := cmdSettings([]string{"--password-stdin"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load()
	if pw := savedPassword(t); pw != "pit's" || cfg.Domain != "pit.off-the.cloud" {
		t.Fatalf("device %q, password %q", cfg.Domain, pw)
	}
	withStdin(t, "cala's again\n")
	if err := cmdSettings([]string{"--name", "cala", "--password-stdin"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ = config.Load()
	if pw := savedPassword(t); pw != "cala's again" || cfg.Domain != "cala.off-the.cloud" {
		t.Fatalf("device %q, password %q", cfg.Domain, pw)
	}
}
