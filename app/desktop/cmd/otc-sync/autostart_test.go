// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"strings"
	"testing"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/app/desktop/internal/engine"
)

// fakeLoginItems stands in for the XDG file / Run key.
type fakeLoginItems struct {
	on       bool
	exe      string
	enables  int
	disables int
}

func (f *fakeLoginItems) Enable(exe string) error {
	f.enables++
	f.on, f.exe = true, exe

	return nil
}

func (f *fakeLoginItems) Disable() error {
	f.disables++
	f.on = false

	return nil
}

func (f *fakeLoginItems) Enabled() (bool, error) { return f.on, nil }

// withFakes points the config directory at a temporary one and the
// registration at a fake, for one test.
func withFakes(t *testing.T, f *fakeLoginItems) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)            // macOS: ~/Library/Application Support
	t.Setenv("XDG_CONFIG_HOME", dir) // Linux
	t.Setenv("AppData", dir)         // Windows
	t.Setenv("OTC_SYNC_NO_KEYRING", "1")
	old := loginItems
	loginItems = f
	t.Cleanup(func() { loginItems = old })
}

func TestFirstRunRegistersNothingAndAsksOnce(t *testing.T) {
	f := &fakeLoginItems{}
	withFakes(t, f)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	registerAtLaunch(cfg)
	if f.on || f.enables != 0 {
		t.Fatalf("registered without consent: %+v", f)
	}
	a := &app{cfg: cfg, eng: &engine.Engine{}}
	if !a.OfferAutostart() {
		t.Fatal("the question is not due on a first run")
	}
	// Not Now: recorded, nothing registered, never asked again.
	if err := a.SetAutostart(false); err != nil {
		t.Fatal(err)
	}
	if f.on || a.OfferAutostart() {
		t.Fatalf("after Not Now: %+v, offer %v", f, a.OfferAutostart())
	}
	again, _ := config.Load()
	if !again.AutostartAsked() || again.AutostartEnabled() {
		t.Fatalf("not recorded as no: %+v", again.Autostart)
	}
	registerAtLaunch(again)
	if f.enables != 0 {
		t.Fatalf("registered after Not Now: %+v", f)
	}
}

func TestStartAtLoginIsRecordedAndRestored(t *testing.T) {
	f := &fakeLoginItems{}
	withFakes(t, f)
	cfg, _ := config.Load()
	a := &app{cfg: cfg, eng: &engine.Engine{}}
	if err := a.SetAutostart(true); err != nil {
		t.Fatal(err)
	}
	if !f.on || a.OfferAutostart() || !a.AutostartEnabled() {
		t.Fatalf("after Start at Login: %+v, offer %v", f, a.OfferAutostart())
	}
	// The entry removed behind its back: the next start puts it back.
	f.on = false
	again, _ := config.Load()
	registerAtLaunch(again)
	if !f.on || f.enables != 2 {
		t.Fatalf("not restored: %+v", f)
	}
}

func TestOlderUnaskedEntryIsKeptUntilAnswered(t *testing.T) {
	// An otc-sync from before this registered itself without asking.
	f := &fakeLoginItems{on: true, exe: "/old/otc-sync"}
	withFakes(t, f)
	cfg, _ := config.Load()
	registerAtLaunch(cfg)
	if !f.on || f.disables != 0 || f.enables != 0 {
		t.Fatalf("touched before an answer: %+v", f)
	}
	a := &app{cfg: cfg, eng: &engine.Engine{}}
	if !a.OfferAutostart() {
		t.Fatal("the question is not due for an unanswered older install")
	}
	if !strings.Contains(autostartStatus(cfg), "earlier version") {
		t.Fatalf("status: %q", autostartStatus(cfg))
	}
	if err := a.SetAutostart(false); err != nil || f.on {
		t.Fatalf("Not Now kept it: %v %+v", err, f)
	}
}

func TestViewerTrayDoesNotAsk(t *testing.T) {
	withFakes(t, &fakeLoginItems{})
	cfg, _ := config.Load()
	if (&app{cfg: cfg}).OfferAutostart() {
		t.Fatal("a tray next to the service asked")
	}
}

func TestAutostartCommand(t *testing.T) {
	f := &fakeLoginItems{}
	withFakes(t, f)
	if err := cmdAutostart([]string{"maybe"}); err == nil {
		t.Fatal("a bad argument was accepted")
	}
	cfg, _ := config.Load()
	if s := autostartStatus(cfg); !strings.Contains(s, "not chosen yet") {
		t.Fatalf("status before: %q", s)
	}
	if err := cmdAutostart([]string{"on"}); err != nil || !f.on {
		t.Fatalf("on: %v %+v", err, f)
	}
	cfg, _ = config.Load()
	if !cfg.AutostartEnabled() || autostartStatus(cfg) != "start at login: on" {
		t.Fatalf("after on: %v %q", cfg.Autostart, autostartStatus(cfg))
	}
	if err := cmdAutostart([]string{"off"}); err != nil || f.on {
		t.Fatalf("off: %v %+v", err, f)
	}
	cfg, _ = config.Load()
	if !cfg.AutostartAsked() || cfg.AutostartEnabled() || autostartStatus(cfg) != "start at login: off" {
		t.Fatalf("after off: %v %q", cfg.Autostart, autostartStatus(cfg))
	}
	if err := cmdAutostart([]string{"status"}); err != nil {
		t.Fatal(err)
	}
}
