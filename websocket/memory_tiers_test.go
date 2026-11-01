// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	filesmanager "github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/i18n"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/supervisor"
)

// withMemory makes this device's memory profile p for the test.
func withMemory(t *testing.T, p filesmanager.MemoryProfile) {
	t.Helper()
	was := deviceMemory
	deviceMemory = func() filesmanager.MemoryProfile { return p }
	t.Cleanup(func() { deviceMemory = was })
}

var (
	eightGB = filesmanager.MemoryProfile{Total: 7900 << 20}
	fourGB  = filesmanager.MemoryProfile{Total: 3700 << 20, Low: true}
	twoGB   = filesmanager.MemoryProfile{Total: 1900 << 20, Low: true}
)

func setTaggingReq(on bool) *pb.ReqEnvelope {
	return &pb.ReqEnvelope{Id: 4, Lang: "es", Payload: &pb.ReqEnvelope_ReqSetImageTaggingEnabled{ReqSetImageTaggingEnabled: &pb.SetImageTaggingEnabled{Enabled: on}}}
}

// A low-memory device has no Image Tagging: Settings says it isn't
// available and off, and turning it on is refused in the request's
// language; turning it off still works. An 8 GB device takes it.
func TestImageTaggingByMemory(t *testing.T) {
	withMemory(t, fourGB)
	mg, mock := languageManager(t, "", "")
	ch := &connHandler{mg: mg}
	ch.setSession(newTestAuthenticatedSession(t))

	mock.ExpectQuery("select `social_storage_limit_mb` from `settings`").
		WillReturnRows(sqlmock.NewRows([]string{"social_storage_limit_mb"}).AddRow(5120))
	mock.ExpectQuery("select sum\\(`size`\\)").WillReturnRows(sqlmock.NewRows([]string{"sum"}).AddRow(nil))
	resp := mustProcess(t, ch, getSettingsReq())
	if s := resp.GetRespSettings(); s == nil || s.ImageTaggingEnabled || s.ImageTaggingAvailable == nil || *s.ImageTaggingAvailable {
		t.Errorf("4 GB settings: %+v", resp)
	}

	resp = mustProcess(t, ch, setTaggingReq(true))
	if !resp.Error || resp.ErrorCode != cCodeTaggingUnavailable || resp.ErrorMessage != i18n.DevDeviceTaggingNeedMemory().Render("es") {
		t.Errorf("turning it on with 4 GB: %+v", resp)
	}
	mock.ExpectExec("update `settings` set `image_tagging_enabled` = \\?, `image_tagging_chosen` = 1").WithArgs(false).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if resp := mustProcess(t, ch, setTaggingReq(false)); resp.Error {
		t.Errorf("turning it off with 4 GB: %+v", resp)
	}

	withMemory(t, eightGB)
	mock.ExpectExec("update `settings` set `image_tagging_enabled` = \\?, `image_tagging_chosen` = 1").WithArgs(true).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if resp := mustProcess(t, ch, setTaggingReq(true)); resp.Error {
		t.Errorf("turning it on with 8 GB: %+v", resp)
	}
	mock.ExpectQuery("select `social_storage_limit_mb` from `settings`").
		WillReturnRows(sqlmock.NewRows([]string{"social_storage_limit_mb"}).AddRow(5120))
	mock.ExpectQuery("select sum\\(`size`\\)").WillReturnRows(sqlmock.NewRows([]string{"sum"}).AddRow(nil))
	resp = mustProcess(t, ch, getSettingsReq())
	if s := resp.GetRespSettings(); s == nil || !s.ImageTaggingEnabled || s.ImageTaggingAvailable == nil || !*s.ImageTaggingAvailable {
		t.Errorf("8 GB settings: %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A 2 GB device takes no other users: the list says so and a new one is
// refused before anything is looked up or provisioned. A 4 GB one takes
// them.
func TestUsersByMemory(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ch := &connHandler{mg: &Manager{dao: dao.NewWithDB(db), sup: &supervisor.Supervisor{}}}
	ch.setSession(newTestAuthenticatedSession(t))
	list := func() *pb.RespUsers {
		t.Helper()
		mock.ExpectQuery("select .* from `users`").WillReturnRows(sqlmock.NewRows([]string{"uuid"}))
		resp, _ := ch.processAuthRequest(&pb.ReqEnvelope{Id: 1, Payload: &pb.ReqEnvelope_ReqListUsers{ReqListUsers: &pb.ReqListUsers{}}}, "")
		if resp == nil || resp.Error || resp.GetRespUsers() == nil {
			t.Fatalf("list: %+v", resp)
		}
		return resp.GetRespUsers()
	}

	withMemory(t, twoGB)
	if u := list(); u.CanAdd == nil || *u.CanAdd {
		t.Errorf("2 GB list: %+v", u)
	}
	resp, _ := ch.processAuthRequest(&pb.ReqEnvelope{Id: 2, Lang: "de",
		Payload: &pb.ReqEnvelope_ReqCreateUser{ReqCreateUser: &pb.ReqCreateUser{Username: "ana"}}}, "de")
	if resp == nil || !resp.Error || resp.ErrorCode != cCodeUsersUnavailable || resp.ErrorMessage != i18n.DevUsersNeedMemory().Render("de") {
		t.Errorf("a new user with 2 GB: %+v", resp)
	}

	withMemory(t, fourGB)
	if u := list(); u.CanAdd == nil || !*u.CanAdd {
		t.Errorf("4 GB list: %+v", u)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
