// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/lantls"
	"github.com/alonsovidales/otc/profile"
	pb "github.com/alonsovidales/otc/proto/generated"
)

func localEndpointReq() *pb.ReqEnvelope {
	return &pb.ReqEnvelope{
		Id:      5,
		Payload: &pb.ReqEnvelope_ReqGetLocalEndpoint{ReqGetLocalEndpoint: &pb.GetLocalEndpoint{}},
	}
}

// fakeLocalAddresses stands in for the machine's interfaces until the test
// ends.
func fakeLocalAddresses(t *testing.T, addrs []string, err error) {
	t.Helper()
	saved := localAddresses
	t.Cleanup(func() { localAddresses = saved })
	localAddresses = func() ([]string, error) { return addrs, err }
}

var testPin = bytes.Repeat([]byte{0xab}, 32)

func servingManager() *Manager {
	mg := &Manager{}
	mg.SetLocalEndpoint(lantls.NewEndpoint(8443, testPin))
	return mg
}

func TestGetLocalEndpointAnswersTheOwner(t *testing.T) {
	fakeLocalAddresses(t, []string{"192.168.1.10", "fd12::10"}, nil)
	ch := &connHandler{mg: servingManager()}
	ch.setSession(newTestAuthenticatedSession(t))

	resp, closeConn := ch.processMessage(localEndpointReq())

	if resp == nil || resp.Error || closeConn {
		t.Fatalf("expected an answer, got %+v (close %v)", resp, closeConn)
	}
	got := resp.GetRespLocalEndpoint()
	if got == nil {
		t.Fatalf("expected a LocalEndpoint, got %T", resp.Payload)
	}
	if !reflect.DeepEqual(got.Addresses, []string{"192.168.1.10", "fd12::10"}) || got.Port != 8443 || !bytes.Equal(got.CertSha256, testPin) {
		t.Errorf("got %v port %d pin %x", got.Addresses, got.Port, got.CertSha256)
	}
}

// Before sign-in nothing answers it: handleConnection turns the nil into
// not_authenticated.
func TestGetLocalEndpointRefusedBeforeSignIn(t *testing.T) {
	fakeLocalAddresses(t, []string{"192.168.1.10"}, nil)
	ch := &connHandler{mg: servingManager()}

	resp, _ := ch.processMessage(localEndpointReq())

	if resp != nil {
		t.Errorf("expected no answer before sign-in, got %+v", resp)
	}
}

// A friend's device is signed in too, but the owner's home network is not
// its business.
func TestGetLocalEndpointRefusedToAFriend(t *testing.T) {
	fakeLocalAddresses(t, []string{"192.168.1.10"}, nil)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("select `status`, `forget_requested` is not null from `social_friendship`").
		WillReturnRows(sqlmock.NewRows([]string{"status", "leaving"}).AddRow("accepted", false))
	mg := servingManager()
	mg.dao = dao.NewWithDB(db)
	ch := &connHandler{mg: mg}
	ch.setFriendProfile(&profile.Profile{})

	resp, _ := ch.processMessage(localEndpointReq())

	if resp != nil {
		t.Errorf("expected no answer for a friend, got %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("the friendship was not checked: %v", err)
	}
}

// Asked before api has set the endpoint (the bridge pool is already up
// then): an error with no code, so the apps keep the endpoint they hold.
func TestGetLocalEndpointBeforeTheListenerIsKnown(t *testing.T) {
	fakeLocalAddresses(t, []string{"192.168.1.10"}, nil)
	ch := &connHandler{mg: &Manager{}}
	ch.setSession(newTestAuthenticatedSession(t))

	resp, closeConn := ch.processMessage(localEndpointReq())

	if resp == nil || !resp.Error || resp.ErrorCode != "" || resp.Payload != nil {
		t.Errorf("expected an error with no code and no payload, got %+v", resp)
	}
	if resp != nil && resp.ErrorMessage == "unknown payload" {
		t.Error("Android reads that message as a device from before the request")
	}
	if closeConn {
		t.Error("the connection must stay open")
	}
}

func TestGetLocalEndpointUnavailable(t *testing.T) {
	stopped := lantls.NewEndpoint(8443, testPin)
	stopped.Stop()
	cases := []struct {
		name  string
		ep    *lantls.Endpoint
		addrs []string
		err   error
	}{
		{"no listener", nil, []string{"192.168.1.10"}, nil},
		{"listener stopped", stopped, []string{"192.168.1.10"}, nil},
		{"no home-network address", lantls.NewEndpoint(8443, testPin), nil, nil},
		{"addresses unreadable", lantls.NewEndpoint(8443, testPin), nil, errors.New("netlink")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fakeLocalAddresses(t, c.addrs, c.err)
			mg := &Manager{}
			mg.SetLocalEndpoint(c.ep)
			ch := &connHandler{mg: mg}
			ch.setSession(newTestAuthenticatedSession(t))

			resp, closeConn := ch.processMessage(localEndpointReq())

			if resp == nil || !resp.Error || resp.ErrorCode != cCodeLocalUnavailable || resp.Payload != nil {
				t.Errorf("expected error_code %q and no payload, got %+v", cCodeLocalUnavailable, resp)
			}
			if closeConn {
				t.Error("the connection must stay open")
			}
		})
	}
}
