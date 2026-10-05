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
//	otc-sync remove <id|path>      stop syncing a folder (nothing is deleted)
//	otc-sync ls [remote path]      browse the device
//	otc-sync service install|uninstall|status   (Linux) run as a systemd user service
//	otc-sync autostart on|off      start the tray app at login
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
	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/app/desktop/internal/engine"
	"github.com/alonsovidales/otc/app/desktop/internal/flasher"
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
	case "remove":
		err = cmdRemove(args)
	case "disconnect":
		err = cmdDisconnect(args)
	case "ls":
		err = cmdLs(args)
	case "service":
		err = cmdService(args)
	case "autostart":
		err = cmdAutostart(args)
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
  otc-sync backup <dir>         back up a local folder to the device, one way and upload only:
                                new and changed files go up, nothing is ever deleted there,
                                and nothing done on the device ever changes this folder
  otc-sync add <dir>            keep a local folder in two-way sync with the device: changes and
                                deletions on either side reach the other (first pass only adds)
  otc-sync add-remote <remote-path> <dir>
                                two-way sync between a device folder and a local one
  otc-sync remove <id|path>     stop syncing a folder (nothing is deleted)
  otc-sync disconnect [--yes]   forget the device: removes every folder (the files stay)
                                and the device and password, before connecting elsewhere
  otc-sync ls [remote-path]     browse the device's folders
  otc-sync service install|uninstall|status
                                run the daemon as a systemd user service (Linux)
  otc-sync autostart on|off     start the tray app at login
`)
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
		// First run on a desktop: register at login unless the owner said no.
		if cfg.AutostartEnabled() {
			if enabled, _ := autostart.Enabled(); !enabled {
				if exe, err := os.Executable(); err == nil {
					_ = autostart.Enable(exe)
				}
			}
		}
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

func (a *app) AutostartEnabled() bool {
	enabled, _ := autostart.Enabled()

	return enabled
}

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
	if on {
		exe, err := os.Executable()
		if err != nil {
			return err
		}

		return autostart.Enable(exe)
	}

	return autostart.Disable()
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
	fmt.Printf("sync:      %s (pid %d)\n", st.Status, st.PID)
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
		fmt.Printf("  ⬆ %-8s %s  [%s]\n", f.ID, f.Path, engine.Describe(f))
	}
	for _, f := range st.RemoteFolders {
		fmt.Printf("  ⇅ %-8s %s  ⇄ %s  [%s]\n", f.ID, f.Path, f.RemotePath, engine.Describe(f))
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
		fmt.Printf("  ⬆ %-8s %s\n", f.ID, f.Path)
	}
	for _, f := range cfg.RemoteFolders {
		fmt.Printf("  ⇅ %-8s %s  ⇄ %s\n", f.ID, f.LocalPath, f.RemotePath)
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

func cmdAdd(args []string, oneWay bool) error {
	if len(args) != 1 {
		if oneWay {
			return errors.New("usage: otc-sync backup <dir>")
		}
		return errors.New("usage: otc-sync add <dir>")
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
	f := config.Folder{ID: config.NewID(), Path: dir, OneWay: oneWay}
	cfg.Folders = append(cfg.Folders, f)
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("added %s as %s\n", dir, f.ID)

	return nil
}

func cmdAddRemote(args []string) error {
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

func cmdAutostart(args []string) error {
	if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
		return errors.New("usage: otc-sync autostart on|off")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	on := args[0] == "on"
	cfg.Autostart = &on
	if err := cfg.Save(); err != nil {
		return err
	}
	if on {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if err := autostart.Enable(exe); err != nil {
			return err
		}
		fmt.Println("the tray app will start at login")

		return nil
	}
	if err := autostart.Disable(); err != nil {
		return err
	}
	fmt.Println("the tray app will not start at login")

	return nil
}

var _ = context.Background
