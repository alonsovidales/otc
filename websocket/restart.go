// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"time"

	"github.com/alonsovidales/otc/devicerestart"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// Settings > Restart device (release 115; devicerestart): the whole
// machine, so the owner of the primary instance only - refused on a
// supervised user's instance (processAuthRequest) and on a demo device
// (demoRules). As Shut down device (shutdown.go), not while an update or a
// Wi-Fi change runs: a reboot in the minutes after an update restarted the
// service, while update.sh checks the new release starts, left a release
// that doesn't start installed with no way back, and one in the middle of
// a Wi-Fi change could leave the device on a network it can't join. The
// root side drops a request that slipped past this too
// (otc-reboot-runner). In a container the entrypoint restarts the device's
// process; nothing there updates or changes a network, so it always may.

// restartRequest is the seam the tests set.
var restartRequest = devicerestart.Request

// restartDevice answers RestartDevice on the primary instance.
func restartDevice(resp *pb.RespEnvelope, lang string) {
	updating := false
	// Under the lock JoinWifi checks for an update in, as ApplyUpdate and
	// ShutDownDevice: neither a change nor an update slips past this check.
	ran, err := wifiUnlessChanging(func() error {
		if updateRunning() {
			updating = true
			return nil
		}
		return restartRequest()
	})
	switch {
	case !ran:
		resp.Error = true
		resp.ErrorCode = "wifi_changing"
		resp.ErrorMessage = i18n.DevDeviceRestartWifiChanging().Render(lang)
	case updating:
		resp.Error = true
		resp.ErrorCode = "update_running"
		resp.ErrorMessage = i18n.DevDeviceRestartUpdateRunning().Render(lang)
	case err != nil:
		log.Error("error requesting a device restart:", err)
		resp.Error = true
		resp.ErrorMessage = errText(lang, err)
	default:
		resp.Payload = &pb.RespEnvelope_RespRestartingDevice{RespRestartingDevice: &pb.RestartingDevice{
			ExpectedSeconds: int32(devicerestart.ExpectedDowntime / time.Second),
		}}
	}
}
