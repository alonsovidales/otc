// SPDX-License-Identifier: AGPL-3.0-or-later

// Package tray is PopoverView.swift as a system tray menu: status, RAID
// health, one row per folder, add local/remote folder, settings, start at
// login, quit. Dialogs are the OS's own (a folder chooser, a text entry, a
// list) rather than a window of ours, so the binary stays free of a GUI
// toolkit and cross-compiles from anywhere.
package tray

import (
	"fmt"
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
	Config() *config.Config
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
	c        Controller
	mu       sync.Mutex
	status   *systray.MenuItem
	raid     *systray.MenuItem
	empty    *systray.MenuItem
	folders  []*folderItem
	autost   *systray.MenuItem
	lastIcon string
	refresh  chan struct{}
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
	title := systray.AddMenuItem("Off The Cloud — Sync", "")
	title.Disable()
	u.status = systray.AddMenuItem("Not connected", "")
	u.status.Disable()
	u.raid = systray.AddMenuItem("", "")
	u.raid.Hide()
	u.raid.Disable()
	systray.AddSeparator()
	u.empty = systray.AddMenuItem("No folders yet — add one below.", "")
	u.empty.Disable()
	systray.AddSeparator()
	addLocal := systray.AddMenuItem("Add Local Folder…", "Mirror a folder on this computer to the device")
	addRemote := systray.AddMenuItem("Add Remote Folder…", "Keep a device folder in sync with a local one")
	settings := systray.AddMenuItem("Settings…", "Device and password")
	u.autost = systray.AddMenuItemCheckbox("Start at login", "", u.c.AutostartEnabled())
	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit", "")

	u.apply()
	go func() {
		for {
			select {
			case <-addLocal.ClickedCh:
				go u.addLocal()
			case <-addRemote.ClickedCh:
				go u.addRemote()
			case <-settings.ClickedCh:
				go u.settings()
			case <-u.autost.ClickedCh:
				go u.toggleAutostart()
			case <-quit.ClickedCh:
				u.c.Quit()
				systray.Quit()

				return
			case <-refreshCh:
				u.apply()
			case <-time.After(2 * time.Second):
				u.apply()
			}
		}
	}()
}

func (u *ui) apply() {
	u.mu.Lock()
	defer u.mu.Unlock()
	st := u.c.Snapshot()
	u.status.SetTitle(statusDot(st.Status) + " " + st.Status)
	if st.Raid != "" && st.Raid != string(engine.RaidUnknown) {
		u.raid.SetTitle(st.RaidSummary)
		u.raid.Show()
	} else {
		u.raid.Hide()
	}
	if st.Raid != u.lastIcon {
		systray.SetIcon(icons.For(st.Raid))
		u.lastIcon = st.Raid
	}
	systray.SetTooltip("Off The Cloud — " + st.Status)

	// Folder rows: rebuilt only when the set changes, retitled otherwise
	// (removing and re-adding items on every tick would flicker).
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
		for _, fi := range u.folders {
			fi.item.Remove()
		}
		u.folders = nil
		for _, f := range want {
			f := f
			item := u.empty // placeholder to keep position: items append at the end, so add after the empty marker
			_ = item
			mi := systray.AddMenuItem("", "")
			rm := mi.AddSubMenuItem("Remove", "Stop syncing this folder (nothing is deleted)")
			fi := &folderItem{item: mi, remove: rm, id: f.ID, remote: f.RemotePath != ""}
			u.folders = append(u.folders, fi)
			go func() {
				for range rm.ClickedCh {
					u.removeFolder(fi)
				}
			}()
		}
	}
	for i, f := range want {
		u.folders[i].item.SetTitle(folderTitle(f))
	}
	if len(want) == 0 {
		u.empty.Show()
	} else {
		u.empty.Hide()
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
		return "🟢"
	case "Disconnected":
		return "🟡"
	case "Missing domain/password", "Wrong password":
		return "🔴"
	default:
		return "⚪"
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
			state = "Watching"
		}
	case string(engine.StateError):
		state = "⚠ " + f.Error
	default:
		if f.CurrentFile != "" {
			state = fmt.Sprintf("%d%% · %s", int(f.Progress*100), f.CurrentFile)
		} else {
			state = "Checking…"
		}
	}

	return fmt.Sprintf("%s %s — %s", arrow, name, state)
}

func (u *ui) removeFolder(fi *folderItem) {
	cfg := u.c.Config()
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

func (u *ui) addLocal() {
	dir, err := zenity.SelectFile(zenity.Directory(), zenity.Title("Choose a folder to keep in sync"))
	if err != nil || dir == "" {
		return
	}
	cfg := u.c.Config()
	cfg.Folders = append(cfg.Folders, config.Folder{ID: config.NewID(), Path: dir})
	if err := u.c.SaveConfig(cfg); err != nil {
		_ = zenity.Error(err.Error(), zenity.Title("Off The Cloud"))
	}
	Refresh()
}

// addRemote is RemoteFolderPickerView: one directory level at a time, in a
// list dialog, then the local destination in the folder chooser.
func (u *ui) addRemote() {
	current := "/"
	for {
		entries, err := u.c.ListRemote(current)
		if err != nil {
			_ = zenity.Error("Could not list the device's folders: "+err.Error(), zenity.Title("Off The Cloud"))

			return
		}
		items := []string{"✔ Choose this folder (" + current + ")"}
		if current != "/" {
			items = append(items, "‹ Up")
		}
		for _, e := range entries {
			if e.IsDir {
				items = append(items, "📁 "+e.Name)
			}
		}
		pick, err := zenity.List("Remote folder: "+current, items, zenity.Title("Choose a Remote Folder"), zenity.Width(420), zenity.Height(420))
		if err != nil || pick == "" {
			return
		}
		switch {
		case strings.HasPrefix(pick, "✔"):
			dir, err := zenity.SelectFile(zenity.Directory(), zenity.Title("Choose where to download “"+current+"” and keep it in sync"))
			if err != nil || dir == "" {
				return
			}
			cfg := u.c.Config()
			cfg.RemoteFolders = append(cfg.RemoteFolders, config.RemoteFolder{ID: config.NewID(), RemotePath: current, LocalPath: dir})
			if err := u.c.SaveConfig(cfg); err != nil {
				_ = zenity.Error(err.Error(), zenity.Title("Off The Cloud"))
			}
			Refresh()

			return
		case pick == "‹ Up":
			current = parentPath(current)
		default:
			name := strings.TrimPrefix(pick, "📁 ")
			for _, e := range entries {
				if e.IsDir && e.Name == name {
					current = e.Path
				}
			}
		}
	}
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
func (u *ui) settings() {
	cfg := u.c.Config()
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
	if err := u.c.SaveConfig(cfg); err != nil {
		_ = zenity.Error(err.Error(), zenity.Title("Off The Cloud"))

		return
	}
	_, pw, err := zenity.Password(zenity.Title("Off The Cloud — Password for " + cfg.Domain))
	if err == nil && pw != "" {
		if err := u.c.SetPassword(pw); err != nil {
			_ = zenity.Error(err.Error(), zenity.Title("Off The Cloud"))
		}
	}
	Refresh()
}

func (u *ui) toggleAutostart() {
	enable := !u.c.AutostartEnabled()
	if err := u.c.SetAutostart(enable); err != nil {
		_ = zenity.Error(err.Error(), zenity.Title("Off The Cloud"))
	}
	Refresh()
}
