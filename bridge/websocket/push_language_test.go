// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/bridge/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/go-sql-driver/mysql"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

func pushRegistrationsFrame(t *testing.T, language string) []byte {
	t.Helper()
	b, err := proto.Marshal(&pb.ReqEnvelope{Id: 1, Payload: &pb.ReqEnvelope_ReqUpdatePushRegistrations{
		ReqUpdatePushRegistrations: &pb.UpdatePushRegistrations{
			Domain: "pit.otc", OwnerUuid: "owner-uuid", Secret: "secret",
			VapidPublicKey: "pub", VapidPrivateKey: "priv", Language: language,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Localization (docs/i18n.md): a device reports the language of the
// bridge's own pushes with its registrations. It is stored as a code this
// build carries, or "" - anything else a device sends is not kept - and a
// bridge whose database lacks the column (migration 010 not run) stores
// the registrations without it and acknowledges them all the same.
func TestUpdatePushRegistrationsStoresTheLanguage(t *testing.T) {
	for _, c := range []struct {
		sent, stored string
		noColumn     bool
	}{
		{"en", "en", false},
		{"EN-gb", "en", false},
		{"zz-ZZ", "", false}, // no such language here
		{"en'; drop table devices; --", "", false},
		{"", "", false},    // a device from before localization
		{"en", "en", true}, // tried, then stored without it
	} {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		mock.ExpectQuery("select `owner_uuid`, `secret` from `devices` where `domain` = \\?").
			WillReturnRows(sqlmock.NewRows([]string{"owner_uuid", "secret"}).AddRow("owner-uuid", "secret"))
		mock.ExpectBegin()
		upsert := mock.ExpectExec("insert into `push_registrations` \\(`domain`, `vapid_public_key`, `vapid_private_key`, `language`\\)").
			WithArgs("pit.otc", "pub", "priv", c.stored)
		if c.noColumn {
			upsert.WillReturnError(&mysql.MySQLError{Number: 1054, Message: "Unknown column 'language' in 'field list'"})
			mock.ExpectExec("insert into `push_registrations` \\(`domain`, `vapid_public_key`, `vapid_private_key`\\) values").
				WithArgs("pit.otc", "pub", "priv").WillReturnResult(sqlmock.NewResult(1, 1))
		} else {
			upsert.WillReturnResult(sqlmock.NewResult(1, 1))
		}
		for _, table := range []string{"push_apns_tokens", "push_fcm_tokens", "push_web_subs"} {
			mock.ExpectExec("delete from `" + table + "`").WillReturnResult(sqlmock.NewResult(0, 0))
		}
		mock.ExpectCommit()

		mg := &Manager{dao: dao.NewWithDB(db), bridges: map[string]*bridgePool{}}
		dial, _ := newTestBridge(t, mg)
		conn := dial()
		if err := conn.WriteMessage(gorilla.BinaryMessage, pushRegistrationsFrame(t, c.sent)); err != nil {
			t.Fatal(err)
		}
		resp := readResp(t, conn)
		if resp.Error || !resp.GetRespUpdatePushRegistrationsAck().GetOk() {
			t.Errorf("%q: got Error=%v %q, want an ok ack", c.sent, resp.Error, resp.ErrorMessage)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("%q: %v", c.sent, err)
		}
		db.Close()
	}
}
