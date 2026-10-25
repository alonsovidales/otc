// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	stdlog "log"
	"os"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/network"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/supervisor"
)

// wifiSeams stands the network package in: managed or not, on "home" at
// 64%, a change asked for recorded (with the password it was given).
type wifiSeams struct {
	managed  bool
	updating bool
	changing bool
	updates  int
	asked    []string
	refuse   error
	last     *network.JoinStatus
}

func withWifi(t *testing.T, managed bool) *wifiSeams {
	t.Helper()
	s := &wifiSeams{managed: managed}
	saved := []any{wifiManaged, wifiCurrent, wifiLast, wifiJoin, updateRunning, wifiUnlessChanging}
	t.Cleanup(func() {
		wifiManaged = saved[0].(func() bool)
		wifiCurrent = saved[1].(func(context.Context) (string, int32, bool))
		wifiLast = saved[2].(func() *network.JoinStatus)
		wifiJoin = saved[3].(func(string, []byte, bool, func() bool) (network.JoinStatus, error))
		updateRunning = saved[4].(func() bool)
		wifiUnlessChanging = saved[5].(func(func() error) (bool, error))
	})
	wifiManaged = func() bool { return s.managed }
	wifiCurrent = func(context.Context) (string, int32, bool) { return "home", 64, true }
	wifiLast = func() *network.JoinStatus { return s.last }
	wifiJoin = func(ssid string, pw []byte, hidden bool, updating func() bool) (network.JoinStatus, error) {
		if !s.managed {
			return network.JoinStatus{}, &network.Refusal{Code: "not_managed", Msg: i18n.DevDeviceWifiNotManaged()}
		}
		if updating() {
			return network.JoinStatus{}, &network.Refusal{Code: "update_running", Msg: i18n.DevStatusWifiJoinUpdateRunning()}
		}
		if s.refuse != nil {
			return network.JoinStatus{}, s.refuse
		}
		s.asked = append(s.asked, ssid+"|"+string(pw))
		return network.JoinStatus{ID: "0123456789abcdef", SSID: ssid, State: "joining", Code: "joining"}, nil
	}
	updateRunning = func() bool { return s.updating }
	wifiUnlessChanging = func(start func() error) (bool, error) {
		if s.changing {
			return false, nil
		}
		s.updates++
		return true, nil
	}
	return s
}

func wifiReq(id int32, p any) *pb.ReqEnvelope {
	env := &pb.ReqEnvelope{Id: id}
	switch v := p.(type) {
	case *pb.GetWifiStatus:
		env.Payload = &pb.ReqEnvelope_ReqGetWifiStatus{ReqGetWifiStatus: v}
	case *pb.JoinWifi:
		env.Payload = &pb.ReqEnvelope_ReqJoinWifi{ReqJoinWifi: v}
	case *pb.SetWifi:
		env.Payload = &pb.ReqEnvelope_ReqSetWifi{ReqSetWifi: v}
	}
	return env
}

// ownerOnPrimary is a signed-in owner's connection on the main instance,
// with a GetPubKey key to seal passwords with.
func ownerOnPrimary(t *testing.T) (*connHandler, func(string) []byte) {
	t.Helper()
	d, mock := mockDB(t)
	mock.ExpectQuery(vaultCount).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	ch := &connHandler{mg: &Manager{sup: &supervisor.Supervisor{}, dao: d}}
	resp, _ := ch.processNonAuthRequest(&pb.ReqEnvelope{Id: 1, Payload: &pb.ReqEnvelope_ReqGetPubKey{ReqGetPubKey: &pb.GetPubKey{}}}, "")
	pubAny, err := x509.ParsePKIXPublicKey(resp.GetRespPubKey().PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	ch.setSession(newTestAuthenticatedSession(t))
	return ch, func(pw string) []byte {
		sealed, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pubAny.(*rsa.PublicKey), []byte(pw), nil)
		if err != nil {
			t.Fatal(err)
		}
		return sealed
	}
}

func TestGetWifiStatusOnThePrimary(t *testing.T) {
	s := withWifi(t, true)
	ch, _ := ownerOnPrimary(t)
	s.last = &network.JoinStatus{ID: "0123456789abcdef", SSID: "home", State: "failed", Code: "wrong_password", Left: true, Restored: true}
	resp, _ := ch.processMessage(wifiReq(2, &pb.GetWifiStatus{}))
	st := resp.GetRespWifiStatus()
	if resp.Error || st == nil || !st.Managed || st.Ssid != "home" || st.Signal != 64 || !st.Wired {
		t.Fatalf("%+v", resp)
	}
	j := st.LastJoin
	if j == nil || j.Id != "0123456789abcdef" || j.State != "failed" || j.Code != "wrong_password" || !j.Restored ||
		!strings.Contains(j.Message, "back on its previous network") {
		t.Errorf("last join %+v", j)
	}

	// Not managed: the section is hidden, nothing else said.
	s.managed = false
	resp, _ = ch.processMessage(wifiReq(3, &pb.GetWifiStatus{}))
	if st := resp.GetRespWifiStatus(); resp.Error || st == nil || st.Managed || st.Ssid != "" || st.LastJoin != nil {
		t.Errorf("unmanaged: %+v", resp)
	}
}

func TestJoinWifiOpensTheSealedPassword(t *testing.T) {
	s := withWifi(t, true)
	ch, seal := ownerOnPrimary(t)
	resp, _ := ch.processMessage(wifiReq(2, &pb.JoinWifi{Ssid: "new home", Password: seal("correct horse"), Hidden: true}))
	st := resp.GetRespWifiStatus()
	if resp.Error || st == nil || !st.Managed || st.LastJoin == nil || st.LastJoin.State != "joining" || st.LastJoin.Id != "0123456789abcdef" {
		t.Fatalf("%+v", resp)
	}
	if st.Ssid != "home" || !st.Wired {
		t.Errorf("the network it is on: %+v", st)
	}
	if !strings.HasPrefix(st.LastJoin.Message, "Connecting to") {
		t.Errorf("message %q", st.LastJoin.Message)
	}
	if len(s.asked) != 1 || s.asked[0] != "new home|correct horse" {
		t.Errorf("asked %q", s.asked)
	}
	// An open network: no password, nothing to open.
	resp, _ = ch.processMessage(wifiReq(3, &pb.JoinWifi{Ssid: "cafe"}))
	if resp.Error || len(s.asked) != 2 || s.asked[1] != "cafe|" {
		t.Errorf("open: %+v %q", resp, s.asked)
	}
	// A password not sealed with this connection's key.
	resp, _ = ch.processMessage(wifiReq(4, &pb.JoinWifi{Ssid: "cafe", Password: []byte("plain text")}))
	if !resp.Error || len(s.asked) != 2 {
		t.Errorf("unsealed: %+v", resp)
	}
}

func TestJoinWifiRefusals(t *testing.T) {
	s := withWifi(t, true)
	ch, seal := ownerOnPrimary(t)
	s.updating = true
	resp, _ := ch.processMessage(wifiReq(2, &pb.JoinWifi{Ssid: "home", Password: seal("password1")}))
	if !resp.Error || resp.ErrorCode != "update_running" || resp.ErrorMessage != "An update is running. Try again once it has finished." {
		t.Errorf("update: %+v", resp)
	}
	s.updating = false
	s.refuse = &network.Refusal{Code: "busy", Msg: i18n.DevStatusWifiJoinBusy()}
	env := wifiReq(3, &pb.JoinWifi{Ssid: "home"})
	env.Lang = "es"
	resp, _ = ch.processMessage(env)
	if !resp.Error || resp.ErrorCode != "busy" || resp.ErrorMessage != i18n.DevStatusWifiJoinBusy().Render("es") || resp.ErrorMessage == i18n.DevStatusWifiJoinBusy().Render("en") {
		t.Errorf("busy in Spanish: %+v", resp)
	}
	s.refuse = nil
	s.managed = false
	resp, _ = ch.processMessage(wifiReq(4, &pb.JoinWifi{Ssid: "home"}))
	if !resp.Error || resp.ErrorCode != "not_managed" || resp.ErrorMessage != "This device's Wi-Fi can't be changed from Settings." {
		t.Errorf("not managed: %+v", resp)
	}
	if len(s.asked) != 0 {
		t.Errorf("asked %q", s.asked)
	}
}

// The gates: a child instance, a container, a demo device.
func TestWifiGates(t *testing.T) {
	s := withWifi(t, true)

	// A child instance: hidden, and no change.
	ch := &connHandler{mg: &Manager{}}
	ch.setSession(newTestAuthenticatedSession(t))
	resp, _ := ch.processMessage(wifiReq(2, &pb.GetWifiStatus{}))
	if st := resp.GetRespWifiStatus(); resp.Error || st == nil || st.Managed {
		t.Errorf("child status: %+v", resp)
	}
	resp, _ = ch.processMessage(wifiReq(3, &pb.JoinWifi{Ssid: "home"}))
	if !resp.Error || resp.ErrorCode != "" || resp.ErrorMessage != "not available on this instance" {
		t.Errorf("child join: %+v", resp)
	}

	// A container: the server's network.
	t.Run("container", func(t *testing.T) {
		inContainer(t)
		ch := &connHandler{mg: &Manager{sup: &supervisor.Supervisor{}}}
		if resp, handled := ch.containerGate(wifiReq(2, &pb.GetWifiStatus{}), ""); handled || resp != nil {
			t.Errorf("no session: %+v", resp)
		}
		ch.setSession(newTestAuthenticatedSession(t))
		resp, _ := ch.processMessage(wifiReq(2, &pb.GetWifiStatus{}))
		if st := resp.GetRespWifiStatus(); resp.Error || st == nil || st.Managed {
			t.Errorf("container status: %+v", resp)
		}
		resp, _ = ch.processMessage(wifiReq(3, &pb.JoinWifi{Ssid: "home"}))
		if !resp.Error || !strings.HasPrefix(resp.ErrorMessage, "Not available in Docker") {
			t.Errorf("container join: %+v", resp)
		}
	})

	// A demo device: shown, on a cable; a change refused before anything.
	t.Run("demo", func(t *testing.T) {
		asDemo(t)
		ch := &connHandler{mg: &Manager{sup: &supervisor.Supervisor{}}}
		if resp, handled := ch.demoGate(wifiReq(2, &pb.GetWifiStatus{}), ""); handled || resp != nil {
			t.Errorf("demo, signed out: %+v", resp)
		}
		ch.setSession(newTestAuthenticatedSession(t))
		resp, _ := ch.processMessage(wifiReq(2, &pb.GetWifiStatus{}))
		if st := resp.GetRespWifiStatus(); resp.Error || st == nil || !st.Managed || !st.Wired || st.Ssid != "" {
			t.Errorf("demo status: %+v", resp)
		}
		resp, _ = ch.processMessage(wifiReq(3, &pb.JoinWifi{Ssid: "home"}))
		if !resp.Error || resp.ErrorCode != cCodeDemo {
			t.Errorf("demo join: %+v", resp)
		}
	})
	if len(s.asked) != 0 {
		t.Errorf("a change was asked: %q", s.asked)
	}
}

// SetWifi is retired: unknown_payload, wherever it comes from, and the
// password it carried in the clear is never logged.
func TestSetWifiIsRetired(t *testing.T) {
	withWifi(t, true)
	var buf bytes.Buffer
	stdlog.SetOutput(&buf)
	t.Cleanup(func() { stdlog.SetOutput(os.Stderr) })
	for _, ch := range []*connHandler{{mg: &Manager{sup: &supervisor.Supervisor{}}}, {mg: &Manager{}}} {
		ch.setSession(newTestAuthenticatedSession(t))
		env := wifiReq(2, &pb.SetWifi{Ssid: "home", Password: "hunter2-secret"})
		env.Lang = "de"
		resp, _ := ch.processMessage(env)
		if !resp.Error || resp.ErrorCode != "unknown_payload" || resp.ErrorMessage != i18n.DevErrUnknownRequest().Render("de") {
			t.Errorf("%+v", resp)
		}
	}
	if strings.Contains(buf.String(), "hunter2") {
		t.Errorf("the password was logged: %s", buf.String())
	}
}

// No update while the Wi-Fi is being changed.
func TestApplyUpdateWaitsForAWifiChange(t *testing.T) {
	s := withWifi(t, true)
	s.changing = true
	ch := &connHandler{mg: &Manager{sup: &supervisor.Supervisor{}}}
	ch.setSession(newTestAuthenticatedSession(t))
	resp, _ := ch.processMessage(&pb.ReqEnvelope{Id: 2, Payload: &pb.ReqEnvelope_ReqApplyUpdate{ReqApplyUpdate: &pb.ReqApplyUpdate{}}})
	if !resp.Error || resp.ErrorCode != "wifi_changing" || resp.ErrorMessage != "the device's Wi-Fi is being changed; try again in a few minutes" || s.updates != 0 {
		t.Errorf("%+v", resp)
	}
	s.changing = false
	resp, _ = ch.processMessage(&pb.ReqEnvelope{Id: 3, Payload: &pb.ReqEnvelope_ReqApplyUpdate{ReqApplyUpdate: &pb.ReqApplyUpdate{}}})
	if resp.Error || resp.GetRespAck() == nil || s.updates != 1 {
		t.Errorf("no change running: %+v", resp)
	}
}

func TestListWifiNetworksCarriesTheNewFields(t *testing.T) {
	got := wifiNetworksProto([]network.Network{
		{SSID: "home", Signal: 70, Secured: true, Current: true},
		{SSID: "office", Signal: 40, Secured: true, Unsupported: true},
	})
	if len(got.Networks) != 2 || !got.Networks[0].Current || got.Networks[0].Unsupported || !got.Networks[1].Unsupported || got.Networks[1].Ssid != "office" {
		t.Errorf("%+v", got)
	}
}
