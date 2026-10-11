// SPDX-License-Identifier: AGPL-3.0-or-later

// otc-sync is the Off The Cloud sync client for Linux and Windows: the
// macOS menu bar app (app/macos) done as a tray app, with a command line
// and, on Linux, a systemd user service.
//
//	otc-sync                       tray app (the default with a display), else the daemon
//	otc-sync tray                  tray app
//	otc-sync run                   the sync daemon, no UI (what the service runs)
//	otc-sync status                what the running daemon is doing
//	otc-sync settings [flags]      device name/address and password
//	otc-sync folders               the folders being synced
//	otc-sync backup <dir>          back up a local folder, one way: it is never changed from the device
//	otc-sync add <dir>             keep a local folder in two-way sync (its first pass uploads)
//	otc-sync add-remote <remote> <dir>   keep a device folder and a local one in two-way sync
//	                               (backup and add take --keep-out-of-images, issue #192, and add
//	                               --upload-only, issue #132; a device folder keeps its own)
//	otc-sync images <id|path> keep-out|show   keep a synced folder out of Images, or show it again
//	otc-sync remove <id|path>      stop syncing a folder (nothing is deleted)
//	otc-sync ls [remote path]      browse the device
//	otc-sync open                  the device's web app in the browser
//	otc-sync service install|uninstall|status   (Linux) run as a systemd user service
//	otc-sync autostart on|off|status   start the tray app at login (only when asked to)
//	otc-sync language [auto|<code>]   the language of every Off The Cloud app (language.go)
//
// Only one process runs the engine (a lock in the config directory); the
// tray started next to the service becomes a viewer of the service's
// state, and every edit goes through config.json, which the engine
// watches - so the CLI, the tray and the service never disagree.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/gofrs/flock"

	"github.com/alonsovidales/otc/app/desktop/internal/autostart"
	"github.com/alonsovidales/otc/app/desktop/internal/browser"
	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/app/desktop/internal/engine"
	"github.com/alonsovidales/otc/app/desktop/internal/flasher"
	"github.com/alonsovidales/otc/app/desktop/internal/oslang"
	"github.com/alonsovidales/otc/app/desktop/internal/selfupdate"
	"github.com/alonsovidales/otc/app/desktop/internal/service"
	"github.com/alonsovidales/otc/app/desktop/internal/tray"
	"github.com/alonsovidales/otc/app/desktop/internal/wsclient"
)

var version = "dev"

func main() {
	tray.Version = version
	// A self-update left the old program aside on Windows; and a tray that
	// a self-update started waits for the one it replaced to have quit.
	selfupdate.Cleanup()
	if os.Getenv("OTC_SYNC_RELAUNCH") == "1" {
		os.Unsetenv("OTC_SYNC_RELAUNCH")
		time.Sleep(3 * time.Second)
	}
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}
	var err error
	switch cmd {
	case "", "tray":
		if cmd == "" && !hasDisplay() {
			err = runDaemon(false)
		} else {
			err = runDaemon(true)
		}
	case "run":
		err = runDaemon(false)
	case "status":
		err = cmdStatus()
	case "settings":
		err = cmdSettings(args)
	case "folders":
		err = cmdFolders()
	case "add":
		err = cmdAdd(args, false)
	case "backup":
		err = cmdAdd(args, true)
	case "add-remote":
		err = cmdAddRemote(args)
	case "images":
		err = cmdImages(args)
	case "remove":
		err = cmdRemove(args)
	case "disconnect":
		err = cmdDisconnect(args)
	case "ls":
		err = cmdLs(args)
	case "open":
		err = cmdOpen()
	case "service":
		err = cmdService(args)
	case "autostart":
		err = cmdAutostart(args)
	case "language":
		err = cmdLanguage(args)
	case "version":
		fmt.Println("otc-sync", version)
	case "update":
		err = cmdUpdate()
	case "flash":
		err = cmdFlash(args)
	case "flash-device": // the elevated half of `flash` and the tray's wizard
		err = flasher.HelperMain(args)
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// cmdUpdate installs a newer otc-sync if the signed manifest has one, and
// restarts the service (the tray updates itself from its menu).
func cmdUpdate() error {
	up, err := selfupdate.Check(version)
	if err != nil {
		return err
	}
	if up == nil {
		fmt.Println("otc-sync", version, "is up to date")
		return nil
	}
	fmt.Println("updating otc-sync", version, "->", up.Version)
	if err := selfupdate.Apply(up); err != nil {
		return err
	}
	service.RestartIfActive()
	fmt.Println("updated to", up.Version, "- restart the tray app to use it")
	return nil
}

// cmdFlash is the setup wizard's card writing for a terminal (issue #184).
func cmdFlash(args []string) error {
	disks, err := flasher.ListDisks()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		if len(disks) == 0 {
			fmt.Println("No SD card or USB disk found - insert the card and run this again.")
			return nil
		}
		fmt.Println("Cards and removable disks:")
		for _, d := range disks {
			fmt.Println("  " + d.Label())
		}
		fmt.Println("\nWrite the image with: otc-sync flash <disk>")
		return nil
	}
	var disk *flasher.Disk
	for i := range disks {
		if disks[i].ID == args[0] {
			disk = &disks[i]
		}
	}
	if disk == nil {
		return fmt.Errorf("%s is not a removable disk - run `otc-sync flash` to list them", args[0])
	}
	if disk.Size < flasher.MinCardSize {
		return fmt.Errorf("the card is too small (%s) - it needs 8 GB or more", flasher.HumanSize(disk.Size))
	}
	fmt.Printf("Everything on %s will be erased. Type yes to continue: ", disk.Label())
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if strings.TrimSpace(line) != "yes" {
		return errors.New("cancelled")
	}
	path, err := flasher.Download(context.Background(), func(done, total int64) {
		if total > 0 {
			fmt.Printf("\rDownloading the image… %d%%   ", done*100/total)
		}
	})
	fmt.Println()
	if err != nil {
		return err
	}
	last := ""
	err = flasher.WriteElevated(path, *disk, func(p flasher.Progress) {
		if t := p.Text(); t != last {
			last = t
			fmt.Printf("\r%-60s", t)
		}
	})
	fmt.Println()
	if err != nil {
		return err
	}
	_ = os.Remove(path)
	fmt.Println(flasher.NextSteps)
	return nil
}

func usage() {
	fmt.Print(`otc-sync - Off The Cloud folder sync

  otc-sync                      tray app (or the daemon, without a display)
  otc-sync tray | run           the tray app | the daemon with no UI
  otc-sync status               what the running daemon is doing
  otc-sync update               install a newer version, if there is one (signed releases only)
  otc-sync flash [disk]         write the Off The Cloud device image to an SD card: without a
                                disk, lists the cards; with one, downloads, verifies and writes
  otc-sync settings --name cala [--password-stdin | --password-prompt]
  otc-sync settings --address ws://192.168.1.10:8080/ws
  otc-sync folders              the folders being synced
  otc-sync backup [--keep-out-of-images] <dir>
                                back up a local folder to the device, one way and upload only:
                                new and changed files go up, nothing is ever deleted there,
                                and nothing done on the device ever changes this folder
  otc-sync add [--upload-only] [--keep-out-of-images] <dir>
                                keep a local folder in two-way sync with the device: changes and
                                deletions on either side reach the other (first pass only adds).
                                --upload-only: the device keeps every older version of a file and
                                nothing in the folder can be deleted there - files you delete here
                                stay on the device and aren't downloaded again; files added or
                                changed on the device still come down
  otc-sync add-remote <remote-path> <dir>
                                two-way sync between a device folder and a local one. No options:
                                what is set for it on the device (upload only, kept out of
                                Images) stays as it is
  otc-sync images <id|path> keep-out|show
                                keep a synced folder out of Images, or show it there again.
                                Kept out, its photos and videos aren't tagged, searched for
                                faces or shown in Images (Files still shows them), and the tags
                                and faces already found in them are deleted. Shown again, they
                                are tagged - and searched for faces, if that is on - again.
                                --keep-out-of-images keeps a folder out when it is added
  otc-sync remove <id|path>     stop syncing a folder (nothing is deleted)
  otc-sync disconnect [--yes]   forget the device: removes every folder (the files stay)
                                and the device and password, before connecting elsewhere
  otc-sync ls [remote-path]     browse the device's folders
  otc-sync open                 open the device's web app in the browser (prints its address)
  otc-sync service install|uninstall|status
                                run the daemon as a systemd user service (Linux)
  otc-sync autostart on|off|status
                                start the tray app at login, or not. Nothing is registered
                                until you say so: here, with the tray's "Start at login", or
                                when the tray asks once after the first connection
`)
	// Nothing to choose while English is the only language that ships.
	if oslang.Choosable() {
		fmt.Print(`  otc-sync language [auto|` + strings.Join(languageCodes(), "|") + `]
                                the language of every Off The Cloud app, kept on the device:
                                without an argument, what is chosen; auto follows this computer
`)
	}
}

func hasDisplay() bool {
	if runtime.GOOS == "windows" {
		return true
	}

	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

// ---- the daemon / tray ----------------------------------------------------

// app is the tray's Controller and the daemon's glue: engine (when this
// process holds the lock), config watching, state.json.
type app struct {
	mu       sync.Mutex
	cfg      *config.Config
	password string
	eng      *engine.Engine // nil in viewer mode
	saveTmr  *time.Timer
	saveMu   sync.Mutex // one state.json write at a time, in order
	quit     chan struct{}
}

func runDaemon(withTray bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.EnsureClientID() {
		if err := cfg.Save(); err != nil {
			return err
		}
	}
	pw, err := config.LoadPassword()
	if err != nil {
		return err
	}
	a := &app{cfg: cfg, password: pw, quit: make(chan struct{})}

	lockPath, err := config.LockPath()
	if err != nil {
		return err
	}
	lock := flock.New(lockPath)
	owned, err := lock.TryLock()
	if err != nil {
		return err
	}
	if owned {
		a.eng = engine.New(cfg, pw, func() { a.scheduleStateWrite(); tray.Refresh() })
		a.eng.Start()
		go a.watchConfig()
		defer func() {
			a.eng.Stop()
			_ = lock.Unlock()
		}()
	} else if !withTray {
		return errors.New("another otc-sync is already running the sync (the tray app or the service)")
	} else {
		fmt.Fprintln(os.Stderr, "the sync is running elsewhere (the service?); this tray shows its state and edits the shared configuration")
	}

	if withTray {
		// One tray per user: a second launch (double-clicking the exe again,
		// the autostart entry firing while it is already up) must not add a
		// second icon. The viewer case - a tray next to the service - is
		// different and still allowed, since the service holds only the
		// engine lock, not this one.
		trayLockPath, err := config.TrayLockPath()
		if err != nil {
			return err
		}
		trayLock := flock.New(trayLockPath)
		if ok, err := trayLock.TryLock(); err != nil || !ok {
			fmt.Fprintln(os.Stderr, "the tray app is already running")

			return nil
		}
		defer func() { _ = trayLock.Unlock() }()
		registerAtLaunch(cfg)
		tray.Run(a)

		return nil
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sig:
	case <-a.quit:
	}

	return nil
}

// watchConfig reloads config.json and the password whenever either is
// written - by the CLI, or by a tray running next to the service.
func (a *app) watchConfig() {
	dir, err := config.Dir()
	if err != nil {
		return
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return
	}
	defer w.Close()
	if err := w.Add(dir); err != nil {
		return
	}
	var pending *time.Timer
	for {
		select {
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			base := filepath.Base(ev.Name)
			if base != "config.json" && base != "secret" {
				continue
			}
			if pending != nil {
				pending.Stop()
			}
			pending = time.AfterFunc(300*time.Millisecond, a.reload)
		case <-a.quit:
			return
		}
	}
}

func (a *app) reload() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config reload failed:", err)

		return
	}
	pw, _ := config.LoadPassword()
	a.mu.Lock()
	a.cfg = cfg
	a.password = pw
	eng := a.eng
	a.mu.Unlock()
	if eng != nil {
		eng.UpdateConfig(cfg, pw)
	}
	tray.Refresh()
}

// scheduleStateWrite writes state.json at most every 300 ms, and always
// after the last change. It used to restart its timer on every change, and
// a pass changes something per file - faster than that on a LAN - so
// state.json was not written for the whole pass, went stale, and `otc-sync
// status` and a tray next to the service said the sync was not running.
func (a *app) scheduleStateWrite() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.saveTmr != nil {
		return // already due: it takes the latest snapshot when it fires
	}
	a.saveTmr = time.AfterFunc(300*time.Millisecond, func() {
		a.mu.Lock()
		// Cleared before the snapshot, so a change made while it is
		// written schedules the next write rather than being missed.
		a.saveTmr = nil
		a.mu.Unlock()
		if a.eng == nil {
			return
		}
		a.saveMu.Lock()
		defer a.saveMu.Unlock()
		st := a.eng.Snapshot()
		_ = config.SaveState(&st)
	})
}

// -- tray.Controller --

func (a *app) Snapshot() config.State {
	if a.eng != nil {
		return a.eng.Snapshot()
	}
	st, _ := config.LoadState()
	if st == nil || time.Since(st.Updated) > 30*time.Second {
		return config.State{Status: "Sync not running", Raid: "unknown", RaidSummary: engine.RaidUnknown.Summary()}
	}

	return *st
}

func (a *app) Config() *config.Config {
	cfg, _ := config.Load()
	if cfg == nil {
		cfg = &config.Config{}
	}

	return cfg
}

// LoadConfig is config.json for an edit about to be saved: a file that
// can't be read is an error here, never the empty config Config shows -
// saving that over the real file dropped every folder (and their sync
// records) and the device. A read that races another process's
// rename-over (Windows) is tried again.
func (a *app) LoadConfig() (*config.Config, error) {
	cfg, err := config.Load()
	for try := 1; err != nil && try < 3; try++ {
		time.Sleep(50 * time.Millisecond)
		cfg, err = config.Load()
	}

	return cfg, err
}

func (a *app) SaveConfig(cfg *config.Config) error {
	if err := cfg.Save(); err != nil {
		return err
	}
	a.reload()

	return nil
}

func (a *app) Password() string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.password
}

func (a *app) SetPassword(pw string) error {
	if err := config.SavePassword(pw); err != nil {
		return err
	}
	a.reload()

	return nil
}

// RemoteBrowser is the remote folder picker's listing for one showing of
// it, and what to call once it closes. This process's engine link when it
// runs the engine. In viewer mode, one connection of its own for the
// whole browse - it used to sign in afresh for every folder opened, each
// time an Argon2id check on the device. A connection that stopped working
// is replaced, and a listing that fails on a reused one is tried once on a
// fresh one, so every listing still works, or fails, as a fresh one would.
func (a *app) RemoteBrowser() (func(string) ([]engine.RemoteEntry, error), func()) {
	if a.eng != nil {
		return a.eng.ListRemoteDirectory, func() {}
	}
	var mu sync.Mutex
	var ws *wsclient.Client
	connect := func() error {
		c, err := connectOnce(a.Config(), a.Password())
		ws = c

		return err
	}
	drop := func() {
		if ws != nil {
			ws.Disconnect()
			ws = nil
		}
	}
	list := func(path string) ([]engine.RemoteEntry, error) {
		mu.Lock()
		defer mu.Unlock()
		if ws != nil && !ws.IsConnected() {
			// The client is reconnecting on its own, and a request would
			// fail at once until it has: start over instead.
			drop()
		}
		reused := ws != nil
		if !reused {
			if err := connect(); err != nil {
				return nil, err
			}
		}
		entries, err := engine.ListRemoteDirectory(ws, path)
		if err != nil && reused {
			// The device restarted, or the link dropped, while a dialog
			// was open: what a fresh connection gets past.
			drop()
			if err := connect(); err != nil {
				return nil, err
			}
			entries, err = engine.ListRemoteDirectory(ws, path)
		}

		return entries, err
	}
	done := func() {
		mu.Lock()
		defer mu.Unlock()
		drop()
	}

	return list, done
}

// loginItems is the start-at-login registration (a fake in tests).
var loginItems = autostart.System

// registerAtLaunch: start at login only with the user's yes
// (autostart.AtLaunch) - a consented entry that went missing comes back,
// nothing else is registered. Without an answer the tray asks once, after
// the first successful connection (tray.askAutostart).
func registerAtLaunch(cfg *config.Config) {
	if exe, err := os.Executable(); err == nil {
		_ = autostart.AtLaunch(loginItems, cfg.Autostart, exe)
	}
}

// AutostartEnabled is the tray's checkbox: whether the entry is there, read
// each time, so one removed outside the app shows.
func (a *app) AutostartEnabled() bool {
	enabled, _ := loginItems.Enabled()

	return enabled
}

// SetAutostart is the user's answer - the tray's question or its checkbox -
// recorded in config.json first (so the question is never asked again),
// then applied to the registration.
func (a *app) SetAutostart(on bool) error {
	// Before the login item changes: a failed read leaves both as they are.
	cfg, err := a.LoadConfig()
	if err != nil {
		return err
	}
	v := on
	cfg.Autostart = &v
	if err := cfg.Save(); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}

	return autostart.Apply(loginItems, on, exe)
}

// OfferAutostart says whether the tray's one-time "Start Off The Cloud when
// you log in?" is due: never answered, and this process runs the sync. A
// tray next to the service is only a viewer, and the service already starts
// at boot; it is asked once the tray runs the sync itself.
func (a *app) OfferAutostart() bool {
	if a.eng == nil {
		return false
	}
	cfg, err := config.Load() // fresh: the CLI may have answered meanwhile
	if err != nil {
		return false
	}

	return !cfg.AutostartAsked()
}

func (a *app) Quit() { close(a.quit) }

// connectOnce dials and authenticates, for one-off commands.
func connectOnce(cfg *config.Config, pw string) (*wsclient.Client, error) {
	if !config.Ready(cfg, pw) {
		return nil, errors.New("device and password are not set yet - run: otc-sync settings --name <device> --password-prompt")
	}
	ws := wsclient.New()
	done := make(chan error, 2)
	ws.OnConnect = func() { done <- nil }
	ws.OnAuthFailed = func(msg string, _ int) { done <- errors.New(msg) }
	ws.OnDisconnect = func(err error) {
		if err != nil {
			select {
			case done <- err:
			default:
			}
		}
	}
	ws.Configure(cfg.Domain, cfg.ClientID, pw)
	ws.SetLang(oslang.Effective(cfg.Language))
	// The home network first, as the engine (issue #190); what this
	// connection learns is not stored, the engine's is.
	ws.SetLocalEndpoint(engine.LocalEndpointFor(cfg.Domain))
	ws.Connect()
	select {
	case err := <-done:
		if err != nil {
			ws.Disconnect()

			return nil, err
		}

		return ws, nil
	case <-time.After(20 * time.Second):
		ws.Disconnect()

		return nil, errors.New("timed out connecting to " + cfg.Domain)
	}
}

// ---- the command line -----------------------------------------------------

func cmdStatus() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pw, _ := config.LoadPassword()
	fmt.Printf("device:    %s\n", orNone(cfg.Domain))
	fmt.Printf("password:  %s\n", map[bool]string{true: "set", false: "not set"}[pw != ""])
	st, err := config.LoadState()
	if err != nil {
		return err
	}
	if st == nil || time.Since(st.Updated) > 30*time.Second {
		fmt.Println("sync:      not running (start the tray app, or: otc-sync service install)")
		if st != nil {
			fmt.Printf("last seen: %s\n", st.Updated.Format(time.RFC3339))
		}

		return nil
	}
	fmt.Printf("sync:      %s (pid %d)\n", engine.StatusLine(*st, cfg.Domain), st.PID)
	fmt.Printf("storage:   %s\n", st.RaidSummary)
	printFolders(st)

	return nil
}

func printFolders(st *config.State) {
	if len(st.Folders)+len(st.RemoteFolders) == 0 {
		fmt.Println("folders:   none - add one with: otc-sync add <dir>")

		return
	}
	fmt.Println("folders:")
	for _, f := range st.Folders {
		fmt.Printf("  ⬆ %-8s %s  [%s]%s%s\n", f.ID, f.Path, engine.Describe(f), uploadOnlyLabel(f), imagesLabel(f))
	}
	for _, f := range st.RemoteFolders {
		fmt.Printf("  ⇅ %-8s %s  ⇄ %s  [%s]%s%s\n", f.ID, f.Path, f.RemotePath, engine.Describe(f), uploadOnlyLabel(f), imagesLabel(f))
	}
}

// uploadOnlyLabel is a folder's upload-only request on its way (issue
// #132), after its state; "" when there is none.
func uploadOnlyLabel(f config.FolderStatus) string {
	label := ""
	switch engine.UploadOnlyState(f.UploadOnly) {
	case engine.UploadOnlyMaking:
		label = " (making it upload only…)"
	case engine.UploadOnlyLifting:
		label = " (turning upload only off…)"
	case engine.UploadOnlyUnsupported:
		label = " (" + engine.UploadOnlyNeedsUpdate + ")"
	}
	if f.UploadOnlyNote != "" {
		label += " (" + f.UploadOnlyNote + ")"
	}

	return label
}

// pendingUploadOnlyLabel is uploadOnlyLabel from config.json alone, while
// the sync isn't running.
func pendingUploadOnlyLabel(want *bool) string {
	return uploadOnlyLabel(config.FolderStatus{UploadOnly: string(engine.UploadOnlyRequestState(want, nil))})
}

// imagesLabel is how a folder stands with Images (issue #192), after its
// state; "" when shown, or not known.
func imagesLabel(f config.FolderStatus) string {
	label := ""
	switch engine.ImagesState(f.OutOfImages) {
	case engine.ImagesKeptOut:
		label = " (kept out of Images)"
	case engine.ImagesKeptOutByParent:
		label = " (kept out of Images by " + f.OutOfImagesBy + ")"
	case engine.ImagesKeeping:
		label = " (keeping out of Images…)"
	case engine.ImagesShowing:
		label = " (showing in Images…)"
	case engine.ImagesUnsupported:
		label = " (" + engine.OutOfImagesNeedsUpdate + ")"
	}
	if f.OutOfImagesNote != "" {
		label += " (" + f.OutOfImagesNote + ")"
	}

	return label
}

// pendingImagesLabel is imagesLabel from config.json alone, while the sync
// isn't running: only a request waiting to be sent is known.
func pendingImagesLabel(want *bool) string {
	switch {
	case want == nil:
		return ""
	case *want:
		return " (keeping out of Images…)"
	default:
		return " (showing in Images…)"
	}
}

func orNone(s string) string {
	if s == "" {
		return "(not set)"
	}

	return s
}

func cmdSettings(args []string) error {
	fs := flag.NewFlagSet("settings", flag.ContinueOnError)
	name := fs.String("name", "", "device name on the bridge (e.g. cala)")
	address := fs.String("address", "", "full address for a device elsewhere (wss://host/ws, ws://192.168.1.10:8080/ws)")
	pwStdin := fs.Bool("password-stdin", false, "read the password from standard input")
	pwPrompt := fs.Bool("password-prompt", false, "ask for the password on the terminal")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	changed := false
	oldDomain := cfg.Domain
	if *name != "" {
		cfg.Domain = config.BridgeDomainForName(*name)
		changed = true
	}
	if *address != "" {
		cfg.Domain = strings.TrimSpace(*address)
		changed = true
	}
	if cfg.EnsureClientID() {
		changed = true
	}
	if cfg.Domain != oldDomain {
		// The old device's home-network endpoint (issue #190).
		if err := config.ClearLocalEndpoint(); err != nil {
			return err
		}
	}
	if changed {
		if err := cfg.Save(); err != nil {
			return err
		}
	}
	switch {
	case *pwStdin:
		raw, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if err := config.SavePassword(strings.TrimRight(raw, "\r\n")); err != nil {
			return err
		}
	case *pwPrompt:
		pw, err := readPasswordPrompt()
		if err != nil {
			return err
		}
		if err := config.SavePassword(pw); err != nil {
			return err
		}
	}
	pw, _ := config.LoadPassword()
	fmt.Printf("device:   %s\npassword: %s\n", orNone(cfg.Domain), map[bool]string{true: "set", false: "not set"}[pw != ""])
	if config.Ready(cfg, pw) {
		fmt.Println("Syncing will start automatically.")
	} else {
		fmt.Println("Enter both device and password to start syncing.")
	}

	return nil
}

func readPasswordPrompt() (string, error) {
	fmt.Fprint(os.Stderr, "Password: ")
	// Without a terminal package: turn echo off through stty where there is one.
	restore := func() {}
	if runtime.GOOS != "windows" {
		if err := exec.Command("stty", "-F", "/dev/tty", "-echo").Run(); err == nil {
			restore = func() { _ = exec.Command("stty", "-F", "/dev/tty", "echo").Run() }
		} else if err := exec.Command("sh", "-c", "stty -echo < /dev/tty").Run(); err == nil {
			restore = func() { _ = exec.Command("sh", "-c", "stty echo < /dev/tty").Run() }
		}
	}
	raw, err := bufio.NewReader(os.Stdin).ReadString('\n')
	restore()
	fmt.Fprintln(os.Stderr)
	if err != nil && raw == "" {
		return "", err
	}

	return strings.TrimRight(raw, "\r\n"), nil
}

func cmdFolders() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	st, _ := config.LoadState()
	if st != nil && time.Since(st.Updated) <= 30*time.Second {
		printFolders(st)

		return nil
	}
	if len(cfg.Folders)+len(cfg.RemoteFolders) == 0 {
		fmt.Println("no folders - add one with: otc-sync add <dir>")

		return nil
	}
	for _, f := range cfg.Folders {
		fmt.Printf("  ⬆ %-8s %s%s%s\n", f.ID, f.Path, pendingUploadOnlyLabel(f.UploadOnly), pendingImagesLabel(f.OutOfImages))
	}
	for _, f := range cfg.RemoteFolders {
		fmt.Printf("  ⇅ %-8s %s  ⇄ %s%s%s\n", f.ID, f.LocalPath, f.RemotePath, pendingUploadOnlyLabel(f.UploadOnly), pendingImagesLabel(f.OutOfImages))
	}
	fmt.Println("(the sync is not running, so no state is shown)")

	return nil
}

func absDir(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s is not a directory", abs)
	}

	return abs, nil
}

// The options a folder from this computer can be added with (the tray's
// and the Mac's step 2): --keep-out-of-images (issue #192) for `backup` and
// `add`, --upload-only (issue #132) for `add` - a backup always is.
const (
	flagKeepOut    = "--keep-out-of-images"
	flagUploadOnly = "--upload-only"
)

// addFlags takes the options out of args, wherever they are: before or
// after the folder. seen lists them as typed.
func addFlags(args []string) (rest []string, outOfImages, uploadOnly *bool, seen []string) {
	on := true
	for _, a := range args {
		// -flag or --flag, as the flag package takes them.
		name, isFlag := strings.CutPrefix(a, "-")
		name = "--" + strings.TrimPrefix(name, "-")
		switch {
		case isFlag && name == flagKeepOut:
			outOfImages = &on
		case isFlag && name == flagUploadOnly:
			uploadOnly = &on
		default:
			rest = append(rest, a)
			continue
		}
		seen = append(seen, a)
	}

	return rest, outOfImages, uploadOnly, seen
}

// addedOptions says what a folder added with options gets, and when.
func addedOptions(outOfImages, uploadOnly *bool) {
	st, _ := config.LoadState()
	if uploadOnly != nil {
		fmt.Println("Upload only: " + engine.UploadOnlyCaption + " " + engine.UploadOnlyTwoWay)
		if st != nil && st.UploadOnlyUnsupported {
			fmt.Println(engine.UploadOnlyNeedsUpdate + " It is done once the device is updated.")
		}
	}
	if outOfImages != nil {
		fmt.Println("Kept out of Images: " + engine.OutOfImagesAddCaption)
		if st != nil && st.OutOfImagesUnsupported {
			fmt.Println(engine.OutOfImagesNeedsUpdate + " It is done once the device is updated.")
		}
	}
}

func cmdAdd(args []string, oneWay bool) error {
	args, outOfImages, uploadOnly, _ := addFlags(args)
	if len(args) != 1 {
		if oneWay {
			return errors.New("usage: otc-sync backup [--keep-out-of-images] <dir>")
		}
		return errors.New("usage: otc-sync add [--upload-only] [--keep-out-of-images] <dir>")
	}
	if oneWay && uploadOnly != nil {
		// Asked for what it always is: nothing to record.
		fmt.Println("A backup is always upload only.")
		uploadOnly = nil
	}
	dir, err := absDir(args[0])
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	for _, f := range cfg.Folders {
		if f.Path == dir {
			return fmt.Errorf("%s is already being synced (%s)", dir, f.ID)
		}
	}
	for _, f := range cfg.RemoteFolders {
		if f.LocalPath == dir {
			return fmt.Errorf("%s is already being synced (%s)", dir, f.ID)
		}
	}
	f := config.Folder{ID: config.NewID(), Path: dir, OneWay: oneWay, OutOfImages: outOfImages, UploadOnly: uploadOnly}
	cfg.Folders = append(cfg.Folders, f)
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("added %s as %s\n", dir, f.ID)
	addedOptions(outOfImages, uploadOnly)

	return nil
}

// cmdAddRemote takes no options: a folder from the device keeps what it
// has there (Images, upload only) - `otc-sync images` changes Images once
// it syncs, and upload only is the lock on the folder in the web app or
// the phone app.
func cmdAddRemote(args []string) error {
	args, _, _, seen := addFlags(args)
	if len(seen) > 0 {
		return fmt.Errorf("add-remote takes no %s: a folder synced from the device keeps what is set for it there "+
			"(change Images later with: otc-sync images <id|path> keep-out|show; upload only from the web app or the phone app)", seen[0])
	}
	if len(args) != 2 {
		return errors.New("usage: otc-sync add-remote <remote-path> <dir>")
	}
	remote := args[0]
	if !strings.HasPrefix(remote, "/") {
		remote = "/" + remote
	}
	dir, err := filepath.Abs(args[1])
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil { // perms: rwxr-xr-x
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	f := config.RemoteFolder{ID: config.NewID(), RemotePath: remote, LocalPath: dir}
	cfg.RemoteFolders = append(cfg.RemoteFolders, f)
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("added %s ⇄ %s as %s\n", remote, dir, f.ID)

	return nil
}

// cmdImages is the tray's "Kept Out of Images" checkbox (issue #192): the
// request goes in config.json, and the sync sends it to the device.
func cmdImages(args []string) error {
	const use = "usage: otc-sync images <id|path> keep-out|show"
	if len(args) != 2 || (args[1] != "keep-out" && args[1] != "show") {
		return errors.New(use)
	}
	keepOut := args[1] == "keep-out"
	key := args[0]
	if abs, err := filepath.Abs(key); err == nil {
		if _, err := os.Stat(abs); err == nil {
			key = abs
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	id, path := "", ""
	for _, f := range cfg.Folders {
		if f.ID == key || f.Path == key {
			id, path = f.ID, f.Path
		}
	}
	for _, f := range cfg.RemoteFolders {
		if f.ID == key || f.LocalPath == key {
			id, path = f.ID, f.LocalPath
		}
	}
	if id == "" {
		return fmt.Errorf("no folder matches %q (see: otc-sync folders)", args[0])
	}
	name := filepath.Base(path)
	// What the running sync knows of it, when it runs.
	st, _ := config.LoadState()
	running := st != nil && time.Since(st.Updated) <= 30*time.Second
	if running {
		for _, f := range append(st.Folders, st.RemoteFolders...) {
			if f.ID != id {
				continue
			}
			switch state := engine.ImagesState(f.OutOfImages); {
			case state == engine.ImagesKeptOutByParent && keepOut:
				// Kept out already, by the folder above.
				fmt.Printf("%s is already kept out of Images (by %s)\n", name, f.OutOfImagesBy)
				return nil
			case state == engine.ImagesKeptOutByParent:
				// As the tray's checkbox, disabled: the device refuses to show
				// it until the folder above is shown.
				return fmt.Errorf("%s is inside %s, which is kept out of Images - show %s in Images first", name, f.OutOfImagesBy, f.OutOfImagesBy)
			case state == engine.ImagesKeptOut && keepOut:
				fmt.Printf("%s is already kept out of Images\n", name)
				return nil
			case state == engine.ImagesShown && !keepOut:
				fmt.Printf("%s is already shown in Images\n", name)
				return nil
			}
		}
	}
	cfg.SetOutOfImagesRequest(id, keepOut)
	if err := cfg.Save(); err != nil {
		return err
	}
	if keepOut {
		fmt.Printf("Keeping %s out of Images. Its photos and videos won't be tagged, searched for faces or shown in Images, and the tags and faces already found in them are deleted. Files still shows them.\n", name)
	} else {
		fmt.Printf("Showing %s in Images. Its photos and videos go back to Images, and are tagged - and searched for faces, if face recognition is on - in the background.\n", name)
	}
	switch {
	case running && st.OutOfImagesUnsupported:
		fmt.Println(engine.OutOfImagesNeedsUpdate + " It is done once the device is updated.")
	case running:
		fmt.Println("The sync tells the device now; see: otc-sync folders")
	default:
		fmt.Println("The sync tells the device when it runs (the tray app, or: otc-sync service install).")
	}

	return nil
}

// cmdDisconnect is the tray's "Disconnect from …": folders kept across a
// change of device would start syncing with, or deleting on, a different
// device, so they all go with it (the files themselves stay).
func cmdDisconnect(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] != "--yes" {
		fmt.Printf("Disconnect from %s? All %d synced folders are removed from otc-sync (the files stay on this computer and on the device). Type yes to continue: ",
			cfg.Domain, len(cfg.Folders)+len(cfg.RemoteFolders))
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if strings.TrimSpace(line) != "yes" {
			return errors.New("cancelled")
		}
	}
	cfg.Folders, cfg.RemoteFolders, cfg.Domain = nil, nil, ""
	if err := cfg.Save(); err != nil {
		return err
	}
	if err := config.SavePassword(""); err != nil {
		return err
	}
	if err := config.ClearLocalEndpoint(); err != nil { // issue #190
		return err
	}
	fmt.Println("Disconnected. Connect to a device with: otc-sync settings --name <device> --password-prompt")
	return nil
}

func cmdRemove(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: otc-sync remove <id|path>")
	}
	key := args[0]
	if abs, err := filepath.Abs(key); err == nil {
		if _, err := os.Stat(abs); err == nil {
			key = abs
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	removed := ""
	kept := cfg.Folders[:0:0]
	for _, f := range cfg.Folders {
		if f.ID == key || f.Path == key {
			removed = f.Path
		} else {
			kept = append(kept, f)
		}
	}
	cfg.Folders = kept
	keptR := cfg.RemoteFolders[:0:0]
	for _, f := range cfg.RemoteFolders {
		if f.ID == key || f.LocalPath == key {
			removed = f.LocalPath
		} else {
			keptR = append(keptR, f)
		}
	}
	cfg.RemoteFolders = keptR
	if removed == "" {
		return fmt.Errorf("no folder matches %q (see: otc-sync folders)", args[0])
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("removed %s (nothing was deleted, locally or on the device)\n", removed)

	return nil
}

func cmdLs(args []string) error {
	path := "/"
	if len(args) > 0 {
		path = args[0]
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pw, _ := config.LoadPassword()
	ws, err := connectOnce(cfg, pw)
	if err != nil {
		return err
	}
	defer ws.Disconnect()
	entries, err := engine.ListRemoteDirectory(ws, path)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir {
			fmt.Printf("  %s/\n", e.Name)
		} else {
			fmt.Printf("  %s\n", e.Name)
		}
	}
	if len(entries) == 0 {
		fmt.Println("  (empty)")
	}

	return nil
}

// cmdOpen is the tray's "Open Web App" (issue #193). The address is printed
// too, for a terminal with no desktop to open it on.
func cmdOpen() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Domain == "" {
		return errors.New("no device is set yet - run: otc-sync settings --name <device> --password-prompt")
	}
	addr := config.WebURL(cfg.Domain)
	if addr == "" {
		return fmt.Errorf("%s has no web address to open", cfg.Domain)
	}
	fmt.Println(addr)
	if !hasDisplay() {
		return nil
	}

	return browser.Open(addr)
}

func cmdService(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: otc-sync service install|uninstall|status")
	}
	switch args[0] {
	case "install":
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if err := service.Install(exe); err != nil {
			return err
		}
		fmt.Println("installed: the sync now runs as a user service and starts at boot; check it with: otc-sync status")

		return nil
	case "uninstall":
		return service.Uninstall()
	case "status":
		return service.Status()
	default:
		return errors.New("usage: otc-sync service install|uninstall|status")
	}
}

// cmdAutostart is the command line's start at login: an explicit answer,
// recorded like the tray's, or what it is now.
func cmdAutostart(args []string) error {
	if len(args) != 1 || (args[0] != "on" && args[0] != "off" && args[0] != "status") {
		return errors.New("usage: otc-sync autostart on|off|status")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if args[0] == "status" {
		fmt.Println(autostartStatus(cfg))

		return nil
	}
	on := args[0] == "on"
	cfg.Autostart = &on
	if err := cfg.Save(); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := autostart.Apply(loginItems, on, exe); err != nil {
		return err
	}
	if on {
		fmt.Println("the tray app will start at login")
	} else {
		fmt.Println("the tray app will not start at login")
	}

	return nil
}

// autostartStatus: whether the tray app starts at login, and whether the
// user chose it.
func autostartStatus(cfg *config.Config) string {
	enabled, err := loginItems.Enabled()
	switch {
	case err != nil:
		return "start at login: unknown (" + err.Error() + ")"
	case !cfg.AutostartAsked() && enabled:
		return "start at login: on, set up by an earlier version - the tray asks to keep it; `otc-sync autostart on|off` answers"
	case !cfg.AutostartAsked():
		return "start at login: off (not chosen yet; `otc-sync autostart on` turns it on)"
	case enabled:
		return "start at login: on"
	case cfg.AutostartEnabled():
		return "start at login: on, but the entry is missing - the tray adds it again at its next start"
	default:
		return "start at login: off"
	}
}

var _ = context.Background
