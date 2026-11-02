// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"errors"
	"testing"

	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/supervisor"
)

// shutDownWorld sets the shutdown's seams: whether this is a container, a
// Wi-Fi change in flight, an update running, and what asking the root side
// answers. It returns how many times the root side was asked.
func shutDownWorld(t *testing.T, inContainer, changing, updating bool, ask error) *int {
	t.Helper()
	wasContainer, wasUnless, wasUpdating, wasShutDown := inContainerHere, wifiUnlessChanging, updateRunning, shutDownDevice
	t.Cleanup(func() {
		inContainerHere, wifiUnlessChanging, updateRunning, shutDownDevice = wasContainer, wasUnless, wasUpdating, wasShutDown
	})
	asked := 0
	inContainerHere = func() bool { return inContainer }
	wifiUnlessChanging = func(start func() error) (bool, error) {
		if changing {
			return false, nil
		}
		return true, start()
	}
	updateRunning = func() bool { return updating }
	shutDownDevice = func() error { asked++; return ask }
	return &asked
}

func shutDownReq(lang string) *pb.ReqEnvelope {
	return &pb.ReqEnvelope{Id: 7, Lang: lang, Payload: &pb.ReqEnvelope_ReqShutDownDevice{ReqShutDownDevice: &pb.ShutDownDevice{}}}
}

// The owner of the primary instance gets the generic Ack once the request
// is written; a supervised user's instance, a container, a Wi-Fi change or
// an update in progress, and a root side that refuses each get their own
// words, in the request's language, and nothing is asked of the root side
// for the first four.
func TestShutDownDevice(t *testing.T) {
	primary := &connHandler{mg: &Manager{sup: &supervisor.Supervisor{}}}

	asked := shutDownWorld(t, false, false, false, nil)
	resp, _ := primary.processAuthRequest(shutDownReq("es"), "es")
	if resp == nil || resp.Error || !resp.GetRespAck().GetOk() || resp.Id != 7 || *asked != 1 {
		t.Fatalf("shut down: %+v, asked %d", resp, *asked)
	}

	child := &connHandler{mg: &Manager{}}
	asked = shutDownWorld(t, false, false, false, nil)
	resp, _ = child.processAuthRequest(shutDownReq(""), "")
	if resp == nil || !resp.Error || resp.ErrorMessage != "not available on this instance" || *asked != 0 {
		t.Errorf("a user's instance: %+v, asked %d", resp, *asked)
	}

	for _, c := range []struct {
		name                          string
		inContainer, changing, update bool
		ask                           error
		code, want                    string
		asks                          int
	}{
		{"in a container", true, false, false, nil, "not_in_container", i18n.DevDeviceShutdownInContainer().Render("de"), 0},
		{"a Wi-Fi change", false, true, false, nil, "wifi_changing", i18n.DevDeviceShutdownWifiChanging().Render("de"), 0},
		{"an update", false, false, true, nil, "update_running", i18n.DevDeviceShutdownUpdateRunning().Render("de"), 0},
		{"no runner", false, false, false, devtext.New(i18n.DevDeviceShutdownNeedsUpdate()), "", i18n.DevDeviceShutdownNeedsUpdate().Render("de"), 1},
		{"too soon", false, false, false, devtext.New(i18n.DevDeviceShutdownTooSoon()), "", i18n.DevDeviceShutdownTooSoon().Render("de"), 1},
	} {
		asked := shutDownWorld(t, c.inContainer, c.changing, c.update, c.ask)
		resp, _ := primary.processAuthRequest(shutDownReq("de"), "de")
		if resp == nil || !resp.Error || resp.GetRespAck() != nil || resp.ErrorCode != c.code || resp.ErrorMessage != c.want || *asked != c.asks {
			t.Errorf("%s: %+v, asked %d", c.name, resp, *asked)
		}
	}

	// A raw error the device can't translate still says something.
	shutDownWorld(t, false, false, false, errors.New("boom"))
	if resp, _ := primary.processAuthRequest(shutDownReq(""), ""); resp == nil || !resp.Error || resp.ErrorMessage == "" {
		t.Errorf("a raw error: %+v", resp)
	}
}
