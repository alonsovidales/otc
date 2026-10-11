// SPDX-License-Identifier: AGPL-3.0-or-later

package push

// ResolveLanguage is the language a device's pushes are written in
// (docs/i18n.md): the user's stored choice (settings.language), else the
// language of the app that last registered for pushes
// (settings.last_ui_language), else English. The device sends it to the
// bridge with its registrations (UpdatePushRegistrations.language), so the
// bridge's own pushes use it too. A code without text in this build
// renders English wherever a push is rendered.
func ResolveLanguage(stored, lastUI string) string {
	if stored != "" {
		return stored
	}
	if lastUI != "" {
		return lastUI
	}
	return "en"
}

// Lang is the language this Push's notifications are rendered in:
// Language's answer, or English when it isn't set (the bridge's Push, or a
// device before Init wired it).
//
// Phase 2 of the localization renders each push from a catalog key in
// Lang() here, in deliver, for every channel. Until then pushes stay
// English and only the bridge registrations read the language.
func (p *Push) Lang() string {
	if p.Language == nil {
		return "en"
	}
	if l := p.Language(); l != "" {
		return l
	}
	return "en"
}
