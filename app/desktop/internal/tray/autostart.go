// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import (
	"errors"

	"github.com/ncruces/zenity"
)

// Start at login, only when the user asks for it - the macOS app's rule
// (LoginItem.swift, App Store guideline 2.4.5(iii)). Nothing is registered
// until the user says yes: once, after the first successful connection, the
// tray asks; the menu's "Start at login" checkbox and `otc-sync autostart
// on|off` change it any time. The words are the Mac's (LoginItemText), with
// "computer" for "Mac" and the menu for its Settings.
const (
	autostartTitle     = "Start Off The Cloud when you log in?"
	autostartMessage   = "Your folders keep syncing in the background after you restart your computer. You can change this with “Start at login” in the menu."
	autostartYes       = "Start at Login"
	autostartNo        = "Not Now"
	autostartToggle    = "Start at login"
	autostartToggleTip = "Start Off The Cloud when you log in, so your folders keep syncing after a restart"
)

// askAutostart is the one-time question. Either answer is recorded, so it
// is never asked again (config.json's "autostart").
func (u *ui) askAutostart() {
	if !u.c.OfferAutostart() {
		return
	}
	// It pops up unasked, so "Not Now" has the focus: Enter or Space -
	// typed into the window it covered - answers no. Consent is a click on
	// "Start at Login", as on the Mac (LoginItemOffer has no shortcut).
	err := zenity.Question(autostartTitle+"\n\n"+autostartMessage, autostartOptions()...)
	on, answered := autostartAnswer(err)
	if !answered {
		return
	}
	if err := u.c.SetAutostart(on); err != nil {
		_ = zenity.Error(err.Error(), zenity.Title("Off The Cloud"))
	}
	Refresh()
}

// autostartOptions are the question's dialog options, DefaultCancel
// among them (a test holds it there).
func autostartOptions() []zenity.Option {
	return []zenity.Option{
		zenity.Title("Off The Cloud"),
		zenity.OKLabel(autostartYes),
		zenity.CancelLabel(autostartNo),
		zenity.DefaultCancel(),
	}
}

// autostartAnswer reads the dialog: OK is yes; "Not Now" - or closing it -
// is no. Any other error (no dialog program on this desktop) records
// nothing: the question comes back at the next start, and the menu's
// checkbox still works.
func autostartAnswer(err error) (on, answered bool) {
	switch {
	case err == nil:
		return true, true
	case errors.Is(err, zenity.ErrCanceled):
		return false, true
	default:
		return false, false
	}
}
