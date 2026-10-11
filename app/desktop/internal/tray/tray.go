// SPDX-License-Identifier: AGPL-3.0-or-later

// Package tray is PopoverView.swift as a system tray menu: status, RAID
// health, the web app, one row per folder, Add Folder… (addfolder.go),
// settings, start at login, quit. Dialogs are the OS's own (a folder
// chooser, a text entry, a list) rather than a window of ours, so the
// binary stays free of a GUI toolkit and cross-compiles from anywhere.
package tray

import (
	"fmt"
	"github.com/alonsovidales/otc/app/desktop/internal/selfupdate"
	"github.com/alonsovidales/otc/app/desktop/internal/service"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"
	"github.com/ncruces/zenity"

	"github.com/alonsovidales/otc/app/desktop/internal/browser"
	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/app/desktop/internal/engine"
	"github.com/alonsovidales/otc/app/desktop/internal/icons"
)

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
	item   *systray.MenuItem
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
	systray.SetTitle("")
	systray.SetTooltip("Off The Cloud — Sync")
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
	mi.SetTitle(s)
}

func (u *ui) setTip(mi *systray.MenuItem, s string) {
	if v, ok := u.tips[mi]; ok && v == s {
		return
	}
	u.tips[mi] = s
	mi.SetTooltip(s)
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
	title := systray.AddMenuItem("Off The Cloud — Sync", "")
	u.appUpdate = systray.AddMenuItem("", "")
	u.appUpdate.Hide()
	title.Disable()
	u.status = systray.AddMenuItem("Not connected", "")
	u.status.Disable()
	// Enabled, unlike the other status lines, or its submenu - the
	// device's storage in GB, CPU and memory - would never open; clicking
	// it does nothing. Storage first, as it details the line itself.
	u.raid = systray.AddMenuItem("", "")
	u.raid.Hide()
	u.store = u.raid.AddSubMenuItem("", "")
	u.store.Disable()
	u.store.Hide()
	u.cpu = u.raid.AddSubMenuItem("", "")
	u.cpu.Disable()
	u.mem = u.raid.AddSubMenuItem("", "")
	u.mem.Disable()
	u.update = systray.AddMenuItem("", "")
	u.update.Disable()
	u.update.Hide()
	// Issue #193: at the top, like the Mac's button beside the gear; shown
	// once a device is set (apply).
	u.web = systray.AddMenuItem("Open Web App", "")
	u.web.Hide()
	systray.AddSeparator()
	u.empty = systray.AddMenuItem("No folders yet — add one below.", "")
	u.empty.Disable()
	u.folders = nil
	for _, f := range folders {
		mi := systray.AddMenuItem(folderTitle(f), "")
		img, info := addImagesItems(mi)
		upInfo := addUploadOnlyInfo(mi)
		rm := mi.AddSubMenuItem("Remove", "Stop syncing this folder (nothing is deleted)")
		u.folders = append(u.folders, &folderItem{item: mi, remove: rm, images: img, imagesInfo: info, uploadInfo: upInfo, id: f.ID, remote: f.RemotePath != ""})
	}
	if len(folders) > 0 {
		u.empty.Hide()
	}
	systray.AddSeparator()
	// The macOS app's "Add Folder…" (AddFolderChooser): the kind, its
	// options, then the folder (addfolder.go). A dialog has no (i) buttons,
	// so "What Do These Do?" explains the kinds and options.
	u.add = systray.AddMenuItem("Add Folder…", "Back up or sync a folder from this computer, or sync one from the device")
	u.explain = systray.AddMenuItem("What Do These Do?", "The kinds of folder, and their options")
	// Connected: the device and Disconnect; not: the device and password.
	u.settings = systray.AddMenuItem(u.settingsTitle(), "")
	u.autost = systray.AddMenuItemCheckbox(autostartToggle, autostartToggleTip, u.c.AutostartEnabled())
	u.language = addLanguageMenu(u.c.Config())
	systray.AddSeparator()
	u.setup = systray.AddMenuItem("Set Up a New Device…", "Prepare the SD card for a new Raspberry Pi device")
	systray.AddSeparator()
	u.quit = systray.AddMenuItem("Quit", "")

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
	// config.json read once per apply (it used to be twice), and never
	// cached: in viewer mode reading it is how the menu sees a device the
	// service or the CLI changed.
	cfg := u.c.Config()
	// Issue #190: once connected, which way - the home network or the
	// bridge - in the line that says it is.
	line := engine.StatusLine(st, cfg.Domain)
	u.setTitle(u.status, statusDot(st.Status)+" "+line)
	configured := cfg.Domain != "" && u.c.Password() != ""
	u.setTitle(u.settings, settingsTitleFor(cfg, configured))
	if addr := config.WebURL(cfg.Domain); configured && addr != "" {
		u.setTip(u.web, "Open "+addr+" in your browser")
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
	if t := "Off The Cloud — " + line; t != u.lastTrayTip {
		systray.SetTooltip(t)
		u.lastTrayTip = t
	}
	for i, f := range want {
		u.setTitle(u.folders[i].item, folderTitle(f))
		u.applyImages(u.folders[i], f)
		u.applyUploadOnly(u.folders[i], f)
	}
	// Read every time (a stat, or a registry read), so an entry removed
	// outside the app shows; only the write is skipped.
	u.setChecked(u.autost, u.c.AutostartEnabled())
	u.applyLanguage(cfg)
	// Once connected, start at login is asked about, once.
	if st.Status == "Connected" && !u.autostartAsked {
		u.autostartAsked = true
		go u.askAutostart()
	}
}

func statusDot(s string) string {
	switch s {
	case "Connected":
		return "●"
	case "Disconnected", "Sync not running", "Connecting…":
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
	var state string
	switch f.State {
	case string(engine.StateWatching):
		if f.RemotePath != "" {
			state = "Synced"
		} else {
			state = "Backed up"
		}
	case string(engine.StateError):
		state = "⚠ " + f.Error
	default:
		switch {
		case f.CurrentFile != "" && f.Progress > 0:
			state = fmt.Sprintf("%d%% · %s", int(f.Progress*100), f.CurrentFile)
		case f.CurrentFile != "":
			// Issue #138: "Checking 12/300 · name" while hashing - no
			// percentage, that is not a transfer.
			state = f.CurrentFile
		default:
			state = "Checking…"
		}
	}

	return fmt.Sprintf("%s %s — %s%s%s", arrow, name, state, uploadOnlySuffix(f), imagesSuffix(f))
}

// editConfig is config.json for an edit, or nil - and the reason shown -
// when it can't be read, so nothing is saved over it.
func (u *ui) editConfig() *config.Config {
	cfg, err := u.c.LoadConfig()
	if err != nil {
		_ = zenity.Error(err.Error(), zenity.Title("Off The Cloud"))

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
		_ = zenity.Error(err.Error(), zenity.Title("Off The Cloud"))
	}
	Refresh()
}

// explainKinds is the tray's (i): the macOS chooser's three explanations.
func (u *ui) explainKinds() {
	_ = zenity.Info(explainKindsText, zenity.Title("Adding a folder"), zenity.Width(520))
}

// explainKindsText is "What Do These Do?": the Mac chooser's (i)s - the
// three kinds, then the options a folder from this computer has.
var explainKindsText = `Back up a folder from this computer - one way: this computer → device (upload only, no deletes)
New and changed files are copied to the device. Nothing is ever deleted there: files you delete here stay on the device, and when a file changes the device keeps its older version too. Nothing done on the device - from a phone, another computer or the web - ever changes or deletes anything in this folder here.

Sync a folder from this computer - two ways
The folder is copied to the device, and from then on it is kept the same in both places: files added, changed or deleted on the device change this folder too, and the other way round. The first sync only adds, it never deletes.

Sync a folder from the device - two ways
Pick a folder already on the device and a place on this computer: it is downloaded there and kept the same in both places from then on, changes and deletions included. What is set for it on the device (upload only, kept out of Images) stays as it is.

Upload only (an option for a folder synced from this computer; always on for a backup)
` + engine.UploadOnlyCaption + " " + engine.UploadOnlyTwoWay + " " + engine.UploadOnlyLater + `

` + imagesExplainKinds

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
	dir, err := zenity.SelectFile(zenity.Directory(), zenity.Title("Choose where to download “"+remote+"” and keep it in sync"))
	if err != nil || dir == "" {
		return
	}
	cfg := u.editConfig()
	if cfg == nil {
		return
	}
	cfg.RemoteFolders = append(cfg.RemoteFolders, config.RemoteFolder{ID: config.NewID(), RemotePath: remote, LocalPath: dir})
	if err := u.c.SaveConfig(cfg); err != nil {
		_ = zenity.Error(err.Error(), zenity.Title("Off The Cloud"))
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
	hint := "Device name (as on the bridge, e.g. “cala”), or a full address for a device elsewhere (wss://host/ws, ws://192.168.1.10:8080/ws)."
	if current == "" {
		current = cfg.Domain
	}
	entered, err := zenity.Entry(hint, zenity.Title("Off The Cloud — Settings"), zenity.EntryText(current))
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
	// password attempts per minute for nothing.
	_, pw, err := zenity.Password(zenity.Title("Off The Cloud — Password for " + cfg.Domain))
	if err != nil {
		return
	}
	if pw != "" {
		if err := u.c.SetPassword(pw); err != nil {
			_ = zenity.Error(err.Error(), zenity.Title("Off The Cloud"))

			return
		}
	}
	if cfg.Domain != oldDomain {
		_ = config.ClearLocalEndpoint() // the old device's (issue #190)
	}
	if err := u.c.SaveConfig(cfg); err != nil {
		_ = zenity.Error(err.Error(), zenity.Title("Off The Cloud"))
	}
	Refresh()
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
		return "Disconnect from " + deviceLabel(cfg.Domain) + "…"
	}
	return "Connect to a Device…"
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
	if zenity.Question("All your synced folders are removed from this app, so none of them starts syncing with a different device by mistake. "+
		"The files themselves stay on this computer and on the device. You can add the folders again after connecting.",
		zenity.Title("Disconnect from "+deviceLabel(cfg.Domain)+"?"), zenity.OKLabel("Disconnect"), zenity.WarningIcon) != nil {
		return
	}
	cfg.Folders = nil
	cfg.RemoteFolders = nil
	cfg.Domain = ""
	if err := u.c.SaveConfig(cfg); err != nil {
		_ = zenity.Error(err.Error(), zenity.Title("Off The Cloud"))
		return
	}
	_ = config.ClearLocalEndpoint() // and its home-network endpoint (issue #190)
	if err := u.c.SetPassword(""); err != nil {
		_ = zenity.Error(err.Error(), zenity.Title("Off The Cloud"))
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
		_ = zenity.Error("The browser could not be opened:\n\n"+err.Error()+"\n\nThe web app is at "+addr,
			zenity.Title("Off The Cloud"))
	}
}

func (u *ui) toggleAutostart() {
	enable := !u.c.AutostartEnabled()
	if err := u.c.SetAutostart(enable); err != nil {
		_ = zenity.Error(err.Error(), zenity.Title("Off The Cloud"))
	}
	Refresh()
}

// storageTitle is the storage line: its health and, once the device has
// said, a bar of how full it is - a menu can only show text, so the bar is
// drawn in characters.
func storageTitle(st config.State) string {
	if st.StorageSize <= 0 {
		return st.RaidSummary
	}
	frac := float64(st.StorageUsed) / float64(st.StorageSize)
	frac = max(0, min(frac, 1))
	const cells = 10
	full := int(frac*cells + 0.5)
	return fmt.Sprintf("%s  %s%s %.0f%% used", st.RaidSummary,
		strings.Repeat("■", full), strings.Repeat("□", cells-full), frac*100)
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
		return "⚠ Critical device update " + a.Version +
			" - install it from the device's Settings as soon as possible", a.Summary
	case "major":
		return "Device update " + a.Version + " available", a.Summary
	}
	return "", ""
}

// storageUseTitle: "Storage: 39.6 of 474 GB" - the storage line's first
// submenu item; "" until the device reports a size.
func storageUseTitle(st config.State) string {
	if st.StorageSize <= 0 {
		return ""
	}
	return "Storage: " + usedOfTotal(st.StorageUsed, st.StorageSize)
}

// cpuTitle: "CPU: 12%".
func cpuTitle(st config.State) string {
	return fmt.Sprintf("CPU: %.0f%%", st.CPUPercent)
}

// memoryTitle: "Memory: 2.1 of 8.2 GB".
func memoryTitle(st config.State) string {
	return "Memory: " + usedOfTotal(st.MemUsed, st.MemSize)
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
// from 1000 GB ("3.6"). The macOS pop-up's sizeText is the same.
func sizeText(units int64) (number, unit string) {
	gb := float64(units) * 1.024 / 1000
	switch {
	case gb < 99.95:
		return fmt.Sprintf("%.1f", gb), "GB"
	case gb < 999.5:
		return fmt.Sprintf("%.0f", gb), "GB"
	}
	return fmt.Sprintf("%.1f", gb/1000), "TB"
}

// usedOfTotal: "39.6 of 474 GB", "1.2 of 3.6 TB", or "39.6 GB of 3.6 TB"
// when the two need different units.
func usedOfTotal(used, size int64) string {
	u, uu := sizeText(used)
	s, su := sizeText(size)
	if uu == su {
		return u + " of " + s + " " + su
	}
	return u + " " + uu + " of " + s + " " + su
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
	u.setTitle(u.appUpdate, "⬆ Update otc-sync to "+up.Version)
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
	u.appUpdate.SetTitle("Updating to " + up.Version + "…")
	u.appUpdate.Disable()
	u.forget(u.appUpdate) // written directly: the next apply writes it again, as it always did
	if err := selfupdate.Apply(up); err != nil {
		u.appUpdate.SetTitle("⬆ Update otc-sync to " + up.Version)
		u.appUpdate.Enable()
		u.forget(u.appUpdate)
		_ = zenity.Error("The update could not be installed:\n\n"+err.Error(), zenity.Title("Off The Cloud"))
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
