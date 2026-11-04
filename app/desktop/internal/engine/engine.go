// SPDX-License-Identifier: AGPL-3.0-or-later

// Package engine is SyncModel.swift: local folders mirrored up to the
// device (event-driven, with a periodic reconcile as the safety net, issue
// #37), device directories kept in two-way sync with local ones through a
// three-way merge (issue #47), hash-first uploads (issue #58), and the
// RAID health the tray icon shows (issue #69). One instance runs per
// user, whichever process holds the lock - the tray app or the service.
package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/app/desktop/internal/wsclient"
	"github.com/alonsovidales/otc/i18n"
	pb "github.com/alonsovidales/otc/proto/generated"
)

const (
	// Issue #37: the watcher does the real-time work; this is only the
	// safety net for anything it missed.
	reconcileInterval = 10 * time.Minute
	// Rapid-fire events for one path are coalesced this long.
	debounceInterval = time.Second
	// A folder in error is retried this soon, not at the next safety net.
	errorRetryInterval = 30 * time.Second
	// Issue #47: polling is the only way to notice a remote-side change.
	remoteReconcileInterval = time.Minute
	// Issue #69: every 10 seconds, so a pulled drive shows within seconds.
	raidPollInterval = 10 * time.Second
	// How long a request may wait for its answer. A dead connection is
	// found sooner by the client's own pings (wsclient); these bound a
	// device that took a request and never answered it - which used to hold
	// a folder's pass for 30 minutes, whatever the request.
	smallRequest    = 2 * time.Minute  // a lookup, a link, a delete, a flag, the status
	listingRequest  = 10 * time.Minute // a whole folder's recursive listing
	transferRequest = 10 * time.Minute // a piece of a file (4 MiB), the end of an upload
)

// StateKind is FolderState's case.
type StateKind string

const (
	StateScanning StateKind = "scanning"
	StateWatching StateKind = "watching"
	StateError    StateKind = "error"
)

// FolderState is SyncModel.FolderState. CurrentFile and Message are the
// English that state.json keeps and the command line prints; Msg, when
// set, is the one of them shown (Message for an error, CurrentFile while
// checking) as a catalog message, which the tray renders in the user's
// language. The device's own words and the system's errors have none. A
// pointer, so that states still compare.
type FolderState struct {
	Kind        StateKind
	Progress    float64
	CurrentFile string
	Message     string
	Msg         *i18n.Msg
}

// errorState is the error state for err: worded for the tray when it is
// one of ours (wsclient.MsgError), as it comes otherwise.
func errorState(err error) FolderState {
	var m interface{ Msg() i18n.Msg }
	if errors.As(err, &m) {
		msg := m.Msg()

		return FolderState{Kind: StateError, Message: err.Error(), Msg: &msg}
	}

	return FolderState{Kind: StateError, Message: err.Error()}
}

// msgState is the error state m words; its English is m's.
func msgState(m i18n.Msg) FolderState {
	return FolderState{Kind: StateError, Message: m.Render(""), Msg: &m}
}

// checkingState is a folder's state while its files are checked against
// the device (issue #138): file i of n.
func checkingState(i, n int, name string) FolderState {
	m := i18n.DeskMainCheckingFile(strconv.Itoa(i), strconv.Itoa(n), name)

	return FolderState{
		Kind:        StateScanning,
		CurrentFile: fmt.Sprintf("Checking %d/%d · %s", i, n, name), // i18n-ignore: state.json's English, for the command line; Msg is the tray's
		Msg:         &m,
	}
}

// noteState is an error state whose English is its own (a number the
// English writes as digits), worded for the tray by m.
func noteState(english string, m i18n.Msg) FolderState {
	return FolderState{Kind: StateError, Message: english, Msg: &m}
}

// The connection's states as state.json keeps them (config.State.Status):
// codes the command line prints as they are and the tray words in the
// user's language (StatusText). Never translated.
const (
	StatusNotConnected    = "Not connected"             // i18n-ignore: a status code in state.json
	StatusConnecting      = "Connecting…"               // i18n-ignore: a status code in state.json
	StatusConnected       = "Connected"                 // i18n-ignore: a status code in state.json
	StatusDisconnected    = "Disconnected"              // i18n-ignore: a status code in state.json
	StatusOffline         = "Device offline - retrying" // i18n-ignore: a status code in state.json
	StatusWrongPassword   = "Wrong password"            // i18n-ignore: a status code in state.json
	StatusMissingSettings = "Missing domain/password"   // i18n-ignore: a status code in state.json
	// StatusWaitingPassword: a device is set but its password can't be read
	// yet - the keyring is locked, as at boot before anyone logs in
	// (config.ErrKeyringLocked). The daemon asks again every half a minute.
	StatusWaitingPassword = "Waiting for the password" // i18n-ignore: a status code in state.json
	// StatusSyncNotRunning is what cmd/otc-sync writes for a viewer with no
	// engine to ask.
	StatusSyncNotRunning = "Sync not running" // i18n-ignore: a status code
	// StatusNoStorage: the device answered that it can't reach its storage
	// disks (issue #207); config.State.StatusMessage has its words. Asked
	// again every 30 s, and at once on a network change or a wake.
	StatusNoStorage = "Device storage unavailable" // i18n-ignore: a status code in state.json
	// statusTooManyAttempts takes the seconds until the next try
	// (TooManyAttempts reads them back).
	statusTooManyAttempts = "Too many attempts - retrying in %ds" // i18n-ignore: a status code in state.json
)

// TooManyAttempts reads the seconds back from a locked-out status.
func TooManyAttempts(status string) (seconds int, ok bool) {
	if _, err := fmt.Sscanf(status, statusTooManyAttempts, &seconds); err != nil {
		return 0, false
	}

	return seconds, true
}

// RaidHealth is what the tray icon says about the device's storage.
type RaidHealth string

const (
	RaidOK       RaidHealth = "ok"
	RaidDegraded RaidHealth = "degraded"
	RaidFailed   RaidHealth = "failed"
	RaidUnknown  RaidHealth = "unknown"
)

// Summary is RaidHealth.summary on macOS, in English: state.json and the
// command line. The tray words Msg in the user's language.
func (r RaidHealth) Summary() string { return r.Msg().Render("") }

// Msg is the storage line for r.
func (r RaidHealth) Msg() i18n.Msg {
	switch r {
	case RaidOK:
		return i18n.DeskMainStorageHealthy()
	case RaidDegraded:
		return i18n.DeskMainStorageDegraded()
	case RaidFailed:
		return i18n.DeskMainStorageFailed()
	default:
		return i18n.DeskMainStorageUnknown()
	}
}

func raidHealth(st *pb.Status) RaidHealth {
	switch st.RaidState {
	case pb.RaidState_RaidInSync, pb.RaidState_RaidSyncing:
		return RaidOK
	case pb.RaidState_RaidDegraded:
		if st.RaidDevicesActive > 0 {
			return RaidDegraded
		}

		return RaidFailed
	default:
		if len(st.Errors) == 0 {
			return RaidOK
		}

		return RaidFailed
	}
}

// RemoteEntry is one row of the remote folder picker.
type RemoteEntry struct {
	Name  string
	Path  string
	IsDir bool
}

// Engine is the SyncModel.
type Engine struct {
	ws *wsclient.Client

	mu           sync.Mutex
	cfg          *config.Config
	password     string
	status       string
	raid         RaidHealth
	devStatus    *pb.Status // the last status answer, nil when unknown
	folderStates map[string]FolderState
	held         map[string]FolderState // see setState
	heldTimers   map[string]*time.Timer
	remoteStates map[string]FolderState
	remoteHashes map[string]map[string]remoteEntry // backup id -> pathKey below it -> its file on the device
	lastSynced   map[string]map[string]string // remote folder id -> relative path (pathKey) -> hash
	savedSynced  map[string]map[string]string // what is on disk (record.go)
	savedDevice  map[string]recordDevice      // the device of what is on disk
	savedMeta    map[string]folderMeta        // what the record on disk says about its folder
	syncedFiles  map[string]syncedFile        // the device each loaded record was made with
	watchers     map[string]*Watcher
	remoteWatch  map[string]*Watcher
	debounce     map[string]*time.Timer // per changed path (upload folders)
	// statusMessage: the device's words with StatusNoStorage ("" when it
	// gave none, and with any other status).
	statusMessage string
	// Debounced changes of a backup folder wait here for its one worker
	// (drainChanges), in order and each path once.
	changeQueue  map[string][]string
	changeQueued map[string]map[string]bool
	draining     map[string]bool
	remoteDeb    map[string]*time.Timer // per remote folder
	errorRetry   map[string]*time.Timer
	remoteRetry  map[string]*time.Timer
	loopsStarted bool
	raidStop     chan struct{}
	netStop      chan struct{} // ends watchNetwork (issue #190)
	authRetry    *time.Timer
	// Local content hashes per folder, keyed by path and validated by
	// size + mtime, so the minute-by-minute two-way poll doesn't re-read
	// a whole tree that hasn't changed (see SyncModel's localHashCache).
	hashCache   map[string]map[string]hashEntry
	hashDirty   map[string]bool      // folders whose cache changed since it was last saved
	hashLoaded  map[string]bool      // folders whose cache file has been read
	hashSavedAt map[string]time.Time // when each folder's cache was last written (saveHashCacheEvery)
	folderBusy  map[string]bool
	// Folders whose own folder was missing at their last pass
	// (localRootPresent): waiting for it, watcher stopped.
	missingRoot map[string]bool
	// Two-way folders whose folder was there at their last pass but wasn't
	// the one synced (rootid.go): waiting for it the same way.
	replacedRoot map[string]bool
	// The disks or shares mounted in a two-way folder that weren't at its
	// last pass, as last logged (noteGoneMounts).
	goneMountsSeen map[string]string
	// Deletes the device-side mass-deletion guard holds per two-way
	// folder, as last logged (noteMassHeld).
	massHeld map[string]int
	// Changes a two-way folder holds until the device's pin is confirmed,
	// as last logged (noteUnconfirmed).
	unconfirmed map[string]int
	// Two-way folders whose marker couldn't be written, as last logged
	// (markFolder).
	markFailed map[string]bool
	// The passes holding one of the maxPasses slots (passSlot), and a
	// channel closed whenever one is freed.
	passing  map[string]passEntry
	passWake chan struct{}
	// Files that changed while they were being sent, backed off (busy.go).
	busy map[string]busyBackoff
	// Two-way files whose transfer failed, backed off (fail.go).
	failing map[string]failBackoff
	// Backups whose SetUploadOnly the device linked now acknowledged, and
	// the last error logged for the others (see ensureUploadOnly).
	uploadOnlyOK  map[string]bool
	uploadOnlyErr map[string]string
	onChange      func()
	hostname      string
	stopped       bool
	// waitingPassword: the password couldn't be read yet (SetWaitingForPassword).
	waitingPassword bool

	// A folder's requests to the device (requests.go): Keep out of Images
	// (issue #192) and Upload only (issue #132).
	images requestState
	upOnly requestState
	// Two-way folders' deletes held until upload only is set (logged when
	// the count changes; noteHeldDeletes).
	heldDeletes map[string]int
	// Issue #192 (out_of_images.go): the folders the device keeps out of
	// Images (ListOutOfImages, each with its slash; nil until it answers),
	// forgotten when the device or password changes (imagesGen counts
	// those changes).
	imagesFolders []string
	imagesGen     int

	// The user's language (language.go): the device's value as last
	// polled (nil when unknown or the device predates localization), a
	// SetLanguage under way, and a device that answered it can't store one
	// (until the next connection).
	devLanguage     *string
	langSending     bool
	langUnsupported bool
	// langEpoch counts SetLanguage requests sent and answered: a status
	// asked for before the last of those may be older than it.
	langEpoch uint64
	// langSentAt is when the last SetLanguage went out: languageWindow
	// runs from the change or from it, whichever is later.
	langSentAt time.Time

	// pinSource stands for e.ws.SessionPin in the tests (record.go).
	pinSource func() ([]byte, bool)
}

// New builds an engine over cfg; onChange fires whenever anything the UI
// shows may have changed.
func New(cfg *config.Config, password string, onChange func()) *Engine {
	host, _ := os.Hostname()
	if host == "" {
		host = "PC"
	}
	e := &Engine{
		ws:            wsclient.New(),
		cfg:           cfg,
		password:      password,
		status:        StatusNotConnected,
		raid:          RaidUnknown,
		folderStates:  map[string]FolderState{},
		remoteStates:  map[string]FolderState{},
		held:          map[string]FolderState{},
		heldTimers:    map[string]*time.Timer{},
		remoteHashes:  map[string]map[string]remoteEntry{},
		lastSynced:    map[string]map[string]string{},
		savedSynced:   map[string]map[string]string{},
		savedDevice:   map[string]recordDevice{},
		savedMeta:     map[string]folderMeta{},
		syncedFiles:   map[string]syncedFile{},
		watchers:      map[string]*Watcher{},
		remoteWatch:   map[string]*Watcher{},
		debounce:      map[string]*time.Timer{},
		changeQueue:   map[string][]string{},
		changeQueued:  map[string]map[string]bool{},
		draining:      map[string]bool{},
		remoteDeb:     map[string]*time.Timer{},
		errorRetry:    map[string]*time.Timer{},
		remoteRetry:   map[string]*time.Timer{},
		folderBusy:    map[string]bool{},
		uploadOnlyOK:  map[string]bool{},
		uploadOnlyErr: map[string]string{},
		images:        newRequestState(),
		upOnly:        newRequestState(),
		heldDeletes:   map[string]int{},
		hashCache:     map[string]map[string]hashEntry{},
		hashDirty:     map[string]bool{},
		hashLoaded:    map[string]bool{},
		hashSavedAt:   map[string]time.Time{},
		missingRoot:   map[string]bool{},
		replacedRoot:  map[string]bool{},
		passing:       map[string]passEntry{},
		passWake:      make(chan struct{}),
		busy:          map[string]busyBackoff{},
		onChange:      onChange,
		hostname:      host,
		netStop:       make(chan struct{}),
	}
	e.migrateFolders(cfg)
	e.applyLanguageLocked()
	for _, f := range cfg.Folders {
		e.folderStates[f.ID] = FolderState{Kind: StateScanning}
	}
	for _, f := range cfg.RemoteFolders {
		e.remoteStates[f.ID] = FolderState{Kind: StateScanning}
	}
	e.ws.OnConnect = func() {
		e.setStatus(StatusConnected)
		// Before the status poll: a pending change is under way by the
		// time the device's value comes back.
		e.languageAtConnect()
		e.startRaidPolling()
		// Issues #192 and #132: alongside the passes, which wait for a
		// folder's request under way before they send anything.
		go e.imagesAtConnect()
		go e.uploadOnlyAtConnect()
		go e.startSync()
	}
	e.ws.OnDisconnect = func(err error) {
		if errors.Is(err, wsclient.ErrReconnecting) {
			// A network change (networkChanged): on its way back. A device
			// waiting for its disks is asked again; it says so until it
			// answers otherwise.
			if !e.waitingForStorage() {
				e.setStatus(StatusConnecting)
			}
			e.stopRaidPolling()

			return
		}
		e.mu.Lock()
		_, lockedOut := TooManyAttempts(e.status)
		wrongPassword := e.status == StatusWrongPassword || lockedOut || e.status == StatusOffline ||
			e.status == StatusNoStorage
		e.mu.Unlock()
		if !wrongPassword {
			e.setStatus(StatusDisconnected)
		}
		e.stopRaidPolling()
	}
	e.ws.OnLocalEndpoint = e.keepLocalEndpoint
	e.ws.OnUnreachable = func(msg string) {
		log.Printf("device unreachable: %s", msg)
		e.setStatus(StatusOffline)
	}
	e.ws.OnStorageUnavailable = func(msg string) {
		// Issue #207: nothing about the password, the folders or what was
		// synced changes; the folders wait as for an offline device.
		log.Printf("the device can't reach its storage disks - asking again in a while")
		e.mu.Lock()
		e.status = StatusNoStorage
		e.statusMessage = msg
		e.mu.Unlock()
		e.stopRaidPolling()
		e.notify()
	}
	e.ws.OnAuthFailed = func(msg string, retryAfter int) {
		log.Printf("authentication failed: %s", msg)
		if retryAfter <= 0 {
			e.setStatus(StatusWrongPassword)

			return
		}
		// Locked out for guessing, not necessarily wrong: say so, and try
		// once more when the lock lifts.
		e.setStatus(fmt.Sprintf(statusTooManyAttempts, retryAfter))
		e.mu.Lock()
		if e.authRetry != nil {
			e.authRetry.Stop()
		}
		e.authRetry = time.AfterFunc(time.Duration(retryAfter+1)*time.Second, func() {
			e.mu.Lock()
			ready := config.Ready(e.cfg, e.password) && !e.stopped
			e.mu.Unlock()
			if ready {
				e.setStatus(StatusConnecting)
				e.ws.Connect()
			}
		})
		e.mu.Unlock()
	}

	return e
}

// Start applies the settings: connect when both halves are there.
func (e *Engine) Start() {
	e.applySettings()
	go e.watchNetwork(e.netStop)
}

// Stop closes everything.
func (e *Engine) Stop() {
	e.mu.Lock()
	e.stopped = true
	if e.netStop != nil {
		close(e.netStop)
		e.netStop = nil
	}
	for id, w := range e.watchers {
		w.Stop()
		delete(e.watchers, id)
	}
	for id, w := range e.remoteWatch {
		w.Stop()
		delete(e.remoteWatch, id)
	}
	e.mu.Unlock()
	e.stopRaidPolling()
	e.ws.Disconnect()
	// What passes hashed since their last save is kept.
	e.saveAllHashCaches()
}

// UpdateConfig is what the CLI-edited config.json (or the tray's own
// edits) feeds back in: new folders start, removed ones stop, changed
// credentials reconnect.
func (e *Engine) UpdateConfig(cfg *config.Config, password string) {
	e.migrateFolders(cfg)
	e.mu.Lock()
	old := e.cfg
	oldPw := e.password
	e.cfg = cfg
	e.password = password
	// Folders that went away.
	keep := map[string]bool{}
	for _, f := range cfg.Folders {
		keep[f.ID] = true
		if _, ok := e.folderStates[f.ID]; !ok {
			e.folderStates[f.ID] = FolderState{Kind: StateScanning}
		}
	}
	keepR := map[string]bool{}
	for _, f := range cfg.RemoteFolders {
		keepR[f.ID] = true
		if _, ok := e.remoteStates[f.ID]; !ok {
			e.remoteStates[f.ID] = FolderState{Kind: StateScanning}
		}
	}
	for _, f := range old.Folders {
		if keepR[f.ID] {
			// Became two-way (migrateFolders): only the upload side stops;
			// the hash cache under the same id carries on.
			if w := e.watchers[f.ID]; w != nil {
				w.Stop()
				delete(e.watchers, f.ID)
			}
			delete(e.folderStates, f.ID)
			delete(e.uploadOnlyOK, f.ID)
			delete(e.uploadOnlyErr, f.ID)
			delete(e.changeQueue, f.ID)
			delete(e.changeQueued, f.ID)
			continue
		}
		if !keep[f.ID] {
			e.dropFolderLocked(f.ID)
		}
	}
	// Removed two-way folders: their markers go with their records
	// (rootid.go), once e.mu is let go - an unlink on a share that stopped
	// answering (the likely reason to remove the folder) can block for
	// good, and with it everything that takes e.mu.
	var unmark []config.RemoteFolder
	for _, f := range old.RemoteFolders {
		if !keepR[f.ID] {
			e.dropRemoteFolderLocked(f.ID)
			unmark = append(unmark, f)
		}
	}
	credsChanged := old.Domain != cfg.Domain || oldPw != password
	var droppedLanguage *time.Time
	if credsChanged && e.authRetry != nil {
		e.authRetry.Stop()
		e.authRetry = nil
	}
	if credsChanged {
		// Possibly another device: every backup is marked there again on
		// the reconnect (harmless on the same one).
		e.uploadOnlyOK = map[string]bool{}
		e.uploadOnlyErr = map[string]string{}
		// What it keeps out of Images is asked again, too, and what it can
		// do; requests still pending go to it.
		e.forgetOutOfImagesLocked()
		e.forgetRequestsLocked(uploadOnlyRequest)
		droppedLanguage = e.forgetLanguageLocked()
	}
	// A language chosen in the tray or on the command line: shown and
	// sent at once.
	langChanged := languageChangedLocked(old, cfg)
	if langChanged {
		e.applyLanguageLocked()
	}
	// A request the tray or the command line just made for a folder
	// already synced goes now, not at the folder's next pass.
	imagesChanged := e.changedRequestsLocked(imagesRequest, old, cfg)
	uploadOnlyChanged := e.changedRequestsLocked(uploadOnlyRequest, old, cfg)
	if old.Domain != cfg.Domain {
		// The old device's home-network endpoint goes with it (issue
		// #190); under e.mu, as keepLocalEndpoint stores one.
		if err := config.ClearLocalEndpoint(); err != nil {
			log.Printf("could not forget the home-network endpoint: %v", err)
		}
	}
	e.mu.Unlock()
	if len(unmark) > 0 {
		go removeMarkers(unmark)
	}
	e.notify()
	if credsChanged {
		dropLanguagePending(droppedLanguage)
		e.ws.Disconnect()
		e.applySettings()

		return
	}
	// Offline too: a clear config.json still owes is only a write.
	if len(imagesChanged) > 0 {
		go e.applyPendingRequests(imagesRequest, imagesChanged...)
	}
	if len(uploadOnlyChanged) > 0 {
		go e.applyPendingRequests(uploadOnlyRequest, uploadOnlyChanged...)
	}
	if langChanged {
		e.sendLanguage()
	}
	if e.ws.IsConnected() {
		go e.startSync()
	}
}

func (e *Engine) dropFolderLocked(id string) {
	if w := e.watchers[id]; w != nil {
		w.Stop()
		delete(e.watchers, id)
	}
	delete(e.remoteHashes, id)
	delete(e.folderStates, id)
	delete(e.uploadOnlyOK, id)
	delete(e.uploadOnlyErr, id)
	e.dropRequestsLocked(id)
	// A running drainChanges ends at its next look at the queue.
	delete(e.changeQueue, id)
	delete(e.changeQueued, id)
	e.dropHashCacheLocked(id)
	if t := e.errorRetry[id]; t != nil {
		t.Stop()
		delete(e.errorRetry, id)
	}
}

func (e *Engine) dropRemoteFolderLocked(id string) {
	if w := e.remoteWatch[id]; w != nil {
		w.Stop()
		delete(e.remoteWatch, id)
	}
	if t := e.remoteDeb[id]; t != nil {
		t.Stop()
		delete(e.remoteDeb, id)
	}
	if t := e.remoteRetry[id]; t != nil {
		t.Stop()
		delete(e.remoteRetry, id)
	}
	e.dropSyncedLocked(id)
	delete(e.heldDeletes, id)
	delete(e.replacedRoot, id)
	delete(e.goneMountsSeen, id)
	delete(e.massHeld, id)
	delete(e.unconfirmed, id)
	delete(e.markFailed, id)
	delete(e.remoteStates, id)
	e.dropRequestsLocked(id)
	e.dropHashCacheLocked(id)
}

func (e *Engine) applySettings() {
	e.mu.Lock()
	cfg, pw := e.cfg, e.password
	e.mu.Unlock()
	if config.Ready(cfg, pw) {
		e.setStatus(StatusConnecting)
		e.ws.Configure(cfg.Domain, cfg.ClientID, pw)
		e.ws.SetLocalEndpoint(LocalEndpointFor(cfg.Domain))
		e.ws.Connect()
	} else {
		e.ws.Disconnect()
		e.mu.Lock()
		waiting := e.waitingPassword && cfg.Domain != ""
		e.mu.Unlock()
		if waiting {
			e.setStatus(StatusWaitingPassword)
		} else {
			e.setStatus(StatusMissingSettings)
		}
	}
}

// SetWaitingForPassword says the password couldn't be read yet, only not
// set (config.ErrKeyringLocked): the status says so until it can.
func (e *Engine) SetWaitingForPassword(waiting bool) {
	e.mu.Lock()
	e.waitingPassword = waiting
	show := !config.Ready(e.cfg, e.password)
	e.mu.Unlock()
	if !show {
		return
	}
	e.mu.Lock()
	domain := e.cfg.Domain
	e.mu.Unlock()
	if waiting && domain != "" {
		e.setStatus(StatusWaitingPassword)
	} else {
		e.setStatus(StatusMissingSettings)
	}
}

// ---- observable state ---------------------------------------------------

func (e *Engine) setStatus(s string) {
	e.mu.Lock()
	e.status = s
	e.statusMessage = ""
	e.mu.Unlock()
	e.notify()
}

// waitingForStorage: the device last answered that it can't reach its
// disks (StatusNoStorage).
func (e *Engine) waitingForStorage() bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.status == StatusNoStorage
}

func (e *Engine) notify() {
	if e.onChange != nil {
		e.onChange()
	}
}

// Snapshot is what the UI and state.json show.
func (e *Engine) Snapshot() config.State {
	route := e.ws.Route()
	e.mu.Lock()
	defer e.mu.Unlock()
	st := config.State{Status: e.status, Raid: string(e.raid), RaidSummary: e.raid.Summary()}
	if st.Status == StatusConnected {
		st.Route = string(route)
	}
	if st.Status == StatusNoStorage {
		st.StatusMessage = e.statusMessage
	}
	if d := e.devStatus; d != nil {
		// The storage path's disk; the OS disk only on a device without one.
		st.StorageUsed, st.StorageSize = int64(d.RaidUsage), int64(d.RaidSize)
		if st.StorageSize <= 0 {
			st.StorageUsed, st.StorageSize = int64(d.DiskUsage), int64(d.DiskSize)
		}
		st.CPUPercent = float64(d.CpuUsagePrc)
		st.MemUsed, st.MemSize = int64(d.MemUsage), int64(d.MemSize)
		if a := d.GetUpdateAlert(); a != nil && (a.GetLevel() == "major" || a.GetLevel() == "critical") {
			st.UpdateAlert = &config.UpdateAlert{Level: a.GetLevel(), Version: a.GetVersion(), Summary: a.GetSummary()}
		}
	}
	st.OutOfImagesUnsupported = e.imagesUnsupportedLocked()
	st.UploadOnlyUnsupported = e.upOnly.unsupportedLocked()
	e.languageStatusLocked(&st)
	for _, f := range e.cfg.Folders {
		fs := toStatus(f.ID, f.Path, "", e.folderStates[f.ID])
		e.imagesStatusLocked(&fs)
		e.uploadOnlyStatusLocked(&fs)
		st.Folders = append(st.Folders, fs)
	}
	for _, f := range e.cfg.RemoteFolders {
		fs := toStatus(f.ID, f.LocalPath, f.RemotePath, e.remoteStates[f.ID])
		e.imagesStatusLocked(&fs)
		e.uploadOnlyStatusLocked(&fs)
		st.RemoteFolders = append(st.RemoteFolders, fs)
	}

	return st
}

func toStatus(id, path, remote string, s FolderState) config.FolderStatus {
	fs := config.FolderStatus{ID: id, Path: path, RemotePath: remote, State: string(s.Kind), Progress: s.Progress, CurrentFile: s.CurrentFile, Error: s.Message}
	if fs.State == "" {
		fs.State = string(StateScanning)
	}
	if s.Msg != nil {
		if args, err := s.Msg.MarshalArgs(); err == nil {
			fs.MsgKey, fs.MsgArgs = s.Msg.Key, args
		}
	}

	return fs
}

func (e *Engine) setFolderState(id string, s FolderState) { e.setState(e.folderStates, id, s) }

func (e *Engine) setRemoteState(id string, s FolderState) { e.setState(e.remoteStates, id, s) }

// quietPassDelay: a synced folder only shows progress once a pass has been
// working this long - a pass that sends one changed file flashed "99%" and
// back to synced.
const quietPassDelay = 1500 * time.Millisecond

// setState is SyncModel.setState: a folder at rest that starts working
// keeps showing it is at rest for quietPassDelay; if the pass is over by
// then nothing changes, otherwise its latest progress shows. Everything
// else applies at once.
func (e *Engine) setState(states map[string]FolderState, id string, s FolderState) {
	e.mu.Lock()
	cur, ok := states[id]
	if !ok {
		e.mu.Unlock()
		return
	}
	if s.Kind == StateScanning && cur.Kind == StateWatching {
		e.held[id] = s
		if e.heldTimers[id] == nil {
			e.heldTimers[id] = time.AfterFunc(quietPassDelay, func() {
				e.mu.Lock()
				delete(e.heldTimers, id)
				held, ok := e.held[id]
				delete(e.held, id)
				if ok {
					if _, still := states[id]; still {
						states[id] = held
					}
				}
				e.mu.Unlock()
				if ok {
					e.notify()
				}
			})
		}
		e.mu.Unlock()
		return
	}
	if t := e.heldTimers[id]; t != nil {
		t.Stop()
		delete(e.heldTimers, id)
	}
	delete(e.held, id)
	states[id] = s
	e.mu.Unlock()
	e.notify()
}

// ---- RAID (issue #69) ---------------------------------------------------

func (e *Engine) startRaidPolling() {
	e.mu.Lock()
	if e.raidStop != nil {
		e.mu.Unlock()

		return
	}
	stop := make(chan struct{})
	e.raidStop = stop
	e.mu.Unlock()
	go func() {
		for {
			e.pollRaid()
			select {
			case <-stop:
				return
			case <-time.After(raidPollInterval):
			}
		}
	}()
}

func (e *Engine) stopRaidPolling() {
	e.mu.Lock()
	if e.raidStop != nil {
		close(e.raidStop)
		e.raidStop = nil
	}
	e.raid = RaidUnknown
	e.devStatus = nil
	e.mu.Unlock()
	e.notify()
}

func (e *Engine) pollRaid() {
	e.mu.Lock()
	langEpoch := e.langEpoch
	e.mu.Unlock()
	resp, err := e.requestWithin(smallRequest, func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqGetStatus{ReqGetStatus: &pb.GetStatus{}}
	})
	if err != nil {
		return
	}
	st, ok := resp.Payload.(*pb.RespEnvelope_RespStatus)
	if !ok {
		return
	}
	e.mu.Lock()
	e.raid = raidHealth(st.RespStatus)
	e.devStatus = st.RespStatus
	e.mu.Unlock()
	e.notify()
	e.deviceLanguage(st.RespStatus.Language, langEpoch)
}

// Raid is the current health.
func (e *Engine) Raid() RaidHealth {
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.raid
}

// ---- remote browsing (the picker) ---------------------------------------

// ListRemoteDirectory is one level of the device's tree, directories first.
func (e *Engine) ListRemoteDirectory(path string) ([]RemoteEntry, error) {
	return ListRemoteDirectory(e.ws, path)
}

// ListRemoteDirectory over any connected client (the CLI has its own).
func ListRemoteDirectory(ws *wsclient.Client, path string) ([]RemoteEntry, error) {
	// dao.GetFilesByPath's non-recursive listing needs the trailing slash
	// to count levels correctly - see SyncModel.listRemoteDirectory.
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	resp, err := ws.Request(ctx, func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqListFiles{ReqListFiles: &pb.ListFiles{Path: path, Recursive: false}}
	})
	if err != nil {
		return nil, err
	}
	if err := wsclient.RespErrorMsg(resp, i18n.DeskMainCouldNotList()); err != nil {
		return nil, err
	}
	lof, ok := resp.Payload.(*pb.RespEnvelope_RespListOfFiles)
	if !ok {
		return nil, nil
	}
	var out []RemoteEntry
	for _, f := range lof.RespListOfFiles.Files {
		out = append(out, RemoteEntry{Name: baseName(f.Path), Path: f.Path, IsDir: f.Mime == "inode/directory"})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}

		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})

	return out, nil
}

func baseName(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}

	return p
}

// fileSize is a device file's size in bytes (issue #187), as the device's
// dao.FileSize: size64, or the int32 size from a device before release
// 93, which leaves size64 at 0 (and wraps a size of 2 GiB or more, as
// it always has).
func fileSize(f *pb.File) int64 {
	if n := f.GetSize64(); n != 0 {
		return n
	}

	return int64(f.GetSize())
}

// ---- orchestration -------------------------------------------------------

// startSync runs every folder's pass that is due - each in its own
// goroutine, a few at a time (passSlot) - and returns once they are over.
// The periodic loops start first, at the first call: they used to wait for
// every first pass, so a big folder's first pass (hours) held every other
// folder's minute check up.
func (e *Engine) startSync() {
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()

		return
	}
	folders := append([]config.Folder(nil), e.cfg.Folders...)
	remotes := append([]config.RemoteFolder(nil), e.cfg.RemoteFolders...)
	first := !e.loopsStarted
	e.loopsStarted = true
	e.mu.Unlock()
	if first {
		go e.reconcileLoop()
		go e.remoteReconcileLoop()
	}

	var wg sync.WaitGroup
	run := func(pass func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pass()
		}()
	}
	for _, f := range folders {
		e.mu.Lock()
		_, watching := e.watchers[f.ID]
		atRest := e.folderStates[f.ID].Kind == StateWatching
		e.mu.Unlock()
		f := f
		switch {
		case !watching:
			run(func() { e.setupFolder(f) })
		case !atRest:
			// Left in an error while the link was down (its retry finds
			// no connection and gives up), or a pass stopped when the
			// device changed (backupPassOver): again now, not in 10
			// minutes. A pass still running is left to finish (busy).
			run(func() { e.reconcile(f) })
		default:
			e.ensureUploadOnly(f)
		}
	}
	for _, f := range remotes {
		e.mu.Lock()
		_, watching := e.remoteWatch[f.ID]
		errored := e.remoteStates[f.ID].Kind == StateError
		e.mu.Unlock()
		f := f
		switch {
		case !watching:
			run(func() {
				e.reconcileRemoteFolder(f)
				e.startRemoteWatcher(f)
			})
		case errored:
			run(func() { e.reconcileRemoteFolder(f) })
		}
	}
	wg.Wait()
}

func (e *Engine) setupFolder(f config.Folder) {
	// Removed while startSync worked through its copy of the folder list:
	// nothing to set up, nor to make upload only on the device.
	e.mu.Lock()
	configured := e.backupConfiguredLocked(f.ID)
	e.mu.Unlock()
	if !configured {
		return
	}
	e.ensureUploadOnly(f)
	e.reconcile(f)
	e.startWatcher(f)
}

// reconcileLoop is the backups' safety net, every reconcileInterval: each
// folder's pass in its own goroutine, so one that takes long doesn't hold
// the others (a folder whose pass still runs is skipped, folderBusy).
func (e *Engine) reconcileLoop() {
	for {
		time.Sleep(reconcileInterval)
		e.mu.Lock()
		stopped := e.stopped
		folders := append([]config.Folder(nil), e.cfg.Folders...)
		ready := config.Ready(e.cfg, e.password)
		e.mu.Unlock()
		if stopped {
			return
		}
		if !ready || !e.ws.IsConnected() {
			continue
		}
		for _, f := range folders {
			go e.reconcile(f)
		}
	}
}

// remoteReconcileLoop polls the two-way folders every minute, each in its
// own goroutine as reconcileLoop.
func (e *Engine) remoteReconcileLoop() {
	for {
		time.Sleep(remoteReconcileInterval)
		e.mu.Lock()
		stopped := e.stopped
		folders := append([]config.RemoteFolder(nil), e.cfg.RemoteFolders...)
		ready := config.Ready(e.cfg, e.password)
		e.mu.Unlock()
		if stopped {
			return
		}
		if !ready || !e.ws.IsConnected() {
			continue
		}
		// Issue #192: a folder kept out of Images, or shown again, from the
		// web or a phone shows here within a minute. Not asked of a device
		// that said it can't (it is asked again at the next connect).
		e.mu.Lock()
		askImages := !e.imagesUnsupportedLocked() && len(e.cfg.Folders)+len(e.cfg.RemoteFolders) > 0
		e.mu.Unlock()
		if askImages {
			e.refreshOutOfImages()
		}
		for _, f := range folders {
			go e.reconcileRemoteFolder(f)
		}
	}
}

func (e *Engine) startWatcher(f config.Folder) {
	e.mu.Lock()
	if _, ok := e.watchers[f.ID]; ok || e.stopped || !e.backupConfiguredLocked(f.ID) {
		e.mu.Unlock()

		return
	}
	e.mu.Unlock()
	if fi, err := os.Stat(f.Path); err != nil || !fi.IsDir() {
		return // not there (localRootPresent): its pass starts the watch once it is back
	}
	w, err := NewWatcher(f.Path, func(path string, isDir, removed bool) {
		if isDir {
			return
		}
		e.debounceChange(path, f)
	}, func() { go e.reconcile(f) })
	if err != nil {
		e.setFolderState(f.ID, msgState(i18n.DeskMainCannotWatch(err.Error())))

		return
	}
	e.mu.Lock()
	// Removed (or stopped) while its first pass ran, or a concurrent
	// startSync got there first: a watcher kept now would never be stopped,
	// and would go on uploading the folder.
	if e.stopped || e.watchers[f.ID] != nil || !e.backupConfiguredLocked(f.ID) {
		e.mu.Unlock()
		w.Stop()

		return
	}
	e.watchers[f.ID] = w
	e.mu.Unlock()
}

func (e *Engine) debounceChange(path string, f config.Folder) {
	e.mu.Lock()
	if t := e.debounce[path]; t != nil {
		t.Stop()
	}
	e.debounce[path] = time.AfterFunc(debounceInterval, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		delete(e.debounce, path)
		if e.stopped {
			return
		}
		// Queued for the folder's worker, not uploaded from here: a
		// directory of 2,000 photos copied in fired 2,000 timers at once,
		// each hashing and holding chunk buffers for its own upload.
		if e.changeQueued[f.ID] == nil {
			e.changeQueued[f.ID] = map[string]bool{}
		}
		if !e.changeQueued[f.ID][path] {
			e.changeQueued[f.ID][path] = true
			e.changeQueue[f.ID] = append(e.changeQueue[f.ID], path)
		}
		if !e.draining[f.ID] {
			e.draining[f.ID] = true
			go e.drainChanges(f)
		}
	})
	e.mu.Unlock()
}

// drainChanges is a backup folder's one worker for its debounced changes:
// one file hashed and uploaded at a time, in the order they came. A path
// leaves the queue before it is processed, so a change during its own
// upload queues it again and the new content follows.
func (e *Engine) drainChanges(f config.Folder) {
	for {
		e.mu.Lock()
		q := e.changeQueue[f.ID]
		if e.stopped || len(q) == 0 || !e.backupConfiguredLocked(f.ID) {
			delete(e.changeQueue, f.ID)
			delete(e.changeQueued, f.ID)
			delete(e.draining, f.ID)
			e.mu.Unlock()

			return
		}
		path := q[0]
		e.changeQueue[f.ID] = q[1:]
		delete(e.changeQueued[f.ID], path)
		e.mu.Unlock()
		e.processChangedPath(path, f)
	}
}

func (e *Engine) processChangedPath(path string, f config.Folder) {
	e.mu.Lock()
	domain := e.cfg.Domain
	e.mu.Unlock()
	// A change still waiting when its folder was removed, or became two-way,
	// is not sent.
	if !e.stillBackingUp(f.ID, domain) {
		return
	}
	if !e.ws.IsConnected() {
		return // the reconcile safety net catches it up later
	}
	fi, err := os.Stat(path)
	if err == nil && !fi.IsDir() {
		// As the reconcile: the file's key below the folder, its path on the
		// device the device's own when it has it (backupKey).
		key, ok := backupKey(f.Path, path)
		if !ok || hiddenHere(path, fi) {
			return // as the reconcile: not part of the backup
		}
		e.mu.Lock()
		known, onDevice := e.remoteHashes[f.ID][key]
		e.mu.Unlock()
		remotePath := e.remotePathFor(f.Path) + "/" + key
		if onDevice {
			remotePath = known.path
		}
		h, err := e.cachedHashInfo(f.ID, path, fi)
		if err != nil || (onDevice && h == known.hash) {
			return
		}
		// Hashing takes a while: removed, or pointed at another device,
		// meanwhile.
		if !e.stillBackingUp(f.ID, domain) {
			return
		}
		if err := e.upload(path, remotePath, h, fi); err != nil {
			log.Printf("error uploading %s: %v", path, err)

			return
		}
		e.mu.Lock()
		if e.backupConfiguredLocked(f.ID) {
			if e.remoteHashes[f.ID] == nil {
				e.remoteHashes[f.ID] = map[string]remoteEntry{}
			}
			e.remoteHashes[f.ID][key] = remoteEntry{path: remotePath, hash: h}
		}
		e.mu.Unlock()
	}
	// Gone here: nothing to do. A backup is upload only - what this
	// computer deletes stays on the device (and the device refuses the
	// delete anyway, see markUploadOnly).
}

// ---- reconcile: local -> remote ----------------------------------------

type uploadItem struct {
	path, remote, hash string
	size               int64
	info               os.FileInfo
	key                string // pathKey below the folder
}

func (e *Engine) reconcile(f config.Folder) {
	defer e.saveHashCacheIfKept(f.ID)
	if !e.ws.IsConnected() {
		return
	}
	e.mu.Lock()
	// Not configured any more: a pass from a stale copy of the folder list
	// (startSync, reconcileLoop) or a retry that fired after the removal.
	if e.folderBusy[f.ID] || !e.backupConfiguredLocked(f.ID) {
		e.mu.Unlock()

		return
	}
	// The device this pass talks to, as reconcileRemoteFolder.
	domainAtStart := e.cfg.Domain
	e.folderBusy[f.ID] = true
	if t := e.errorRetry[f.ID]; t != nil {
		t.Stop()
		delete(e.errorRetry, f.ID)
	}
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.folderBusy, f.ID)
		e.mu.Unlock()
	}()
	// A folder that isn't there (a disk unplugged) waits, as a two-way
	// one does; a backup never deletes anything anyway.
	present, wasMissing := e.localRootPresent(f.ID, f.Path, false)
	if !present {
		e.scheduleErrorRetry(f)

		return
	}
	release, ok := e.passSlot(f.ID, f.Path, func() bool { return e.stillBackingUp(f.ID, domainAtStart) },
		func(s FolderState) { e.setFolderState(f.ID, s) })
	if !ok {
		return
	}
	defer release()
	e.ensureUploadOnly(f)
	// Issue #192: before anything is sent, so a new folder's photos never
	// reach Images. Not waited on beyond its one request: an old or
	// unreachable device doesn't stop the backup.
	e.applyRequestsBeforePass(f.ID)

	remotePrefix := e.remotePathFor(f.Path) + "/"
	resp, err := e.requestWithin(listingRequest, func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqListFiles{ReqListFiles: &pb.ListFiles{Path: remotePrefix, Recursive: true}}
	})
	if err != nil {
		e.setFolderState(f.ID, errorState(err))
		e.scheduleErrorRetry(f)

		return
	}
	if err := wsclient.RespErrorMsg(resp, i18n.DeskMainCouldNotList()); err != nil {
		e.setFolderState(f.ID, errorState(err))
		e.scheduleErrorRetry(f)

		return
	}
	// By relative path as the two-way passes key it (pathKey, NFC): a file
	// already on the device is sent to its own path there, a new one to its
	// NFC name.
	remoteMap := map[string]remoteEntry{}
	if lof, ok := resp.Payload.(*pb.RespEnvelope_RespListOfFiles); ok {
		for _, rf := range lof.RespListOfFiles.Files {
			rel, under := strings.CutPrefix(rf.Path, remotePrefix)
			if !under || rel == "" {
				continue
			}
			key := pathKey(rel)
			if prev, dup := remoteMap[key]; dup && prev.path == remotePrefix+key {
				continue // the NFC spelling, when the device has both
			}
			remoteMap[key] = remoteEntry{path: rf.Path, hash: rf.Hash}
		}
	}

	// An unreadable subdirectory only means fewer uploads here: a backup
	// never deletes on the device.
	local, failedDirs, err := enumerateFiles(f.Path)
	if err != nil {
		e.setFolderState(f.ID, errorState(err))
		e.scheduleErrorRetry(f)

		return
	}
	e.pruneHashCache(f.ID, f.Path, local, failedDirs)
	var toUpload []uploadItem
	// Issue #138 (as SyncModel.reconcile): say which file is being checked,
	// once a second at most - most files are answered from the hash cache
	// in no time, the new ones are what takes a while.
	var lastShown time.Time
	var folderBytes int64
	for i, p := range local {
		if time.Since(lastShown) > time.Second {
			if e.backupPassOver(f, domainAtStart) {
				return
			}
			lastShown = time.Now()
			e.setFolderState(f.ID, checkingState(i+1, len(local), filepath.Base(p)))
			e.saveHashCacheEvery(f.ID)
		}
		key, ok := backupKey(f.Path, p)
		if !ok {
			continue
		}
		fi, _ := os.Stat(p)
		var size int64
		if fi != nil {
			size = fi.Size()
		}
		folderBytes += size
		// Nothing on the device at this path: it's sent whatever its
		// content, so it is hashed just before the upload, not here - a new
		// folder used to sit on "Checking" for as long as reading all of it
		// took (as SyncModel.reconcile).
		h := ""
		rp := remotePrefix + key
		if known, onDevice := remoteMap[key]; onDevice {
			rp = known.path
			var err error
			h, err = e.cachedHashInfo(f.ID, p, fi)
			if err != nil || known.hash == h {
				continue
			}
		}
		toUpload = append(toUpload, uploadItem{p, rp, h, size, fi, key})
	}

	if len(toUpload) > 0 {
		// Of the whole folder, as SyncModel.reconcile: what is already on
		// the device counts as done, so the counter and bar say how much
		// of the folder is safe rather than restarting at 0 each pass.
		total := folderBytes
		if total < 1 {
			total = 1
		}
		done := folderBytes
		for _, it := range toUpload {
			done -= it.size
		}
		alreadyThere := len(local) - len(toUpload)
		for k, it := range toUpload {
			if e.backupPassOver(f, domainAtStart) {
				return
			}
			// The link went: stop rather than "fail" every remaining file
			// in a second each, racing the bar to 100% with nothing sent
			// (as SyncModel.reconcile); OnConnect's startSync resumes it.
			if !e.ws.IsConnected() {
				e.setFolderState(f.ID, msgState(i18n.DeskMainFolderOffline()))

				return
			}
			e.setFolderState(f.ID, FolderState{Kind: StateScanning, Progress: float64(done) / float64(total), CurrentFile: fmt.Sprintf("%d/%d · %s", alreadyThere+k+1, len(local), filepath.Base(it.path))})
			e.saveHashCacheEvery(f.ID)
			if it.hash == "" {
				fi, err := os.Stat(it.path)
				var h string
				if err == nil {
					h, err = e.cachedHashInfo(f.ID, it.path, fi)
				}
				if err != nil {
					log.Printf("cannot hash %s: %v", filepath.Base(it.path), err)
					done += it.size

					continue
				}
				it.hash, it.info = h, fi
				// A large file takes a while to hash: checked again so
				// nothing goes to a device this folder no longer syncs with.
				if e.backupPassOver(f, domainAtStart) {
					return
				}
			}
			if err := e.upload(it.path, it.remote, it.hash, it.info); err != nil {
				if errors.Is(err, errBusyFile) {
					log.Printf("%s %v", filepath.Base(it.path), err)
				} else {
					log.Printf("error syncing %s: %v", filepath.Base(it.path), err)
				}
			} else {
				remoteMap[it.key] = remoteEntry{path: it.remote, hash: it.hash}
			}
			done += it.size
		}
	}

	// A backup is upload only: what is no longer here stays on the device.
	if e.backupPassOver(f, domainAtStart) {
		return
	}
	e.mu.Lock()
	if e.backupConfiguredLocked(f.ID) {
		e.remoteHashes[f.ID] = remoteMap
	}
	e.mu.Unlock()
	if wasMissing {
		// Back after it was missing: its watcher went with it.
		e.startWatcher(f)
	}
	e.setFolderState(f.ID, FolderState{Kind: StateWatching})
}

// remoteEntry is a file a backup's device folder holds: its path there
// (the device's own bytes) and its hash.
type remoteEntry struct {
	path, hash string
}

// backupKey is file p's key in the backup of root (pathKey of its path
// below root), false for what a backup doesn't send (notSynced).
func backupKey(root, p string) (string, bool) {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return "", false
	}
	key := pathKey(filepath.ToSlash(rel))
	if key == "." || key == "" || notSynced(key) {
		return "", false
	}

	return key, true
}

// backupPassOver: a backup pass ends once its folder is removed or this
// computer is pointed at another device (disconnected, another device set
// up) - what is left is not sent there, as stillSyncing does for two-way
// passes. When only the device changed, the folder goes again shortly,
// from the new device's own listing. No status of its own, as on the Mac:
// the folder shows a plain scan (not the stopped pass's file and progress)
// until that pass, which the retry or, if the new device is not connected
// by then, startSync's resume of a folder not at rest starts.
func (e *Engine) backupPassOver(f config.Folder, domain string) bool {
	e.mu.Lock()
	configured := e.backupConfiguredLocked(f.ID)
	sameDevice := e.cfg.Domain == domain
	e.mu.Unlock()
	if configured && sameDevice {
		return false
	}
	log.Printf("backup %s: folder removed or device changed - pass stopped", f.Path)
	if configured {
		e.setFolderState(f.ID, FolderState{Kind: StateScanning})
		e.scheduleErrorRetry(f)
	}

	return true
}

func (e *Engine) scheduleErrorRetry(f config.Folder) {
	e.mu.Lock()
	defer e.mu.Unlock()
	// A removed folder's pass that failed: a retry would list the device
	// every 30 seconds for a folder nobody syncs any more.
	if e.stopped || !e.backupConfiguredLocked(f.ID) {
		return
	}
	if t := e.errorRetry[f.ID]; t != nil {
		t.Stop()
	}
	e.errorRetry[f.ID] = time.AfterFunc(errorRetryInterval, func() {
		e.mu.Lock()
		delete(e.errorRetry, f.ID)
		e.mu.Unlock()
		e.reconcile(f)
	})
}

// ---- reconcile: two-way (issue #47) -------------------------------------

type actionKind int

const (
	actUpload actionKind = iota
	actDownload
	actDeleteLocal
	actDeleteRemote
	// Conflicts (both sides changed the same file): the losing version is
	// kept as a "(conflict …)" copy next to it, which then syncs like any
	// new file, instead of being overwritten.
	actDownloadKeepLocal // rename the local file to the copy, then download
	actUploadKeepRemote  // download the device's version to the copy, then upload
)

type action struct {
	relative string
	kind     actionKind
	hash     string
}

func (e *Engine) reconcileRemoteFolder(f config.RemoteFolder) {
	defer e.saveHashCacheIfKept(f.ID)
	if !e.ws.IsConnected() {
		return
	}
	// The device this pass talks to: a pass that outlives its folder or
	// the device (disconnect, another device set up) stops instead of
	// carrying on there - as the Mac app's reconcileRemoteFolder.
	e.mu.Lock()
	domainAtStart := e.cfg.Domain
	if e.folderBusy[f.ID] || !e.remoteConfiguredLocked(f.ID) {
		e.mu.Unlock()

		return
	}
	e.folderBusy[f.ID] = true
	if t := e.remoteRetry[f.ID]; t != nil {
		t.Stop()
		delete(e.remoteRetry, f.ID)
	}
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.folderBusy, f.ID)
		e.mu.Unlock()
	}()
	// A missing local root is not "everything was deleted here": an
	// unplugged disk, a share not mounted yet, a folder renamed. The folder
	// waits, untouched on both sides, and carries on once it is back - it
	// used to be dropped from the configuration for good, record and all.
	present, wasMissing := e.localRootPresent(f.ID, f.LocalPath, true)
	if !present {
		e.scheduleRemoteRetry(f)

		return
	}
	// A few folders' passes at a time (passSlot); the folder says which one
	// it waits for meanwhile.
	release, ok := e.passSlot(f.ID, f.LocalPath, func() bool { return e.stillSyncing(f.ID, domainAtStart) },
		func(s FolderState) { e.setRemoteState(f.ID, s) })
	if !ok {
		return
	}
	defer release()
	// The device, as sync records name it (record.go): taken once, at the
	// start, like the address.
	device := e.deviceFor(domainAtStart)

	// The folder here first: whether it is the one its record was made
	// with (rootid.go) needs nothing from the device, and a folder that
	// isn't waits without even listing it.
	e.mu.Lock()
	recorded, meta, written := e.recordedLocked(f.ID)
	pinned := e.recordPinnedLocked(f.ID)
	e.mu.Unlock()
	// A record that names its device by its pin, and none confirmed on
	// this sign-in (record.go): the device is asked now, once a minute at
	// most.
	if pinned && device.Pin == "" && e.ws.ConfirmPin(context.Background()) {
		device = e.deviceFor(domainAtStart)
	}
	e.mu.Lock()
	last, uncertain, saveDevice := e.recordForDeviceLocked(f.ID, device)
	e.mu.Unlock()
	if last == nil {
		last = map[string]string{}
	}
	lv, err := e.lookHere(f, meta, last, written)
	if err != nil {
		e.setRemoteState(f.ID, errorState(err))
		e.scheduleRemoteRetry(f)

		return
	}
	// The maybe mounts (mounts.go) are not left out of the folder's
	// identity: only what is known to be unknown here is.
	verdict := e.identifyFolder(f, lv.byRel, recorded, meta,
		func(rel string) bool { return lv.unknown(rel) || lv.skipped.covers(rel) })
	if !verdict.ok {
		e.folderReplaced(f)
		e.scheduleRemoteRetry(f)

		return
	}
	if e.folderBack(f) {
		wasMissing = true // its watcher went with it
	}
	meta.Root, meta.Mounts, meta.MaybeMounts = verdict.token, lv.mounts, lv.maybeMounts
	if verdict.mark {
		e.markFolder(f, verdict.token)
	}
	e.noteGoneMounts(f, lv.heldDirs())

	// As reconcile - first, and never holding the pass up: upload only
	// (issue #132) before anything could be deleted, and Images (issue
	// #192) before anything is sent.
	e.applyRequestsBeforePass(f.ID)
	// Upload only asked for and not acknowledged yet (it failed, or the
	// device can't yet): no delete goes to the device until it is. A file
	// deleted here keeps its baseline, and its delete goes once the device
	// has the flag - which then refuses it and keeps the file
	// (keptOnDevice).
	holdDeletes := e.uploadOnlyAwaited(f.ID)

	remotePrefix := strings.TrimSuffix(f.RemotePath, "/") + "/"
	resp, err := e.requestWithin(listingRequest, func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqListFiles{ReqListFiles: &pb.ListFiles{Path: remotePrefix, Recursive: true}}
	})
	if err != nil {
		e.setRemoteState(f.ID, errorState(err))
		e.scheduleRemoteRetry(f)

		return
	}
	if err := wsclient.RespErrorMsg(resp, i18n.DeskMainCouldNotList()); err != nil {
		e.setRemoteState(f.ID, errorState(err))
		e.scheduleRemoteRetry(f)

		return
	}
	// Every path below is a key (pathKey: its NFC form), so a name the
	// device holds in one normalization and this computer in another is
	// one file. The device's own bytes address what it has (deviceRel);
	// a new file goes up as its key.
	remoteByRel := map[string]*pb.File{}
	// Device entries whose names Windows can't hold (names.go).
	unusable := 0
	// The keys of the device entries left out for their names (those, and
	// the unsafe ones): left out here too - a file here at one is neither
	// sent over the device's nor taken for deleted there, and keeps its
	// record.
	excludedHere := map[string]bool{}
	if lof, ok := resp.Payload.(*pb.RespEnvelope_RespListOfFiles); ok {
		skipped, twins := 0, 0
		for _, rf := range lof.RespListOfFiles.Files {
			// Device paths are data, not trusted: one that isn't under the
			// folder, or that climbs out of it, would be written (and later
			// deleted from the device) outside the folder.
			rel, under := strings.CutPrefix(rf.Path, remotePrefix)
			key := pathKey(rel)
			if under && rel != "" && (unusableName(rel) || unusableName(key)) {
				unusable++
				excludedHere[key] = true

				continue
			}
			if !under || !safeRelative(rel) || !safeRelative(key) {
				skipped++
				if under && rel != "" {
					excludedHere[key] = true
				}

				continue
			}
			if notSynced(key) {
				// Never seen here either (enumerateFiles): listing it
				// meant a download, then a delete from the device.
				continue
			}
			if prev := remoteByRel[key]; prev != nil {
				// The same name twice on the device, in two
				// normalizations: the NFC one is synced, the other left
				// there as it is - never deleted or renamed for it.
				twins++
				if prev.Path == remotePrefix+key {
					continue
				}
			}
			remoteByRel[key] = rf
		}
		if skipped > 0 {
			log.Printf("two-way %s: %d device entries outside the folder ignored", f.RemotePath, skipped)
		}
		if twins > 0 {
			log.Printf("two-way %s: %d name(s) listed twice by the device in two Unicode normalizations - one of each is synced, the other left as it is", f.RemotePath, twins)
		}
		if unusable > 0 {
			log.Printf("two-way %s: %d device file(s) have names Windows can't hold as they are - left as they are, not synced here", f.RemotePath, unusable)
		}
	}

	// A folder renamed only in case on the device is renamed so here too,
	// on a disk that ignores case (casefix.go), and the folder looked at
	// again; what the record and the hash cache say of its files goes with
	// them, so nothing is sent or fetched for it.
	deviceKeys := make(map[string]bool, len(remoteByRel))
	for k := range remoteByRel {
		deviceKeys[k] = true
	}
	for round := 0; round < 8; round++ {
		renames := matchFolderCase(f.LocalPath, lv.byRel, deviceKeys, last)
		if len(renames) == 0 {
			break
		}
		last = renamedRecord(last, renames)
		e.renameHashCache(f, renames)
		for i, m := range meta.Mounts {
			meta.Mounts[i] = renamedKey(m, renames)
		}
		for i, m := range meta.MaybeMounts {
			meta.MaybeMounts[i] = renamedKey(m, renames)
		}
		if lv, err = e.lookHere(f, meta, last, written); err != nil {
			e.setRemoteState(f.ID, errorState(err))
			e.scheduleRemoteRetry(f)

			return
		}
		meta.Mounts, meta.MaybeMounts = lv.mounts, lv.maybeMounts
	}
	// The hash cache forgets what is no longer here - only now that the
	// folder is known to be the synced one (the empty mount point of a disk
	// that isn't mounted emptied it, and every file was read again once the
	// disk was back), and not what is under a folder that can't be read or a
	// disk that isn't mounted: unknown, not gone.
	e.pruneHashCache(f.ID, f.LocalPath, lv.files, lv.unknownDirs())
	local, localByRel, skippedSet := lv.files, lv.byRel, lv.skipped
	unknownHere := func(rel string) bool { return lv.unknown(rel) || lv.maybe.covers(rel) }

	localHashes := map[string]string{}
	// The stat each hash was taken with: sizes for the progress bar, and
	// what the file must still look like before a planned action
	// overwrites or deletes it.
	localInfo := map[string]os.FileInfo{}
	// Only here, not on the device and never synced: an upload whatever
	// its content, hashed when it is sent (see reconcile()).
	newLocal := map[string]bool{}
	// A file that can't be read is left out of the comparison entirely:
	// treating it as "not here" would fetch (and overwrite) something that
	// is here, just unreadable - an evicted cloud-drive placeholder, say.
	unreadable := map[string]bool{}
	var lastShown time.Time
	for i, p := range local {
		rel, err := filepath.Rel(f.LocalPath, p)
		if err != nil {
			continue
		}
		key := pathKey(filepath.ToSlash(rel))
		if localByRel[key] != p {
			continue // its twin is the one synced
		}
		// Issue #138: which file is being checked, see reconcile().
		if time.Since(lastShown) > time.Second {
			lastShown = time.Now()
			e.setRemoteState(f.ID, checkingState(i+1, len(local), filepath.Base(p)))
			e.saveHashCacheEvery(f.ID)
		}
		if excludedHere[key] {
			continue // never compared (excludedHere)
		}
		if _, onDevice := remoteByRel[key]; !onDevice {
			if _, synced := last[key]; !synced {
				newLocal[key] = true

				continue
			}
		}
		fi, err := os.Stat(p)
		var h string
		if err == nil {
			h, err = e.cachedHashInfo(f.ID, p, fi)
		}
		if err == nil {
			localHashes[key] = h
			localInfo[key] = fi
		} else {
			unreadable[key] = true
			if len(unreadable) <= 5 {
				log.Printf("cannot hash %s: %v", rel, err)
			}
		}
	}

	all := map[string]bool{}
	for k := range remoteByRel {
		all[k] = true
	}
	for k := range localByRel {
		all[k] = true
	}
	for k := range last {
		all[k] = true
	}
	// Under a directory that could not be read (permissions, a dead
	// network mount) files look deleted here; they are left out like an
	// unreadable file, or their device copies would be deleted - and are
	// counted in the folder's "could not be read" like one. So are those
	// under a disk or share mounted in the folder that isn't now.
	notSyncedHere := map[string]bool{}
	for rel := range all {
		switch {
		case unknownHere(rel):
			unreadable[rel] = true
		case skippedSet.covers(rel):
			notSyncedHere[rel] = true
		}
	}
	// Of those, the ones the device has - what the note says is left there.
	leftOnDevice := 0
	for rel := range notSyncedHere {
		if remoteByRel[rel] != nil {
			leftOnDevice++
		}
	}

	newSynced := map[string]string{}
	for k, v := range last {
		newSynced[k] = v
	}
	var actions []action
	deletesHeld := 0 // waiting for upload only (holdDeletes)
	for rel := range all {
		if unreadable[rel] || notSyncedHere[rel] || excludedHere[rel] {
			continue
		}
		if newLocal[rel] {
			actions = append(actions, action{rel, actUpload, ""})

			continue
		}
		localHash, hasLocal := localHashes[rel]
		remoteFile := remoteByRel[rel]
		remoteHash := ""
		if remoteFile != nil {
			remoteHash = remoteFile.Hash
		}
		// What each side had after the last pass: the same, except for a
		// file the device kept when it was deleted here (keptOnDevice) -
		// and none at all for one whose folder is no longer upload only.
		lastLocal, lastRemote := baselinesFor(last[rel], remoteFile)
		// Issue #141: listed without a hash means the device has lost
		// this file's content. A copy here is sent again, which restores
		// it; with none here there is nothing to fetch - never a download
		// (it failed on every pass, forever) nor a delete.
		if remoteFile != nil && remoteHash == "" {
			if hasLocal {
				actions = append(actions, action{rel, actUpload, localHash})
				newSynced[rel] = localHash
			}

			continue
		}
		if hasLocal == (remoteFile != nil) && localHash == remoteHash {
			if hasLocal {
				newSynced[rel] = localHash
			} else {
				delete(newSynced, rel)
			}

			continue
		}
		localChanged := localHash != lastLocal
		remoteChanged := remoteHash != lastRemote
		if !localChanged && !remoteChanged {
			// Deleted here and kept by the device (an upload-only folder),
			// and neither side changed it since: not downloaded again, nor
			// the delete sent again. The record keeps saying so.
			continue
		}
		remoteWins := remoteChanged
		if localChanged && remoteChanged {
			// Genuine conflict: the newer modification wins.
			var localDate, remoteDate time.Time
			if p, ok := localByRel[rel]; ok {
				if fi, err := os.Stat(p); err == nil {
					localDate = fi.ModTime()
				}
			}
			if remoteFile != nil && remoteFile.Modified != nil {
				remoteDate = remoteFile.Modified.AsTime()
			}
			switch {
			case localDate.IsZero():
				remoteWins = true
			case remoteDate.IsZero():
				remoteWins = false
			default:
				remoteWins = remoteDate.After(localDate)
			}
		}
		// Both sides have different content: whichever loses is kept.
		conflict := localChanged && remoteChanged && hasLocal && remoteFile != nil
		if remoteWins {
			if conflict {
				actions = append(actions, action{rel, actDownloadKeepLocal, remoteHash})
				newSynced[rel] = remoteHash
			} else if remoteFile != nil {
				actions = append(actions, action{rel, actDownload, remoteHash})
				newSynced[rel] = remoteHash
			} else {
				actions = append(actions, action{rel, actDeleteLocal, ""})
				delete(newSynced, rel)
			}
		} else {
			if conflict {
				actions = append(actions, action{rel, actUploadKeepRemote, localHash + "\x00" + remoteHash})
				newSynced[rel] = localHash
			} else if hasLocal {
				actions = append(actions, action{rel, actUpload, localHash})
				newSynced[rel] = localHash
			} else if holdDeletes {
				// Not upload only on the device yet: the delete waits, and
				// newSynced keeps its baseline.
				deletesHeld++
			} else {
				// The device's hash goes with it: kept there (upload only),
				// it is what the record holds.
				actions = append(actions, action{rel, actDeleteRemote, remoteHash})
				delete(newSynced, rel)
			}
		}
	}

	e.noteHeldDeletes(f, deletesHeld)

	// Mass-deletion guard (as SyncModel.reconcileRemoteFolder): a device
	// wiped or set up again looks like one whose owner deleted everything,
	// and this used to delete the whole folder here to match. Past a
	// handful of files and a quarter of the folder, the device is taken to
	// have lost them: they are sent back and nothing here is touched. A
	// file whose content the device lists under a new name is no loss - a
	// folder moved or renamed there (from the web or a phone) - and doesn't
	// count: the guard used to send the old names back next to the new.
	moved := movedOnDevice(remoteByRel, last)
	localDeletes := 0
	for _, a := range actions {
		if a.kind == actDeleteLocal && !moved[localHashes[a.relative]] {
			localDeletes++
		}
	}
	var guardNote FolderState // an error state to end the pass with
	if localDeletes > massDeleteMin && localDeletes*4 > max(len(localByRel), 1) {
		log.Printf("%d files gone from the device at once - restoring them instead of deleting them here", localDeletes)
		for i, a := range actions {
			if a.kind == actDeleteLocal {
				if _, here := localByRel[a.relative]; here {
					actions[i] = action{a.relative, actUpload, ""}
					delete(newSynced, a.relative)
				}
			}
		}
		guardNote = noteState(
			fmt.Sprintf("%d files had disappeared from the device - restored them from this computer instead of deleting them here", localDeletes), // i18n-ignore: state.json's English, for the command line; Msg is the tray's
			i18n.DeskMainRestoredFiles(localDeletes))
	}

	// The same guard the other way. A delete on the device is for good -
	// its versions and, with the last copy, its content - and files gone
	// from here in numbers are what a folder emptied by accident looks like:
	// a disk or share mounted inside it that isn't, a program that emptied
	// it, a folder that isn't the synced one at all (rootid.go). Past a
	// handful of files and a quarter of what the device holds, those deletes
	// are held: the files stay on the device, their baselines here, and the
	// folder says so until they are back here or deleted there too (from the
	// web or a phone), which agrees. A file whose content is here under
	// another name is no loss - a folder moved or renamed here - and its
	// delete goes.
	var heldNote FolderState
	held := e.holdMassDeviceDeletes(f, actions, localByRel, localHashes, newLocal, remoteByRel)
	e.noteMassHeld(f, len(held))
	if len(held) > 0 {
		kept := actions[:0]
		for _, a := range actions {
			if held[a.relative] {
				newSynced[a.relative] = last[a.relative]

				continue
			}
			kept = append(kept, a)
		}
		actions = kept
		heldNote = noteState(
			fmt.Sprintf("%d files deleted here at once are kept on the device - if that was meant, delete them there too", len(held)), // i18n-ignore: state.json's English, for the command line; Msg is the tray's
			i18n.DeskMainDeletesHeld(len(held)))
	}

	// A device whose pin this sign-in hasn't confirmed, where the record
	// names its device by one (record.go): the same device most likely, or
	// one set up again since under its name, empty. Nothing that can't be
	// taken back goes ahead - no delete either way, no download over a file
	// here - until it is confirmed; the rest does, and the baselines of what
	// waits stay.
	var waitNote FolderState
	if uncertain {
		waiting := 0
		kept := actions[:0]
		for _, a := range actions {
			_, here := localByRel[a.relative]
			if a.kind == actDeleteLocal || a.kind == actDeleteRemote || (a.kind == actDownload && here) {
				if prior, ok := last[a.relative]; ok {
					newSynced[a.relative] = prior
				} else {
					delete(newSynced, a.relative)
				}
				waiting++

				continue
			}
			kept = append(kept, a)
		}
		actions = kept
		e.noteUnconfirmed(f, waiting)
		if waiting > 0 {
			waitNote = noteState(fmt.Sprintf("%d change(s) wait until this computer recognizes the device", waiting), // i18n-ignore: state.json's English, for the command line; Msg is the tray's
				i18n.DeskMainDeviceUnconfirmed(waiting))
		}
	} else {
		e.noteUnconfirmed(f, 0)
	}

	// Local deletes first: on a disk that ignores case (Windows, macOS) a
	// file renamed only in case elsewhere is a download of "A.txt" and a
	// delete of "a.txt" here - the same file. Downloading first, the delete
	// then removed what had just been written.
	sort.SliceStable(actions, func(i, j int) bool {
		return actions[i].kind == actDeleteLocal && actions[j].kind != actDeleteLocal
	})

	// stillAsScanned: the local file is as the plan saw it - same size and
	// modification time, or still absent. Checked at the last moment before
	// it is overwritten or deleted: a pass can run for hours, and an edit
	// (or a new file) made here meanwhile must not be lost. The next pass
	// sees it as a change, and a conflict keeps both versions.
	stillAsScanned := func(rel, p string) error {
		fi, err := os.Lstat(p)
		seen, had := localInfo[rel]
		if !had {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}

			return errChangedHere
		}
		if err != nil {
			return err
		}
		if fi.Size() != seen.Size() || !fi.ModTime().Equal(seen.ModTime()) {
			return errChangedHere
		}

		return nil
	}
	absent := func(p string) func() error {
		return func() error {
			if _, err := os.Lstat(p); err == nil {
				return errChangedHere
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}

			return nil
		}
	}
	// Where a path is, each side: the file there now, or for one only on
	// the other side, its key (NFC) - new names are made in NFC, names
	// already there are never respelled.
	localPathOf := func(rel string) string {
		if p, ok := localByRel[rel]; ok {
			return p
		}

		return filepath.Join(f.LocalPath, filepath.FromSlash(rel))
	}
	remotePathOf := func(rel string) string {
		if rf := remoteByRel[rel]; rf != nil {
			return rf.Path
		}

		return remotePrefix + rel
	}

	// Of the whole folder, as SyncModel.reconcileRemoteFolder: every path
	// on either side counts and what already agrees is done. Only worked
	// out when there is something to do - a folder at rest is the usual
	// pass, and summing it stat'ed every file again every minute.
	sizes := map[string]int64{}
	bytesOf := func(rel string) int64 {
		if n, ok := sizes[rel]; ok {
			return n
		}
		fi, ok := localInfo[rel]
		if p, here := localByRel[rel]; !ok && here {
			// Not hashed this pass (new here): stat it now.
			var err error
			if fi, err = os.Stat(p); err == nil {
				ok = true
			}
		}
		var n int64
		if ok {
			n = fi.Size()
		} else if rf := remoteByRel[rel]; rf != nil {
			n = fileSize(rf)
		}
		sizes[rel] = n
		return n
	}
	var folderCount, alreadyAgree int
	var totalBytes, bytesDone int64
	if len(actions) > 0 {
		folderPaths := map[string]bool{}
		for k := range localByRel {
			folderPaths[k] = true
		}
		for k := range remoteByRel {
			folderPaths[k] = true
		}
		folderCount = max(len(folderPaths), len(actions))
		alreadyAgree = folderCount - len(actions)
		for k := range folderPaths {
			totalBytes += bytesOf(k)
		}
		totalBytes = max(totalBytes, 1)
		bytesDone = totalBytes
		for _, a := range actions {
			bytesDone -= bytesOf(a.relative)
		}
	}

	// The folders local deletions emptied: removed after the pass (see
	// removeEmptiedDirs).
	emptied := map[string]bool{}
	// What the pass couldn't sync, and the paths it had something to do
	// for - failures backed off elsewhere are forgotten (fail.go).
	var failures passFailures
	acted := map[string]bool{}
	for i, a := range actions {
		if !e.stillSyncing(f.ID, domainAtStart) {
			log.Printf("%s: folder removed or device changed - pass stopped", f.RemotePath)
			return
		}
		// As reconcile(): a dropped link ends the pass; what's left keeps
		// its baseline and goes on reconnect.
		if !e.ws.IsConnected() {
			removeEmptiedDirs(emptied, f.LocalPath)
			for _, rest := range actions[i:] {
				if prior, ok := last[rest.relative]; ok {
					newSynced[rest.relative] = prior
				} else {
					delete(newSynced, rest.relative)
				}
			}
			e.mu.Lock()
			e.lastSynced[f.ID] = newSynced
			e.mu.Unlock()
			e.saveSyncedFor(f.ID, saveDevice, meta, newSynced)
			e.setRemoteState(f.ID, msgState(i18n.DeskMainFolderOffline()))

			return
		}
		e.setRemoteState(f.ID, FolderState{Kind: StateScanning, Progress: float64(max(bytesDone, 0)) / float64(totalBytes), CurrentFile: fmt.Sprintf("%d/%d · %s", alreadyAgree+i+1, folderCount, baseName(a.relative))})
		e.saveHashCacheEvery(f.ID)
		bytesDone += bytesOf(a.relative)
		if !safeRelative(a.relative) {
			// Never reached from the listing (filtered above); a record
			// written by an older version could still carry such a path.
			delete(newSynced, a.relative)

			continue
		}
		localPath := localPathOf(a.relative)
		remotePath := remotePathOf(a.relative)
		acted[localPath] = true
		// The device's file must still be what the listing said when an
		// upload replaces it or a delete removes it (deviceStillAsListed).
		listed := remoteByRel[a.relative]
		asListed := func() error { return e.deviceStillAsListed(remotePath, listed) }
		transfer := a.kind != actDeleteLocal && a.kind != actDeleteRemote
		var err error
		// A transfer that failed for a reason of its own waits a while
		// (fail.go), and still counts as not synced.
		if kind, waiting := e.failWait(localPath); transfer && waiting {
			failures.add(kind)
			if prior, ok := last[a.relative]; ok {
				newSynced[a.relative] = prior
			} else {
				delete(newSynced, a.relative)
			}

			continue
		}
		switch a.kind {
		case actUpload:
			fi, statErr := os.Stat(localPath)
			if statErr != nil {
				err = statErr
			} else {
				// The stat the hash goes with: the plan's (a file changed
				// since is a busy file, not sent), or now for one hashed now.
				if a.hash == "" {
					a.hash, err = e.cachedHashInfo(f.ID, localPath, fi)
				} else if seen, ok := localInfo[a.relative]; ok {
					fi = seen
				}
				// Hashing a new file takes a while: the same check as the
				// loop's, so nothing goes to a device this folder left.
				if err == nil && !e.stillSyncing(f.ID, domainAtStart) {
					log.Printf("%s: folder removed or device changed - pass stopped", f.RemotePath)

					return
				}
				if err == nil {
					err = e.uploadChecked(localPath, remotePath, a.hash, fi, asListed)
				}
				if err == nil {
					newSynced[a.relative] = a.hash
				}
			}
		case actDownload:
			err = e.downloadIf(remotePath, localPath, a.hash, f.ID, func() error { return stillAsScanned(a.relative, localPath) })
		case actDownloadKeepLocal:
			// The local version first, under its conflict name; only then
			// the device's version over the original name. The rename takes
			// whatever is there now, edits included; only a file created
			// at the name during the download could still be overwritten.
			copyPath := conflictPath(localPath, hostLabel(), time.Now())
			if err = os.Rename(localPath, copyPath); err == nil {
				log.Printf("conflict on %s: this computer's version kept as %s", a.relative, filepath.Base(copyPath))
				err = e.downloadIf(remotePath, localPath, a.hash, f.ID, absent(localPath))
			}
		case actUploadKeepRemote:
			localHash, remoteHash, _ := strings.Cut(a.hash, "\x00")
			// A busy file is left as it is before a copy is made for it, and
			// a copy of the device's version an earlier pass kept (its
			// upload failed) is the one kept: each pass used to download
			// another.
			if e.busyWait(localPath) {
				err = errBusyFile

				break
			}
			copyPath, kept := e.conflictCopyOf(f.ID, localPath, remoteHash)
			if !kept {
				copyPath = conflictPath(localPath, "", time.Now())
				if err = e.downloadIf(remotePath, copyPath, remoteHash, f.ID, absent(copyPath)); err == nil {
					log.Printf("conflict on %s: the other version kept as %s", a.relative, filepath.Base(copyPath))
				}
			}
			if err == nil {
				if !e.stillSyncing(f.ID, domainAtStart) {
					log.Printf("%s: folder removed or device changed - pass stopped", f.RemotePath)

					return
				}
				fi, ok := localInfo[a.relative] // the stat localHash goes with
				if !ok {
					fi, err = os.Stat(localPath)
				}
				if err == nil {
					err = e.uploadChecked(localPath, remotePath, localHash, fi, asListed)
				}
			}
		case actDeleteRemote:
			// Still nothing here by that very name, right before it goes: a
			// file put back during the pass, or anything the scan doesn't
			// sync (a link, a hidden file) - left for the next pass. Only
			// that name: on a disk that ignores case, the file the old name
			// finds after a rename made here only in case is the new name,
			// whose upload goes with this delete (presentAsNamed).
			if here, herr := presentAsNamed(f.LocalPath, a.relative); herr != nil {
				err = herr
			} else if here {
				err = errChangedHere
			}
			if err == nil {
				err = asListed()
			}
			if err == nil {
				err = e.deleteRemote(remotePath)
			}
			if errors.Is(err, errKeptOnDevice) {
				// The folder is upload only on the device: the file stays
				// there, and stays deleted here (recordBaselines).
				newSynced[a.relative] = keptOnDevice(a.hash)
				err = nil
			}
		case actDeleteLocal:
			// To the trash, never deleted outright (trash.go).
			if err = stillAsScanned(a.relative, localPath); err == nil {
				err = moveToTrash(localPath, f.LocalPath)
			}
			if err == nil {
				emptied[filepath.Dir(localPath)] = true
			}
		}
		if err != nil {
			// Back to the baseline for this path so a transient failure is
			// retried next pass instead of being taken as "agrees".
			if prior, ok := last[a.relative]; ok {
				newSynced[a.relative] = prior
			} else {
				delete(newSynced, a.relative)
			}
			switch {
			case transient(err):
				log.Printf("%s %v", a.relative, err)
			case transfer:
				failures.add(failKindOf(err))
				e.noteFailed(localPath, err)
			default:
				failures.add(failKindOf(err))
				log.Printf("error syncing %s: %v", a.relative, err)
			}
		} else if transfer {
			e.noteSynced(localPath)
		}
	}
	e.forgetFailedUnder(f.LocalPath, acted)

	if n := removeEmptiedDirs(emptied, f.LocalPath); n > 0 {
		log.Printf("%s: removed %d folder(s) left empty by deletions on the device", f.RemotePath, n)
	}
	pruneFolderTrashes(f.LocalPath, meta.Mounts, time.Now())
	e.mu.Lock()
	e.lastSynced[f.ID] = newSynced
	e.mu.Unlock()
	e.saveSyncedFor(f.ID, saveDevice, meta, newSynced)
	if wasMissing {
		// Back after it was missing: its watcher went with it.
		e.startRemoteWatcher(f)
	}
	// The pass ends saying what is left undone, the most pressing first:
	// deletes held here, then files it couldn't sync (no room for them
	// here first). Files under a directory that could not be read are
	// in unreadable (see above) and counted here. Such a directory with
	// nothing synced under it - lost+found at a mount's root, System Volume
	// Information at a drive's - is only logged (enumerateFiles), as on the
	// Mac: it would otherwise hold the folder in an error for good.
	switch {
	case heldNote.Kind != "":
		e.setRemoteState(f.ID, heldNote)
	case waitNote.Kind != "":
		e.setRemoteState(f.ID, waitNote)
	case failures.total() > 0:
		e.setRemoteState(f.ID, failures.note())
	case len(unreadable) > 0:
		e.setRemoteState(f.ID, noteState(
			fmt.Sprintf("%d file(s) could not be read", len(unreadable)), // i18n-ignore: state.json's English, for the command line; Msg is the tray's
			i18n.DeskMainFilesUnreadable(len(unreadable))))
	case guardNote.Kind != "":
		e.setRemoteState(f.ID, guardNote)
	case leftOnDevice > 0:
		e.setRemoteState(f.ID, noteState(
			fmt.Sprintf("%d file(s) are links or hidden here - left as they are on the device", leftOnDevice), // i18n-ignore: state.json's English, for the command line; Msg is the tray's
			i18n.DeskMainFilesNotSyncedHere(leftOnDevice)))
	case unusable > 0:
		e.setRemoteState(f.ID, noteState(
			fmt.Sprintf("%d file(s) on the device have names Windows can't use - not synced here", unusable), // i18n-ignore: state.json's English, for the command line; Msg is the tray's
			i18n.DeskMainNamesNotAllowed(unusable)))
	default:
		e.setRemoteState(f.ID, FolderState{Kind: StateWatching})
	}
}

// holdMassDeviceDeletes is the device-side mass-deletion guard: the paths
// whose deletes on the device the pass holds - every planned one whose
// content isn't here under another name, when there are more than
// massDeleteMin of them and more than a quarter of the files the device
// lists - or none. Files new here are hashed for it only when the count
// says it may hold (moves don't count, and only their hashes tell).
func (e *Engine) holdMassDeviceDeletes(f config.RemoteFolder, actions []action, localByRel, localHashes map[string]string,
	newLocal map[string]bool, remoteByRel map[string]*pb.File) map[string]bool {
	deviceFiles := max(len(remoteByRel), 1)
	over := func(n int) bool { return n > massDeleteMin && n*4 > deviceFiles }
	deletes := 0
	for _, a := range actions {
		if a.kind == actDeleteRemote {
			deletes++
		}
	}
	if !over(deletes) {
		return nil
	}
	here := map[string]bool{}
	for key, p := range localByRel {
		h, ok := localHashes[key]
		if !ok {
			if !newLocal[key] {
				continue // could not be read
			}
			var err error
			if h, err = e.cachedHash(f.ID, p); err != nil {
				continue
			}
		}
		here[h] = true
	}
	held := map[string]bool{}
	for _, a := range actions {
		if a.kind == actDeleteRemote && !here[a.hash] {
			held[a.relative] = true
		}
	}
	if !over(len(held)) {
		return nil
	}

	return held
}

// markFolder writes the folder's marker (rootid.go); a folder that can't
// take it is checked by its files at each pass instead, and said once.
func (e *Engine) markFolder(f config.RemoteFolder, token string) {
	err := writeMarker(f.LocalPath, f.ID, token)
	e.mu.Lock()
	if e.markFailed == nil {
		e.markFailed = map[string]bool{}
	}
	first := err != nil && !e.markFailed[f.ID]
	if err != nil {
		e.markFailed[f.ID] = true
	} else {
		delete(e.markFailed, f.ID)
	}
	e.mu.Unlock()
	if first {
		log.Printf("could not mark %s as the folder otc-sync syncs (%v) - its files are checked again at each pass instead", f.LocalPath, err)
	}
}

// noteUnconfirmed logs how many changes of a two-way folder wait for the
// device's pin to be confirmed - when that changes, not at every pass.
func (e *Engine) noteUnconfirmed(f config.RemoteFolder, n int) {
	e.mu.Lock()
	if e.unconfirmed == nil {
		e.unconfirmed = map[string]int{}
	}
	changed := e.unconfirmed[f.ID] != n
	if n == 0 {
		delete(e.unconfirmed, f.ID)
	} else {
		e.unconfirmed[f.ID] = n
	}
	e.mu.Unlock()
	if changed && n > 0 {
		log.Printf("%s: the device hasn't confirmed it is the one this folder was synced with (its home-network certificate) - %d delete(s) or overwrite(s) wait until it has", f.RemotePath, n)
	}
}

// noteMassHeld logs how many deletes the device-side mass-deletion guard
// holds for a two-way folder - when that changes, not at every pass.
func (e *Engine) noteMassHeld(f config.RemoteFolder, n int) {
	e.mu.Lock()
	if e.massHeld == nil {
		e.massHeld = map[string]int{}
	}
	changed := e.massHeld[f.ID] != n
	if n == 0 {
		delete(e.massHeld, f.ID)
	} else {
		e.massHeld[f.ID] = n
	}
	e.mu.Unlock()
	if changed && n > 0 {
		log.Printf("%s: %d files gone from here at once - not deleted on the device, which keeps them (deleted there too, they are gone everywhere)", f.RemotePath, n)
	}
}

// noteGoneMounts logs the folders mounted inside a two-way folder that are
// not mounted now (mounts.go) - when that changes, not at every pass.
func (e *Engine) noteGoneMounts(f config.RemoteFolder, gone []string) {
	now := strings.Join(gone, "\x00")
	e.mu.Lock()
	if e.goneMountsSeen == nil {
		e.goneMountsSeen = map[string]string{}
	}
	changed := e.goneMountsSeen[f.ID] != now
	if now == "" {
		delete(e.goneMountsSeen, f.ID)
	} else {
		e.goneMountsSeen[f.ID] = now
	}
	e.mu.Unlock()
	if changed && now != "" {
		log.Printf("%s: nothing is mounted on %s now (a disk or share not connected?) - what was synced under it is left as it is on both sides until it is back", f.LocalPath, strings.Join(gone, ", "))
	}
}

// localView is what a two-way pass sees of its folder here (lookHere).
type localView struct {
	folderScan
	// byRel is the files by key (pathKey of the path below the folder).
	byRel map[string]string
	// failed, heldMounts and skipped are the scan's unreadable folders,
	// the mounts the record lists that aren't now (mounts.go) and what it
	// skipped, by key; goneDirs and mounts are mounts.go's.
	failed, heldMounts, skipped pathSet
	goneDirs, mounts            []string
	// maybe is maybeMounts by key: folders held as maybe a disk or share
	// not mounted (mounts.go).
	maybe       pathSet
	maybeMounts []string
}

// unknown: rel is under a folder that could not be read, or under a disk
// or share mounted in the folder that isn't now - unknown here rather than
// gone, so nothing is done for it either way.
func (lv localView) unknown(rel string) bool { return lv.failed.covers(rel) || lv.heldMounts.covers(rel) }

// unknownDirs are the folders (relative, slash-separated) what is under is
// unknown here, not gone: those that could not be read and the mounts that
// aren't now, known or maybe.
func (lv localView) unknownDirs() []string {
	return append(append([]string(nil), lv.folderScan.failed...), lv.heldDirs()...)
}

// heldDirs are the mounts that aren't now, known (goneDirs) or maybe.
func (lv localView) heldDirs() []string {
	held := append(append([]string(nil), lv.goneDirs...), lv.maybeMounts...)
	sort.Strings(held)

	return held
}

// lookHere scans the folder (scanFolder) and keys what it found; meta is
// what the record says of the folder, record the record of this device's
// files and written when its file was written (maybeMounts).
func (e *Engine) lookHere(f config.RemoteFolder, meta folderMeta, record map[string]string, written time.Time) (localView, error) {
	scan, err := scanFolder(f.LocalPath)
	if err != nil {
		return localView{}, err
	}
	lv := localView{folderScan: scan, byRel: map[string]string{}, failed: pathSet{}, heldMounts: pathSet{}, skipped: pathSet{}, maybe: pathSet{}}
	twins := 0
	for _, p := range scan.files {
		rel, err := filepath.Rel(f.LocalPath, p)
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		key := pathKey(rel)
		if prev, dup := lv.byRel[key]; dup {
			// Two files here whose names differ only in normalization
			// (Linux and Windows keep both): the NFC one is synced, the
			// other left alone.
			twins++
			if prevRel, _ := filepath.Rel(f.LocalPath, prev); filepath.ToSlash(prevRel) == key {
				continue
			}
		}
		lv.byRel[key] = p
	}
	if twins > 0 {
		log.Printf("two-way %s: %d name(s) here twice, in two Unicode normalizations - one of each is synced", f.RemotePath, twins)
	}
	for _, d := range scan.failed {
		lv.failed[pathKey(d)] = true
	}
	lv.goneDirs, lv.mounts = goneMounts(f.LocalPath, meta.Mounts, scan.mounts)
	for _, m := range lv.goneDirs {
		lv.heldMounts[pathKey(m)] = true
	}
	// What is here but isn't synced (folderScan.skipped): a file synced
	// before that is now a link, or under one, or hidden on Windows, is
	// still here - its copy on the device stays, nothing comes down over
	// it, and it is counted in the folder's note.
	for _, s := range scan.skipped {
		lv.skipped[pathKey(s)] = true
	}
	lv.maybeMounts = maybeMounts(f.LocalPath, meta, record, lv.byRel,
		func(rel string) bool { return lv.unknown(rel) || lv.skipped.covers(rel) }, written)
	for _, m := range lv.maybeMounts {
		lv.maybe[m] = true
	}

	return lv, nil
}

// identifyFolder is judgeRoot for this pass: what the marker file says,
// and when that isn't enough, the hashes of the files the record lists that
// are here (local, by key), leaving out those unknown here. It hashes only
// as far as the answer needs, showing the files it checks.
func (e *Engine) identifyFolder(f config.RemoteFolder, local map[string]string, recorded map[string]string, meta folderMeta,
	unknown func(string) bool) rootVerdict {
	marker := readMarker(f.LocalPath, f.ID)
	if meta.Root != "" && marker == meta.Root {
		return rootVerdict{ok: true, token: meta.Root}
	}
	listed := 0
	var present []string
	for rel, entry := range recorded {
		if strings.HasPrefix(entry, keptOnDevicePrefix) || unknown(rel) {
			continue
		}
		listed++
		if _, ok := local[rel]; ok {
			present = append(present, rel)
		}
	}
	sort.Strings(present)
	here := map[string]string{}
	unchanged := 0
	var lastShown time.Time
	for i, rel := range present {
		// Settled either way: at least half unchanged, or too few left.
		if unchanged*2 >= listed || (unchanged+len(present)-i)*2 < listed {
			break
		}
		p := local[rel]
		if time.Since(lastShown) > time.Second {
			lastShown = time.Now()
			e.setRemoteState(f.ID, checkingState(i+1, len(present), filepath.Base(p)))
		}
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if h, err := e.cachedHashInfo(f.ID, p, fi); err == nil {
			here[rel] = h
			if h == recorded[rel] {
				unchanged++
			}
		}
	}

	return judgeRoot(meta.Root, marker, recorded, here, unknown)
}

// movedOnDevice: the hashes of the files the device lists at paths the
// record doesn't have - new there, or a file moved or renamed there.
func movedOnDevice(remote map[string]*pb.File, last map[string]string) map[string]bool {
	moved := map[string]bool{}
	for rel, rf := range remote {
		if _, had := last[rel]; !had && rf.Hash != "" {
			moved[rf.Hash] = true
		}
	}

	return moved
}

// errChangedOnDevice: the device's file is no longer what the pass's
// listing said - edited, replaced or created there meanwhile (a phone, the
// web, another computer). The upload or delete is left for the next pass,
// which sees both sides changed and keeps both versions (a conflict copy).
var errChangedOnDevice = errors.New("changed on the device during the pass - left for the next pass") // i18n-ignore: a file's error, only logged

// deviceStillAsListed asks the device, right before an upload replaces its
// file or a delete removes it, what it has at remotePath now, and says
// whether that is still what the pass's listing said (listed; nil when it
// listed nothing there). The listing can be hours old by the time a long
// pass reaches a file, and an upload (ForceOverride) or a delete acted on
// it as it was: a version made on a phone meanwhile was lost. A listing of
// the one path is a few rows from the device's database - never the file's
// content - and says "nothing there" plainly, which ReadFile can't.
func (e *Engine) deviceStillAsListed(remotePath string, listed *pb.File) error {
	resp, err := e.requestWithin(smallRequest, func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqListFiles{ReqListFiles: &pb.ListFiles{Path: remotePath, Recursive: false}}
	})
	if err != nil {
		return err
	}
	if err := wsclient.RespError(resp, "listing refused"); err != nil { // i18n-ignore: a file's error, only logged
		return err
	}
	var now *pb.File
	if lof, ok := resp.Payload.(*pb.RespEnvelope_RespListOfFiles); ok {
		for _, rf := range lof.RespListOfFiles.Files {
			if rf.Path == remotePath && rf.Mime != "inode/directory" {
				now = rf

				break
			}
		}
	}
	switch {
	case listed == nil && now == nil:
		return nil
	case listed == nil || now == nil || now.Hash != listed.Hash:
		return errChangedOnDevice
	}

	return nil
}

// localRootPresent: the folder's own folder is there, and whether it was
// missing at the pass before. When it isn't, the folder says "Folder not
// found - reconnect the drive" and waits, untouched on both sides: its
// watcher is stopped, and the pass that finds the folder again starts a
// new one. remote says which kind the id is.
func (e *Engine) localRootPresent(id, root string, remote bool) (present, wasMissing bool) {
	fi, err := os.Stat(root)
	if err == nil && fi.IsDir() {
		e.mu.Lock()
		wasMissing = e.missingRoot[id]
		delete(e.missingRoot, id)
		e.mu.Unlock()
		if wasMissing {
			log.Printf("the folder %s is back - syncing it again", root)
		}

		return true, wasMissing
	}
	e.mu.Lock()
	if e.missingRoot == nil {
		e.missingRoot = map[string]bool{}
	}
	first := !e.missingRoot[id]
	e.missingRoot[id] = true
	watchers := e.watchers
	if remote {
		watchers = e.remoteWatch
	}
	if w := watchers[id]; w != nil {
		w.Stop()
		delete(watchers, id)
	}
	e.mu.Unlock()
	if first {
		log.Printf("the folder %s is not there (a disk unplugged, a share not mounted, a folder moved?) - waiting for it, nothing changed on either side", root)
	}
	state := msgState(i18n.DeskMainFolderNotFound())
	if remote {
		e.setRemoteState(id, state)
	} else {
		e.setFolderState(id, state)
	}

	return false, false
}

// uploadOnlyAwaited: folder id asked to be made upload only (issue #132)
// and the device hasn't acknowledged it yet - a request that failed, or a
// device that can't until it is updated.
func (e *Engine) uploadOnlyAwaited(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	pending, _, _ := e.pendingLocked(uploadOnlyRequest, id)

	return pending != nil && *pending
}

// noteHeldDeletes logs how many deletes a two-way folder holds until it is
// upload only on the device - when that changes, not at every pass.
func (e *Engine) noteHeldDeletes(f config.RemoteFolder, held int) {
	e.mu.Lock()
	changed := e.heldDeletes[f.ID] != held
	if held == 0 {
		delete(e.heldDeletes, f.ID)
	} else {
		e.heldDeletes[f.ID] = held
	}
	e.mu.Unlock()
	if changed && held > 0 {
		log.Printf("%s: %d delete(s) made here wait until the device has made the folder upload only", f.RemotePath, held)
	}
}

// errChangedHere: a planned overwrite or delete found the local file no
// longer as the pass saw it (see stillAsScanned).
var errChangedHere = errors.New("changed on this computer during the pass - left for the next pass") // i18n-ignore: a file's error, only logged

// massDeleteMin: two-way folders - more local deletions than this in one
// pass (and a quarter of the folder) mean the device lost the files.
const massDeleteMin = 20

func (e *Engine) startRemoteWatcher(f config.RemoteFolder) {
	e.mu.Lock()
	// Not on a folder that isn't the synced one (rootid.go): it would go on
	// watching it once the synced one is mounted over it.
	if _, ok := e.remoteWatch[f.ID]; ok || e.stopped || !e.remoteConfiguredLocked(f.ID) || e.replacedRoot[f.ID] {
		e.mu.Unlock()

		return
	}
	e.mu.Unlock()
	if fi, err := os.Stat(f.LocalPath); err != nil || !fi.IsDir() {
		return // not there (localRootPresent): its pass starts the watch once it is back
	}
	changed := func(string, bool, bool) {
		// Whole-folder debounce: one reconcile handles a batch of changes.
		e.mu.Lock()
		if t := e.remoteDeb[f.ID]; t != nil {
			t.Stop()
		}
		e.remoteDeb[f.ID] = time.AfterFunc(debounceInterval, func() {
			e.mu.Lock()
			delete(e.remoteDeb, f.ID)
			var current *config.RemoteFolder
			for _, rf := range e.cfg.RemoteFolders {
				if rf.ID == f.ID {
					c := rf
					current = &c
				}
			}
			e.mu.Unlock()
			if current != nil {
				e.reconcileRemoteFolder(*current)
			}
		})
		e.mu.Unlock()
	}
	w, err := NewWatcher(f.LocalPath, changed, func() { changed("", true, false) })
	if err != nil {
		e.setRemoteState(f.ID, msgState(i18n.DeskMainCannotWatch(err.Error())))

		return
	}
	e.mu.Lock()
	// As startWatcher: removed meanwhile (the pass's own safety guard does
	// that), or already watched - never keep an orphan watcher.
	if e.stopped || e.remoteWatch[f.ID] != nil || !e.remoteConfiguredLocked(f.ID) {
		e.mu.Unlock()
		w.Stop()

		return
	}
	e.remoteWatch[f.ID] = w
	e.mu.Unlock()
}

func (e *Engine) scheduleRemoteRetry(f config.RemoteFolder) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped || !e.remoteConfiguredLocked(f.ID) {
		return
	}
	if t := e.remoteRetry[f.ID]; t != nil {
		t.Stop()
	}
	e.remoteRetry[f.ID] = time.AfterFunc(errorRetryInterval, func() {
		e.mu.Lock()
		delete(e.remoteRetry, f.ID)
		e.mu.Unlock()
		e.reconcileRemoteFolder(f)
	})
}

// ---- wire helpers --------------------------------------------------------

// upload is hash-first (issue #58): link when the device already has the
// content, send the bytes otherwise.
func (e *Engine) upload(path, remotePath, hash string, fi os.FileInfo) error {
	return e.uploadChecked(path, remotePath, hash, fi, nil)
}

// uploadChecked is upload with a last look at the device: check, when
// given, runs right before the content becomes the device's file - before
// LinkFile, before FinishUpload, after the bytes went up - and an error
// from it leaves the device's file as it is (deviceStillAsListed).
func (e *Engine) uploadChecked(path, remotePath, hash string, fi os.FileInfo, check func() error) error {
	// A file that changed while it was being sent waits a while (busy.go).
	if e.busyWait(path) {
		return errBusyFile
	}
	has, err := e.requestWithin(smallRequest, func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqHasFile{ReqHasFile: &pb.HasFile{Hash: hash}}
	})
	if err != nil {
		return err
	}
	// Issue #134 (as SyncModel.upload): the file's own dates go with it -
	// its creation time where the platform has one (times_*.go), its
	// modification time - so the same file synced down elsewhere gets the
	// same dates.
	created, modified := timestamppb.Now(), timestamppb.Now()
	if fi != nil {
		created = timestamppb.New(creationTime(path, fi))
		modified = timestamppb.New(fi.ModTime())
	}
	if fe, ok := has.Payload.(*pb.RespEnvelope_RespFileExists); ok && fe.RespFileExists.Exists {
		if check != nil {
			if err := check(); err != nil {
				return err
			}
		}
		resp, err := e.requestWithin(smallRequest, func(r *pb.ReqEnvelope) {
			r.Payload = &pb.ReqEnvelope_ReqLinkFile{ReqLinkFile: &pb.LinkFile{Hash: hash, Path: remotePath, ForceOverride: true, Created: created, Modified: modified}}
		})
		if err != nil {
			return err
		}

		return wsclient.RespError(resp, "link rejected") // i18n-ignore: a file's error, only logged
	}
	err = e.uploadChunked(path, remotePath, hash, fi, created, modified, check)
	if err == nil {
		e.noteSent(path)
	}

	return err
}

// cChunk is how much of a file one message carries each way (the device's
// MaxChunk): no message, and no memory here or on the device, ever holds
// a whole file (issue #168).
const cChunk = 4 << 20

// expectedHash is the hash the listing gave for this path: the content
// is checked against it before anything touches the disk (as
// SyncModel.download: a device whose blob had gone missing used to answer
// with empty content and no error, and the 0-byte file that made went
// back up over the device's row on the next pass).
func (e *Engine) download(remotePath, dest, expectedHash string) error {
	return e.downloadIf(remotePath, dest, expectedHash, "", nil)
}

// downloadIf is download with a last check: ready, when given, runs once
// the content is here and verified, just before it takes dest's place,
// and an error from it leaves dest as it is. With a folderID, the hash of
// what was written goes in that folder's hash cache.
func (e *Engine) downloadIf(remotePath, dest, expectedHash, folderID string, ready func() error) error {
	// Issue #168: ReadFile, in pieces, and the file's original bytes -
	// GetFile turns a HEIC into a JPEG for viewers, so what came back never
	// matched the listed hash: never written, fetched again every pass,
	// each time a full HEIC decode on the Pi. Written to the disk and
	// hashed as it arrives, never whole in memory.
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil { // perms: rwxr-xr-x
		return err
	}
	// A name of its own ("photo.jpg.123456.otc-part"): two passes writing
	// the same file - folders that overlap, kept from before that was
	// refused - each wrote into the other's "photo.jpg.otc-part" and
	// checked the hash of what it had received, not of what was on the
	// disk.
	out, err := os.CreateTemp(filepath.Dir(dest), filepath.Base(dest)+".*.otc-part")
	if err != nil {
		return err
	}
	tmp := out.Name()
	keep := false
	defer func() {
		out.Close()
		if !keep {
			os.Remove(tmp)
		}
	}()

	h := sha256.New()
	var offset, size int64 = 0, -1
	var last *pb.FileChunk
	for size < 0 || offset < size {
		at := offset
		resp, err := e.requestWithin(transferRequest, func(r *pb.ReqEnvelope) {
			r.Payload = &pb.ReqEnvelope_ReqReadFile{ReqReadFile: &pb.ReadFile{Path: remotePath, Offset: at, Length: cChunk}}
		})
		if err != nil {
			return err
		}
		if err := wsclient.RespError(resp, "download rejected"); err != nil { // i18n-ignore: a file's error, only logged
			return err
		}
		c, ok := resp.Payload.(*pb.RespEnvelope_RespFileChunk)
		if !ok {
			return errors.New("unexpected response") // i18n-ignore: a file's error, only logged
		}
		last = c.RespFileChunk
		if expectedHash != "" && last.Hash != "" && last.Hash != expectedHash {
			return errors.New("the file changed on the device while downloading - will retry") // i18n-ignore: a file's error, only logged
		}
		// Its size known: refused before anything is written when this
		// disk can't take it (fail.go) - it used to fill the disk, fail,
		// free it and start again at every pass.
		if at == 0 {
			if err := roomFor(filepath.Dir(dest), last.Size); err != nil {
				return err
			}
		}
		size = last.Size
		if len(last.Data) == 0 && offset < size {
			return errors.New("the device sent an empty piece of the file") // i18n-ignore: a file's error, only logged
		}
		if _, err := out.Write(last.Data); err != nil {
			return err
		}
		h.Write(last.Data)
		offset += int64(len(last.Data))
	}
	// Checked before anything takes the file's place (a device whose blob
	// had gone missing used to answer with empty content, and the 0-byte
	// file that made went back up over the device's row on the next pass).
	got := hex.EncodeToString(h.Sum(nil))
	if expectedHash != "" && got != expectedHash {
		return fmt.Errorf("the device sent %d bytes that don't match the file's hash - not written", offset) // i18n-ignore: a file's error, only logged
	}
	// On the disk before it takes the file's place: a power cut after the
	// rename must not leave an empty or partial file under the real name,
	// which the next pass would send back over the device's.
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	// CreateTemp made it 0600: a downloaded file reads like any other.
	if err := os.Chmod(tmp, 0o644); err != nil { // perms: rw-r--r--
		return err
	}
	if ready != nil {
		if err := ready(); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	keep = true
	config.SyncDir(filepath.Dir(dest))
	// Issue #134 (as SyncModel.download): the file keeps the dates it has
	// on the device. Creation time only where the platform lets it be set.
	if last != nil && last.Modified != nil {
		created := time.Time{}
		if last.Created != nil {
			created = last.Created.AsTime()
		}
		modified := last.Modified.AsTime()
		if err := setFileTimes(dest, created, modified); err != nil {
			log.Printf("could not set the dates of %s: %v", dest, err)
		} else if folderID != "" {
			e.cacheDownloadedHash(folderID, dest, offset, modified, got)
		}
	}

	return nil
}

// cacheDownloadedHash records the hash of a file just downloaded, so the
// next pass (the folder's own watcher starts one a second later) doesn't
// read it all again. Only when the file still has exactly the size written
// and the date just set: anything written to it since moved its date on.
func (e *Engine) cacheDownloadedHash(folderID, p string, size int64, modified time.Time, hash string) {
	fi, err := os.Stat(p)
	if err != nil || fi.Size() != size || !fi.ModTime().Equal(modified) {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.loadHashCacheLocked(folderID)
	if e.hashCache[folderID] == nil {
		e.hashCache[folderID] = map[string]hashEntry{}
	}
	e.hashCache[folderID][p] = hashEntry{size: fi.Size(), modTime: fi.ModTime(), ctime: changeTime(fi), hash: hash}
	e.hashDirty[folderID] = true
}

// markUploadOnly makes a backup's folder on the device upload only (issue
// #132): the device then refuses deletes there and keeps the older version
// of a file when it changes. Sent at every start, so backups added before
// this get it too; harmless when already set. As SyncModel.markUploadOnly.
func (e *Engine) markUploadOnly(f config.Folder) {
	e.mu.Lock()
	domain := e.cfg.Domain
	e.mu.Unlock()
	resp, err := e.requestWithin(smallRequest, func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqSetUploadOnly{ReqSetUploadOnly: &pb.SetUploadOnly{Path: e.remotePathFor(f.Path) + "/", UploadOnly: true}}
	})
	if err == nil {
		err = wsclient.RespError(resp, "upload only refused") // i18n-ignore: only logged
	}
	e.mu.Lock()
	logIt := false
	if e.backupConfiguredLocked(f.ID) && e.cfg.Domain == domain {
		if err == nil {
			e.uploadOnlyOK[f.ID] = true
			delete(e.uploadOnlyErr, f.ID)
		} else if e.uploadOnlyErr[f.ID] != err.Error() {
			// Once per error, not every pass: a device older than #132
			// refuses every attempt the same way.
			e.uploadOnlyErr[f.ID] = err.Error()
			logIt = true
		}
	}
	e.mu.Unlock()
	if logIt {
		log.Printf("could not make backup %s upload only on the device: %v", f.Path, err)
	}
}

// ensureUploadOnly sends markUploadOnly until the device linked now has
// acknowledged it. It used to be sent once, when the folder was set up: a
// failure there (the link dropping right after connect, a device not yet
// updated) left the backup unprotected - deletes from a phone accepted,
// older versions overwritten - until otc-sync restarted. Now every pass
// and reconnect tries again until it succeeds; after that, nothing more.
func (e *Engine) ensureUploadOnly(f config.Folder) {
	e.mu.Lock()
	done := e.uploadOnlyOK[f.ID]
	e.mu.Unlock()
	if done || !e.ws.IsConnected() {
		return
	}
	e.markUploadOnly(f)
}

func (e *Engine) deleteRemote(remotePath string) error {
	resp, err := e.requestWithin(smallRequest, func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqDelFile{ReqDelFile: &pb.DelFile{Path: remotePath}}
	})
	if err != nil {
		return err
	}
	// Issue #132: the owner made that folder upload only, so the device
	// keeps the file however the local copy goes. That is the folder
	// working as intended, not a failure to retry: errKeptOnDevice
	// (SyncModel.delete does the same).
	if resp != nil && resp.Error && resp.ErrorCode == "upload_only" {
		log.Printf("delete of %s skipped: the folder is upload only - kept on the device", remotePath)

		return errKeptOnDevice
	}

	return wsclient.RespError(resp, "delete rejected") // i18n-ignore: a file's error, only logged
}

// errKeptOnDevice: the device refused a delete because the folder is
// upload only there (error_code "upload_only").
var errKeptOnDevice = errors.New("kept on the device: the folder is upload only") // i18n-ignore: an outcome the pass records, never shown

// remotePathFor is SyncModel.remotePathFor: "/mac/<host><path>" there;
// "/linux/<host><path>" and "/windows/<host>/C/Users/..." here, so a device
// used from several computers keeps them apart.
func (e *Engine) remotePathFor(path string) string {
	host := strings.NewReplacer("/", "-", ":", "-", " ", "_", "\\", "-").Replace(e.hostname)
	p := filepath.ToSlash(filepath.Clean(path))
	if runtime.GOOS == "windows" {
		// C:/Users/x -> /C/Users/x
		p = strings.ReplaceAll(p, ":", "")
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
	}

	return "/" + runtime.GOOS + "/" + host + strings.TrimSuffix(p, "/")
}

// DevicePathFor is where this computer's folder at path is synced on the
// device (remotePathFor): the command line and the tray check a folder
// being added against those already synced there (config.FolderOverlap).
func DevicePathFor(path string) string {
	host, _ := os.Hostname()
	if host == "" {
		host = "PC"
	}

	return (&Engine{hostname: host}).remotePathFor(path)
}

// safeRelative: rel (slash-separated, from the device) names a file inside
// the synced folder - not empty, no "", "." or ".." component, and nothing
// this OS reads as absolute, a drive, a "\" separator or a device name
// (filepath.IsLocal). Same rule as SyncModel.safeRelative on the Mac.
func safeRelative(rel string) bool {
	if rel == "" || strings.ContainsRune(rel, 0) {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}

	return filepath.IsLocal(filepath.FromSlash(rel))
}

// removeEmptiedDirs: after a two-way pass, the folders its local
// deletions left empty go too. The device has no empty folders - a folder
// there is only the files under it - so a folder deleted there used to
// leave its whole tree here, empty but "synced". From each folder a
// deleted file was in, up to (not including) root: removed while it holds
// nothing but a Finder .DS_Store, which is never synced. os.Remove only
// removes an empty folder, so a file that arrives meanwhile keeps it. As
// the Mac's SyncPaths.removeEmptiedFolders. Returns how many went.
func removeEmptiedDirs(dirs map[string]bool, root string) int {
	root = filepath.Clean(root)
	starts := make([]string, 0, len(dirs))
	for d := range dirs {
		starts = append(starts, filepath.Clean(d))
	}
	// Deepest first, so a parent is judged after its children went.
	sort.Slice(starts, func(i, j int) bool { return len(starts[i]) > len(starts[j]) })
	removed := 0
	for _, dir := range starts {
		for {
			if rel, err := filepath.Rel(root, dir); err != nil || rel == "." || !filepath.IsLocal(rel) {
				break
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				break
			}
			onlyJunk := true
			for _, en := range entries {
				if en.Name() != ".DS_Store" {
					onlyJunk = false

					break
				}
			}
			if !onlyJunk {
				break
			}
			for _, en := range entries {
				_ = os.Remove(filepath.Join(dir, en.Name()))
			}
			if os.Remove(dir) != nil {
				break
			}
			removed++
			dir = filepath.Dir(dir)
		}
	}

	return removed
}

// enumerateFiles is scanFolder's files and unreadable directories.
func enumerateFiles(root string) (out, failed []string, err error) {
	s, err := scanFolder(root)

	return s.files, s.failed, err
}

// folderScan is what scanFolder found below a folder.
type folderScan struct {
	// files are the regular files to sync (full paths).
	files []string
	// failed are the directories that could not be read (relative to the
	// root, slash-separated): what is under them is unknown, not gone.
	failed []string
	// skipped is what is here but isn't synced, and isn't left out by its
	// name on both sides (skip.go) - a symbolic link, to a file or a folder
	// (the scan never follows one), anything that is neither a regular
	// file nor a folder (on Windows also a junction), and on Windows a file
	// or folder with the Hidden or System attribute - relative to the
	// root, slash-separated. A file synced before that is one of these now,
	// or is under one, is still here: its copy on the device must not be
	// deleted for it.
	skipped []string
	// mounts are the folders below the root that are another file system
	// than their parent - a disk or a share mounted there (mounts.go) -,
	// relative to the root, slash-separated, as the disk spells them.
	mounts []string
}

// scanFolder walks a tree for regular files, leaving out what is never
// synced (skip.go: hidden entries, like the macOS enumerator's
// .skipsHiddenFiles, this client's own partial downloads, Office's and
// Explorer's scratch files), and notes what it skipped for other reasons
// and the file systems mounted below it (folderScan). An unreadable root is
// an error.
func scanFolder(root string) (folderScan, error) {
	var s folderScan
	// Each folder's file system, to find those mounted below the root.
	devs := map[string]uint64{}
	if fi, err := os.Stat(root); err == nil {
		if dev, ok := fileSystemOf(fi); ok {
			devs[root] = dev
		}
	}
	rel := func(p string) string {
		r, _ := filepath.Rel(root, p)

		return filepath.ToSlash(r)
	}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			// Below the root only a directory's ReadDir reports here.
			if rel, ok := unreadableDir(root, p, err); ok {
				if len(s.failed) < 5 {
					log.Printf("cannot read directory %s: %v", rel, err)
				}
				s.failed = append(s.failed, rel)
			}

			return nil
		}
		if p == root {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}

			return nil
		}
		if runtime.GOOS == "windows" {
			if fi, err := d.Info(); err == nil && hiddenAttr(fi) {
				s.skipped = append(s.skipped, rel(p))
				if d.IsDir() {
					return filepath.SkipDir
				}

				return nil
			}
		}
		switch {
		case d.IsDir():
			if parent, ok := devs[filepath.Dir(p)]; ok {
				if fi, err := d.Info(); err == nil {
					if dev, ok := fileSystemOf(fi); ok {
						devs[p] = dev
						if dev != parent {
							s.mounts = append(s.mounts, rel(p))
						}
					}
				}
			}
		case strings.HasSuffix(p, ".otc-part") || skippedName(d.Name()):
			// Never synced, either way, by its name.
		case d.Type().IsRegular():
			s.files = append(s.files, p)
		default:
			s.skipped = append(s.skipped, rel(p))
		}

		return nil
	})

	return s, err
}

// unreadableDir: the directory below root whose read failed with err, as
// enumerateFiles lists it, and whether it counts as one that could not be
// read. One deleted or renamed since its parent was listed does not: it is
// gone, and what was under it is handled as any delete.
func unreadableDir(root, p string, err error) (string, bool) {
	if errors.Is(err, os.ErrNotExist) {
		return "", false
	}
	rel, _ := filepath.Rel(root, p)

	return filepath.ToSlash(rel), true
}

type hashEntry struct {
	size    int64
	modTime time.Time
	// ctime is the change time it was hashed at (changeTime), 0 where it
	// isn't read or for an entry of an older version's cache.
	ctime int64
	hash  string
}

// matches: the entry was hashed from a file that still looks like fi -
// size, modification time and, where both are known, change time. An
// entry from before change times were kept matches on the other two and
// learns the change time (cachedHashInfo).
func (h hashEntry) matches(fi os.FileInfo) bool {
	if h.size != fi.Size() || !h.modTime.Equal(fi.ModTime()) {
		return false
	}
	ct := changeTime(fi)

	return h.ctime == 0 || ct == 0 || h.ctime == ct
}

// cachedHash is sha256File through the per-folder cache.
func (e *Engine) cachedHash(folderID, p string) (string, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return "", err
	}

	return e.cachedHashInfo(folderID, p, fi)
}

// cachedHashInfo is cachedHash with the file's stat already taken by the
// caller, moments before.
func (e *Engine) cachedHashInfo(folderID, p string, fi os.FileInfo) (string, error) {
	if fi == nil {
		return "", fmt.Errorf("cannot read %s", filepath.Base(p)) // i18n-ignore: a file's error, only logged
	}
	e.mu.Lock()
	e.loadHashCacheLocked(folderID)
	hit, ok := e.hashCache[folderID][p]
	if ok && hit.matches(fi) {
		if hit.ctime == 0 {
			if ct := changeTime(fi); ct != 0 {
				hit.ctime = ct
				e.hashCache[folderID][p] = hit
				e.hashDirty[folderID] = true
			}
		}
		e.mu.Unlock()

		return hit.hash, nil
	}
	e.mu.Unlock()
	h, err := sha256File(p)
	if err != nil {
		return "", err
	}
	e.mu.Lock()
	if e.hashCache[folderID] == nil {
		e.hashCache[folderID] = map[string]hashEntry{}
	}
	e.hashCache[folderID][p] = hashEntry{size: fi.Size(), modTime: fi.ModTime(), ctime: changeTime(fi), hash: h}
	e.hashDirty[folderID] = true
	e.mu.Unlock()

	return h, nil
}

func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// Describe is the one-line status the CLI prints.
func Describe(st config.FolderStatus) string {
	switch st.State {
	case string(StateWatching):
		return "synced"
	case string(StateError):
		return "error: " + st.Error
	default:
		if st.CurrentFile != "" && st.Progress == 0 {
			// The hashing pass: "Checking 12/400 · name" (#138).
			return st.CurrentFile
		}
		if st.CurrentFile != "" {
			return fmt.Sprintf("%d%% %s", int(st.Progress*100), st.CurrentFile)
		}

		return "checking…"
	}
}

// conflictPath is where the losing version of a conflicting file is kept:
// "report (conflict from laptop 2026-10-04 20.15).txt" next to it, with a
// number added if that name is taken. from names the computer the version
// comes from, when known. Same naming as SyncModel.conflictURL on the Mac.
func conflictPath(path, from string, at time.Time) string {
	dir, base := filepath.Split(path)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" { // ".bashrc"
		stem, ext = base, ""
	}
	label := "conflict " + at.Format("2006-01-02 15.04")
	if from != "" {
		label = "conflict from " + from + " " + at.Format("2006-01-02 15.04") // i18n-ignore: a file name the app writes, never translated
	}
	candidate := filepath.Join(dir, fmt.Sprintf("%s (%s)%s", stem, label, ext))
	for i := 2; ; i++ {
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
			return candidate
		}
		candidate = filepath.Join(dir, fmt.Sprintf("%s (%s %d)%s", stem, label, i, ext))
	}
}

// conflictCopyRe matches the names conflictPath gives the device's
// version of a file: "name (conflict 2026-11-04 10.16).ext", "… 10.16 2)…".
const conflictCopyRe = ` \(conflict \d{4}-\d{2}-\d{2} \d{2}\.\d{2}( \d+)?\)`

// conflictCopyOf is a copy of the device's version of path (hash) that an
// earlier pass kept next to it, when there is one: that pass's upload of
// the local version failed, and the next one is to reuse it rather than
// download another.
func (e *Engine) conflictCopyOf(folderID, path, hash string) (string, bool) {
	dir, base := filepath.Split(path)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" {
		stem, ext = base, ""
	}
	re, err := regexp.Compile("^" + regexp.QuoteMeta(stem) + conflictCopyRe + regexp.QuoteMeta(ext) + "$")
	if err != nil {
		return "", false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, en := range entries {
		if !en.Type().IsRegular() || !re.MatchString(en.Name()) {
			continue
		}
		p := filepath.Join(dir, en.Name())
		if h, err := e.cachedHash(folderID, p); err == nil && h == hash {
			return p, true
		}
	}

	return "", false
}

// hostLabel is this computer's name for conflict copies.
func hostLabel() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "this computer" // i18n-ignore: part of a conflict copy's file name, never translated
	}
	return strings.TrimSuffix(h, ".local")
}

// backupConfiguredLocked: the backup folder id is still in the config;
// e.mu must be held.
func (e *Engine) backupConfiguredLocked(id string) bool {
	if e.cfg == nil {
		return false
	}
	for _, f := range e.cfg.Folders {
		if f.ID == id {
			return true
		}
	}

	return false
}

// remoteConfiguredLocked: the same for a two-way folder.
func (e *Engine) remoteConfiguredLocked(id string) bool {
	if e.cfg == nil {
		return false
	}
	for _, f := range e.cfg.RemoteFolders {
		if f.ID == id {
			return true
		}
	}

	return false
}

// stillBackingUp: stillSyncing for a backup folder.
func (e *Engine) stillBackingUp(id, domain string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.backupConfiguredLocked(id) && e.cfg.Domain == domain
}

// saveHashCacheIfKept is the end of a pass's saveHashCache - unless the
// folder was removed meanwhile: what the pass cached then goes too, rather
// than bringing back the file the removal deleted.
func (e *Engine) saveHashCacheIfKept(id string) {
	e.mu.Lock()
	kept := e.backupConfiguredLocked(id) || e.remoteConfiguredLocked(id)
	if !kept {
		delete(e.hashCache, id)
		delete(e.hashDirty, id)
		delete(e.hashLoaded, id)
	}
	e.mu.Unlock()
	if kept {
		e.saveHashCache(id)
	}
}

// stillSyncing: the two-way folder id is still configured and the device
// is still the one a pass started on.
func (e *Engine) stillSyncing(id, domain string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cfg.Domain != domain {
		return false
	}
	for _, f := range e.cfg.RemoteFolders {
		if f.ID == id {
			return true
		}
	}
	return false
}
