// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/lantls"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// cCodeLocalUnavailable is RespEnvelope.error_code when this device can't be
// reached on the home network (issue #190): the apps forget the endpoint
// they stored and stay on the bridge.
const cCodeLocalUnavailable = "local_unavailable"

// errLocalNotYet answers GetLocalEndpoint between websocket.Init, which
// starts the bridge pool, and api.Init setting the endpoint.
var errLocalNotYet = devtext.New(i18n.DevDeviceLocalStarting())

// localAddresses lists the home-network addresses; replaced in tests.
var localAddresses = lantls.Addresses

// SetLocalEndpoint is called by api once its home-network TLS listener is
// up (nil when it couldn't start). Connections are already being served
// by then, through the bridge pool, hence the atomic.
func (mg *Manager) SetLocalEndpoint(ep *lantls.Endpoint) {
	mg.local.Store(ep)
	mg.localSet.Store(true)
}

// localEndpoint answers GetLocalEndpoint, or says why there is nothing to
// answer.
func (mg *Manager) localEndpoint() (*pb.LocalEndpoint, error) {
	if !mg.localSet.Load() {
		return nil, errLocalNotYet
	}
	ep := mg.local.Load()
	if !ep.Serving() {
		return nil, devtext.New(i18n.DevDeviceLocalNoListener())
	}
	addrs, err := localAddresses()
	if err != nil {
		log.Error("could not list the home-network addresses:", err)
		return nil, devtext.New(i18n.DevDeviceLocalAddressesFailed())
	}
	if len(addrs) == 0 {
		return nil, devtext.New(i18n.DevDeviceLocalNoAddress())
	}
	return &pb.LocalEndpoint{
		Addresses:  addrs,
		Port:       int32(ep.Port()),
		CertSha256: ep.Pin(),
	}, nil
}
