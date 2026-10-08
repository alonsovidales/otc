// SPDX-License-Identifier: AGPL-3.0-or-later

package autostart

import (
	"errors"
	"testing"
)

// fake is a registration in memory: never the real file or Run key.
type fake struct {
	exe      string
	on       bool
	enables  int
	disables int
	err      error
}

func (f *fake) Enable(exe string) error {
	f.enables++
	f.on, f.exe = true, exe

	return nil
}

func (f *fake) Disable() error {
	f.disables++
	f.on = false

	return nil
}

func (f *fake) Enabled() (bool, error) { return f.on, f.err }

func ptr(b bool) *bool { return &b }

func TestAtLaunchNeverRegistersWithoutConsent(t *testing.T) {
	for _, choice := range []*bool{nil, ptr(false)} {
		f := &fake{}
		if err := AtLaunch(f, choice, "/x/otc-sync"); err != nil || f.on || f.enables != 0 || f.disables != 0 {
			t.Fatalf("choice %v: err %v, registration %+v", choice, err, f)
		}
	}
}

func TestAtLaunchLeavesAnOlderEntryUntilAnswered(t *testing.T) {
	// Registered unasked by an otc-sync before consent: kept until the
	// user answers the tray's question.
	f := &fake{on: true, exe: "/old/otc-sync"}
	if err := AtLaunch(f, nil, "/x/otc-sync"); err != nil || !f.on || f.exe != "/old/otc-sync" || f.disables != 0 {
		t.Fatalf("err %v, registration %+v", err, f)
	}
}

func TestAtLaunchRestoresAConsentedEntry(t *testing.T) {
	f := &fake{}
	if err := AtLaunch(f, ptr(true), "/x/otc-sync"); err != nil || !f.on || f.exe != "/x/otc-sync" {
		t.Fatalf("err %v, registration %+v", err, f)
	}
	// Already there: not written again.
	if err := AtLaunch(f, ptr(true), "/x/otc-sync"); err != nil || f.enables != 1 {
		t.Fatalf("err %v, enables %d", err, f.enables)
	}
	// Unreadable: reported, nothing written.
	g := &fake{err: errors.New("registry")}
	if err := AtLaunch(g, ptr(true), "/x/otc-sync"); err == nil || g.enables != 0 {
		t.Fatalf("err %v, enables %d", err, g.enables)
	}
}

func TestApply(t *testing.T) {
	f := &fake{}
	if err := Apply(f, true, "/x/otc-sync"); err != nil || !f.on {
		t.Fatalf("on: err %v, %+v", err, f)
	}
	if err := Apply(f, false, "/x/otc-sync"); err != nil || f.on {
		t.Fatalf("off: err %v, %+v", err, f)
	}
}
