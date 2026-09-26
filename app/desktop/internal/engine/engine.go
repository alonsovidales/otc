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
	folderStates map[string]FolderState
	remoteStates map[string]FolderState
	remoteHashes map[string]map[string]string // folder id -> remote path -> hash
	lastSynced   map[string]map[string]string // remote folder id -> relative path -> hash
	watchers     map[string]*Watcher
	remoteWatch  map[string]*Watcher
	debounce     map[string]*time.Timer // per changed path (upload folders)
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
	folderBusy map[string]bool
	onChange   func()
	hostname   string
	stopped    bool
}

// New builds an engine over cfg; onChange fires whenever anything the UI
// shows may have changed.
func New(cfg *config.Config, password string, onChange func()) *Engine {
	host, _ := os.Hostname()
	if host == "" {
		host = "PC"
	}
	e := &Engine{
		ws:           wsclient.New(),
		cfg:          cfg,
		password:     password,
		status:       "Not connected",
		raid:         RaidUnknown,
		folderStates: map[string]FolderState{},
		remoteStates: map[string]FolderState{},
		remoteHashes: map[string]map[string]string{},
		lastSynced:   map[string]map[string]string{},
		watchers:     map[string]*Watcher{},
		remoteWatch:  map[string]*Watcher{},
		debounce:     map[string]*time.Timer{},
		remoteDeb:    map[string]*time.Timer{},
		errorRetry:   map[string]*time.Timer{},
		remoteRetry:  map[string]*time.Timer{},
		folderBusy:   map[string]bool{},
		hashCache:    map[string]map[string]hashEntry{},
		onChange:     onChange,
		hostname:     host,
	}
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
		wrongPassword := e.status == "Wrong password" || strings.HasPrefix(e.status, "Too many attempts")
		e.mu.Unlock()
		if !wrongPassword {
			e.setStatus("Disconnected")
		}
		e.stopRaidPolling()
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
	for _, f := range old.Folders {
		if !keep[f.ID] {
			e.dropFolderLocked(f.ID)
		}
	}
	keepR := map[string]bool{}
	for _, f := range cfg.RemoteFolders {
		keepR[f.ID] = true
		if _, ok := e.remoteStates[f.ID]; !ok {
			e.remoteStates[f.ID] = FolderState{Kind: StateScanning}
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
	delete(e.hashCache, id)
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
	delete(e.lastSynced, id)
	delete(e.remoteStates, id)
	delete(e.hashCache, id)
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

func (e *Engine) setFolderState(id string, s FolderState) {
	e.mu.Lock()
	if _, ok := e.folderStates[id]; ok {
		e.folderStates[id] = s
	}
	e.mu.Unlock()
	e.notify()
}

func (e *Engine) setRemoteState(id string, s FolderState) {
	e.mu.Lock()
	if _, ok := e.remoteStates[id]; ok {
		e.remoteStates[id] = s
	}
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
		e.mu.Unlock()
		if !watching {
			e.setupFolder(f)
		}
	}
	for _, f := range remotes {
		e.mu.Lock()
		_, watching := e.remoteWatch[f.ID]
		e.mu.Unlock()
		if !watching {
			e.reconcileRemoteFolder(f)
			e.startRemoteWatcher(f)
		}
	}
	if first {
		go e.reconcileLoop()
		go e.remoteReconcileLoop()
	}
}

func (e *Engine) setupFolder(f config.Folder) {
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
	if _, ok := e.watchers[f.ID]; ok || e.stopped {
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
		delete(e.debounce, path)
		e.mu.Unlock()
		e.processChangedPath(path, f)
	})
	e.mu.Unlock()
}

func (e *Engine) processChangedPath(path string, f config.Folder) {
	if !e.ws.IsConnected() {
		return // the reconcile safety net catches it up later
	}
	remotePath := e.remotePathFor(path)
	fi, err := os.Stat(path)
	e.mu.Lock()
	known := e.remoteHashes[f.ID][remotePath]
	e.mu.Unlock()
	if err == nil && !fi.IsDir() {
		if isHidden(path) {
			return
		}
		h, err := e.cachedHash(f.ID, path)
		if err != nil || h == known {
			return
		}
		if err := e.upload(path, remotePath, h, fi); err != nil {
			log.Printf("error uploading %s: %v", path, err)

			return
		}
		e.mu.Lock()
		if e.remoteHashes[f.ID] == nil {
			e.remoteHashes[f.ID] = map[string]string{}
		}
		e.remoteHashes[f.ID][remotePath] = h
		e.mu.Unlock()
	} else if known != "" {
		// Synced before and gone now: a real deletion.
		if err := e.deleteRemote(remotePath); err != nil {
			log.Printf("error deleting %s: %v", remotePath, err)

			return
		}
		e.mu.Lock()
		delete(e.remoteHashes[f.ID], remotePath)
		e.mu.Unlock()
	}
}

// ---- reconcile: local -> remote ----------------------------------------

type uploadItem struct {
	path, remote, hash string
	size               int64
	info               os.FileInfo
}

func (e *Engine) reconcile(f config.Folder) {
	if !e.ws.IsConnected() {
		return
	}
	e.mu.Lock()
	if e.folderBusy[f.ID] {
		e.mu.Unlock()

		return
	}
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

	local, err := enumerateFiles(f.Path)
	if err != nil {
		e.setFolderState(f.ID, FolderState{Kind: StateError, Message: err.Error()})
		e.scheduleErrorRetry(f)

		return
	}
	localRemote := map[string]bool{}
	var toUpload []uploadItem
	// Issue #138 (as SyncModel.reconcile): say which file is being checked,
	// once a second at most - most files are answered from the hash cache
	// in no time, the new ones are what takes a while.
	var lastShown time.Time
	for i, p := range local {
		if time.Since(lastShown) > time.Second {
			lastShown = time.Now()
			e.setFolderState(f.ID, FolderState{Kind: StateScanning, CurrentFile: fmt.Sprintf("Checking %d/%d · %s", i+1, len(local), filepath.Base(p))})
		}
		rp := e.remotePathFor(p)
		localRemote[rp] = true
		h, err := e.cachedHash(f.ID, p)
		if err != nil || remoteMap[rp] == h {
			continue
		}
		fi, _ := os.Stat(p)
		var size int64
		if fi != nil {
			size = fi.Size()
		}
		toUpload = append(toUpload, uploadItem{p, rp, h, size, fi})
	}

	if len(toUpload) > 0 {
		var total int64
		for _, it := range toUpload {
			total += it.size
		}
		if total < 1 {
			total = 1
		}
		var done int64
		for _, it := range toUpload {
			e.setFolderState(f.ID, FolderState{Kind: StateScanning, Progress: float64(done) / float64(total), CurrentFile: filepath.Base(it.path)})
			if err := e.upload(it.path, it.remote, it.hash, it.info); err != nil {
				log.Printf("error syncing %s: %v", filepath.Base(it.path), err)
			} else {
				remoteMap[it.remote] = it.hash
			}
			done += it.size
		}
	}

	for rp := range remoteMap {
		if localRemote[rp] {
			continue
		}
		if err := e.deleteRemote(rp); err != nil {
			log.Printf("error deleting stale %s: %v", rp, err)

			continue
		}
		delete(remoteMap, rp)
	}

	e.mu.Lock()
	e.remoteHashes[f.ID] = remoteMap
	e.mu.Unlock()
	e.setFolderState(f.ID, FolderState{Kind: StateWatching})
}

func (e *Engine) scheduleErrorRetry(f config.Folder) {
	e.mu.Lock()
	defer e.mu.Unlock()
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
)

type action struct {
	relative string
	kind     actionKind
	hash     string
}

func (e *Engine) reconcileRemoteFolder(f config.RemoteFolder) {
	if !e.ws.IsConnected() {
		return
	}
	e.mu.Lock()
	if e.folderBusy[f.ID] {
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
		for _, rf := range lof.RespListOfFiles.Files {
			remoteByRel[strings.TrimPrefix(rf.Path, remotePrefix)] = rf
		}
	}

	local, err := enumerateFiles(f.LocalPath)
	if err != nil {
		e.setRemoteState(f.ID, FolderState{Kind: StateError, Message: err.Error()})
		e.scheduleRemoteRetry(f)

		return
	}
	localByRel := map[string]string{}
	localHashes := map[string]string{}
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
		if h, err := e.cachedHash(f.ID, p); err == nil {
			localHashes[rel] = h
		} else {
			unreadable[rel] = true
			if len(unreadable) <= 5 {
				log.Printf("cannot hash %s: %v", rel, err)
			}
		}
	}

	e.mu.Lock()
	last := e.lastSynced[f.ID]
	e.mu.Unlock()
	if last == nil {
		last = map[string]string{}
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

	newSynced := map[string]string{}
	for k, v := range last {
		newSynced[k] = v
	}
	var actions []action
	for rel := range all {
		if unreadable[rel] {
			continue
		}
		localHash, hasLocal := localHashes[rel]
		remoteFile := remoteByRel[rel]
		remoteHash := ""
		if remoteFile != nil {
			remoteHash = remoteFile.Hash
		}
		lastHash := last[rel]
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
		if remoteWins {
			if remoteFile != nil {
				actions = append(actions, action{rel, actDownload, ""})
				newSynced[rel] = remoteHash
			} else {
				actions = append(actions, action{rel, actDeleteLocal, ""})
				delete(newSynced, rel)
			}
		} else {
			if hasLocal {
				actions = append(actions, action{rel, actUpload, localHash})
				newSynced[rel] = localHash
			} else {
				actions = append(actions, action{rel, actDeleteRemote, ""})
				delete(newSynced, rel)
			}
		}
	}

	for i, a := range actions {
		e.setRemoteState(f.ID, FolderState{Kind: StateScanning, Progress: float64(i) / float64(len(actions)), CurrentFile: fmt.Sprintf("%d/%d · %s", i+1, len(actions), baseName(a.relative))})
		localPath := filepath.Join(f.LocalPath, filepath.FromSlash(a.relative))
		remotePath := remotePrefix + a.relative
		var err error
		switch a.kind {
		case actUpload:
			fi, statErr := os.Stat(localPath)
			if statErr != nil {
				err = statErr
			} else {
				err = e.upload(localPath, remotePath, a.hash, fi)
			}
		case actDownload:
			err = e.download(remotePath, localPath)
		case actDeleteRemote:
			err = e.deleteRemote(remotePath)
		case actDeleteLocal:
			err = os.Remove(localPath)
		}
		if err != nil {
			// Back to the baseline for this path so a transient failure is
			// retried next pass instead of being taken as "agrees".
			if prior, ok := last[a.relative]; ok {
				newSynced[a.relative] = prior
			} else {
				delete(newSynced, a.relative)
			}
			log.Printf("error syncing %s: %v", a.relative, err)
		}
	}

	e.mu.Lock()
	e.lastSynced[f.ID] = newSynced
	e.mu.Unlock()
	if len(unreadable) > 0 {
		e.setRemoteState(f.ID, FolderState{Kind: StateError, Message: fmt.Sprintf("%d file(s) could not be read", len(unreadable))})

		return
	}
	e.setRemoteState(f.ID, FolderState{Kind: StateWatching})
}

func (e *Engine) startRemoteWatcher(f config.RemoteFolder) {
	e.mu.Lock()
	if _, ok := e.remoteWatch[f.ID]; ok || e.stopped {
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
	e.remoteWatch[f.ID] = w
	e.mu.Unlock()
}

func (e *Engine) scheduleRemoteRetry(f config.RemoteFolder) {
	e.mu.Lock()
	defer e.mu.Unlock()
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
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	resp, err := e.request(func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqUploadFile{ReqUploadFile: &pb.UploadFile{Path: remotePath, Content: data, ForceOverride: true, Created: created, Modified: modified}}
	})
	if err != nil {
		return err
	}

	return wsclient.RespError(resp, "upload rejected")
}

func (e *Engine) download(remotePath, dest string) error {
	resp, err := e.request(func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqGetFile{ReqGetFile: &pb.GetFile{Path: remotePath}}
	})
	if err != nil {
		return err
	}
	if err := wsclient.RespError(resp, "download rejected"); err != nil {
		return err
	}
	file, ok := resp.Payload.(*pb.RespEnvelope_RespFile)
	if !ok {
		return errors.New("unexpected response")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil { // perms: rwxr-xr-x
		return err
	}
	tmp := dest + ".otc-part"
	if err := os.WriteFile(tmp, file.RespFile.Content, 0o644); err != nil { // perms: rw-r--r--
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	// Issue #134 (as SyncModel.download): the file keeps the dates it has
	// on the device rather than "now" - the modification time is also
	// what the conflict rule in reconcile compares, so it must be the
	// device's. Creation time only where the platform lets it be set.
	f := file.RespFile
	if f.Modified != nil {
		created := time.Time{}
		if f.Created != nil {
			created = f.Created.AsTime()
		}
		if err := setFileTimes(dest, created, f.Modified.AsTime()); err != nil {
			log.Printf("could not set the dates of %s: %v", dest, err)
		}
	}

	return nil
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
// partial downloads.
func enumerateFiles(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
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

	return out, err
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
	e.mu.Lock()
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
