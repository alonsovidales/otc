// SPDX-License-Identifier: AGPL-3.0-or-later

package devtext

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/i18n"
)

const joinRunner = "../scripts/device-runner/otc-wifi-join-runner.py"

// The runner writes exactly the codes the device reads, but no_answer,
// which only the device decides (the runner never heard of that change).
func TestWifiJoinRunnerCodes(t *testing.T) {
	raw, err := os.ReadFile(joinRunner)
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)\nCODES = frozenset\(\{(.*?)\}\)`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("no CODES = frozenset({...}) in the runner")
	}
	var runner []string
	for _, q := range regexp.MustCompile(`"([a-z_]+)"`).FindAllSubmatch(m[1], -1) {
		runner = append(runner, string(q[1]))
	}
	var table []string
	for code := range wifiJoinCodes {
		if code != "no_answer" {
			table = append(table, code)
		}
	}
	sort.Strings(runner)
	sort.Strings(table)
	if strings.Join(runner, " ") != strings.Join(table, " ") {
		t.Errorf("the runner's CODES\n  %v\nare not the table's (without no_answer)\n  %v", runner, table)
	}
}

// Every code names a catalog key, each variant exists for the codes that
// can come after the switch, and the network's name goes exactly where a
// key declares it.
func TestWifiJoinTable(t *testing.T) {
	if len(wifiJoinCodes) != 19 {
		t.Errorf("%d Wi-Fi change codes, want 19", len(wifiJoinCodes))
	}
	for _, table := range []map[string]string{wifiJoinCodes, wifiJoinBackCodes, wifiJoinLostCodes} {
		for code, key := range table {
			if _, err := i18n.ParseStored(key, []byte(`{"ssid":"x"}`)); err != nil {
				if _, err2 := i18n.ParseStored(key, nil); err2 != nil {
					t.Errorf("%s (%s): %v / %v", code, key, err, err2)
				}
			}
		}
	}
	for code := range wifiJoinBackCodes {
		if _, ok := wifiJoinLostCodes[code]; !ok {
			t.Errorf("%s has a back variant and no lost one", code)
		}
		if _, ok := wifiJoinCodes[code]; !ok {
			t.Errorf("variant %s has no base code", code)
		}
	}
	if !WifiJoinCode("auth_failed") || !WifiJoinCode("no_answer") || WifiJoinCode("restarted") || WifiJoinCode("") {
		t.Error("WifiJoinCode")
	}
}

func TestWifiJoinText(t *testing.T) {
	iso := func(s string) string { return "\u2068" + s + "\u2069" }
	cases := []struct {
		code           string
		left, restored bool
		want           string
	}{
		{"joining", false, false, "Connecting to " + iso("home") + "…"},
		{"connected", true, true, "Connected to " + iso("home") + "."},
		{"wrong_password", false, false, "Could not connect to " + iso("home") + ": the password is wrong."},
		{"wrong_password", true, true, "Could not connect to " + iso("home") + ": the password is wrong. The device is back on its previous network."},
		{"wrong_password", true, false, "Could not connect to " + iso("home") + ": the password is wrong. The device couldn't get back on its previous network: if it doesn't answer, connect it to your router with an Ethernet cable."},
		// restored without left can't happen; it reads as never left.
		{"no_internet", false, true, "The device connected to " + iso("home") + ", but couldn't reach the internet through it, so it disconnected."},
		// Codes without variants ignore the flags; some take no name.
		{"busy", true, false, "The device's Wi-Fi is busy with another change. Try again in a few minutes."},
		{"password_required", true, true, "The network " + iso("home") + " needs a password."},
		// A code from a newer runner.
		{"exploded", true, true, "Could not connect to " + iso("home") + ". The device is back on its previous network."},
		{"", false, false, "Could not connect to " + iso("home") + "."},
	}
	for _, c := range cases {
		if got := WifiJoinText("", c.code, "home", c.left, c.restored); got != c.want {
			t.Errorf("%s left=%v restored=%v:\n got %q\nwant %q", c.code, c.left, c.restored, got, c.want)
		}
	}
	// Another language: the catalog's, the name isolated the same way.
	if got := WifiJoinText("es", "wrong_password", "casa", true, true); got == WifiJoinText("", "wrong_password", "casa", true, true) || !strings.Contains(got, iso("casa")) {
		t.Errorf("Spanish: %q", got)
	}
}
