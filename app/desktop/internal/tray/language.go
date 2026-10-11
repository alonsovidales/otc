// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import (
	"time"

	"fyne.io/systray"
	"github.com/ncruces/zenity"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/app/desktop/internal/oslang"
	"github.com/alonsovidales/otc/i18n"
)

// The Language submenu (docs/i18n.md, "The stored choice"): Automatic,
// with the language it gives ("Automatic (Español)"), then each language
// by its own name - names are never translated - with a check on the
// choice. The Mac's LanguagePicker in Settings. Only in a build with more
// than one language: while English is the only one that ships there is
// nothing to choose, and the menu is as it was.
//
// A pick is recorded in config.json as the local copy, pending, against
// the device's value as the engine last saw it (state.json); the engine
// sends it (engine/language.go) and the menu shows it at once.

type languageMenu struct {
	root *systray.MenuItem
	// items[i] picks codes[i]; Automatic ("") first.
	items []*systray.MenuItem
	codes []string
}

// addLanguageMenu adds the submenu under the items already there; nil in
// a build with only English.
func addLanguageMenu(cfg *config.Config) *languageMenu {
	if !oslang.Choosable() {
		return nil
	}
	lang := oslang.Effective(cfg.Language)
	m := &languageMenu{root: systray.AddMenuItem(languageTitle(lang), "")}
	m.items = append(m.items, m.root.AddSubMenuItemCheckbox(automaticTitle(lang), "", false))
	m.codes = append(m.codes, "")
	for _, l := range i18n.Languages() {
		m.items = append(m.items, m.root.AddSubMenuItemCheckbox(l.Name, "", false))
		m.codes = append(m.codes, l.Code)
	}

	return m
}

func languageTitle(lang string) string { return i18n.DeskMainLanguage().Render(lang) }

// automaticTitle is "Automatic (<the computer's language, by its own
// name>)", in lang.
func automaticTitle(lang string) string {
	return i18n.DeskMainLanguageAutomatic(oslang.Name(oslang.System())).Render(lang)
}

// shownChoice is the item checked for choice: a language this build
// doesn't carry is kept as the choice but shown, and checked, as English.
func shownChoice(choice string) string {
	if choice == "" || oslang.Carried(choice) {
		return choice
	}

	return oslang.English()
}

// applyLanguage writes the submenu for cfg (u.mu held): its own text in
// the language the choice gives, and the check.
func (u *ui) applyLanguage(cfg *config.Config) {
	m := u.language
	if m == nil {
		return
	}
	lang := oslang.Effective(cfg.Language)
	u.setTitle(m.root, languageTitle(lang))
	u.setTitle(m.items[0], automaticTitle(lang))
	shown := shownChoice(cfg.Language)
	for i, code := range m.codes {
		u.setChecked(m.items[i], code == shown)
	}
}

// watchLanguage handles the submenu's clicks until stop.
func (u *ui) watchLanguage(m *languageMenu, stop chan struct{}) {
	for i := range m.items {
		item, code := m.items[i], m.codes[i]
		go func() {
			for {
				select {
				case <-stop:
					return
				case <-item.ClickedCh:
					// Whatever the desktop did to the check, the next
					// apply writes it.
					u.forget(item)
					go u.chooseLanguage(code)
				}
			}
		}()
	}
}

// chooseLanguage records code ("" Automatic) as the choice made here.
func (u *ui) chooseLanguage(code string) {
	defer Refresh()
	cfg := u.editConfig()
	if cfg == nil || cfg.Language == code {
		return
	}
	pickLanguage(cfg, code, u.c.Snapshot(), time.Now())
	if err := u.c.SaveConfig(cfg); err != nil {
		_ = zenity.Error(err.Error(), zenity.Title("Off The Cloud"))
	}
}

// pickLanguage records code in cfg, pending, against the device's value
// in st (the running engine's; a tray next to a stopped sync has none).
func pickLanguage(cfg *config.Config, code string, st config.State, now time.Time) {
	cfg.SetLanguage(code, st.DeviceLanguage, now)
}
