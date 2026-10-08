// SPDX-License-Identifier: AGPL-3.0-or-later

package autostart

// Start at login only when the user asks for it - the rule the macOS app
// follows for the Mac App Store (guideline 2.4.5(iii); LoginItem.swift),
// kept here too so the three clients behave alike. The user's answer is
// config.json's "autostart": true (the tray's "Start at Login", its
// checkbox, `otc-sync autostart on`), false ("Not Now", unticked,
// `autostart off`), absent while never asked. Nothing here reads config:
// the caller passes the choice in.

// Backend is where the registration lives: the XDG autostart file on
// Linux, the HKCU Run key on Windows. Tests use a fake.
type Backend interface {
	Enable(exe string) error
	Disable() error
	Enabled() (bool, error)
}

type system struct{}

func (system) Enable(exe string) error { return Enable(exe) }
func (system) Disable() error          { return Disable() }
func (system) Enabled() (bool, error)  { return Enabled() }

// System is this platform's registration.
var System Backend = system{}

// AtLaunch is the tray's start. With the user's yes, a registration that
// went missing (the file or Run value deleted) is written again. One that
// exists is left as it is, even if it names a program that has since moved
// (Enabled only checks that it exists): rewriting it at every start would
// undo a desktop's own "don't start" edit to the .desktop file
// (X-GNOME-Autostart-enabled=false, Hidden=true). Unticking and ticking
// "Start at login", or `otc-sync autostart on`, writes the current path.
// Without a recorded answer nothing is touched: no entry is ever made, and
// one an older otc-sync made unasked stays until the user answers the
// tray's one-time question. With a no, nothing either.
func AtLaunch(b Backend, choice *bool, exe string) error {
	if choice == nil || !*choice {
		return nil
	}
	on, err := b.Enabled()
	if err != nil || on {
		return err
	}

	return b.Enable(exe)
}

// Apply makes the registration match an answer the user just gave.
func Apply(b Backend, on bool, exe string) error {
	if on {
		return b.Enable(exe)
	}

	return b.Disable()
}
