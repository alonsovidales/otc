// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"errors"
	"testing"

	"github.com/alonsovidales/otc/devicerestart"
	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/supervisor"
)

// restartWorld sets the restart's seams: a Wi-Fi change in flight, an
// update running, and what asking the root side answers. It returns how
// many times the root side was asked.
func restartWorld(t *testing.T, changing, updating bool, ask error) *int {
	t.Helper()
	wasUnless, wasUpdating, wasRestart := wifiUnlessChanging, updateRunning, restartRequest
	t.Cleanup(func() { wifiUnlessChanging, updateRunning, restartRequest = wasUnless, wasUpdating, wasRestart })
	asked := 0
	wifiUnlessChanging = func(start func() error) (bool, error) {
		if changing {
			return false, nil
		}
		return true, start()
	}
	updateRunning = func() bool { return updating }
	restartRequest = func() error { asked++; return ask }
	return &asked
}

func restartReq(lang string) *pb.ReqEnvelope {
	return &pb.ReqEnvelope{Id: 9, Lang: lang, Payload: &pb.ReqEnvelope_ReqRestartDevice{ReqRestartDevice: &pb.RestartDevice{}}}
}

// The owner of the primary instance gets RestartingDevice once the request
// is written. A supervised user's instance, a Wi-Fi change or an update in
// progress, and a root side that refuses each get their own words, in the
// request's language, and nothing is asked of the root side for the first
// three: a restart during an update's start check left a release that
// doesn't start installed, with no way back.
func TestRestartDevice(t *testing.T) {
	primary := &connHandler{mg: &Manager{sup: &supervisor.Supervisor{}}}

	asked := restartWorld(t, false, false, nil)
	resp, _ := primary.processAuthRequest(restartReq("es"), "es")
	want := int32(devicerestart.ExpectedDowntime.Seconds())
	if resp == nil || resp.Error || resp.GetRespRestartingDevice().GetExpectedSeconds() != want || resp.Id != 9 || *asked != 1 {
		t.Fatalf("restart: %+v, asked %d", resp, *asked)
	}

	child := &connHandler{mg: &Manager{}}
	asked = restartWorld(t, false, false, nil)
	resp, _ = child.processAuthRequest(restartReq(""), "")
	if resp == nil || !resp.Error || resp.ErrorMessage != "not available on this instance" || *asked != 0 {
		t.Errorf("a user's instance: %+v, asked %d", resp, *asked)
	}

	for _, c := range []struct {
		name             string
		changing, update bool
		ask              error
		code, want       string
		asks             int
	}{
		{"a Wi-Fi change", true, false, nil, "wifi_changing", i18n.DevDeviceRestartWifiChanging().Render("de"), 0},
		{"an update", false, true, nil, "update_running", i18n.DevDeviceRestartUpdateRunning().Render("de"), 0},
		{"no runner", false, false, devicerestart.ErrNotInstalled, "", i18n.DevDeviceRestartNeedsUpdate().Render("de"), 1},
		{"too soon", false, false, devtext.New(i18n.DevDeviceRestartTooSoon()), "", i18n.DevDeviceRestartTooSoon().Render("de"), 1},
	} {
		asked := restartWorld(t, c.changing, c.update, c.ask)
		resp, _ := primary.processAuthRequest(restartReq("de"), "de")
		if resp == nil || !resp.Error || resp.GetRespRestartingDevice() != nil || resp.ErrorCode != c.code || resp.ErrorMessage != c.want || *asked != c.asks {
			t.Errorf("%s: %+v, asked %d", c.name, resp, *asked)
		}
	}

	// In English, a request without lang: today's words.
	restartWorld(t, false, true, nil)
	if resp, _ := primary.processAuthRequest(restartReq(""), ""); resp == nil || resp.ErrorMessage != "an update is running; try again once it has finished" {
		t.Errorf("an update, in English: %+v", resp)
	}

	// A raw error the device can't translate still says something.
	restartWorld(t, false, false, errors.New("boom"))
	if resp, _ := primary.processAuthRequest(restartReq(""), ""); resp == nil || !resp.Error || resp.ErrorMessage == "" {
		t.Errorf("a raw error: %+v", resp)
	}
}
