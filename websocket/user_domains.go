// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/wsframe"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

// The bridge's refusals of a user's address (ReqRegisterUserDomain).
const (
	cUserDomainTaken = "name_taken"
	cUserDomainHeld  = "name_held"
	cUserDomainLimit = "domain_limit"
)

// askBridge is askBridgeOnce; tests answer for the bridge.
var askBridge = askBridgeOnce

// askBridgeOnce sends one request to the bridge on a connection of its own
// and returns the answer, as the other one-off bridge calls do.
func askBridgeOnce(payload *pb.ReqEnvelope) (*pb.RespEnvelope, error) {
	addr := url.URL{Scheme: "wss", Host: cfg.GetStr("otc", "bridge-addr"), Path: "/ws"}
	h := http.Header{}
	h.Set("Sec-WebSocket-Protocol", "protobuf")
	c, err := wsframe.Dial(addr.String(), h)
	if err != nil {
		return nil, fmt.Errorf("could not reach the bridge: %w", err) // i18n-ignore: the detail after dev.users.bridge_check_failed
	}
	defer c.Close()

	b, err := proto.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if err := c.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		return nil, fmt.Errorf("asking the bridge: %w", err) // i18n-ignore: the detail after dev.users.bridge_check_failed
	}
	_, data, err := c.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("reading the bridge's answer: %w", err) // i18n-ignore: the detail after dev.users.bridge_check_failed
	}
	var resp pb.RespEnvelope
	if err := proto.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("unreadable answer from the bridge: %w", err) // i18n-ignore: the detail after dev.users.bridge_check_failed
	}
	return &resp, nil
}

// registerUserDomain registers a user's address (issue #103) on the
// bridge under this device's account (ReqRegisterUserDomain): since issue
// #124 the bridge turns away an address that no account holds. userUuid
// and userSecret are the identity the user's own instance registers with.
// The errors are the user-facing reasons Settings > Users shows.
func registerUserDomain(d *dao.Dao, userDomain, userUuid, userSecret string) error {
	domain, owner, secret, err := d.GetSettings()
	if err != nil {
		err = fmt.Errorf("reading this device's own bridge identity: %w", err) // i18n-ignore: the detail after dev.users.bridge_check_failed
		return devtext.Wrap(i18n.DevUsersBridgeCheckFailed(userDomain, err.Error()), err)
	}
	resp, err := askBridge(&pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqRegisterUserDomain{
			ReqRegisterUserDomain: &pb.ReqRegisterUserDomain{
				OwnerUuid:     owner,
				Domain:        domain,
				Secret:        secret,
				UserDomain:    userDomain,
				UserOwnerUuid: userUuid,
				UserSecret:    userSecret,
			},
		},
	})
	if err != nil {
		return devtext.Wrap(i18n.DevUsersBridgeCheckFailed(userDomain, err.Error()), err)
	}
	if !resp.Error {
		return nil
	}
	switch resp.ErrorCode {
	case cUserDomainTaken, cUserDomainHeld:
		return devtext.New(i18n.DevUsersNameTakenOnBridge(userDomain))
	case cUserDomainLimit:
		return devtext.New(i18n.DevUsersBridgeDomainLimit(userDomain))
	}
	err = errors.New(resp.ErrorMessage)
	return devtext.Wrap(i18n.DevUsersBridgeCheckFailed(userDomain, err.Error()), err)
}

// releaseUserDomain gives a user's address back to the bridge, as the
// user's own instance would (BridgeReleaseDomain with its identity).
func releaseUserDomain(userDomain, userUuid, userSecret string) error {
	resp, err := askBridge(&pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqBridgeReleaseDomain{
			ReqBridgeReleaseDomain: &pb.BridgeReleaseDomain{OwnerUuid: userUuid, Domain: userDomain, Secret: userSecret},
		},
	})
	if err != nil {
		return err
	}
	if resp.Error {
		return errors.New(resp.ErrorMessage)
	}
	return nil
}

// cUserDomainRetries are the waits before each new try of the addresses
// registerUserDomainsAtStart couldn't register; then it waits for the
// next start.
var cUserDomainRetries = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour}

// registerUserDomainsAtStart registers, on the primary, the address of
// every active user with bridge access. Users created before the bridge
// took their addresses (every one since issue #124) dial in and are
// turned away until this runs; for the others it is a quick "already
// yours". A failure is logged and tried again later.
func registerUserDomainsAtStart(d *dao.Dao) {
	users, err := d.ListActiveUsersInternal()
	if err != nil {
		log.Error("could not list the users to register their bridge addresses:", err)
		return
	}
	var pending []*dao.UserInternal
	for _, u := range users {
		if u.BridgeAccess {
			pending = append(pending, u)
		}
	}
	for try := 0; len(pending) > 0; try++ {
		var failed []*dao.UserInternal
		for _, u := range pending {
			if err := registerUserDomain(d, u.Subdomain, u.Uuid, u.BridgeSecret); err != nil {
				log.Error("could not register", u.Subdomain, "on the bridge:", devtext.Text("", err))
				failed = append(failed, u)
			}
		}
		pending = failed
		if len(pending) == 0 || try >= len(cUserDomainRetries) {
			return
		}
		time.Sleep(cUserDomainRetries[try])
	}
}
