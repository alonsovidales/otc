// SPDX-License-Identifier: AGPL-3.0-or-later

package accounts

import (
	"time"

	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/bridge/mailer"
	"github.com/alonsovidales/otc/log"
)

// Issue #176: the terms say an account unused for six months is removed,
// with an email a month before. Unused means no sign-in to the account
// page, no client reaching any of its devices through the bridge, and
// none of its devices connected to the bridge (dao.InactiveAccounts) - a
// device in use only at home or over Tailscale is still connected. And
// never while one of them is connected right now, whatever the stamps
// say (inUseNow). Removing is the same as the owner's "Delete my
// account": its names are released (held 30 days) and its devices go on
// working at home.
const (
	cInactiveFor     = 6 // months
	cWarnAhead       = 30 * 24 * time.Hour
	cInactivityEvery = 24 * time.Hour
)

// StartInactivityJob runs the check ten minutes after start and then once a
// day. dropDomains disconnects the devices of a removed account;
// deviceOnline reports whether a device is connected to the bridge now.
// Without email the job doesn't run at all: nobody is removed unwarned.
// Both cluster nodes run it; the warning is claimed with a conditional
// update, so each account gets one email.
func (a *Accounts) StartInactivityJob(dropDomains func([]string), deviceOnline func(string) bool) {
	if a.mailer == nil {
		log.Info("account emails are off: accounts are not removed for inactivity")
		return
	}
	a.deviceOnline = deviceOnline
	go func() {
		time.Sleep(10 * time.Minute)
		for {
			a.inactivityPass(time.Now(), a.mailer.Send, dropDomains)
			time.Sleep(cInactivityEvery)
		}
	}()
}

// inactivityPass warns the accounts that have been unused for a month less
// than the limit, and removes the ones unused for the whole limit that were
// warned at least a month ago.
func (a *Accounts) inactivityPass(now time.Time, send func(to, subject, body string) error, dropDomains func([]string)) {
	if err := a.dao.ClearUsedInactivityWarnings(); err != nil {
		log.Error("inactivity: clearing warnings:", err)
		return
	}
	removeAt := now.AddDate(0, -cInactiveFor, 0)
	warn, err := a.dao.InactiveAccounts(removeAt.Add(cWarnAhead))
	if err != nil {
		log.Error("inactivity: listing accounts:", err)
		return
	}
	for _, acc := range warn {
		if a.inUseNow(acc) || acc.WarnedAt != nil {
			continue
		}
		claimed, err := a.dao.MarkInactivityWarned(acc.ID, now)
		if err != nil || !claimed {
			continue
		}
		// In the account's language (emails.go), never logged.
		subject, body := a.inactivityEmail(acc.Lang, acc.Name, acc.Email, now.Add(cWarnAhead))
		if err := send(acc.Email, subject, body); mailer.AddressRefused(err) {
			// The mail server refuses the address itself: no later pass
			// would get a warning through either, and releasing it would
			// keep the account for ever. The attempt stands as the
			// warning, the month runs from it.
			log.Error("inactivity: the mail server refuses the address of account", acc.ID, "- counted as warned:", err)
		} else if err != nil {
			log.Error("inactivity: could not email the warning to account", acc.ID, "(will retry tomorrow):", err)
			// Not warned after all: the next pass tries again, and the
			// month before removal starts from a warning that went out.
			if uerr := a.dao.UnmarkInactivityWarned(acc.ID); uerr != nil {
				log.Error("inactivity: could not release the warning of account", acc.ID, ":", uerr)
			}
		} else {
			log.Info("inactivity: warned account", acc.ID)
		}
	}

	remove, err := a.dao.InactiveAccounts(removeAt)
	if err != nil {
		log.Error("inactivity: listing accounts:", err)
		return
	}
	for _, acc := range remove {
		// A month's notice, even when the warning went out late.
		if acc.WarnedAt == nil || now.Sub(*acc.WarnedAt) < cWarnAhead-24*time.Hour || a.inUseNow(acc) {
			continue
		}
		domains, err := a.dao.DeleteAccount(acc.ID)
		if err != nil {
			log.Error("inactivity: removing account", acc.ID, ":", err)
			continue
		}
		if dropDomains != nil && len(domains) > 0 {
			dropDomains(domains)
		}
		log.Info("inactivity: removed account", acc.ID, "domains released:", len(domains))
	}
}

// inUseNow reports whether one of acc's devices is connected to the bridge
// right now, which is use: such an account is never warned or removed.
// The stamps say so too (devices.last_online_at), but not on a database
// without the column, nor when a stamp failed to be written. A warning it
// had is forgotten, as ClearUsedInactivityWarnings would, so a later spell
// of inactivity warns again. When its devices can't be listed, it counts
// as in use until the next pass.
func (a *Accounts) inUseNow(acc dao.InactiveAccount) bool {
	if a.deviceOnline == nil {
		return false
	}
	domains, err := a.dao.ListAccountDomains(acc.ID)
	if err != nil {
		log.Error("inactivity: listing the devices of account", acc.ID, "- left for the next pass:", err)
		return true
	}
	for _, d := range domains {
		if !a.deviceOnline(d.Domain) {
			continue
		}
		if acc.WarnedAt != nil {
			if err := a.dao.UnmarkInactivityWarned(acc.ID); err != nil {
				log.Error("inactivity: could not forget the warning of account", acc.ID, ":", err)
			} else {
				log.Info("inactivity: account", acc.ID, "has a device online: its warning is forgotten")
			}
		}
		return true
	}
	return false
}
