// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"github.com/alonsovidales/otc/container"
	"github.com/alonsovidales/otc/devicerestart"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// Settings > Shut down device (issue #202, release 164; devicerestart's
// shutdown.go): the whole machine, so the owner of the primary instance
// only, as Restart device - refused on a supervised user's instance
// (processAuthRequest), in a container (there is no machine of its own to
// switch off: the server stops it) and on a demo device (demoRules). Not
// while an update or a Wi-Fi change runs either: switching off in the
// middle of one could leave the device half updated, or on a network it
// can't join. Answered with the generic Ack before anything happens - the
// root side waits a moment for the answer to leave.

// Seams for the tests.
var (
	shutDownDevice  = devicerestart.ShutDown
	inContainerHere = container.Enabled
)

// shutDown answers ShutDownDevice on the primary instance.
func shutDown(resp *pb.RespEnvelope, lang string) {
	if inContainerHere() {
		resp.Error = true
		resp.ErrorCode = "not_in_container"
		resp.ErrorMessage = i18n.DevDeviceShutdownInContainer().Render(lang)
		return
	}
	updating := false
	// Under the lock JoinWifi checks for an update in, as ApplyUpdate:
	// neither a change nor an update slips past this check.
	ran, err := wifiUnlessChanging(func() error {
		if updateRunning() {
			updating = true
			return nil
		}
		return shutDownDevice()
	})
	switch {
	case !ran:
		resp.Error = true
		resp.ErrorCode = "wifi_changing"
		resp.ErrorMessage = i18n.DevDeviceShutdownWifiChanging().Render(lang)
	case updating:
		resp.Error = true
		resp.ErrorCode = "update_running"
		resp.ErrorMessage = i18n.DevDeviceShutdownUpdateRunning().Render(lang)
	case err != nil:
		log.Error("error requesting a device shutdown:", err)
		resp.Error = true
		resp.ErrorMessage = errText(lang, err)
	default:
		resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
	}
}
