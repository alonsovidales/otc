// SPDX-License-Identifier: AGPL-3.0-or-later

// Package tray is PopoverView.swift as a system tray menu: status, RAID
// health, the web app, one row per folder (its submenu: Open - see
// openfolder.go -, Images, Remove), Add Folder… (addfolder.go),
// settings, start at login, quit. Dialogs are the OS's own (a folder
// chooser, a text entry, a list) rather than a window of ours, so the
// binary stays free of a GUI toolkit and cross-compiles from anywhere.
// Every text in them and in the menu goes through native.go (Windows draws
// the isolation marks around user text as boxes).
package tray

import (
	"errors"
	"fmt"
	"github.com/alonsovidales/otc/app/desktop/internal/selfupdate"
	"github.com/alonsovidales/otc/app/desktop/internal/service"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/systray"
	"github.com/ncruces/zenity"

	"github.com/alonsovidales/otc/app/desktop/internal/browser"
	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/app/desktop/internal/engine"
	"github.com/alonsovidales/otc/app/desktop/internal/icons"
	"github.com/alonsovidales/otc/app/desktop/internal/oslang"
	"github.com/alonsovidales/otc/i18n"
)

// shown is the language the menu and its dialogs speak (docs/i18n.md):
// the choice in config.json, else the computer's. apply sets it from the
// config it reads before it writes any text, and writes every item again
// when it changes; until the first apply - and in the tests - it is "",
// English.
var shown atomic.Value // string

// lang is the language the menu speaks now.
func lang() string {
	s, _ := shown.Load().(string)

	return s
}

func setLang(cfg *config.Config) string {
	l := oslang.Effective(cfg.Language)
	shown.Store(l)

	return l
}

// errorText is err for a dialog in lang: worded by the catalog when it is
// one of ours (config.InvalidError, selfupdate's), as it comes otherwise
// (the system's own words).
func errorText(lang string, err error) string {
	var m interface{ Msg() i18n.Msg }
	if errors.As(err, &m) {
		return m.Msg().Render(lang)
	}

	return err.Error()
}

// Controller is what the menu acts on: the running app, whether it owns
// the sync engine or is only a viewer next to the service.
type Controller interface {
	Snapshot() config.State
	// Config is for display: empty when config.json can't be read.
	Config() *config.Config
	// LoadConfig is for an edit that is saved back: it fails rather than
	// hand over an empty config to save over the real one.
	LoadConfig() (*config.Config, error)
	SaveConfig(*config.Config) error
	Password() string
	SetPassword(string) error
	// RemoteBrowser lists device folders for one use of the remote folder
	// picker; done is called once it closes.
	RemoteBrowser() (list func(path string) ([]engine.RemoteEntry, error), done func())
	// AutostartEnabled is whether the tray starts at login now;
	// SetAutostart records the user's answer and applies it.
	AutostartEnabled() bool
	SetAutostart(bool) error
	// OfferAutostart: the one-time question is due (autostart.go).
	OfferAutostart() bool
	Quit()
}

type folderItem struct {
	item *systray.MenuItem
	// Issue #197: shows the folder in the file manager (openfolder.go).
	open   *systray.MenuItem
	remove *systray.MenuItem
	// Issue #192: "Kept Out of Images" and the line under it (images.go).
	images     *systray.MenuItem
	imagesInfo *systray.MenuItem
	// Issue #132: the device's refusal of its upload-only request
	// (uploadonly.go).
	uploadInfo *systray.MenuItem
	id         string
	remote     bool
}

type ui struct {
	c         Controller
	mu        sync.Mutex
	title     *systray.MenuItem
	status    *systray.MenuItem
	raid      *systray.MenuItem
	store     *systray.MenuItem // the storage line's submenu: its storage in
	cpu       *systray.MenuItem // GB and the device's load, shown when the
	mem       *systray.MenuItem // pointer rests on it
	update    *systray.MenuItem // issue #183: a major or critical device update
	web       *systray.MenuItem // issue #193: the device's web app, in the browser
	appUpdate *systray.MenuItem // a newer otc-sync (selfupdate)
	setup     *systray.MenuItem // issue #184: the SD card wizard
	empty     *systray.MenuItem
	folders   []*folderItem
	add       *systray.MenuItem
	explain   *systray.MenuItem
	settings  *systray.MenuItem
	autost    *systray.MenuItem
	language  *languageMenu // nil while only English ships (language.go)
	quit      *systray.MenuItem
	lastIcon  string
	// The start-at-login question has been put this run (autostart.go).
	autostartAsked bool
	refresh        chan struct{}
	stopLoop       chan struct{}
	// What each item was last set to (u.mu): apply runs every 2 s and on
	// every engine change, and on Linux every write is D-Bus signals plus
	// a re-fetch of the whole menu by the desktop shell, so a write that
	// changes nothing is skipped. Emptied with the menu (build).
	titles, tips            map[*systray.MenuItem]string
	shown, checked, enabled map[*systray.MenuItem]bool
	lastTrayTip             string
}

// Run blocks until the tray quits.
func Run(c Controller) {
	u := &ui{c: c, refresh: make(chan struct{}, 1)}
	systray.Run(u.onReady, func() {})
}

// Refresh asks the menu to re-read the snapshot.
var refreshCh = make(chan struct{}, 1)

// Refresh is called by the engine's onChange (or the viewer's poll).
func Refresh() {
	select {
	case refreshCh <- struct{}{}:
	default:
	}
}

func (u *ui) onReady() {
	l := setLang(u.c.Config())
	setTrayTitle("")
	setTrayTip(i18n.DeskMainTitle().Render(l))
	u.build(nil)
	u.apply()
	go u.watchUpdates()
	if !u.configured() { // nothing set up yet: the settings come first
		go u.settingsDialog()
	}
	go func() {
		var last time.Time
		for {
			select {
			case <-refreshCh:
				// At most four a second: a pass changes something per
				// file. Changes meanwhile wait in refreshCh, and the apply
				// after the pause shows the latest.
				if d := 250*time.Millisecond - time.Since(last); d > 0 {
					time.Sleep(d)
				}
			case <-time.After(2 * time.Second):
			}
			u.apply()
			last = time.Now()
		}
	}()
}

// setTitle, setTip, setShown, setChecked and setEnabled write an item only
// when the value changed since the last write (u.mu held).
func (u *ui) setTitle(mi *systray.MenuItem, s string) {
	if v, ok := u.titles[mi]; ok && v == s {
		return
	}
	u.titles[mi] = s
	setItemTitle(mi, s)
}

func (u *ui) setTip(mi *systray.MenuItem, s string) {
	if v, ok := u.tips[mi]; ok && v == s {
		return
	}
	u.tips[mi] = s
	setItemTip(mi, s)
}

func (u *ui) setShown(mi *systray.MenuItem, on bool) {
	if v, ok := u.shown[mi]; ok && v == on {
		return
	}
	u.shown[mi] = on
	if on {
		mi.Show()
	} else {
		mi.Hide()
	}
}

func (u *ui) setChecked(mi *systray.MenuItem, on bool) {
	if v, ok := u.checked[mi]; ok && v == on {
		return
	}
	u.checked[mi] = on
	if on {
		mi.Check()
	} else {
		mi.Uncheck()
	}
}

func (u *ui) setEnabled(mi *systray.MenuItem, on bool) {
	if v, ok := u.enabled[mi]; ok && v == on {
		return
	}
	u.enabled[mi] = on
	if on {
		mi.Enable()
	} else {
		mi.Disable()
	}
}

// forget makes the next write of mi happen whatever it was last set to -
// for writes that went around the helpers.
func (u *ui) forget(mi *systray.MenuItem) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.titles, mi)
	delete(u.tips, mi)
	delete(u.shown, mi)
	delete(u.checked, mi)
	delete(u.enabled, mi)
}

// build lays the whole menu out in the macOS popover's order - status,
// folders, actions - which means starting over whenever the folder set
// changes, since a tray menu can only ever append.
func (u *ui) build(folders []config.FolderStatus) {
	if u.stopLoop != nil {
		close(u.stopLoop)
	}
	systray.ResetMenu()
	// Every item is new: the first apply writes them all, as before.
	u.titles, u.tips = map[*systray.MenuItem]string{}, map[*systray.MenuItem]string{}
	u.shown, u.checked, u.enabled = map[*systray.MenuItem]bool{}, map[*systray.MenuItem]bool{}, map[*systray.MenuItem]bool{}
	// The texts are the menu's language now; apply writes them again
	// (applyTexts), in the language then in effect.
	l := lang()
	u.title = addMenuItem(i18n.DeskMainTitle().Render(l), "")
	u.appUpdate = addMenuItem("", "")
	u.appUpdate.Hide()
	u.title.Disable()
	u.status = addMenuItem(i18n.DeskMainStatusNotConnected().Render(l), "")
	u.status.Disable()
	// Enabled, unlike the other status lines, or its submenu - the
	// device's storage in GB, CPU and memory - would never open; clicking
	// it does nothing. Storage first, as it details the line itself.
	u.raid = addMenuItem("", "")
	u.raid.Hide()
	u.store = addSubMenuItem(u.raid, "", "")
	u.store.Disable()
	u.store.Hide()
	u.cpu = addSubMenuItem(u.raid, "", "")
	u.cpu.Disable()
	u.mem = addSubMenuItem(u.raid, "", "")
	u.mem.Disable()
	u.update = addMenuItem("", "")
	u.update.Disable()
	u.update.Hide()
	// Issue #193: at the top, like the Mac's button beside the gear; shown
	// once a device is set (apply).
	u.web = addMenuItem(i18n.DeskMainOpenWebApp().Render(l), "")
	u.web.Hide()
	systray.AddSeparator()
	u.empty = addMenuItem(i18n.DeskMainNoFolders().Render(l), "")
	u.empty.Disable()
	u.folders = nil
	for _, f := range folders {
		mi := addMenuItem(folderTitle(f), "")
		open := addOpenItem(mi, f)
		img, info := addImagesItems(mi)
		upInfo := addUploadOnlyInfo(mi)
		rm := addSubMenuItem(mi, i18n.CommonRemove().Render(l), i18n.DeskMainRemoveTip().Render(l))
		u.folders = append(u.folders, &folderItem{item: mi, open: open, remove: rm, images: img, imagesInfo: info, uploadInfo: upInfo, id: f.ID, remote: f.RemotePath != ""})
	}
	if len(folders) > 0 {
		u.empty.Hide()
	}
	systray.AddSeparator()
	// The macOS app's "Add Folder…" (AddFolderChooser): the kind, its
	// options, then the folder (addfolder.go). A dialog has no (i) buttons,
	// so "What Do These Do?" explains the kinds and options.
	u.add = addMenuItem(i18n.DeskMainAddFolder().Render(l), i18n.DeskMainAddFolderTip().Render(l))
	u.explain = addMenuItem(i18n.DeskMainExplain().Render(l), i18n.DeskMainExplainTip().Render(l))
	// Connected: the device and Disconnect; not: the device and password.
	u.settings = addMenuItem(u.settingsTitle(), "")
	u.autost = addMenuItemCheckbox(autostartToggle(l), autostartToggleTip(l), u.c.AutostartEnabled())
	u.language = addLanguageMenu(u.c.Config())
	systray.AddSeparator()
	u.setup = addMenuItem(i18n.DeskMainSetUpDevice().Render(l), i18n.DeskMainSetUpDeviceTip().Render(l))
	systray.AddSeparator()
	u.quit = addMenuItem(i18n.CommonQuit().Render(l), "")

	stop := make(chan struct{})
	u.stopLoop = stop
	items := u.folders
	add, settings, autost, quit := u.add, u.settings, u.autost, u.quit
	explain, appUpdate, setup, web := u.explain, u.appUpdate, u.setup, u.web
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-appUpdate.ClickedCh:
				go u.installUpdate()
			case <-web.ClickedCh:
				go u.openWebApp()
			case <-add.ClickedCh:
				go u.addFolder()
			case <-explain.ClickedCh:
				go u.explainKinds()
			case <-setup.ClickedCh:
				go u.setupDevice()
			case <-settings.ClickedCh:
				if u.configured() {
					go u.disconnectDialog()
				} else {
					go u.settingsDialog()
				}
			case <-autost.ClickedCh:
				go u.toggleAutostart()
			case <-quit.ClickedCh:
				u.c.Quit()
				systray.Quit()

				return
			}
		}
	}()
	if u.language != nil {
		u.watchLanguage(u.language, stop)
	}
	for _, fi := range items {
		fi := fi
		go func() {
			for {
				select {
				case <-stop:
					return
				case <-fi.open.ClickedCh:
					go u.openFolder(fi)
				case <-fi.remove.ClickedCh:
					u.removeFolder(fi)
				case <-fi.images.ClickedCh:
					go u.toggleImages(fi)
				}
			}
		}()
	}
}

func (u *ui) apply() {
	u.mu.Lock()
	defer u.mu.Unlock()
	defer u.showUpdate() // the menu may have just been rebuilt
	// config.json read once per apply (it used to be twice), and never
	// cached: in viewer mode reading it is how the menu sees a device the
	// service or the CLI changed - and the language picked in another app.
	cfg := u.c.Config()
	l := setLang(cfg)
	st := u.c.Snapshot()
	want := make([]config.FolderStatus, 0, len(st.Folders)+len(st.RemoteFolders))
	want = append(want, st.Folders...)
	want = append(want, st.RemoteFolders...)
	same := len(want) == len(u.folders)
	if same {
		for i, f := range want {
			if u.folders[i].id != f.ID {
				same = false

				break
			}
		}
	}
	if !same {
		u.build(want)
	}
	u.applyTexts(l)
	// Issue #190: once connected, which way - the home network or the
	// bridge - in the line that says it is.
	line := engine.StatusText(l, st, cfg.Domain)
	u.setTitle(u.status, statusDot(st.Status)+" "+line)
	configured := cfg.Domain != "" && u.c.Password() != ""
	u.setTitle(u.settings, settingsTitleFor(cfg, configured))
	if addr := config.WebURL(cfg.Domain); configured && addr != "" {
		u.setTip(u.web, i18n.DeskMainOpenWebAppTip(addr).Render(l))
		u.setShown(u.web, true)
	} else {
		u.setShown(u.web, false)
	}
	if st.Raid != "" && st.Raid != string(engine.RaidUnknown) {
		u.setTitle(u.raid, storageTitle(st))
		if t := storageUseTitle(st); t != "" {
			u.setTitle(u.store, t)
			u.setShown(u.store, true)
		} else {
			u.setShown(u.store, false)
		}
		u.setTitle(u.cpu, cpuTitle(st))
		u.setTitle(u.mem, memoryTitle(st))
		u.setTip(u.raid, loadTip(st))
		u.setShown(u.raid, true)
	} else {
		u.setShown(u.raid, false)
	}
	if title, tip := updateTitle(st.UpdateAlert); title != "" {
		u.setTitle(u.update, title)
		u.setTip(u.update, tip)
		u.setShown(u.update, true)
	} else {
		u.setShown(u.update, false)
	}
	if st.Raid != u.lastIcon {
		systray.SetIcon(icons.For(st.Raid))
		u.lastIcon = st.Raid
	}
	if t := i18n.DeskMainTrayTip(line).Render(l); t != u.lastTrayTip {
		setTrayTip(t)
		u.lastTrayTip = t
	}
	for i, f := range want {
		u.setTitle(u.folders[i].item, folderTitle(f))
		u.applyOpen(u.folders[i], f)
		u.applyImages(u.folders[i], f)
		u.applyUploadOnly(u.folders[i], f)
	}
	// Read every time (a stat, or a registry read), so an entry removed
	// outside the app shows; only the write is skipped.
	u.setChecked(u.autost, u.c.AutostartEnabled())
	u.applyLanguage(cfg)
	// Once connected, start at login is asked about, once.
	if st.Status == engine.StatusConnected && !u.autostartAsked {
		u.autostartAsked = true
		go u.askAutostart()
	}
}

// applyTexts writes the items whose words don't change with the state,
// in lang: a language picked here, in another app or by the CLI shows at
// the next apply, without a restart (only an item whose words changed is
// written).
func (u *ui) applyTexts(l string) {
	u.setTitle(u.title, i18n.DeskMainTitle().Render(l))
	u.setTitle(u.web, i18n.DeskMainOpenWebApp().Render(l))
	u.setTitle(u.empty, i18n.DeskMainNoFolders().Render(l))
	u.setTitle(u.add, i18n.DeskMainAddFolder().Render(l))
	u.setTip(u.add, i18n.DeskMainAddFolderTip().Render(l))
	u.setTitle(u.explain, i18n.DeskMainExplain().Render(l))
	u.setTip(u.explain, i18n.DeskMainExplainTip().Render(l))
	u.setTitle(u.autost, autostartToggle(l))
	u.setTip(u.autost, autostartToggleTip(l))
	u.setTitle(u.setup, i18n.DeskMainSetUpDevice().Render(l))
	u.setTip(u.setup, i18n.DeskMainSetUpDeviceTip().Render(l))
	u.setTitle(u.quit, i18n.CommonQuit().Render(l))
	for _, fi := range u.folders {
		u.setTitle(fi.remove, i18n.CommonRemove().Render(l))
		u.setTip(fi.remove, i18n.DeskMainRemoveTip().Render(l))
	}
}

func statusDot(s string) string {
	switch s {
	case engine.StatusConnected:
		return "●"
	case engine.StatusDisconnected, engine.StatusSyncNotRunning, engine.StatusConnecting:
		return "◐"
	default:
		return "○"
	}
}

func folderTitle(f config.FolderStatus) string {
	arrow := "⬆"
	if f.RemotePath != "" {
		arrow = "⇅"
	}
	name := filepath.Base(f.Path)
	l := lang()
	var state string
	switch f.State {
	case string(engine.StateWatching):
		if f.RemotePath != "" {
			state = i18n.DeskMainFolderSynced().Render(l)
		} else {
			state = i18n.DeskMainFolderBackedUp().Render(l)
		}
	case string(engine.StateError):
		state = "⚠ " + i18n.RenderStored(l, f.MsgKey, f.MsgArgs, f.Error)
	default:
		switch {
		case f.CurrentFile != "" && f.Progress > 0:
			state = progressText(l, f.Progress, f.CurrentFile)
		case f.CurrentFile != "":
			// Issue #138: "Checking 12/300 · name" while hashing - no
			// percentage, that is not a transfer.
			state = i18n.RenderStored(l, f.MsgKey, f.MsgArgs, f.CurrentFile)
		default:
			state = i18n.DeskMainChecking().Render(l)
		}
	}

	return fmt.Sprintf("%s %s — %s%s%s", arrow, name, state, uploadOnlySuffix(f), imagesSuffix(f))
}

// progressText is a pass's progress in a folder's line: the percentage as
// lang writes one ("42% · 12/340 · photo.jpg", "42 % · …" in French), then
// the file it is on (state.json's "12/340 · photo.jpg").
func progressText(lang string, progress float64, file string) string {
	return i18n.DeskMainPercent(int(progress*100)).Render(lang) + " · " + file
}

// editConfig is config.json for an edit, or nil - and the reason shown -
// when it can't be read, so nothing is saved over it.
func (u *ui) editConfig() *config.Config {
	cfg, err := u.c.LoadConfig()
	if err != nil {
		_ = dlgError(errorText(lang(), err), dlgTitle("Off The Cloud"))

		return nil
	}

	return cfg
}

func (u *ui) removeFolder(fi *folderItem) {
	cfg := u.editConfig()
	if cfg == nil {
		return
	}
	if fi.remote {
		kept := cfg.RemoteFolders[:0:0]
		for _, f := range cfg.RemoteFolders {
			if f.ID != fi.id {
				kept = append(kept, f)
			}
		}
		cfg.RemoteFolders = kept
	} else {
		kept := cfg.Folders[:0:0]
		for _, f := range cfg.Folders {
			if f.ID != fi.id {
				kept = append(kept, f)
			}
		}
		cfg.Folders = kept
	}
	if err := u.c.SaveConfig(cfg); err != nil {
		_ = dlgError(errorText(lang(), err), dlgTitle("Off The Cloud"))
	}
	Refresh()
}

// explainKinds is the tray's (i): the macOS chooser's three explanations.
func (u *ui) explainKinds() {
	l := lang()
	_ = dlgInfo(explainText(l), dlgTitle(i18n.DeskMainExplainTitle().Render(l)), zenity.Width(520))
}

// explainText is "What Do These Do?" in lang: the Mac chooser's (i)s - the
// three kinds, then the options a folder from this computer has - one
// paragraph each.
func explainText(lang string) string {
	return strings.Join([]string{
		i18n.DeskMainExplainBackup().Render(lang),
		i18n.DeskMainExplainLocal().Render(lang),
		i18n.DeskMainExplainDevice().Render(lang),
		i18n.DeskMainExplainUploadOnly().Render(lang),
		i18n.DeskMainExplainImages().Render(lang),
	}, "\n\n")
}

// explainKindsText is the dialog's English, which the tests read; the
// dialog itself is made when it opens, in the menu's language.
var explainKindsText = explainText("")

// addRemote is RemoteFolderPickerView: browse the device's tree (a native
// window on Windows, the desktop's list dialog on Linux - see picker_*.go),
// then the local destination in the folder chooser. A folder from the
// device has no options: it keeps what it has there (Images, upload only),
// which its menu, the web or a phone change.
func (u *ui) addRemote() {
	list, done := u.c.RemoteBrowser()
	remote, ok := pickRemoteFolder(list)
	done() // not held open while the folder chooser is up
	if !ok {
		return
	}
	dir, err := zenity.SelectFile(zenity.Directory(), dlgTitle(i18n.DeskMainChooseDownload(remote).Render(lang())))
	if err != nil || dir == "" {
		return
	}
	cfg := u.editConfig()
	if cfg == nil {
		return
	}
	cfg.RemoteFolders = append(cfg.RemoteFolders, config.RemoteFolder{ID: config.NewID(), RemotePath: remote, LocalPath: dir})
	if err := u.c.SaveConfig(cfg); err != nil {
		_ = dlgError(errorText(lang(), err), dlgTitle("Off The Cloud"))
	}
	Refresh()
}

func baseName(p string) string {
	p = strings.TrimSuffix(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}

	return p
}

func parentPath(p string) string {
	p = strings.TrimSuffix(p, "/")
	i := strings.LastIndex(p, "/")
	if i <= 0 {
		return "/"
	}

	return p[:i]
}

// settings is SettingsInlineView: the device by name (issue #121) or any
// address, then the password.
func (u *ui) settingsDialog() {
	// Before the password is asked for: it is saved only with this config.
	cfg := u.editConfig()
	if cfg == nil {
		return
	}
	current := config.BridgeName(cfg.Domain)
	l := lang()
	if current == "" {
		current = cfg.Domain
	}
	// The buttons' words too: Windows' own dialog says OK and Cancel in
	// English otherwise.
	entered, err := dlgEntry(settingsHint(l), dlgTitle(i18n.DeskMainSettingsTitle().Render(l)), dlgEntryText(current),
		dlgOK(i18n.CommonOk().Render(l)), dlgCancel(i18n.CommonCancel().Render(l)))
	if err != nil {
		return
	}
	entered = strings.TrimSpace(entered)
	if entered == "" {
		return
	}
	oldDomain := cfg.Domain
	if strings.Contains(entered, "://") || strings.Contains(entered, ".") {
		cfg.Domain = entered
	} else {
		cfg.Domain = config.BridgeDomainForName(entered)
	}
	// Both halves are asked for before anything is saved, so the engine
	// reconnects once with the new pair - not once with the new device and
	// the old password, which would spend one of the device's five
	// password attempts per minute for nothing. In the menu's language as
	// the dialog opens: one may have been picked in the Language menu
	// while the first dialog was open (at the first start, it opens by
	// itself).
	l = lang()
	pw, err := askPassword(l, i18n.DeskMainPasswordTitle(cfg.Domain).Render(l))
	if err != nil {
		return
	}
	// config.json as it is now, with the new device: what the tray
	// changed while the dialogs were open - a language picked in its
	// Language menu, a change for the device set up now - stays.
	latest := u.editConfig()
	if latest == nil {
		return
	}
	latest.Domain = cfg.Domain
	if pw != "" {
		if err := u.c.SetPassword(pw); err != nil {
			_ = dlgError(errorText(lang(), err), dlgTitle("Off The Cloud"))

			return
		}
	}
	if latest.Domain != oldDomain {
		_ = config.ClearLocalEndpoint() // the old device's (issue #190)
	}
	if err := u.c.SaveConfig(latest); err != nil {
		_ = dlgError(errorText(lang(), err), dlgTitle("Off The Cloud"))
	}
	Refresh()
}

// settingsHint is the device dialog's prompt in lang. Windows' entry
// dialog shows its prompt on one line 241 pixels wide and cuts the rest,
// so there it is the short "Device name or address:"; Linux's dialog
// wraps the whole explanation, with its examples.
func settingsHint(lang string) string {
	if runtime.GOOS == "windows" {
		return i18n.DeskMainSettingsHintWindows().Render(lang)
	}

	return i18n.DeskMainSettingsHint().Render(lang)
}

func (u *ui) configured() bool {
	return u.c.Config().Domain != "" && u.c.Password() != ""
}

func deviceLabel(domain string) string {
	if name := config.BridgeName(domain); name != "" {
		return config.BridgeDomainForName(name)
	}
	return domain
}

func (u *ui) settingsTitle() string {
	cfg := u.c.Config()

	return settingsTitleFor(cfg, cfg.Domain != "" && u.c.Password() != "")
}

func settingsTitleFor(cfg *config.Config, configured bool) string {
	if configured {
		return i18n.DeskMainDisconnectMenu(deviceLabel(cfg.Domain)).Render(lang())
	}
	return i18n.DeskMainConnectMenu().Render(lang())
}

// disconnectDialog forgets the device: every folder is removed (the files
// stay where they are, here and on the device) and the address and
// password are cleared - folders kept across a change of device would
// start syncing with, or deleting on, a different device. Same as the
// Mac's Settings > Disconnect.
func (u *ui) disconnectDialog() {
	// Folders and device go, but the client id and autostart stay.
	cfg := u.editConfig()
	if cfg == nil {
		return
	}
	l := lang()
	if dlgQuestion(i18n.DeskMainDisconnectMessage().Render(l),
		dlgTitle(i18n.DeskMainDisconnectTitle(deviceLabel(cfg.Domain)).Render(l)),
		dlgOK(i18n.CommonDisconnect().Render(l)), zenity.WarningIcon) != nil {
		return
	}
	cfg.Folders = nil
	cfg.RemoteFolders = nil
	cfg.Domain = ""
	if err := u.c.SaveConfig(cfg); err != nil {
		_ = dlgError(errorText(l, err), dlgTitle("Off The Cloud"))
		return
	}
	_ = config.ClearLocalEndpoint() // and its home-network endpoint (issue #190)
	if err := u.c.SetPassword(""); err != nil {
		_ = dlgError(errorText(l, err), dlgTitle("Off The Cloud"))
	}
	Refresh()
	u.settingsDialog()
}

// openWebApp is the Mac's "Open Web App": the device's own web app at its
// configured address, in the default browser. Only the address goes - the
// web app asks for the password itself.
func (u *ui) openWebApp() {
	addr := config.WebURL(u.c.Config().Domain)
	if addr == "" {
		return
	}
	if err := browser.Open(addr); err != nil {
		_ = dlgError(i18n.DeskMainBrowserFailed(err.Error(), addr).Render(lang()),
			dlgTitle("Off The Cloud"))
	}
}

func (u *ui) toggleAutostart() {
	enable := !u.c.AutostartEnabled()
	if err := u.c.SetAutostart(enable); err != nil {
		_ = dlgError(errorText(lang(), err), dlgTitle("Off The Cloud"))
	}
	Refresh()
}

// raidSummary is the storage line's health in the menu's language: from
// the health's code, or as state.json words it when there is none.
func raidSummary(st config.State) string {
	if st.Raid == "" {
		return st.RaidSummary
	}

	return engine.RaidHealth(st.Raid).Msg().Render(lang())
}

// storageTitle is the storage line: its health and, once the device has
// said, a bar of how full it is - a menu can only show text, so the bar is
// drawn in characters.
func storageTitle(st config.State) string {
	if st.StorageSize <= 0 {
		return raidSummary(st)
	}
	frac := float64(st.StorageUsed) / float64(st.StorageSize)
	frac = max(0, min(frac, 1))
	const cells = 10
	full := int(frac*cells + 0.5)
	// %.0f rounds half to even, as RoundToEven.
	used := i18n.DeskMainStorageUsed(int(math.RoundToEven(frac * 100))).Render(lang())
	return fmt.Sprintf("%s  %s%s %s", raidSummary(st),
		strings.Repeat("■", full), strings.Repeat("□", cells-full), used)
}

// updateTitle is the device-update line (issue #183) and its tooltip, the
// update's summary; "" when the device has no major or critical update
// waiting. A menu item has no colour, so a critical one leads with a
// warning sign, like the macOS popover's red line.
func updateTitle(a *config.UpdateAlert) (title, tooltip string) {
	if a == nil {
		return "", ""
	}
	switch a.Level {
	case "critical":
		return "⚠ " + i18n.DeskMainUpdateCritical(a.Version).Render(lang()), a.Summary
	case "major":
		return i18n.DeskMainUpdateAvailable(a.Version).Render(lang()), a.Summary
	}
	return "", ""
}

// storageUseTitle: "Storage: 39.6 of 474 GB" - the storage line's first
// submenu item; "" until the device reports a size.
func storageUseTitle(st config.State) string {
	if st.StorageSize <= 0 {
		return ""
	}
	return i18n.DeskMainStorageLine(usedOfTotal(st.StorageUsed, st.StorageSize)).Render(lang())
}

// cpuTitle: "CPU: 12%".
func cpuTitle(st config.State) string {
	// %.0f rounds half to even, as RoundToEven.
	return i18n.DeskMainCpuLine(int(math.RoundToEven(st.CPUPercent))).Render(lang())
}

// memoryTitle: "Memory: 2.1 of 8.2 GB".
func memoryTitle(st config.State) string {
	return i18n.DeskMainMemoryLine(usedOfTotal(st.MemUsed, st.MemSize)).Render(lang())
}

// loadTip is the storage line's tooltip: its submenu on one line.
func loadTip(st config.State) string {
	tip := cpuTitle(st) + " · " + memoryTitle(st)
	if t := storageUseTitle(st); t != "" {
		tip = t + " · " + tip
	}
	return tip
}

// sizeText writes an amount in the status's units (1.024 MB): one decimal
// under 100 GB ("8.5"), whole GB from 100 GB ("474"), TB with one decimal
// from 1000 GB ("3.6"). The macOS pop-up's sizeText is the same. The
// number has a dot whatever the language: the Go runtime has no decimal
// formatter yet.
func sizeText(units int64) (number string, tb bool) {
	gb := float64(units) * 1.024 / 1000
	switch {
	case gb < 99.95:
		return fmt.Sprintf("%.1f", gb), false
	case gb < 999.5:
		return fmt.Sprintf("%.0f", gb), false
	}
	return fmt.Sprintf("%.1f", gb/1000), true
}

// usedOfTotal: "39.6 of 474 GB", "1.2 of 3.6 TB", or "39.6 GB of 3.6 TB"
// when the two need different units.
func usedOfTotal(used, size int64) string {
	u, utb := sizeText(used)
	s, stb := sizeText(size)
	var m i18n.Msg
	switch {
	case !utb && !stb:
		m = i18n.DeskMainUsedOfTotalGb(u, s)
	case utb && stb:
		m = i18n.DeskMainUsedOfTotalTb(u, s)
	case !utb:
		m = i18n.DeskMainUsedGbOfTotalTb(u, s)
	default:
		m = i18n.DeskMainUsedTbOfTotalGb(u, s)
	}
	return m.Render(lang())
}

// Version is this build's version, set by main (for the self-update).
var Version = "dev"

var (
	pendingMu sync.Mutex
	pending   *selfupdate.Update
)

// watchUpdates checks the signed desktop manifest shortly after start and
// every 6 hours; a newer build shows as "Update otc-sync to X" at the top
// of the menu.
func (u *ui) watchUpdates() {
	time.Sleep(20 * time.Second)
	for {
		if up, err := selfupdate.Check(Version); err == nil {
			pendingMu.Lock()
			pending = up
			pendingMu.Unlock()
			u.mu.Lock()
			u.showUpdate()
			u.mu.Unlock()
		}
		time.Sleep(6 * time.Hour)
	}
}

// showUpdate sets the update item from pending (u.mu held). The item is
// rebuilt with the menu, so apply() calls this too.
func (u *ui) showUpdate() {
	if u.appUpdate == nil {
		return
	}
	pendingMu.Lock()
	up := pending
	pendingMu.Unlock()
	if up == nil {
		u.setShown(u.appUpdate, false)
		return
	}
	u.setTitle(u.appUpdate, "⬆ "+i18n.DeskMainAppUpdate(up.Version).Render(lang()))
	u.setTip(u.appUpdate, up.Notes)
	u.setEnabled(u.appUpdate, true)
	u.setShown(u.appUpdate, true)
}

// installUpdate is the one click: download, check it against the signed
// manifest, replace this program, restart the service (Linux) and start
// the new tray once this one has quit.
func (u *ui) installUpdate() {
	pendingMu.Lock()
	up := pending
	pendingMu.Unlock()
	if up == nil {
		return
	}
	l := lang()
	setItemTitle(u.appUpdate, i18n.DeskMainAppUpdating(up.Version).Render(l))
	u.appUpdate.Disable()
	u.forget(u.appUpdate) // written directly: the next apply writes it again, as it always did
	if err := selfupdate.Apply(up); err != nil {
		setItemTitle(u.appUpdate, "⬆ "+i18n.DeskMainAppUpdate(up.Version).Render(l))
		u.appUpdate.Enable()
		u.forget(u.appUpdate)
		_ = dlgError(i18n.DeskMainAppUpdateFailed(errorText(l, err)).Render(l), dlgTitle("Off The Cloud"))
		return
	}
	service.RestartIfActive()
	if exe, err := selfupdate.Executable(); err == nil {
		cmd := exec.Command(exe, os.Args[1:]...)
		cmd.Env = append(os.Environ(), "OTC_SYNC_RELAUNCH=1")
		_ = cmd.Start()
	}
	u.c.Quit()
	systray.Quit()
}

// askPassword asks for the device's password in a dialog titled title.
// zenity's Windows password dialog labels its field "Password:" in English,
// so there an entry with hidden text carries the label in l instead.
func askPassword(l, title string) (string, error) {
	opts := []zenity.Option{dlgTitle(title), dlgOK(i18n.CommonOk().Render(l)), dlgCancel(i18n.CommonCancel().Render(l))}
	if runtime.GOOS == "windows" {
		return dlgEntry(i18n.CommonPassword().Render(l), append(opts, zenity.HideText())...)
	}
	_, pw, err := zenity.Password(opts...)
	return pw, err
}
