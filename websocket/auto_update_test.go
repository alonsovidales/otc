// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"testing"

	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/supervisor"
	"github.com/alonsovidales/otc/updater"
)

func autoUpdateReq(on bool) *pb.ReqEnvelope {
	return &pb.ReqEnvelope{Id: 9, Payload: &pb.ReqEnvelope_ReqSetAutoUpdate{ReqSetAutoUpdate: &pb.SetAutoUpdate{Enabled: on}}}
}

// Automatic updates are the device's: the owner of the primary instance
// turns them off and on again; a user's instance and a container are
// refused, and change nothing.
func TestSetAutoUpdate(t *testing.T) {
	// The switch is kept in the working directory.
	t.Chdir(t.TempDir())
	wasContainer := inContainerHere
	t.Cleanup(func() { inContainerHere = wasContainer })
	inContainerHere = func() bool { return false }

	primary := &connHandler{mg: &Manager{sup: &supervisor.Supervisor{}}}
	if !updater.AutoEnabled() {
		t.Fatal("automatic updates start off")
	}
	resp, _ := primary.processAuthRequest(autoUpdateReq(false), "")
	if resp == nil || resp.Error || !resp.GetRespAck().GetOk() || resp.Id != 9 || updater.AutoEnabled() {
		t.Fatalf("turning off: %+v, enabled %v", resp, updater.AutoEnabled())
	}

	child := &connHandler{mg: &Manager{}}
	resp, _ = child.processAuthRequest(autoUpdateReq(true), "")
	if resp == nil || !resp.Error || updater.AutoEnabled() {
		t.Errorf("a user's instance: %+v, enabled %v", resp, updater.AutoEnabled())
	}

	inContainerHere = func() bool { return true }
	resp, _ = primary.processAuthRequest(autoUpdateReq(true), "")
	if resp == nil || !resp.Error || resp.ErrorCode != "not_in_container" || updater.AutoEnabled() {
		t.Errorf("a container: %+v, enabled %v", resp, updater.AutoEnabled())
	}

	inContainerHere = func() bool { return false }
	resp, _ = primary.processAuthRequest(autoUpdateReq(true), "")
	if resp == nil || resp.Error || !updater.AutoEnabled() {
		t.Errorf("turning on again: %+v, enabled %v", resp, updater.AutoEnabled())
	}
}
