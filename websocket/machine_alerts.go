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
			updateAlert(dao, ps.Notify, a)
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

// updateTitles are the English titles of the alert for an update to
// version: a major one's and a critical one's. Rows written before
// release 118 have no update_version, only one of these, so these exact
// strings stay in the match even if the English title is ever reworded.
func updateTitles(version string) (major, critical string) {
	return fmt.Sprintf("Update %s is available", version),
		fmt.Sprintf("Critical update %s - please install it soon", version)
}

// updateAlert adds the alert for a, once per version
// (dao.AddUpdateNotification: by update_version, and by both English
// titles for the rows from before it), and for a critical update pushes
// it with the alert, so once too.
func updateAlert(d *dao.Dao, notify func(title, body string, t push.Target), a *updater.Alert) {
	major, critical := updateTitles(a.Version)
	title := major
	isCritical := a.Level == updater.KindCritical
	if isCritical {
		title = critical
	}
	body := a.Summary + " Install it from Settings."
	added, err := d.AddUpdateNotification(a.Version, title, body, []string{major, critical})
	if err != nil {
		log.Error("could not add the update notification:", err)
		return
	}
	if added && isCritical {
		notify(title, body, push.Target{})
	}
}
