// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import (
	"runtime"
	"slices"
	"strings"

	"fyne.io/systray"
	"github.com/ncruces/zenity"
)

// Every text otc-sync puts in the system's own dialogs and menus goes
// through nativeText: zenity's message, question, entry, list and progress
// dialogs (their text, title, buttons and rows), the tray menu's titles and
// tooltips, and the Windows remote folder picker (picker_windows.go's
// utf16). The i18n runtime wraps user text - a folder's name or path, a
// device's name - in Unicode isolation marks (FSI ... PDI), and the device's
// own words carry them too; Windows' message boxes, dialogs and menus draw
// them as boxes, so there they are left out. GTK on Linux isolates with them
// as it should, and gets the text as it is. Call the wrappers below, never
// zenity's or systray's own: TestNativeTextOnly fails on a direct call.

// winText is s as a dialog or menu on goos shows it: on Windows without the
// isolation marks LRI, RLI, FSI and PDI (U+2066-2069), elsewhere unchanged.
func winText(goos, s string) string {
	if goos != "windows" || !strings.ContainsFunc(s, isIsolationMark) {
		return s
	}

	return strings.Map(func(r rune) rune {
		if isIsolationMark(r) {
			return -1
		}

		return r
	}, s)
}

func isIsolationMark(r rune) bool { return r >= 0x2066 && r <= 0x2069 }

// nativeText is s as this computer's dialogs and menus show it.
func nativeText(s string) string { return winText(runtime.GOOS, s) }

// The dialogs (zenity).

func dlgInfo(text string, opts ...zenity.Option) error {
	return zenity.Info(nativeText(text), opts...)
}

func dlgError(text string, opts ...zenity.Option) error {
	return zenity.Error(nativeText(text), opts...)
}

func dlgWarning(text string, opts ...zenity.Option) error {
	return zenity.Warning(nativeText(text), opts...)
}

func dlgQuestion(text string, opts ...zenity.Option) error {
	return zenity.Question(nativeText(text), opts...)
}

func dlgEntry(text string, opts ...zenity.Option) (string, error) {
	return zenity.Entry(nativeText(text), opts...)
}

// dlgList returns the row picked as it is in items, marks and all, so a
// caller finds it there.
func dlgList(text string, items []string, opts ...zenity.Option) (string, error) {
	rows := nativeRows(items)
	pick, err := zenity.List(nativeText(text), rows, opts...)

	return fromRow(items, rows, pick), err
}

func dlgListMultiple(text string, items []string, opts ...zenity.Option) ([]string, error) {
	rows := nativeRows(items)
	picked, err := zenity.ListMultiple(nativeText(text), rows, opts...)
	for i, p := range picked {
		picked[i] = fromRow(items, rows, p)
	}

	return picked, err
}

func nativeRows(items []string) []string {
	rows := make([]string, len(items))
	for i, s := range items {
		rows[i] = nativeText(s)
	}

	return rows
}

// fromRow is the item a picked row shows.
func fromRow(items, rows []string, pick string) string {
	if i := slices.Index(rows, pick); i >= 0 {
		return items[i]
	}

	return pick
}

// dlgProgress is a progress dialog whose Text goes through nativeText too.
func dlgProgress(opts ...zenity.Option) (zenity.ProgressDialog, error) {
	d, err := zenity.Progress(opts...)
	if err != nil {
		return d, err
	}

	return nativeProgress{d}, nil
}

type nativeProgress struct{ zenity.ProgressDialog }

func (p nativeProgress) Text(s string) error { return p.ProgressDialog.Text(nativeText(s)) }

func dlgTitle(s string) zenity.Option     { return zenity.Title(nativeText(s)) }
func dlgOK(s string) zenity.Option        { return zenity.OKLabel(nativeText(s)) }
func dlgCancel(s string) zenity.Option    { return zenity.CancelLabel(nativeText(s)) }
func dlgExtra(s string) zenity.Option     { return zenity.ExtraButton(nativeText(s)) }
func dlgEntryText(s string) zenity.Option { return zenity.EntryText(nativeText(s)) }

// The tray menu (systray).

func addMenuItem(title, tip string) *systray.MenuItem {
	return systray.AddMenuItem(nativeText(title), nativeText(tip))
}

func addMenuItemCheckbox(title, tip string, checked bool) *systray.MenuItem {
	return systray.AddMenuItemCheckbox(nativeText(title), nativeText(tip), checked)
}

func addSubMenuItem(mi *systray.MenuItem, title, tip string) *systray.MenuItem {
	return mi.AddSubMenuItem(nativeText(title), nativeText(tip))
}

func addSubMenuItemCheckbox(mi *systray.MenuItem, title, tip string, checked bool) *systray.MenuItem {
	return mi.AddSubMenuItemCheckbox(nativeText(title), nativeText(tip), checked)
}

func setItemTitle(mi *systray.MenuItem, s string) { mi.SetTitle(nativeText(s)) }

func setItemTip(mi *systray.MenuItem, s string) { mi.SetTooltip(nativeText(s)) }

// setTrayTitle and setTrayTip are the tray icon's own title (beside it, on
// the desktops that show one) and tooltip.
func setTrayTitle(s string) { systray.SetTitle(nativeText(s)) }

func setTrayTip(s string) { systray.SetTooltip(nativeText(s)) }
