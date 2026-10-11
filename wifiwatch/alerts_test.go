// SPDX-License-Identifier: AGPL-3.0-or-later

package wifiwatch

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/alonsovidales/otc/alerttext"
	"github.com/alonsovidales/otc/i18n"
)

// Each Alert says exactly what it said before it had keys - the English
// reproduced from the code that wrote it - and carries the keys that say
// it in other languages.
func TestAlertsKeepTheirEnglish(t *testing.T) {
	const restartIt = " Restart the device from Settings, or connect it to your router with an Ethernet cable."
	const reboot = " It didn't reconnect after the restart, so the device restarted."
	for _, sp := range []Speed{{Rate: 140_000, Pct: 7.7}, {Rate: 1_700_000, Pct: 12.25}} {
		k := streak{peakTx: sp.Rate, segs: 10_000, retrans: uint64(sp.Pct * 100)}
		slow := k.describe()
		w := &Watchdog{st: State{Slow: slow, SlowSpeed: k.speed()}}
		check := func(name string, a alerttext.Alert, title, detail, key, detailKey string) {
			t.Helper()
			if a.Title != title || a.Detail != detail {
				t.Errorf("%s (%s):\n got %q: %q\nwant %q: %q", name, slow, a.Title, a.Detail, title, detail)
			}
			if a.Key != key || a.DetailKey != detailKey {
				t.Errorf("%s: keys %q, %q", name, a.Key, a.DetailKey)
			}
		}
		check("recovered", w.restartedAlert(i18n.DevAlertWifiRecovered, i18n.DevAlertWifiRecoveredRebooted),
			"The Wi-Fi was restarted", "sending data had slowed down to about "+slow+"."+""+" It is working normally again.",
			"dev.alert.wifi.restarted", "dev.alert.wifi.recovered")
		check("unjudged", w.restartedAlert(i18n.DevAlertWifiUnjudged, i18n.DevAlertWifiUnjudgedRebooted),
			"The Wi-Fi was restarted", "sending data had slowed down to about "+slow+"."+""+" There hasn't been enough traffic since to tell whether it is working normally again.",
			"dev.alert.wifi.restarted", "dev.alert.wifi.unjudged")
		w.st.RootNote = "rebooted"
		check("recovered after a reboot", w.restartedAlert(i18n.DevAlertWifiRecovered, i18n.DevAlertWifiRecoveredRebooted),
			"The Wi-Fi was restarted", "sending data had slowed down to about "+slow+"."+reboot+" It is working normally again.",
			"dev.alert.wifi.restarted", "dev.alert.wifi.recovered_rebooted")
		check("unjudged after a reboot", w.restartedAlert(i18n.DevAlertWifiUnjudged, i18n.DevAlertWifiUnjudgedRebooted),
			"The Wi-Fi was restarted", "sending data had slowed down to about "+slow+"."+reboot+" There hasn't been enough traffic since to tell whether it is working normally again.",
			"dev.alert.wifi.restarted", "dev.alert.wifi.unjudged_rebooted")
		check("no runner", w.slowAlert(i18n.DevAlertWifiVerySlow(), i18n.DevAlertWifiNoRunner),
			"The Wi-Fi is sending data very slowly", "sending data has slowed down to about "+slow+". This device can't restart its Wi-Fi by itself until it is updated."+restartIt,
			"dev.alert.wifi.very_slow", "dev.alert.wifi.no_runner")
		check("no answer", w.slowAlert(i18n.DevAlertWifiNotRestarted(), i18n.DevAlertWifiNoAnswer),
			"The Wi-Fi could not be restarted", "sending data had slowed down to about "+slow+", and the Wi-Fi restart never answered."+restartIt,
			"dev.alert.wifi.not_restarted", "dev.alert.wifi.no_answer")

		for _, c := range []struct {
			st        rootStatus
			detailKey string
		}{
			{rootStatus{Message: "the Wi-Fi was restarted less than 30 minutes ago", Code: "too_soon"}, "dev.alert.wifi.restart_failed"},
			{rootStatus{Message: "the device isn't connected over Wi-Fi", Code: "not_wifi", Args: json.RawMessage(`{}`)}, "dev.alert.wifi.restart_failed"},
			{rootStatus{Message: "the device has no network connection to restart", Code: "no_connection"}, "dev.alert.wifi.restart_failed"},
			// No code (a runner from before codes), one this build doesn't
			// know, arguments the key doesn't take, or a message that isn't
			// the key's English: the runner's English, as it is.
			{rootStatus{Message: "the device isn't connected over Wi-Fi"}, "dev.alert.wifi.restart_failed_other"},
			{rootStatus{Message: "something new", Code: "from_a_newer_runner"}, "dev.alert.wifi.restart_failed_other"},
			{rootStatus{Message: "the device isn't connected over Wi-Fi", Code: "not_wifi", Args: json.RawMessage(`{"x":"1"}`)}, "dev.alert.wifi.restart_failed_other"},
			{rootStatus{Message: "the device isn't connected over Wi-Fi at all", Code: "not_wifi"}, "dev.alert.wifi.restart_failed_other"},
		} {
			msg := truncate(c.st.Message, 200)
			check("failed: "+c.st.Code, w.slowAlert(i18n.DevAlertWifiNotRestarted(), failedDetail(c.st, msg)),
				"The Wi-Fi could not be restarted", "sending data had slowed down to about "+slow+", and restarting the Wi-Fi failed ("+msg+")."+restartIt,
				"dev.alert.wifi.not_restarted", c.detailKey)
		}

		w.k = k
		speed, pct := w.k.speed().args()
		check("still slow", alerttext.New(i18n.DevAlertWifiStillSlow(), i18n.DevAlertWifiStillSlowDetail(speed, pct)),
			"The Wi-Fi is still slow after a restart", "sending data is still slow (about "+k.describe()+"). The Wi-Fi won't be restarted again until it has worked normally for a while."+restartIt,
			"dev.alert.wifi.still_slow", "dev.alert.wifi.still_slow_detail")
	}
}

// A state written before the speed was kept as numbers: the speed is read
// back out of its English; one that doesn't read back gives the title
// alone, never a wrong number.
func TestStateFromBeforeKeys(t *testing.T) {
	w := &Watchdog{st: State{Slow: "140 KB/s, with 7.7% of it sent twice"}}
	a := w.restartedAlert(i18n.DevAlertWifiRecovered, i18n.DevAlertWifiRecoveredRebooted)
	if a.Detail != "sending data had slowed down to about 140 KB/s, with 7.7% of it sent twice. It is working normally again." || a.DetailKey != "dev.alert.wifi.recovered" {
		t.Errorf("legacy Slow: %+v", a)
	}
	for _, slow := range []string{"", "fast", "140 KB/s, with 7.7%", ", with % of it sent twice"} {
		w.st.Slow = slow
		if a := w.restartedAlert(i18n.DevAlertWifiRecovered, i18n.DevAlertWifiRecoveredRebooted); a.Title != "The Wi-Fi was restarted" || a.Detail != "" || a.DetailKey != "" || !a.Keyed() {
			t.Errorf("Slow %q: %+v", slow, a)
		}
	}
}

// storing gives h's watchdog the keyed store, recording what it stores.
func storing(h *harness, stored *[]alerttext.Alert) {
	h.w.store = func(a alerttext.Alert) error {
		if !h.alertOK {
			return fmt.Errorf("database down")
		}
		*stored = append(*stored, a)
		return nil
	}
}

// End to end: the stored Alert has its English and its keys, the speed
// before the restart kept as numbers in the state.
func TestStoredAlertsCarryTheirKeys(t *testing.T) {
	var stored []alerttext.Alert
	h := newHarness(t)
	storing(h, &stored)
	h.run(5, pitSegs, pitRetrans, 20)
	if s := h.w.st.SlowSpeed; s == nil || s.Rate != 140_000 || s.Pct < 7.69 || s.Pct > 7.71 {
		t.Fatalf("speed kept %+v", h.w.st.SlowSpeed)
	}
	h.w = h.watchdog() // read back from the state file
	storing(h, &stored)
	if s := h.w.st.SlowSpeed; s == nil || s.Rate != 140_000 {
		t.Fatalf("speed after a restart of otc %+v", s)
	}
	h.rootAnswers("done", "The Wi-Fi was restarted")
	h.run(1, 0, 0, 0)
	h.run(2+5, pitSegs, goodRetrans, 0)
	if len(stored) != 1 || len(h.alerts) != 0 {
		t.Fatalf("stored %+v, English-only %v", stored, h.alerts)
	}
	a := stored[0]
	if a.Title != "The Wi-Fi was restarted" || a.Detail != "sending data had slowed down to about 140 KB/s, with 7.7% of it sent twice. It is working normally again." ||
		a.Key != "dev.alert.wifi.restarted" || a.DetailKey != "dev.alert.wifi.recovered" || string(a.DetailArgs) != `{"pct":"7.7","speed":"140 KB/s"}` {
		t.Errorf("stored %+v", a)
	}

	// The runner's code names its reason.
	stored = nil
	h = newHarness(t)
	storing(h, &stored)
	h.run(5, pitSegs, pitRetrans, 20)
	b, _ := json.Marshal(rootStatus{State: "failed", Message: "the device isn't connected over Wi-Fi", Code: "not_wifi", Args: json.RawMessage(`{}`), Updated: h.sim.t.UTC().Format("2006-01-02T15:04:05Z")})
	if err := os.WriteFile(h.w.statusPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	h.run(1, 0, 0, 0)
	if len(stored) != 1 || stored[0].DetailKey != "dev.alert.wifi.restart_failed" || string(stored[0].DetailArgs) != `{"pct":"7.7","reason":"dev.status.wifi.not_wifi","speed":"140 KB/s"}` {
		t.Fatalf("stored %+v", stored)
	}
}

// An Alert waiting in the state keeps its keys across a restart of otc,
// and one an older binary left there (English only) is raised as it is.
func TestPendingAlertKeepsItsKeys(t *testing.T) {
	var stored []alerttext.Alert
	h := newHarness(t)
	storing(h, &stored)
	h.run(5, pitSegs, pitRetrans, 20)
	h.rootAnswers("failed", "nope")
	h.alertOK = false
	h.run(1, 0, 0, 0)
	if h.w.st.Pending == nil || h.w.st.Pending.DetailKey != "dev.alert.wifi.restart_failed_other" {
		t.Fatalf("pending %+v", h.w.st.Pending)
	}
	h.w = h.watchdog()
	storing(h, &stored)
	h.alertOK = true
	h.run(1, 0, 0, 0)
	if len(stored) != 1 || stored[0].Key != "dev.alert.wifi.not_restarted" || stored[0].DetailKey != "dev.alert.wifi.restart_failed_other" {
		t.Fatalf("stored %+v", stored)
	}

	// Left by an older binary: {"title", "detail"} only.
	stored = nil
	raw := `{"phase":"gave_up","since":"2026-10-11T10:00:00Z","pending_alert":{"title":"The Wi-Fi could not be restarted","detail":"sending data had slowed down to about 140 KB/s, with 7.7% of it sent twice, and the Wi-Fi restart never answered."}}`
	if err := os.WriteFile(h.w.statePath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	h.alertOK = false // its first look tries the English seam
	h.w = h.watchdog()
	storing(h, &stored)
	h.alertOK = true
	h.run(1, 0, 0, 0)
	if len(stored) != 1 || stored[0].Keyed() || stored[0].Title != "The Wi-Fi could not be restarted" || stored[0].Detail != "sending data had slowed down to about 140 KB/s, with 7.7% of it sent twice, and the Wi-Fi restart never answered." {
		t.Fatalf("stored %+v", stored)
	}
}
