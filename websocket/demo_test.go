// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"reflect"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// asDemo turns demo mode on until the test ends.
func asDemo(t *testing.T) {
	t.Helper()
	saved := demoMode
	t.Cleanup(func() { demoMode = saved })
	demoMode = true
}

// Every request in ReqEnvelope is classified for a demo device, so one
// added later is a decision, not an accident (it would be refused anyway).
func TestDemoRulesCoverEveryRequest(t *testing.T) {
	env := &pb.ReqEnvelope{}
	m := env.ProtoReflect()
	fields := m.Descriptor().Oneofs().ByName("payload").Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		e := (&pb.ReqEnvelope{}).ProtoReflect()
		e.Set(fd, protoreflect.ValueOfMessage(e.NewField(fd).Message()))
		p := e.Interface().(*pb.ReqEnvelope).Payload
		if _, ok := demoRules[reflect.TypeOf(p)]; !ok {
			t.Errorf("%s is not in demoRules", fd.Name())
		}
	}
}

// A demo device refuses what would change it with error_code "demo" (also
// as an Ack), in the request's language, and runs nothing.
func TestDemoRefusesChanges(t *testing.T) {
	asDemo(t)
	ch, mock := ownerWithLibrary(t)
	for _, c := range []struct {
		req  *pb.ReqEnvelope
		want string
	}{
		{delFileReq("/Phone/a.jpg", nil, ""), "This is a demo device: changes are turned off."},
		{&pb.ReqEnvelope{Id: 11, Lang: "es", Payload: &pb.ReqEnvelope_ReqChangeKey{ReqChangeKey: &pb.ChangeKey{}}}, "Este es un dispositivo de demostración: los cambios están desactivados."},
		{&pb.ReqEnvelope{Id: 11, Payload: &pb.ReqEnvelope_ReqRestartDevice{ReqRestartDevice: &pb.RestartDevice{}}}, "This is a demo device: changes are turned off."},
		{&pb.ReqEnvelope{Id: 11, Payload: &pb.ReqEnvelope_ReqFriendshipInterRequest{ReqFriendshipInterRequest: &pb.FriendshipInterRequest{}}}, "This is a demo device: changes are turned off."},
		{&pb.ReqEnvelope{Id: 11}, "This is a demo device: changes are turned off."},
	} {
		resp, closeConn := ch.processMessage(c.req)
		if closeConn || resp == nil || resp.Id != 11 || !resp.Error || resp.ErrorCode != "demo" || resp.ErrorMessage != c.want ||
			resp.GetRespAck().GetOk() || resp.GetRespAck().GetCode() != "demo" || resp.GetRespAck().GetErrorMsg() != c.want {
			t.Errorf("%T: %+v", c.req.Payload, resp)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// What the apps send by themselves after signing in is answered ok and
// stored nowhere; the home network is answered as absent.
func TestDemoAnswersWithoutStoring(t *testing.T) {
	asDemo(t)
	ch, mock := ownerWithLibrary(t)
	for _, req := range []*pb.ReqEnvelope{
		{Id: 3, Payload: &pb.ReqEnvelope_ReqSetLanguage{ReqSetLanguage: &pb.SetLanguage{Language: "es"}}},
		{Id: 3, Payload: &pb.ReqEnvelope_ReqRegisterApnsToken{ReqRegisterApnsToken: &pb.RegisterApnsToken{Token: "t"}}},
		{Id: 3, Payload: &pb.ReqEnvelope_ReqRegisterFcmToken{ReqRegisterFcmToken: &pb.RegisterFcmToken{Token: "t"}}},
		{Id: 3, Payload: &pb.ReqEnvelope_ReqRegisterWebPush{ReqRegisterWebPush: &pb.RegisterWebPush{Endpoint: "e"}}},
		{Id: 3, Payload: &pb.ReqEnvelope_ReqMarkNotificationsAcknowledged{ReqMarkNotificationsAcknowledged: &pb.ReqMarkNotificationsAcknowledged{}}},
	} {
		resp, _ := ch.processMessage(req)
		if resp == nil || resp.Id != 3 || resp.Error || !resp.GetRespAck().GetOk() {
			t.Errorf("%T: %+v", req.Payload, resp)
		}
	}
	resp, _ := ch.processMessage(localEndpointReq())
	if resp == nil || !resp.Error || resp.ErrorCode != cCodeLocalUnavailable || resp.GetRespLocalEndpoint() != nil {
		t.Errorf("GetLocalEndpoint: %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// Reading runs as usual.
func TestDemoAllowsReading(t *testing.T) {
	asDemo(t)
	ch, _ := ownerWithLibrary(t)
	resp, handled := ch.demoGate(&pb.ReqEnvelope{Id: 4, Payload: &pb.ReqEnvelope_ReqListFiles{ReqListFiles: &pb.ListFiles{}}}, "")
	if handled || resp != nil {
		t.Errorf("ListFiles: %+v", resp)
	}
}

// A sign-in that would set the owner password (none set yet) is refused:
// it would be the first visitor's. With one set, Auth goes on as usual.
func TestDemoAuthNeedsTheOwnerPassword(t *testing.T) {
	asDemo(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ch := &connHandler{mg: &Manager{dao: dao.NewWithDB(db)}}
	auth := &pb.ReqEnvelope{Id: 9, Payload: &pb.ReqEnvelope_ReqAuth{ReqAuth: &pb.Auth{Create: true}}}

	mock.ExpectQuery("select count\\(\\*\\) from `vault`").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	resp, handled := ch.demoGate(auth, "")
	if !handled || resp == nil || resp.ErrorCode != "demo" {
		t.Errorf("no owner password: %+v", resp)
	}

	mock.ExpectQuery("select count\\(\\*\\) from `vault`").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	if resp, handled := ch.demoGate(auth, ""); handled || resp != nil {
		t.Errorf("owner password set: %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// Off, nothing changes: the usual switches answer.
func TestDemoOffChangesNothing(t *testing.T) {
	if demoMode {
		t.Fatal("demo mode on by default")
	}
	if got := demoLanguage("es"); got == nil || *got != "es" {
		t.Errorf("language: %v", got)
	}
	asDemo(t)
	if got := demoLanguage("es"); got != nil {
		t.Errorf("demo language: %v", *got)
	}
}

// A demo server's status reads like a Pi's with two mirrored disks; what
// the library uses stays real.
func TestDemoStatusReadsLikeAPi(t *testing.T) {
	for _, at := range []int64{1_800_000_000, 1_800_000_317, 1_800_004_000} {
		st := &pb.Status{RaidUsage: 5_000, RaidState: pb.RaidState_RaidNone, Errors: []*pb.StatusErrors{{}}}
		demoPiStatus(st, time.Unix(at, 0))
		if st.Disks != 2 || st.RaidDevicesActive != 2 || st.RaidLevel != "raid1" || st.RaidState != pb.RaidState_RaidInSync ||
			st.Errors != nil || st.RaidUsage != 5_000 || st.RaidSize < 1_900_000 || st.MemSize != 8_047 ||
			st.MemUsage < 2_270 || st.MemUsage > 2_630 || st.CpuUsagePrc < 0.5 || st.CpuUsagePrc > 9.5 {
			t.Errorf("at %d: %+v", at, st)
		}
	}
}

// The password is published only by a demo device.
func TestDemoPasswordOnlyOnADemo(t *testing.T) {
	saved := demoPassword
	t.Cleanup(func() { demoPassword = saved })
	demoPassword = "try-it"
	if got := demoPasswordShown(); got != "" {
		t.Errorf("demo off: %q", got)
	}
	asDemo(t)
	if got := demoPasswordShown(); got != "try-it" {
		t.Errorf("demo on: %q", got)
	}
}

// Settings shows every section on a demo device: no disks or Wi-Fi to set
// up, the bridge and Tailscale read as usual, and the update check kept
// for a while rather than fetched for each visitor.
func TestDemoAnswersSettingsReads(t *testing.T) {
	asDemo(t)
	ch, _ := ownerWithLibrary(t)
	resp, handled := ch.demoGate(&pb.ReqEnvelope{Id: 6, Payload: &pb.ReqEnvelope_ReqListWifiNetworks{ReqListWifiNetworks: &pb.ListWifiNetworks{}}}, "")
	if !handled || resp.Error || resp.GetRespWifiNetworks() == nil || len(resp.GetRespWifiNetworks().Networks) != 0 {
		t.Errorf("Wi-Fi: %+v", resp)
	}
	resp, handled = ch.demoGate(&pb.ReqEnvelope{Id: 6, Payload: &pb.ReqEnvelope_ReqListStorageDevices{ReqListStorageDevices: &pb.ListStorageDevices{}}}, "")
	if !handled || resp.Error || resp.GetRespStorageDevices() == nil || len(resp.GetRespStorageDevices().Devices) != 0 {
		t.Errorf("disks: %+v", resp)
	}
	for _, req := range []*pb.ReqEnvelope{
		{Payload: &pb.ReqEnvelope_ReqGetBridgeAccess{ReqGetBridgeAccess: &pb.ReqGetBridgeAccess{}}},
		{Payload: &pb.ReqEnvelope_ReqGetTailscaleStatus{ReqGetTailscaleStatus: &pb.ReqGetTailscaleStatus{}}},
	} {
		if resp, handled := ch.demoGate(req, ""); handled {
			t.Errorf("%T: answered by the gate: %+v", req.Payload, resp)
		}
	}
	if resp, handled := ch.demoGate(&pb.ReqEnvelope{Payload: &pb.ReqEnvelope_ReqGetLogs{ReqGetLogs: &pb.GetLogs{}}}, ""); !handled || resp.ErrorCode != "demo" {
		t.Errorf("logs: %+v", resp)
	}

	saved := buildDemoUpdateInfo
	t.Cleanup(func() { buildDemoUpdateInfo = saved; demoUpdateCache = map[string]demoUpdateEntry{} })
	demoUpdateCache = map[string]demoUpdateEntry{}
	calls := 0
	buildDemoUpdateInfo = func(lang string) *pb.RespUpdateInfo {
		calls++
		return &pb.RespUpdateInfo{CurrentVersion: int32(calls)}
	}
	at := time.Unix(1_800_000_000, 0)
	if v := demoUpdateInfo("", at).CurrentVersion; v != 1 {
		t.Errorf("first check: %d", v)
	}
	if v := demoUpdateInfo("", at.Add(5*time.Minute)).CurrentVersion; v != 1 || calls != 1 {
		t.Errorf("within ten minutes: %d after %d checks", v, calls)
	}
	if v := demoUpdateInfo("es", at.Add(5*time.Minute)).CurrentVersion; v != 2 {
		t.Errorf("another language: %d", v)
	}
	if v := demoUpdateInfo("", at.Add(11*time.Minute)).CurrentVersion; v != 3 {
		t.Errorf("after ten minutes: %d", v)
	}
	resp, handled = ch.demoGate(&pb.ReqEnvelope{Id: 7, Payload: &pb.ReqEnvelope_ReqCheckUpdate{ReqCheckUpdate: &pb.ReqCheckUpdate{}}}, "")
	if !handled || resp.Id != 7 || resp.GetRespUpdateInfo() == nil {
		t.Errorf("CheckUpdate: %+v", resp)
	}
}
