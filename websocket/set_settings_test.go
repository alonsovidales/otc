// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/i18n"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/settings"
)

// SetSettings no longer changes the device's name on the bridge, which any
// signed-in session could set to a name that made the device leave the
// bridge or never register again: refused in the request's language,
// nothing written. The name it has is answered as done.
func TestSetSettingsLeavesTheBridgeNameAlone(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ch := &connHandler{mg: &Manager{dao: dao.NewWithDB(db), settings: settings.Offline("me", "kim.off-the.cloud", "x")}}
	ch.setSession(newTestAuthenticatedSession(t))
	set := func(domain, lang string) *pb.RespEnvelope {
		resp, _ := ch.processMessage(&pb.ReqEnvelope{Id: 1, Lang: lang, Payload: &pb.ReqEnvelope_ReqSetSettings{ReqSetSettings: &pb.SetSettings{Domain: domain}}})
		return resp
	}
	for _, lang := range []string{"", "es"} {
		if resp := set("x", lang); !resp.Error || resp.ErrorMessage != i18n.DevDeviceDomainManaged().Render(lang) {
			t.Errorf("%q: %+v", lang, resp)
		}
	}
	if resp := set("kim.off-the.cloud", ""); resp.Error || !resp.GetRespAck().GetOk() {
		t.Errorf("its own name: %+v", resp)
	}
	if got := ch.mg.settings.Domain(); got != "kim.off-the.cloud" {
		t.Errorf("the name is now %q", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
