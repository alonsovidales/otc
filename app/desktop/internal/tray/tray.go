// SPDX-License-Identifier: AGPL-3.0-or-later

// Package tray is PopoverView.swift as a system tray menu: status, RAID
// health, one row per folder, add local/remote folder, settings, start at
// login, quit. Dialogs are the OS's own (a folder chooser, a text entry, a
// list) rather than a window of ours, so the binary stays free of a GUI
// toolkit and cross-compiles from anywhere.
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
	ListRemote(path string) ([]engine.RemoteEntry, error)
	AutostartEnabled() bool
	SetAutostart(bool) error
	Quit()
}

type folderItem struct {
	item   *systray.MenuItem
	remove *systray.MenuItem
	id     string
	remote bool
}

type ui struct {
	c         Controller
	mu        sync.Mutex
	status    *systray.MenuItem
	raid      *systray.MenuItem
	cpu       *systray.MenuItem // the storage line's submenu: the device's
	mem       *systray.MenuItem // load, shown when the pointer rests on it
	update    *systray.MenuItem // issue #183: a major or critical device update
	appUpdate *systray.MenuItem // a newer otc-sync (selfupdate)
	setup     *systray.MenuItem // issue #184: the SD card wizard
	empty     *systray.MenuItem
	folders   []*folderItem
	addLocal  *systray.MenuItem
	addBackup *systray.MenuItem
	explain   *systray.MenuItem
	addRem    *systray.MenuItem
	settings  *systray.MenuItem
	autost    *systray.MenuItem
	quit      *systray.MenuItem
	lastIcon  string
	refresh   chan struct{}
	stopLoop  chan struct{}
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
		for {
			select {
			case <-refreshCh:
				u.apply()
			case <-time.After(2 * time.Second):
				u.apply()
			}
		}
	}()
}

// build lays the whole menu out in the macOS popover's order - status,
// folders, actions - which means starting over whenever the folder set
// changes, since a tray menu can only ever append.
func (u *ui) build(folders []config.FolderStatus) {
	if u.stopLoop != nil {
		close(u.stopLoop)
	}
	systray.ResetMenu()
	title := systray.AddMenuItem("Off The Cloud — Sync", "")
	u.appUpdate = systray.AddMenuItem("", "")
	u.appUpdate.Hide()
	title.Disable()
	u.status = systray.AddMenuItem("Not connected", "")
	u.status.Disable()
	// Enabled, unlike the other status lines, or its submenu - the
	// device's CPU and memory - would never open; clicking it does nothing.
	u.raid = systray.AddMenuItem("", "")
	u.raid.Hide()
	u.cpu = u.raid.AddSubMenuItem("", "")
	u.cpu.Disable()
	u.mem = u.raid.AddSubMenuItem("", "")
	u.mem.Disable()
	u.update = systray.AddMenuItem("", "")
	u.update.Disable()
	u.update.Hide()
	systray.AddSeparator()
	u.empty = systray.AddMenuItem("No folders yet — add one below.", "")
	u.empty.Disable()
	u.folders = nil
	for _, f := range folders {
		mi := systray.AddMenuItem(folderTitle(f), "")
		rm := mi.AddSubMenuItem("Remove", "Stop syncing this folder (nothing is deleted)")
		u.folders = append(u.folders, &folderItem{item: mi, remove: rm, id: f.ID, remote: f.RemotePath != ""})
	}
	if len(folders) > 0 {
		u.empty.Hide()
	}
	systray.AddSeparator()
	// The macOS app's three kinds of folder (AddFolderChooser); a tray menu
	// has no (i) buttons, so each has a tooltip and "What do these do?"
	// explains all three.
	u.addBackup = systray.AddMenuItem("Back Up a Folder from This Computer…", "One way: this computer → device (upload only, no deletes)")
	u.addLocal = systray.AddMenuItem("Sync a Folder from This Computer…", "Two ways: kept the same here and on the device, changes and deletions included")
	u.addRem = systray.AddMenuItem("Sync a Folder from the Device…", "Two ways, starting from a folder already on the device")
	u.explain = systray.AddMenuItem("What Do These Do?", "The difference between backing up and syncing")
	// Connected: the device and Disconnect; not: the device and password.
	u.settings = systray.AddMenuItem(u.settingsTitle(), "")
	u.autost = systray.AddMenuItemCheckbox("Start at login", "", u.c.AutostartEnabled())
	systray.AddSeparator()
	u.setup = systray.AddMenuItem("Set Up a New Device…", "Prepare the SD card for a new Raspberry Pi device")
	systray.AddSeparator()
	u.quit = systray.AddMenuItem("Quit", "")

	stop := make(chan struct{})
	u.stopLoop = stop
	items := u.folders
	addLocal, addRem, settings, autost, quit := u.addLocal, u.addRem, u.settings, u.autost, u.quit
	addBackup, explain, appUpdate, setup := u.addBackup, u.explain, u.appUpdate, u.setup
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-appUpdate.ClickedCh:
				go u.installUpdate()
			case <-addBackup.ClickedCh:
				go u.addBackup_()
			case <-explain.ClickedCh:
				go u.explainKinds()
			case <-addLocal.ClickedCh:
				go u.addLocal_()
			case <-addRem.ClickedCh:
				go u.addRemote()
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
	for _, fi := range items {
		fi := fi
		go func() {
			for {
				select {
				case <-stop:
					return
				case <-fi.remove.ClickedCh:
					u.removeFolder(fi)
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
	u.status.SetTitle(statusDot(st.Status) + " " + st.Status)
	u.settings.SetTitle(u.settingsTitle())
	if st.Raid != "" && st.Raid != string(engine.RaidUnknown) {
		u.raid.SetTitle(storageTitle(st))
		u.cpu.SetTitle(fmt.Sprintf("CPU: %.0f%%", st.CPUPercent))
		u.mem.SetTitle(memoryTitle(st))
		u.raid.SetTooltip(fmt.Sprintf("CPU: %.0f%% · %s", st.CPUPercent, memoryTitle(st)))
		u.raid.Show()
	} else {
		u.raid.Hide()
	}
	if title, tip := updateTitle(st.UpdateAlert); title != "" {
		u.update.SetTitle(title)
		u.update.SetTooltip(tip)
		u.update.Show()
	} else {
		u.update.Hide()
	}
	if st.Raid != u.lastIcon {
		systray.SetIcon(icons.For(st.Raid))
		u.lastIcon = st.Raid
	}
	systray.SetTooltip("Off The Cloud — " + st.Status)
	for i, f := range want {
		u.folders[i].item.SetTitle(folderTitle(f))
	}
	if u.c.AutostartEnabled() {
		u.autost.Check()
	} else {
		u.autost.Uncheck()
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

	return fmt.Sprintf("%s %s — %s", arrow, name, state)
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

// addBackup_ adds a one-way backup (config.Folder.OneWay).
func (u *ui) addBackup_() {
	dir, err := zenity.SelectFile(zenity.Directory(), zenity.Title("Choose a folder to back up to the device"))
	if err != nil || dir == "" {
		return
	}
	cfg := u.editConfig()
	if cfg == nil {
		return
	}
	cfg.Folders = append(cfg.Folders, config.Folder{ID: config.NewID(), Path: dir, OneWay: true})
	if err := u.c.SaveConfig(cfg); err != nil {
		_ = zenity.Error(err.Error(), zenity.Title("Off The Cloud"))
	}
	Refresh()
}

// explainKinds is the tray's (i): the macOS chooser's three explanations.
func (u *ui) explainKinds() {
	_ = zenity.Info(`Back up a folder from this computer - one way: this computer → device (upload only, no deletes)
New and changed files are copied to the device. Nothing is ever deleted there: files you delete here stay on the device, and when a file changes the device keeps its older version too. Nothing done on the device - from a phone, another computer or the web - ever changes or deletes anything in this folder here.

Sync a folder from this computer - two ways
The folder is copied to the device, and from then on it is kept the same in both places: files added, changed or deleted on the device change this folder too, and the other way round. The first sync only adds, it never deletes.

Sync a folder from the device - two ways
Pick a folder already on the device and a place on this computer: it is downloaded there and kept the same in both places from then on, changes and deletions included.`,
		zenity.Title("Adding a folder"), zenity.Width(520))
}

func (u *ui) addLocal_() {
	dir, err := zenity.SelectFile(zenity.Directory(), zenity.Title("Choose a folder to keep in sync"))
	if err != nil || dir == "" {
		return
	}
	cfg := u.editConfig()
	if cfg == nil {
		return
	}
	cfg.Folders = append(cfg.Folders, config.Folder{ID: config.NewID(), Path: dir})
	if err := u.c.SaveConfig(cfg); err != nil {
		_ = zenity.Error(err.Error(), zenity.Title("Off The Cloud"))
	}
	Refresh()
}

// addRemote is RemoteFolderPickerView: browse the device's tree (a native
// window on Windows, the desktop's list dialog on Linux - see picker_*.go),
// then the local destination in the folder chooser.
func (u *ui) addRemote() {
	remote, ok := pickRemoteFolder(u.c.ListRemote)
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
	if u.configured() {
		return "Disconnect from " + deviceLabel(u.c.Config().Domain) + "…"
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
	if err := u.c.SetPassword(""); err != nil {
		_ = zenity.Error(err.Error(), zenity.Title("Off The Cloud"))
	}
	Refresh()
	u.settingsDialog()
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

// memoryTitle: "Memory: 2.1 of 8.2 GB". The status counts in units of
// 1.024 MB.
func memoryTitle(st config.State) string {
	gb := func(v int64) float64 { return float64(v) * 1.024 / 1000 }
	return fmt.Sprintf("Memory: %.1f of %.1f GB", gb(st.MemUsed), gb(st.MemSize))
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
		u.appUpdate.Hide()
		return
	}
	u.appUpdate.SetTitle("⬆ Update otc-sync to " + up.Version)
	u.appUpdate.SetTooltip(up.Notes)
	u.appUpdate.Enable()
	u.appUpdate.Show()
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
	if err := selfupdate.Apply(up); err != nil {
		u.appUpdate.SetTitle("⬆ Update otc-sync to " + up.Version)
		u.appUpdate.Enable()
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
