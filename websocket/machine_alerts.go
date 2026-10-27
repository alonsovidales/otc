// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"fmt"

	"github.com/alonsovidales/otc/alerttext"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/container"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/network"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/push"
	"github.com/alonsovidales/otc/raidwatch"
	"github.com/alonsovidales/otc/storagewatch"
	"github.com/alonsovidales/otc/supervisor"
	"github.com/alonsovidales/otc/updater"
	"github.com/alonsovidales/otc/wifiwatch"
)

// startMachineAlerts starts, from Init, the watchers whose alerts are
// about the machine rather than one user's library: updates, the
// mirror's disks, the storage's free space and the Wi-Fi. Each runs on the main instance only (sup
// != nil), so they land in the primary owner's Notifications and push
// with the primary's Push. Alerts are stored with their keys and pushes
// rendered at delivery, each in its reader's language (alerttext,
// push.Text).
func startMachineAlerts(dao *dao.Dao, ps *push.Push, sup *supervisor.Supervisor) {
	// Issue #183: check for updates by itself, on the main instance (the
	// one that can install them), and tell the owner about a major or
	// critical one in Notifications - a critical one also as a push, sent
	// with the notification so once per release.
	if sup != nil {
		go updater.Watch(func(a *updater.Alert) {
			updateAlert(dao, ps.NotifyText, a)
		})
	}

	// A disk of the mirror that stops working: an Alert and a push naming
	// the USB port it is in, and another once the mirror is whole again.
	// The one device error that is pushed - the others never are (#64).
	// In a container only with its own mirror (mirrorWatched), whose alerts
	// name a disk as in .env and whose pushes don't name it at all.
	if sup != nil && mirrorWatched() && cfg.HasSection("otc") && cfg.GetStr("otc", "storage-path") != "" {
		go raidwatch.Watch(cfg.GetStr("otc", "storage-path"), raidwatch.Notify{
			AlertText: func(a alerttext.Alert) {
				if err := dao.AddStorageNotification(a); err != nil {
					log.Error("could not add the storage notification:", err)
				}
			},
			PushText: func(t push.Text) { ps.NotifyText(t, push.Target{}) },
		})
		// What the root mirror watcher (raid_watch.py, or otc-mirror in a
		// container) sees and does, copied into this device's log
		// (Settings > Logs > Device): the disks, the mirror, why a disk
		// wasn't added, the kernel's lines about them.
		go raidwatch.FollowLog(cfg.GetStr("otc", "storage-path"))
	}

	// The storage filling up: an Alert at 90% full and at 98%, which is
	// also pushed (storagewatch) - in a container too, its folder or mirror.
	if sup != nil && cfg.HasSection("otc") && cfg.GetStr("otc", "storage-path") != "" {
		go storagewatch.Watch(cfg.GetStr("otc", "storage-path"), storagewatch.Notify{
			AlertText: func(a alerttext.Alert) {
				if err := dao.AddStorageNotification(a); err != nil {
					log.Error("could not add the disk space notification:", err)
				}
			},
			PushText: func(t push.Text) { ps.NotifyText(t, push.Target{}) },
		})
	}

	// A Wi-Fi that stopped sending properly (Pit, brcmfmac over SDIO) is
	// restarted, once per episode, with an Alert once it has been judged
	// - see wifiwatch. Machine-level, so main instance only.
	if sup != nil {
		go wifiwatch.Watch(dao.AddErrorNotification)
	}

	// Settings > Wi-Fi changes the network through NetworkManager, whose
	// version the log names once (Settings > Logs), and whether this
	// device can.
	if sup != nil && !container.Enabled() {
		go func() {
			network.LogVersion()
			network.Managed()
		}()
	}
}

// mirrorWatched: this machine has a mirror of the device's own to watch -
// md0 on a Raspberry Pi, or in a container the one docker/compose.mirror.yaml
// builds (container.MirrorArray), whose disks the alerts name as in .env.
// Never another of the host's arrays: /proc/mdstat and /sys/block are the
// host's in a container, and the host watches its own.
func mirrorWatched() bool {
	return !container.Enabled() || container.MirrorArray() != ""
}

// updateTitles are the English titles of the alert for an update to
// version: a major one's and a critical one's. Rows written before
// release 118 have no update_version, only one of these, so these exact
// strings stay in the match even if the English title is ever reworded -
// they are matched, never shown: the alert's own title is the catalog's.
func updateTitles(version string) (major, critical string) {
	return fmt.Sprintf("Update %s is available", version), // i18n-ignore: matched against rows from before release 118, never shown
		fmt.Sprintf("Critical update %s - please install it soon", version) // i18n-ignore: matched against rows from before release 118, never shown
}

// updateAlert adds the alert for a, once per version
// (dao.AddUpdateNotification: by update_version, and by both English
// titles for the rows from before it), and for a critical update pushes
// it with the alert, so once too (push.CriticalUpdate, rendered in the
// push language when it is delivered).
//
// In a container the release is installed by pulling its image, so the
// alert and the push say that instead, and come only once the image is
// published (updater.Check already keeps the newer releases out until
// then; checked again here, as nothing must send the owner to pull an
// image that isn't there).
func updateAlert(d *dao.Dao, notify func(t push.Text, target push.Target), a *updater.Alert) {
	inContainer := container.Enabled()
	if inContainer {
		published, err := imageAvailable(a.Target)
		if err != nil {
			log.Info("update", a.Version, "is not announced yet: the registry could not be asked:", err)
			return
		}
		if !published {
			log.Info("update", a.Version, "is not announced yet: its image is not published")
			return
		}
	}
	major, critical := updateTitles(a.Version)
	title := i18n.DevAlertUpdateMajor(a.Version)
	isCritical := a.Level == updater.KindCritical
	if isCritical {
		title = i18n.DevAlertUpdateCritical(a.Version)
	}
	detail := i18n.DevAlertUpdateDetail(a.Summary)
	if inContainer {
		detail = i18n.DevAlertUpdateDetailContainer(a.Summary)
	}
	alert := alerttext.New(title, detail)
	added, err := d.AddUpdateNotification(a.Version, alert, []string{major, critical})
	if err != nil {
		log.Error("could not add the update notification:", err)
		return
	}
	if added && isCritical {
		text := push.CriticalUpdate(a.Version, a.Summary)
		if inContainer {
			text = push.CriticalImageUpdate(a.Version, a.Summary)
		}
		notify(text, push.Target{})
	}
}

// imageAvailable is container.ImageAvailable; a var for tests.
var imageAvailable = container.ImageAvailable

// localizeAlerts puts the alerts among notifications (ListNotifications'
// answer) in lang, the request's language; on a failure they keep their
// English (logged).
func localizeAlerts(d *dao.Dao, lang string, notifications []*pb.Notification) {
	if err := d.LocalizeNotifications(lang, notifications); err != nil {
		log.Error("could not read the alerts' keys, shown in English:", err)
	}
}
