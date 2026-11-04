// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import (
	"slices"
	"testing"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/app/desktop/internal/engine"
)

// pairController records the device and password the engine would sign in
// with at each save (each save reconnects it).
type pairController struct {
	domain, password string
	pairs            []string
}

func (c *pairController) saved() { c.pairs = append(c.pairs, c.domain+"/"+c.password) }

func (c *pairController) Snapshot() config.State                              { return config.State{} }
func (c *pairController) Config() *config.Config                              { return &config.Config{Domain: c.domain} }
func (c *pairController) LoadConfig() (*config.Config, error)                 { return c.Config(), nil }
func (c *pairController) EditConfig(func(*config.Config) (bool, error)) error { return nil }
func (c *pairController) ConfigError() error                                  { return nil }
func (c *pairController) ResetSettings() error                                { return nil }
func (c *pairController) LogPath() string                                     { return "" }
func (c *pairController) Password() string                                    { return c.password }
func (c *pairController) SetPassword(pw string) error                         { c.password = pw; c.saved(); return nil }
func (c *pairController) RemoteBrowser() (func(string) ([]engine.RemoteEntry, error), func()) {
	return nil, func() {}
}
func (c *pairController) AutostartEnabled() bool  { return false }
func (c *pairController) SetAutostart(bool) error { return nil }
func (c *pairController) OfferAutostart() bool    { return false }
func (c *pairController) Quit()                   {}

// Connect to another device: never the new device with the old password,
// nor the old device with the new one. The same device: the new password,
// then the respelled address.
func TestConnectSavesThePairInOrder(t *testing.T) {
	c := &pairController{domain: "cala.off-the.cloud", password: "cala's"}
	address := func(d string) func() bool {
		return func() bool { c.domain = d; c.saved(); return true }
	}
	if !saveConnection(c, true, "pit's", address("pit.off-the.cloud")) {
		t.Fatal("not saved")
	}
	want := []string{"cala.off-the.cloud/", "pit.off-the.cloud/", "pit.off-the.cloud/pit's"}
	if !slices.Equal(c.pairs, want) {
		t.Fatalf("signed in with %v, want %v", c.pairs, want)
	}
	c.pairs = nil
	if !saveConnection(c, false, "pit's new", address("Pit.off-the.cloud")) {
		t.Fatal("not saved")
	}
	if want := []string{"pit.off-the.cloud/pit's new", "Pit.off-the.cloud/pit's new"}; !slices.Equal(c.pairs, want) {
		t.Fatalf("signed in with %v, want %v", c.pairs, want)
	}
}

// An empty password field goes on only for the device the kept password
// belongs to.
func TestConnectNeedsAPasswordForAnotherDevice(t *testing.T) {
	for _, tc := range []struct {
		newDevice       bool
		entered, stored string
		missing         bool
	}{
		{true, "", "cala's", true}, // the old device's would go to the new one
		{true, "pit's", "cala's", false},
		{false, "", "cala's", false}, // the same device keeps its password
		{false, "", "", true},        // none kept (after Reset Settings…)
	} {
		if got := passwordMissing(tc.newDevice, tc.entered, tc.stored); got != tc.missing {
			t.Errorf("%+v: missing %v", tc, got)
		}
	}
}
