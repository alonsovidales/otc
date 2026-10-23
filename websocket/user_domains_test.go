// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// registerUserDomain asks as this device, for the user's identity, and
// words the bridge's refusals for Settings > Users.
func TestRegisterUserDomainAsksAsTheDeviceAndWordsRefusals(t *testing.T) {
	const pepe = "pepe.off-the.cloud"
	for _, c := range []struct {
		name   string
		resp   *pb.RespEnvelope
		err    error
		wantOK bool
		want   string
	}{
		{"registered", &pb.RespEnvelope{Payload: &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}}, nil, true, ""},
		{"taken", &pb.RespEnvelope{Error: true, ErrorCode: "name_taken", ErrorMessage: "that name is already taken"}, nil, false, i18n.DevUsersNameTakenOnBridge(pepe).Render("")},
		{"held", &pb.RespEnvelope{Error: true, ErrorCode: "name_held", ErrorMessage: "held"}, nil, false, i18n.DevUsersNameTakenOnBridge(pepe).Render("")},
		{"limit", &pb.RespEnvelope{Error: true, ErrorCode: "domain_limit", ErrorMessage: "up to 5"}, nil, false, i18n.DevUsersBridgeDomainLimit(pepe).Render("")},
		{"other refusal", &pb.RespEnvelope{Error: true, ErrorMessage: "Invalid Secret"}, nil, false, i18n.DevUsersBridgeCheckFailed(pepe, "Invalid Secret").Render("")},
		{"unreachable", nil, errors.New("could not reach the bridge: timeout"), false, i18n.DevUsersBridgeCheckFailed(pepe, "could not reach the bridge: timeout").Render("")},
	} {
		t.Run(c.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectQuery("select `subdomain`, `device_uuid`, `bridge_secret` from `settings`").
				WillReturnRows(sqlmock.NewRows([]string{"subdomain", "device_uuid", "bridge_secret"}).AddRow("cala.off-the.cloud", "cala-uuid", "cala-secret"))

			var asked *pb.ReqRegisterUserDomain
			old := askBridge
			askBridge = func(env *pb.ReqEnvelope) (*pb.RespEnvelope, error) {
				asked = env.GetReqRegisterUserDomain()
				return c.resp, c.err
			}
			defer func() { askBridge = old }()

			err = registerUserDomain(dao.NewWithDB(db), pepe, "pepe-uuid", "pepe-secret")
			if asked == nil || asked.OwnerUuid != "cala-uuid" || asked.Domain != "cala.off-the.cloud" || asked.Secret != "cala-secret" ||
				asked.UserDomain != pepe || asked.UserOwnerUuid != "pepe-uuid" || asked.UserSecret != "pepe-secret" {
				t.Fatalf("asked %+v", asked)
			}
			if c.wantOK {
				if err != nil {
					t.Fatalf("expected ok, got %v", err)
				}
				return
			}
			if err == nil || devtext.Text("", err) != c.want {
				t.Fatalf("expected %q, got %v", c.want, err)
			}
		})
	}
}

// The address goes back with the user's own identity.
func TestReleaseUserDomainUsesTheUsersIdentity(t *testing.T) {
	var asked *pb.BridgeReleaseDomain
	old := askBridge
	askBridge = func(env *pb.ReqEnvelope) (*pb.RespEnvelope, error) {
		asked = env.GetReqBridgeReleaseDomain()
		return &pb.RespEnvelope{Payload: &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}}, nil
	}
	defer func() { askBridge = old }()
	if err := releaseUserDomain("pepe.off-the.cloud", "pepe-uuid", "pepe-secret"); err != nil {
		t.Fatal(err)
	}
	if asked == nil || asked.OwnerUuid != "pepe-uuid" || asked.Domain != "pepe.off-the.cloud" || asked.Secret != "pepe-secret" {
		t.Fatalf("asked %+v", asked)
	}
}
