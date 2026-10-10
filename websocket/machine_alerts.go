// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"fmt"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/push"
	"github.com/alonsovidales/otc/raidwatch"
	"github.com/alonsovidales/otc/supervisor"
	"github.com/alonsovidales/otc/updater"
	"github.com/alonsovidales/otc/wifiwatch"
)

// startMachineAlerts starts, from Init, the watchers whose alerts are
// about the machine rather than one user's library: updates, the
// mirror's disks and the Wi-Fi. Each runs on the main instance only (sup
// != nil), so they land in the primary owner's Notifications and push
// with the primary's Push.
func startMachineAlerts(dao *dao.Dao, ps *push.Push, sup *supervisor.Supervisor) {
	// Issue #183: check for updates by itself, on the main instance (the
	// one that can install them), and tell the owner about a major or
	// critical one in Notifications - a critical one also as a push, sent
	// with the notification so once per release.
	if sup != nil {
		go updater.Watch(func(a *updater.Alert) {
			title := fmt.Sprintf("Update %s is available", a.Version)
			critical := a.Level == updater.KindCritical
			if critical {
				title = fmt.Sprintf("Critical update %s - please install it soon", a.Version)
			}
			body := a.Summary + " Install it from Settings."
			added, err := dao.AddUpdateNotification(title, body)
			if err != nil {
				log.Error("could not add the update notification:", err)
				return
			}
			if added && critical {
				ps.Notify(title, body, push.Target{})
			}
		})
	}

	// A disk of the mirror that stops working: an Alert and a push naming
	// the USB port it is in, and another once the mirror is whole again.
	// The one device error that is pushed - the others never are (#64).
	if sup != nil && cfg.HasSection("otc") && cfg.GetStr("otc", "storage-path") != "" {
		go raidwatch.Watch(cfg.GetStr("otc", "storage-path"), raidwatch.Notify{
			Alert: func(title, detail string) {
				if err := dao.AddStorageNotification(title, detail); err != nil {
					log.Error("could not add the storage notification:", err)
				}
			},
			Push: func(title, body string) { ps.Notify(title, body, push.Target{}) },
		})
	}

	// A Wi-Fi that stopped sending properly (Pit, brcmfmac over SDIO) is
	// restarted, once per episode, with an Alert once it has been judged
	// - see wifiwatch. Machine-level, so main instance only.
	if sup != nil {
		go wifiwatch.Watch(dao.AddErrorNotification)
	}
}
