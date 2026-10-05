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
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/app/desktop/internal/wsclient"
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
	requestTimeout   = 30 * time.Minute
)

// StateKind is FolderState's case.
type StateKind string

const (
	StateScanning StateKind = "scanning"
	StateWatching StateKind = "watching"
	StateError    StateKind = "error"
)

// FolderState is SyncModel.FolderState.
type FolderState struct {
	Kind        StateKind
	Progress    float64
	CurrentFile string
	Message     string
}

// RaidHealth is what the tray icon says about the device's storage.
type RaidHealth string

const (
	RaidOK       RaidHealth = "ok"
	RaidDegraded RaidHealth = "degraded"
	RaidFailed   RaidHealth = "failed"
	RaidUnknown  RaidHealth = "unknown"
)

// Summary is RaidHealth.summary on macOS.
func (r RaidHealth) Summary() string {
	switch r {
	case RaidOK:
		return "Storage healthy"
	case RaidDegraded:
		return "A drive is down - the RAID is degraded"
	case RaidFailed:
		return "The RAID has failed"
	default:
		return "Storage status unknown"
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
	remoteHashes map[string]map[string]string // folder id -> remote path -> hash
	lastSynced   map[string]map[string]string // remote folder id -> relative path -> hash
	savedSynced  map[string]map[string]string // what is on disk (twoway.go)
	watchers     map[string]*Watcher
	remoteWatch  map[string]*Watcher
	debounce     map[string]*time.Timer // per changed path (upload folders)
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
	authRetry    *time.Timer
	// Local content hashes per folder, keyed by path and validated by
	// size + mtime, so the minute-by-minute two-way poll doesn't re-read
	// a whole tree that hasn't changed (see SyncModel's localHashCache).
	hashCache  map[string]map[string]hashEntry
	hashDirty  map[string]bool // folders whose cache changed since it was last saved
	hashLoaded map[string]bool // folders whose cache file has been read
	folderBusy map[string]bool
	// Backups whose SetUploadOnly the device linked now acknowledged, and
	// the last error logged for the others (see ensureUploadOnly).
	uploadOnlyOK  map[string]bool
	uploadOnlyErr map[string]string
	onChange      func()
	hostname      string
	stopped       bool
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
		status:        "Not connected",
		raid:          RaidUnknown,
		folderStates:  map[string]FolderState{},
		remoteStates:  map[string]FolderState{},
		held:          map[string]FolderState{},
		heldTimers:    map[string]*time.Timer{},
		remoteHashes:  map[string]map[string]string{},
		lastSynced:    map[string]map[string]string{},
		savedSynced:   map[string]map[string]string{},
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
		hashCache:     map[string]map[string]hashEntry{},
		hashDirty:     map[string]bool{},
		hashLoaded:    map[string]bool{},
		onChange:      onChange,
		hostname:      host,
	}
	e.migrateFolders(cfg)
	for _, f := range cfg.Folders {
		e.folderStates[f.ID] = FolderState{Kind: StateScanning}
	}
	for _, f := range cfg.RemoteFolders {
		e.remoteStates[f.ID] = FolderState{Kind: StateScanning}
	}
	e.ws.OnConnect = func() {
		e.setStatus("Connected")
		e.startRaidPolling()
		go e.startSync()
	}
	e.ws.OnDisconnect = func(err error) {
		e.mu.Lock()
		wrongPassword := e.status == "Wrong password" || strings.HasPrefix(e.status, "Too many attempts") || e.status == "Device offline - retrying"
		e.mu.Unlock()
		if !wrongPassword {
			e.setStatus("Disconnected")
		}
		e.stopRaidPolling()
	}
	e.ws.OnUnreachable = func(msg string) {
		log.Printf("device unreachable: %s", msg)
		e.setStatus("Device offline - retrying")
	}
	e.ws.OnAuthFailed = func(msg string, retryAfter int) {
		log.Printf("authentication failed: %s", msg)
		if retryAfter <= 0 {
			e.setStatus("Wrong password")

			return
		}
		// Locked out for guessing, not necessarily wrong: say so, and try
		// once more when the lock lifts.
		e.setStatus(fmt.Sprintf("Too many attempts - retrying in %ds", retryAfter))
		e.mu.Lock()
		if e.authRetry != nil {
			e.authRetry.Stop()
		}
		e.authRetry = time.AfterFunc(time.Duration(retryAfter+1)*time.Second, func() {
			e.mu.Lock()
			ready := config.Ready(e.cfg, e.password) && !e.stopped
			e.mu.Unlock()
			if ready {
				e.setStatus("Connecting…")
				e.ws.Connect()
			}
		})
		e.mu.Unlock()
	}

	return e
}

// Start applies the settings: connect when both halves are there.
func (e *Engine) Start() { e.applySettings() }

// Stop closes everything.
func (e *Engine) Stop() {
	e.mu.Lock()
	e.stopped = true
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
	for _, f := range old.RemoteFolders {
		if !keepR[f.ID] {
			e.dropRemoteFolderLocked(f.ID)
		}
	}
	credsChanged := old.Domain != cfg.Domain || oldPw != password
	if credsChanged && e.authRetry != nil {
		e.authRetry.Stop()
		e.authRetry = nil
	}
	if credsChanged {
		// Possibly another device: every backup is marked there again on
		// the reconnect (harmless on the same one).
		e.uploadOnlyOK = map[string]bool{}
		e.uploadOnlyErr = map[string]string{}
	}
	e.mu.Unlock()
	e.notify()
	if credsChanged {
		e.ws.Disconnect()
		e.applySettings()
	} else if e.ws.IsConnected() {
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
	delete(e.remoteStates, id)
	e.dropHashCacheLocked(id)
}

func (e *Engine) applySettings() {
	e.mu.Lock()
	cfg, pw := e.cfg, e.password
	e.mu.Unlock()
	if config.Ready(cfg, pw) {
		e.setStatus("Connecting…")
		e.ws.Configure(cfg.Domain, cfg.ClientID, pw)
		e.ws.Connect()
	} else {
		e.ws.Disconnect()
		e.setStatus("Missing domain/password")
	}
}

// ---- observable state ---------------------------------------------------

func (e *Engine) setStatus(s string) {
	e.mu.Lock()
	e.status = s
	e.mu.Unlock()
	e.notify()
}

func (e *Engine) notify() {
	if e.onChange != nil {
		e.onChange()
	}
}

// Snapshot is what the UI and state.json show.
func (e *Engine) Snapshot() config.State {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := config.State{Status: e.status, Raid: string(e.raid), RaidSummary: e.raid.Summary()}
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
	for _, f := range e.cfg.Folders {
		st.Folders = append(st.Folders, toStatus(f.ID, f.Path, "", e.folderStates[f.ID]))
	}
	for _, f := range e.cfg.RemoteFolders {
		st.RemoteFolders = append(st.RemoteFolders, toStatus(f.ID, f.LocalPath, f.RemotePath, e.remoteStates[f.ID]))
	}

	return st
}

func toStatus(id, path, remote string, s FolderState) config.FolderStatus {
	fs := config.FolderStatus{ID: id, Path: path, RemotePath: remote, State: string(s.Kind), Progress: s.Progress, CurrentFile: s.CurrentFile, Error: s.Message}
	if fs.State == "" {
		fs.State = string(StateScanning)
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
	resp, err := e.request(func(r *pb.ReqEnvelope) {
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
}

// Raid is the current health.
func (e *Engine) Raid() RaidHealth {
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.raid
}

func (e *Engine) request(build func(*pb.ReqEnvelope)) (*pb.RespEnvelope, error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	return e.ws.Request(ctx, build)
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
	if err := wsclient.RespError(resp, "could not list remote files"); err != nil {
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

// ---- orchestration -------------------------------------------------------

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

	for _, f := range folders {
		e.mu.Lock()
		_, watching := e.watchers[f.ID]
		errored := e.folderStates[f.ID].Kind == StateError
		e.mu.Unlock()
		if !watching {
			e.setupFolder(f)
		} else if errored {
			// Left in an error while the link was down (its retry finds
			// no connection and gives up): again now, not in 10 minutes.
			e.reconcile(f)
		} else {
			e.ensureUploadOnly(f)
		}
	}
	for _, f := range remotes {
		e.mu.Lock()
		_, watching := e.remoteWatch[f.ID]
		errored := e.remoteStates[f.ID].Kind == StateError
		e.mu.Unlock()
		if !watching {
			e.reconcileRemoteFolder(f)
			e.startRemoteWatcher(f)
		} else if errored {
			e.reconcileRemoteFolder(f)
		}
	}
	if first {
		go e.reconcileLoop()
		go e.remoteReconcileLoop()
	}
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
			e.reconcile(f)
		}
	}
}

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
		for _, f := range folders {
			e.reconcileRemoteFolder(f)
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
	w, err := NewWatcher(f.Path, func(path string, isDir, removed bool) {
		if isDir {
			return
		}
		e.debounceChange(path, f)
	})
	if err != nil {
		e.setFolderState(f.ID, FolderState{Kind: StateError, Message: "cannot watch: " + err.Error()})

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
	remotePath := e.remotePathFor(path)
	fi, err := os.Stat(path)
	e.mu.Lock()
	known := e.remoteHashes[f.ID][remotePath]
	e.mu.Unlock()
	if err == nil && !fi.IsDir() {
		rel, relErr := filepath.Rel(f.Path, path)
		if relErr != nil {
			rel = filepath.Base(path)
		}
		if notSynced(filepath.ToSlash(rel)) {
			return // as the reconcile: not part of the backup
		}
		h, err := e.cachedHashInfo(f.ID, path, fi)
		if err != nil || h == known {
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
				e.remoteHashes[f.ID] = map[string]string{}
			}
			e.remoteHashes[f.ID][remotePath] = h
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
	e.ensureUploadOnly(f)

	remotePrefix := e.remotePathFor(f.Path) + "/"
	resp, err := e.request(func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqListFiles{ReqListFiles: &pb.ListFiles{Path: remotePrefix, Recursive: true}}
	})
	if err != nil {
		e.setFolderState(f.ID, FolderState{Kind: StateError, Message: err.Error()})
		e.scheduleErrorRetry(f)

		return
	}
	if err := wsclient.RespError(resp, "could not list remote files"); err != nil {
		e.setFolderState(f.ID, FolderState{Kind: StateError, Message: err.Error()})
		e.scheduleErrorRetry(f)

		return
	}
	remoteMap := map[string]string{}
	if lof, ok := resp.Payload.(*pb.RespEnvelope_RespListOfFiles); ok {
		for _, rf := range lof.RespListOfFiles.Files {
			remoteMap[rf.Path] = rf.Hash
		}
	}

	// An unreadable subdirectory only means fewer uploads here: a backup
	// never deletes on the device.
	local, failedDirs, err := enumerateFiles(f.Path)
	if err != nil {
		e.setFolderState(f.ID, FolderState{Kind: StateError, Message: err.Error()})
		e.scheduleErrorRetry(f)

		return
	}
	e.pruneHashCache(f.ID, f.Path, local, failedDirs)
	localRemote := map[string]bool{}
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
			e.setFolderState(f.ID, FolderState{Kind: StateScanning, CurrentFile: fmt.Sprintf("Checking %d/%d · %s", i+1, len(local), filepath.Base(p))})
		}
		rp := e.remotePathFor(p)
		localRemote[rp] = true
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
		if _, onDevice := remoteMap[rp]; onDevice {
			var err error
			h, err = e.cachedHashInfo(f.ID, p, fi)
			if err != nil || remoteMap[rp] == h {
				continue
			}
		}
		toUpload = append(toUpload, uploadItem{p, rp, h, size, fi})
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
				e.setFolderState(f.ID, FolderState{Kind: StateError, Message: "Device offline - will resume"})

				return
			}
			e.setFolderState(f.ID, FolderState{Kind: StateScanning, Progress: float64(done) / float64(total), CurrentFile: fmt.Sprintf("%d/%d · %s", alreadyThere+k+1, len(local), filepath.Base(it.path))})
			if it.hash == "" {
				h, err := e.cachedHash(f.ID, it.path)
				if err != nil {
					log.Printf("cannot hash %s: %v", filepath.Base(it.path), err)
					done += it.size

					continue
				}
				it.hash = h
				// A large file takes a while to hash: checked again so
				// nothing goes to a device this folder no longer syncs with.
				if e.backupPassOver(f, domainAtStart) {
					return
				}
			}
			if err := e.upload(it.path, it.remote, it.hash, it.info); err != nil {
				log.Printf("error syncing %s: %v", filepath.Base(it.path), err)
			} else {
				remoteMap[it.remote] = it.hash
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
	e.setFolderState(f.ID, FolderState{Kind: StateWatching})
}

// backupPassOver: a backup pass ends once its folder is removed or this
// computer is pointed at another device (disconnected, another device set
// up) - what is left is not sent there, as stillSyncing does for two-way
// passes. When only the device changed, the folder goes again shortly,
// from the new device's own listing.
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
		e.setFolderState(f.ID, FolderState{Kind: StateError, Message: "Device changed - will resume"})
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
	e.mu.Unlock()
	e.mu.Lock()
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

	// Safety guard: a missing local root is not "delete everything
	// remotely" - stop syncing this folder and leave the remote alone.
	if fi, err := os.Stat(f.LocalPath); err != nil || !fi.IsDir() {
		log.Printf("remote folder's local root is gone (%s) - stopping sync, remote left untouched", f.LocalPath)
		e.RemoveRemoteFolder(f.ID)

		return
	}

	remotePrefix := strings.TrimSuffix(f.RemotePath, "/") + "/"
	resp, err := e.request(func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqListFiles{ReqListFiles: &pb.ListFiles{Path: remotePrefix, Recursive: true}}
	})
	if err != nil {
		e.setRemoteState(f.ID, FolderState{Kind: StateError, Message: err.Error()})
		e.scheduleRemoteRetry(f)

		return
	}
	if err := wsclient.RespError(resp, "could not list remote files"); err != nil {
		e.setRemoteState(f.ID, FolderState{Kind: StateError, Message: err.Error()})
		e.scheduleRemoteRetry(f)

		return
	}
	remoteByRel := map[string]*pb.File{}
	if lof, ok := resp.Payload.(*pb.RespEnvelope_RespListOfFiles); ok {
		skipped := 0
		for _, rf := range lof.RespListOfFiles.Files {
			// Device paths are data, not trusted: one that isn't under the
			// folder, or that climbs out of it, would be written (and later
			// deleted from the device) outside the folder.
			rel, under := strings.CutPrefix(rf.Path, remotePrefix)
			if !under || !safeRelative(rel) {
				skipped++

				continue
			}
			if notSynced(rel) {
				// Never seen here either (enumerateFiles): listing it
				// meant a download, then a delete from the device.
				continue
			}
			remoteByRel[rel] = rf
		}
		if skipped > 0 {
			log.Printf("two-way %s: %d device entries outside the folder ignored", f.RemotePath, skipped)
		}
	}

	local, failedDirs, err := enumerateFiles(f.LocalPath)
	if err != nil {
		e.setRemoteState(f.ID, FolderState{Kind: StateError, Message: err.Error()})
		e.scheduleRemoteRetry(f)

		return
	}
	e.pruneHashCache(f.ID, f.LocalPath, local, failedDirs)
	e.mu.Lock()
	e.loadSyncedLocked(f.ID)
	last := e.lastSynced[f.ID]
	e.mu.Unlock()
	if last == nil {
		last = map[string]string{}
	}
	localByRel := map[string]string{}
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
		rel = filepath.ToSlash(rel)
		localByRel[rel] = p
		// Issue #138: which file is being checked, see reconcile().
		if time.Since(lastShown) > time.Second {
			lastShown = time.Now()
			e.setRemoteState(f.ID, FolderState{Kind: StateScanning, CurrentFile: fmt.Sprintf("Checking %d/%d · %s", i+1, len(local), filepath.Base(p))})
		}
		if _, onDevice := remoteByRel[rel]; !onDevice {
			if _, synced := last[rel]; !synced {
				newLocal[rel] = true

				continue
			}
		}
		fi, err := os.Stat(p)
		var h string
		if err == nil {
			h, err = e.cachedHashInfo(f.ID, p, fi)
		}
		if err == nil {
			localHashes[rel] = h
			localInfo[rel] = fi
		} else {
			unreadable[rel] = true
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
	// counted in the folder's "could not be read" like one.
	for rel := range all {
		for _, d := range failedDirs {
			if rel == d || strings.HasPrefix(rel, d+"/") {
				unreadable[rel] = true

				break
			}
		}
	}

	newSynced := map[string]string{}
	for k, v := range last {
		newSynced[k] = v
	}
	var actions []action
	for rel := range all {
		if unreadable[rel] {
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
		lastHash := last[rel]
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
		localChanged := localHash != lastHash
		remoteChanged := remoteHash != lastHash
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
			} else {
				actions = append(actions, action{rel, actDeleteRemote, ""})
				delete(newSynced, rel)
			}
		}
	}

	// Mass-deletion guard (as SyncModel.reconcileRemoteFolder): a device
	// wiped or set up again looks like one whose owner deleted everything,
	// and this used to delete the whole folder here to match. Past a
	// handful of files and a quarter of the folder, the device is taken to
	// have lost them: they are sent back and nothing here is touched.
	localDeletes := 0
	for _, a := range actions {
		if a.kind == actDeleteLocal {
			localDeletes++
		}
	}
	guardNote := ""
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
		guardNote = fmt.Sprintf("%d files had disappeared from the device - restored them from this computer instead of deleting them here", localDeletes)
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
			n = int64(rf.Size)
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

	for i, a := range actions {
		if !e.stillSyncing(f.ID, domainAtStart) {
			log.Printf("%s: folder removed or device changed - pass stopped", f.RemotePath)
			return
		}
		// As reconcile(): a dropped link ends the pass; what's left keeps
		// its baseline and goes on reconnect.
		if !e.ws.IsConnected() {
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
			e.saveSynced(f.ID, newSynced)
			e.setRemoteState(f.ID, FolderState{Kind: StateError, Message: "Device offline - will resume"})

			return
		}
		e.setRemoteState(f.ID, FolderState{Kind: StateScanning, Progress: float64(max(bytesDone, 0)) / float64(totalBytes), CurrentFile: fmt.Sprintf("%d/%d · %s", alreadyAgree+i+1, folderCount, baseName(a.relative))})
		bytesDone += bytesOf(a.relative)
		if !safeRelative(a.relative) {
			// Never reached from the listing (filtered above); a record
			// written by an older version could still carry such a path.
			delete(newSynced, a.relative)

			continue
		}
		localPath := filepath.Join(f.LocalPath, filepath.FromSlash(a.relative))
		remotePath := remotePrefix + a.relative
		var err error
		switch a.kind {
		case actUpload:
			fi, statErr := os.Stat(localPath)
			if statErr != nil {
				err = statErr
			} else {
				if a.hash == "" {
					a.hash, err = e.cachedHash(f.ID, localPath)
				}
				// Hashing a new file takes a while: the same check as the
				// loop's, so nothing goes to a device this folder left.
				if err == nil && !e.stillSyncing(f.ID, domainAtStart) {
					log.Printf("%s: folder removed or device changed - pass stopped", f.RemotePath)

					return
				}
				if err == nil {
					err = e.upload(localPath, remotePath, a.hash, fi)
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
			copyPath := conflictPath(localPath, "", time.Now())
			if err = e.downloadIf(remotePath, copyPath, remoteHash, f.ID, absent(copyPath)); err == nil {
				log.Printf("conflict on %s: the other version kept as %s", a.relative, filepath.Base(copyPath))
				if !e.stillSyncing(f.ID, domainAtStart) {
					log.Printf("%s: folder removed or device changed - pass stopped", f.RemotePath)

					return
				}
				var fi os.FileInfo
				if fi, err = os.Stat(localPath); err == nil {
					err = e.upload(localPath, remotePath, localHash, fi)
				}
			}
		case actDeleteRemote:
			err = e.deleteRemote(remotePath)
		case actDeleteLocal:
			if err = stillAsScanned(a.relative, localPath); err == nil {
				err = os.Remove(localPath)
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
			if errors.Is(err, errChangedHere) {
				log.Printf("%s %v", a.relative, err)
			} else {
				log.Printf("error syncing %s: %v", a.relative, err)
			}
		}
	}

	e.mu.Lock()
	e.lastSynced[f.ID] = newSynced
	e.mu.Unlock()
	e.saveSynced(f.ID, newSynced)
	// Files under a directory that could not be read are in unreadable
	// (see above) and counted here. Such a directory with nothing synced
	// under it - lost+found at a mount's root, System Volume Information
	// at a drive's - is only logged (enumerateFiles), as on the Mac: it
	// would otherwise hold the folder in an error for good.
	if len(unreadable) > 0 {
		e.setRemoteState(f.ID, FolderState{Kind: StateError, Message: fmt.Sprintf("%d file(s) could not be read", len(unreadable))})

		return
	}
	if guardNote != "" {
		e.setRemoteState(f.ID, FolderState{Kind: StateError, Message: guardNote})

		return
	}
	e.setRemoteState(f.ID, FolderState{Kind: StateWatching})
}

// errChangedHere: a planned overwrite or delete found the local file no
// longer as the pass saw it (see stillAsScanned).
var errChangedHere = errors.New("changed on this computer during the pass - left for the next pass")

// massDeleteMin: two-way folders - more local deletions than this in one
// pass (and a quarter of the folder) mean the device lost the files.
const massDeleteMin = 20

func (e *Engine) startRemoteWatcher(f config.RemoteFolder) {
	e.mu.Lock()
	if _, ok := e.remoteWatch[f.ID]; ok || e.stopped || !e.remoteConfiguredLocked(f.ID) {
		e.mu.Unlock()

		return
	}
	e.mu.Unlock()
	w, err := NewWatcher(f.LocalPath, func(string, bool, bool) {
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
	})
	if err != nil {
		e.setRemoteState(f.ID, FolderState{Kind: StateError, Message: "cannot watch: " + err.Error()})

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

// RemoveRemoteFolder drops a two-way folder from the config (the engine's
// own safety guard uses it; the UI edits config.json instead).
func (e *Engine) RemoveRemoteFolder(id string) {
	e.mu.Lock()
	kept := e.cfg.RemoteFolders[:0:0]
	for _, rf := range e.cfg.RemoteFolders {
		if rf.ID != id {
			kept = append(kept, rf)
		}
	}
	e.cfg.RemoteFolders = kept
	cfg := *e.cfg
	e.dropRemoteFolderLocked(id)
	e.mu.Unlock()
	if err := cfg.Save(); err != nil {
		log.Printf("could not save config: %v", err)
	}
	e.notify()
}

// ---- wire helpers --------------------------------------------------------

// upload is hash-first (issue #58): link when the device already has the
// content, send the bytes otherwise.
func (e *Engine) upload(path, remotePath, hash string, fi os.FileInfo) error {
	has, err := e.request(func(r *pb.ReqEnvelope) {
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
		resp, err := e.request(func(r *pb.ReqEnvelope) {
			r.Payload = &pb.ReqEnvelope_ReqLinkFile{ReqLinkFile: &pb.LinkFile{Hash: hash, Path: remotePath, ForceOverride: true, Created: created, Modified: modified}}
		})
		if err != nil {
			return err
		}

		return wsclient.RespError(resp, "link rejected")
	}
	return e.uploadChunked(path, remotePath, hash, created, modified)
}

// cChunk is how much of a file one message carries each way (the device's
// MaxChunk): no message, and no memory here or on the device, ever holds
// a whole file (issue #168).
const cChunk = 4 << 20

// uploadChunked sends the file in pieces read from the disk as they go -
// BeginUpload, UploadChunk in order, FinishUpload with the hash - instead
// of one UploadFile with the whole file in memory. The device checks the
// hash before the content becomes the file.
func (e *Engine) uploadChunked(path, remotePath, hash string, created, modified *timestamppb.Timestamp) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	resp, err := e.request(func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqBeginUpload{ReqBeginUpload: &pb.BeginUpload{
			Path: remotePath, Size: st.Size(), Created: created, Modified: modified, ForceOverride: true,
		}}
	})
	if err != nil {
		return err
	}
	if err := wsclient.RespError(resp, "upload rejected"); err != nil {
		return err
	}
	started, ok := resp.Payload.(*pb.RespEnvelope_RespUploadStarted)
	if !ok {
		return errors.New("unexpected response to BeginUpload")
	}
	id := started.RespUploadStarted.UploadId

	buf := make([]byte, cChunk)
	var offset int64
	for offset < st.Size() {
		n, err := io.ReadFull(f, buf)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			return err
		}
		if n == 0 {
			return fmt.Errorf("%s shrank while it was being sent", path)
		}
		chunk := buf[:n]
		at := offset
		resp, err := e.request(func(r *pb.ReqEnvelope) {
			r.Payload = &pb.ReqEnvelope_ReqUploadChunk{ReqUploadChunk: &pb.UploadChunk{UploadId: id, Offset: at, Data: chunk}}
		})
		if err != nil {
			return err
		}
		if err := wsclient.RespError(resp, "upload rejected"); err != nil {
			return err
		}
		offset += int64(n)
	}
	resp, err = e.request(func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqFinishUpload{ReqFinishUpload: &pb.FinishUpload{UploadId: id, Sha256: hash}}
	})
	if err != nil {
		return err
	}

	return wsclient.RespError(resp, "upload rejected")
}

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
	tmp := dest + ".otc-part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644) // perms: rw-r--r--
	if err != nil {
		return err
	}
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
		resp, err := e.request(func(r *pb.ReqEnvelope) {
			r.Payload = &pb.ReqEnvelope_ReqReadFile{ReqReadFile: &pb.ReadFile{Path: remotePath, Offset: at, Length: cChunk}}
		})
		if err != nil {
			return err
		}
		if err := wsclient.RespError(resp, "download rejected"); err != nil {
			return err
		}
		c, ok := resp.Payload.(*pb.RespEnvelope_RespFileChunk)
		if !ok {
			return errors.New("unexpected response")
		}
		last = c.RespFileChunk
		if expectedHash != "" && last.Hash != "" && last.Hash != expectedHash {
			return errors.New("the file changed on the device while downloading - will retry")
		}
		size = last.Size
		if len(last.Data) == 0 && offset < size {
			return errors.New("the device sent an empty piece of the file")
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
		return fmt.Errorf("the device sent %d bytes that don't match the file's hash - not written", offset)
	}
	if err := out.Close(); err != nil {
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
	e.hashCache[folderID][p] = hashEntry{size: fi.Size(), modTime: fi.ModTime(), hash: hash}
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
	resp, err := e.request(func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqSetUploadOnly{ReqSetUploadOnly: &pb.SetUploadOnly{Path: e.remotePathFor(f.Path) + "/", UploadOnly: true}}
	})
	if err == nil {
		err = wsclient.RespError(resp, "upload only refused")
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
	resp, err := e.request(func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqDelFile{ReqDelFile: &pb.DelFile{Path: remotePath}}
	})
	if err != nil {
		return err
	}
	// Issue #132: the owner made that folder upload only, so the device
	// keeps the file however the local copy goes. That is the folder
	// working as intended, not a failure to retry (SyncModel.delete does
	// the same).
	if resp != nil && resp.Error && resp.ErrorCode == "upload_only" {
		log.Printf("delete of %s skipped: the folder is upload only", remotePath)

		return nil
	}

	return wsclient.RespError(resp, "delete rejected")
}

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

// notSynced: paths this client never syncs - hidden ones (as
// enumerateFiles and the Mac's .skipsHiddenFiles) and its own partial
// downloads. rel is relative to the folder, so a folder that itself sits
// under a dot directory is still synced.
func notSynced(rel string) bool {
	return isHidden(rel) || strings.HasSuffix(rel, ".otc-part")
}

func isHidden(p string) bool {
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if strings.HasPrefix(part, ".") && part != "." && part != ".." {
			return true
		}
	}

	return false
}

// enumerateFiles walks a tree for regular files, skipping hidden entries
// like the macOS enumerator's .skipsHiddenFiles and this client's own
// partial downloads. failed lists the directories below root that could
// not be read (relative to root, slash-separated): what is under them is
// unknown, not gone. An unreadable root is an error.
func enumerateFiles(root string) (out, failed []string, err error) {
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			// Below the root only a directory's ReadDir reports here.
			if rel, ok := unreadableDir(root, p, err); ok {
				if len(failed) < 5 {
					log.Printf("cannot read directory %s: %v", rel, err)
				}
				failed = append(failed, rel)
			}

			return nil
		}
		if p != root && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}

			return nil
		}
		if d.Type().IsRegular() && !strings.HasSuffix(p, ".otc-part") {
			out = append(out, p)
		}

		return nil
	})

	return out, failed, err
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
	hash    string
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
		return "", fmt.Errorf("cannot read %s", filepath.Base(p))
	}
	e.mu.Lock()
	e.loadHashCacheLocked(folderID)
	hit, ok := e.hashCache[folderID][p]
	e.mu.Unlock()
	if ok && hit.size == fi.Size() && hit.modTime.Equal(fi.ModTime()) {
		return hit.hash, nil
	}
	h, err := sha256File(p)
	if err != nil {
		return "", err
	}
	e.mu.Lock()
	if e.hashCache[folderID] == nil {
		e.hashCache[folderID] = map[string]hashEntry{}
	}
	e.hashCache[folderID][p] = hashEntry{size: fi.Size(), modTime: fi.ModTime(), hash: h}
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
		label = "conflict from " + from + " " + at.Format("2006-01-02 15.04")
	}
	candidate := filepath.Join(dir, fmt.Sprintf("%s (%s)%s", stem, label, ext))
	for i := 2; ; i++ {
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
			return candidate
		}
		candidate = filepath.Join(dir, fmt.Sprintf("%s (%s %d)%s", stem, label, i, ext))
	}
}

// hostLabel is this computer's name for conflict copies.
func hostLabel() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "this computer"
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
