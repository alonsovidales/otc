// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/alonsovidales/otc/bridgeaccess"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/wsframe"
	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

// Issue #182: leaving the bridge. A device whose owner switches the bridge
// off, or whose name the bridge no longer knows (its account deleted, the
// name released), goes on working locally at http://otc.local:8080, as if
// it had been set up without the bridge: [otc] bridge-addr is emptied by
// the root runner and its domain becomes "otc" again.

// cCodeDomainNotRegistered is the bridge's RespEnvelope.error_code for a
// name it doesn't know.
const cCodeDomainNotRegistered = "domain_not_registered"

// cLocalDomain is a local-only device's domain (install.sh's default).
const cLocalDomain = "otc"

// cNameUnknownGrace is how long the bridge must keep answering "this name
// is not registered" before the device takes it as final: one answer
// could be a mistake on the bridge's side, and leaving costs the device
// its friends.
var cNameUnknownGrace = 15 * time.Minute

type leaveState struct {
	mu           sync.Mutex
	firstUnknown time.Time
	leaving      bool
}

// nameUnknownOnBridge records one "not registered" answer, and leaves the
// bridge once they have lasted cNameUnknownGrace.
func (mg *Manager) nameUnknownOnBridge() {
	if mg.sup == nil {
		return // an additional user's instance: the primary decides
	}
	mg.leave.mu.Lock()
	now := time.Now()
	if mg.leave.firstUnknown.IsZero() {
		mg.leave.firstUnknown = now
	}
	due := !mg.leave.leaving && now.Sub(mg.leave.firstUnknown) >= cNameUnknownGrace
	if due {
		mg.leave.leaving = true
	}
	mg.leave.mu.Unlock()
	if !due {
		return
	}
	_, domain, _ := mg.settings.Identity()
	log.Info("the bridge no longer knows", domain, "- switching to local-only")
	if err := mg.dao.AddErrorNotification("This device left the bridge",
		fmt.Sprintf("The bridge no longer knows %s: its account was deleted or the name released. The device goes on working at home, at %s - set the apps to that address, or join the bridge again in Settings.", domain, localURL())); err != nil {
		log.Error("could not add the notification:", err)
	}
	if err := mg.switchToLocal(bridgeaccess.LeftReleased); err != nil {
		log.Error("could not switch to local-only:", err)
		mg.leave.mu.Lock()
		mg.leave.leaving = false
		mg.leave.mu.Unlock()
	}
}

// nameKnownOnBridge resets the count after a successful registration.
func (mg *Manager) nameKnownOnBridge() {
	mg.leave.mu.Lock()
	mg.leave.firstUnknown = time.Time{}
	mg.leave.mu.Unlock()
}

// leaveBridge is the owner's "Leave the bridge": the name is given back
// to the bridge (best effort - if the bridge can't be reached the owner
// can still release it from the account page) and the device switches to
// local-only.
func (mg *Manager) leaveBridge() (releaseErr, err error) {
	releaseErr = mg.releaseBridgeDomain()
	if releaseErr != nil {
		log.Error("could not give the name back to the bridge:", releaseErr)
	}
	return releaseErr, mg.switchToLocal(bridgeaccess.LeftByOwner)
}

// switchToLocal resets the identity's domain and asks the root side to
// take the device off the bridge; it restarts when that is done.
func (mg *Manager) switchToLocal(reason string) error {
	if err := bridgeaccess.CanSwitchOn(); err != nil {
		return err
	}
	owner, _, secret := mg.settings.Identity()
	if err := mg.dao.SetBridgeIdentity(owner, cLocalDomain, secret); err != nil {
		return fmt.Errorf("storing the local identity: %w", err)
	}
	mg.settings.SetIdentity(owner, cLocalDomain, secret)
	return bridgeaccess.RequestSwitchOff(reason)
}

// localAddress is this device's address on the home network with its
// HTTP port ("192.168.1.20:8080"): the source address of the route to the
// internet (a UDP "connection" sends nothing). Empty if there is none.
func localAddress() string {
	c, err := net.Dial("udp", "1.1.1.1:53")
	if err != nil {
		return ""
	}
	defer c.Close()
	addr, ok := c.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP.IsLoopback() || !addr.IP.IsPrivate() {
		return ""
	}
	port := 8080
	if cfg.HasSection("otc-api") {
		if p := cfg.GetInt("otc-api", "port"); p > 0 {
			port = int(p)
		}
	}
	return net.JoinHostPort(addr.IP.String(), strconv.Itoa(port))
}

// localURL is how the owner opens the device at home.
func localURL() string {
	if a := localAddress(); a != "" {
		return "http://" + a
	}
	return "http://otc.local:8080"
}

// releaseBridgeDomain asks the bridge to delete this device's name
// (BridgeReleaseDomain), authenticated by its secret - a one-off
// connection like regenerateBridgeSecret's.
func (mg *Manager) releaseBridgeDomain() error {
	if !bridgeConfigured() {
		return errors.New("this device is not on the bridge")
	}
	u := url.URL{Scheme: "wss", Host: cfg.GetStr("otc", "bridge-addr"), Path: "/ws"}
	h := http.Header{}
	h.Set("Sec-WebSocket-Protocol", "protobuf")
	c, err := wsframe.Dial(u.String(), h)
	if err != nil {
		return fmt.Errorf("dialing bridge: %w", err)
	}
	defer c.Close()

	owner, domain, secret := mg.settings.Identity()
	b, err := proto.Marshal(&pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqBridgeReleaseDomain{
			ReqBridgeReleaseDomain: &pb.BridgeReleaseDomain{OwnerUuid: owner, Domain: domain, Secret: secret},
		},
	})
	if err != nil {
		return err
	}
	if err := c.WriteMessage(gorilla.BinaryMessage, b); err != nil {
		return fmt.Errorf("writing to bridge: %w", err)
	}
	_, data, err := c.ReadMessage()
	if err != nil {
		return fmt.Errorf("reading from bridge: %w", err)
	}
	var resp pb.RespEnvelope
	if err := proto.Unmarshal(data, &resp); err != nil {
		return err
	}
	if resp.Error {
		return errors.New(resp.ErrorMessage)
	}
	return nil
}
