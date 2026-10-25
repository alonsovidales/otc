// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"context"
	"errors"

	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/network"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/updater"
)

// Settings > Wi-Fi (release 140): the owner of the primary instance sees
// the network the device is on and changes it - a new network, or the
// same one with its new password. See the network package; the root
// runner does the change. Before the demo and container gates
// (demoGate, containerGate) and a child instance (sup == nil), which
// answer without reaching these.

// Seams for the tests: the network package's answers.
var (
	wifiManaged = network.Managed
	wifiCurrent = network.Current
	wifiLast    = network.Status
	wifiJoin    = network.Join
	// wifiUnlessChanging starts an update unless a change is running
	// (ApplyUpdate), under the lock Join checks updateRunning in.
	wifiUnlessChanging = network.UnlessChanging
	// updateRunning: an update was asked for or runs (its downloads would
	// fail mid-switch, and it restarts the service at its end).
	updateRunning = updater.Pending
)

// wifiJoinProto is a change as WifiJoin, its message in lang.
func wifiJoinProto(j *network.JoinStatus, lang string) *pb.WifiJoin {
	if j == nil {
		return nil
	}
	return &pb.WifiJoin{
		Id:       j.ID,
		Ssid:     j.SSID,
		State:    j.State,
		Code:     j.Code,
		Message:  j.Text(lang),
		Restored: j.Left && j.Restored,
	}
}

// wifiStatus answers GetWifiStatus on the primary instance.
func wifiStatus(lang string) *pb.WifiStatus {
	if !wifiManaged() {
		return &pb.WifiStatus{}
	}
	ssid, signal, wired := wifiCurrent(context.Background())
	return &pb.WifiStatus{
		Managed:  true,
		Ssid:     ssid,
		Signal:   signal,
		Wired:    wired,
		LastJoin: wifiJoinProto(wifiLast(), lang),
	}
}

// joinWifi answers JoinWifi on the primary instance: refused with a code
// before anything changes, or the change as it starts.
func (ch *connHandler) joinWifi(req *pb.JoinWifi, resp *pb.RespEnvelope, lang string) {
	var password []byte
	if len(req.Password) > 0 {
		pw, err := ch.decryptSecretBytes(req.Password)
		if err != nil {
			resp.Error = true
			resp.ErrorMessage = errText(lang, err)
			return
		}
		password = pw
		defer clear(password)
	}
	// The network it is on as the change starts (the answer is a
	// WifiStatus like any other: the apps show what it says), read
	// before the runner can take it away.
	ssid, signal, wired := wifiCurrent(context.Background())
	j, err := wifiJoin(req.Ssid, password, req.Hidden, updateRunning)
	if err != nil {
		resp.Error = true
		var r *network.Refusal
		if errors.As(err, &r) {
			resp.ErrorCode = r.Code
			resp.ErrorMessage = r.Msg.Render(lang)
			return
		}
		log.Error("wifi: could not hand the change over:", err)
		resp.ErrorMessage = errText(lang, err)
		return
	}
	resp.Payload = &pb.RespEnvelope_RespWifiStatus{RespWifiStatus: &pb.WifiStatus{
		Managed:  true,
		Ssid:     ssid,
		Signal:   signal,
		Wired:    wired,
		LastJoin: wifiJoinProto(&j, lang),
	}}
}

// wifiNetworksProto is the scan as WifiNetworks.
func wifiNetworksProto(networks []network.Network) *pb.WifiNetworks {
	out := make([]*pb.WifiNetwork, len(networks))
	for i, n := range networks {
		out[i] = &pb.WifiNetwork{
			Ssid:        n.SSID,
			Signal:      n.Signal,
			Secured:     n.Secured,
			Current:     n.Current,
			Unsupported: n.Unsupported,
		}
	}
	return &pb.WifiNetworks{Networks: out}
}
