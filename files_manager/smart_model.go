// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/i18n"
	imagesembedder "github.com/alonsovidales/otc/images_embedder"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/modelfetch"
	"github.com/alonsovidales/otc/modelserver"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"github.com/alonsovidales/otc/supervisor"
)

// Smart search's model on demand (release 152). Release 151 had every
// update download it - about 415 MB - on every device, Smart search on or
// off; on a slow home link that took the whole connection for a long
// while. Now the primary instance downloads it only once someone turns
// Smart search on, in the background at a capped speed (package
// modelfetch), and loads it once it is there:
//
//   - [embedder]'s paths are used where the files are (a device that got
//     them from release 151's update, the demo): each tower's .onnx with
//     its .data beside it - the .onnx names it - and the tokenizer.
//   - What isn't there is downloaded into [embedder] download-dir, by
//     default models/siglip2-base in the primary's working directory
//     (/var/lib/otc on a Raspberry Pi, the state volume in the container):
//     the service can't write under /usr.
//   - From the "models" release on GitHub ([embedder] model-url for a
//     fork), pinned in smartModelFiles, at [embedder] download-kbps
//     kilobytes a second (2048 by default; 0 for no cap).
//
// It is downloaded while any instance has Smart search on: the primary's
// own switch, or a user's instance asking over the model socket
// ("embed-fetch", asked again every cSmartPoll while it waits and
// forgotten cSmartLease after its last ask). Turned off by every one of
// them, the download stops and its partial files go; whole files stay -
// they aren't anyone's data. Once the model is there the primary loads it
// (adoptSmartModel), a user's instance finds it in "info", and each
// describes its photos: with the session that turned Smart search on, or
// at the next sign-in.

// smartModelFiles is Smart search's model as release 151 published it
// (models/siglip2_export.py): each file's name here, its name in the
// release, its size and SHA-256.
var smartModelFiles = []modelfetch.File{
	{Name: "image.onnx", Asset: "siglip2-base-image.onnx", Size: 267528, SHA256: "e7eca79ab2103e51dd722e3d9e3c24507af450ccb1b5cb94e37c0fc051cbac22"},
	{Name: "image.onnx.data", Asset: "siglip2-base-image.onnx.data", Size: 113968128, SHA256: "b9a8e6cfe59f54cb991163c26b82b54bb01e6bbe038291f9116503a3f7dc3f21"},
	{Name: "text.onnx", Asset: "siglip2-base-text.onnx", Size: 231373, SHA256: "05ad0676b6ffc7c500bc04f9cf80ad442de5bd4bc537c237d33876bc7ba4db92"},
	{Name: "text.onnx.data", Asset: "siglip2-base-text.onnx.data", Size: 296227840, SHA256: "951bd580188f27cdfc72d13e4233e11110b8a8240cb12806fed61a871e444655"},
	{Name: "tokenizer.model", Asset: "siglip2-base-tokenizer.model", Size: 4241003, SHA256: "61a7b147390c64585d6c3543dd6fc636906c9af3865a5548f27f31aee1d4c8e2"},
}

const (
	// cSmartModelURL is where smartModelFiles are published.
	cSmartModelURL = "https://github.com/alonsovidales/otc/releases/download/models/"
	// cSmartModelDir is where they are downloaded, under the primary's
	// working directory, unless [embedder] download-dir says otherwise.
	cSmartModelDir = "models/siglip2-base"
	// cSmartModelKBps caps the download (kilobytes a second): about 16
	// Mbit/s, so it never fills a home link.
	cSmartModelKBps = 2048
	// cSmartLease: a user's instance that hasn't asked for the model for
	// this long no longer counts as wanting it.
	cSmartLease = 5 * time.Minute
	// cSmartPoll: how often a user's instance waiting for the model asks
	// the primary for it again.
	cSmartPoll = time.Minute
)

// embedderFiles are where Smart search's model is used from: each tower's
// .onnx (its .data beside it) and the tokenizer.
type embedderFiles struct {
	image, text, tokenizer string
}

// smartModelConf is [embedder]'s settings.
type smartModelConf struct {
	// configured: [embedder]'s paths ("" when not set).
	configured embedderFiles
	// dir is where the model is downloaded, "" when there is nowhere.
	dir string
	url string
	// bytesPerSec caps the download; 0: no cap.
	bytesPerSec int64
}

// smartModelConfig reads [embedder] (absent: the defaults, nothing
// configured).
func smartModelConfig() smartModelConf {
	c := smartModelConf{url: cSmartModelURL, bytesPerSec: cSmartModelKBps << 10}
	if cfg.HasSection("embedder") {
		c.configured = embedderFiles{
			image:     strings.TrimSpace(cfg.GetStr("embedder", "image-model-path")),
			text:      strings.TrimSpace(cfg.GetStr("embedder", "text-model-path")),
			tokenizer: strings.TrimSpace(cfg.GetStr("embedder", "tokenizer-path")),
		}
		c.dir = strings.TrimSpace(cfg.GetStr("embedder", "download-dir"))
		if v := strings.TrimSpace(cfg.GetStr("embedder", "model-url")); v != "" {
			if u, err := url.Parse(v); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
				c.url = v
			} else {
				log.Error("[embedder] model-url isn't an http(s) address, using the default")
			}
		}
		if v := strings.TrimSpace(cfg.GetStr("embedder", "download-kbps")); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 && n < 1<<40 {
				c.bytesPerSec = n << 10
			} else {
				log.Error("[embedder] download-kbps isn't a number of kilobytes a second, using", cSmartModelKBps)
			}
		}
	}
	if c.dir == "" {
		if wd, err := os.Getwd(); err == nil {
			c.dir = filepath.Join(wd, cSmartModelDir)
		}
	} else if abs, err := filepath.Abs(c.dir); err == nil {
		c.dir = abs
	} else {
		c.dir = ""
	}
	return c
}

// smartModelStatus is where Smart search's model is for an instance.
type smartModelStatus struct {
	// ready: there; downloading: asked for and on its way, or waiting to
	// try again (problem says why); unavailable: this device can't get it.
	ready, downloading, unavailable bool
	percent                         int
	problem                         *modelfetch.Problem
}

// smartModelSource is where an instance's Smart search model comes from:
// the device's own files and download (localModel, the primary), or the
// primary's (remoteModel, a user's instance).
type smartModelSource interface {
	// want says whether this instance has Smart search on - wants the
	// model - and is where the model is.
	want(on bool) (smartModelStatus, error)
	// status is where the model is.
	status() smartModelStatus
	// embedder is the model to use once status says it is ready.
	embedder() modelserver.Embedder
}

// localModel is the primary's Smart search model: its files, the
// download of the ones missing, and who wants them.
type localModel struct {
	paths embedderFiles
	// missing: some file isn't at [embedder]'s paths; fetch downloads
	// them (nil: nowhere to put them).
	missing bool
	fetch   *modelfetch.Fetcher
	low     bool
	run     *sync.Mutex

	loadOnce sync.Once
	loaded   modelserver.Embedder

	// opMu: one change of who wants it - and the Start or Stop it makes -
	// at a time.
	opMu sync.Mutex
	mu   sync.Mutex
	// self: the primary's own Smart search is on; leases: the users'
	// instances asking for it, by user, when they last asked.
	self   bool
	leases map[string]time.Time
	now    func() time.Time
}

// regularFile is whether p names a regular file.
func regularFile(p string) bool {
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// newLocalModel is the model c describes. low and run as newEmbedModels';
// onReady is called once a download has put every missing file in place.
func newLocalModel(c smartModelConf, low bool, run *sync.Mutex, onReady func()) *localModel {
	m := &localModel{low: low, run: run, leases: map[string]time.Time{}, now: time.Now}
	in := func(name string) string { return filepath.Join(c.dir, name) }
	var fetch []modelfetch.File
	// A tower's .onnx names the .data file beside it: the two are used
	// from the same place.
	if regularFile(c.configured.image) && regularFile(c.configured.image+".data") {
		m.paths.image = c.configured.image
	} else {
		m.paths.image = in("image.onnx")
		fetch = append(fetch, smartModelFiles[0], smartModelFiles[1])
	}
	if regularFile(c.configured.text) && regularFile(c.configured.text+".data") {
		m.paths.text = c.configured.text
	} else {
		m.paths.text = in("text.onnx")
		fetch = append(fetch, smartModelFiles[2], smartModelFiles[3])
	}
	if regularFile(c.configured.tokenizer) {
		m.paths.tokenizer = c.configured.tokenizer
	} else {
		m.paths.tokenizer = in("tokenizer.model")
		fetch = append(fetch, smartModelFiles[4])
	}
	m.missing = len(fetch) > 0
	if m.missing && c.dir != "" {
		m.fetch = modelfetch.New(modelfetch.Config{
			Name:           "smart search's model", // i18n-ignore: a log line
			Dir:            c.dir,
			BaseURL:        c.url,
			Files:          fetch,
			BytesPerSecond: c.bytesPerSec,
			Room:           smartModelRoom,
			Wanted:         m.wanted,
			OnReady:        onReady,
		})
	}
	return m
}

// smartModelRoom is what can be written in dir, the device's reserve kept
// (freespace.go).
func smartModelRoom(dir string) (int64, error) {
	u, err := diskUsageOf(dir)
	if err != nil {
		return 0, err
	}
	return int64(min(u.Avail, 1<<62)) - freeSpaceReserve(u.Total), nil
}

// ready is whether every file is in place.
func (m *localModel) ready() bool {
	return !m.missing || (m.fetch != nil && m.fetch.Ready())
}

// wanted is whether any instance wants the model.
func (m *localModel) wanted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.wantedLocked()
}

func (m *localModel) wantedLocked() bool {
	if m.self {
		return true
	}
	now := m.now()
	wanted := false
	for who, at := range m.leases {
		if now.Sub(at) < cSmartLease {
			wanted = true
		} else {
			delete(m.leases, who)
		}
	}
	return wanted
}

// wantFor records whether who ("" for the primary itself, else a user's
// instance) wants the model, starts the download when it isn't there, and
// stops it once nobody wants it.
func (m *localModel) wantFor(who string, on bool) smartModelStatus {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	switch {
	case who == "":
		m.self = on
	case on:
		m.leases[who] = m.now()
	default:
		delete(m.leases, who)
	}
	wanted := m.wantedLocked()
	m.mu.Unlock()
	if m.fetch != nil && !m.ready() {
		if on {
			m.fetch.Start()
		} else if !wanted {
			m.fetch.Stop()
		}
	}
	return m.status()
}

func (m *localModel) want(on bool) (smartModelStatus, error) { return m.wantFor("", on), nil }

func (m *localModel) status() smartModelStatus {
	if m.ready() {
		return smartModelStatus{ready: true}
	}
	if m.fetch == nil {
		return smartModelStatus{unavailable: true}
	}
	st := m.fetch.State()
	return smartModelStatus{downloading: st.Downloading, percent: st.Percent(), problem: st.Problem}
}

// embedder is the model from its files, each tower loaded when first
// needed; one for the life of the process.
func (m *localModel) embedder() modelserver.Embedder {
	m.loadOnce.Do(func() {
		lo := imagesembedder.LoadOptions{}
		if m.low {
			lo = imagesembedder.LoadOptions{NoArena: true, Threads: 2}
		}
		e := newEmbedModels(m.paths, lo, m.run)
		go e.releaseWhenIdle()
		m.loaded = e
	})
	return m.loaded
}

// wire is s as the model socket carries it to the users' instances.
func (s smartModelStatus) wire() modelserver.EmbedderState {
	if s.unavailable {
		return modelserver.EmbedderState{}
	}
	w := modelserver.EmbedderState{CanFetch: true, Downloading: s.downloading && !s.ready, Percent: s.percent}
	if s.problem != nil {
		w.Problem, w.Need, w.Left = s.problem.Kind, s.problem.Need, s.problem.Left
	}
	return w
}

// remoteModel is a user's instance's view of the primary's model.
type remoteModel struct {
	client *modelserver.Client
	who    string
}

// fromWire is the primary's answer as a status.
func fromWire(has bool, st modelserver.EmbedderState) smartModelStatus {
	if has {
		return smartModelStatus{ready: true}
	}
	if !st.CanFetch {
		return smartModelStatus{unavailable: true}
	}
	s := smartModelStatus{downloading: st.Downloading, percent: min(max(st.Percent, 0), 100)}
	if st.Problem != "" {
		s.problem = &modelfetch.Problem{Kind: st.Problem, Need: st.Need, Left: st.Left}
	}
	return s
}

func (r *remoteModel) want(on bool) (smartModelStatus, error) {
	has, st, err := r.client.FetchEmbedder(r.who, on)
	if errors.Is(err, modelserver.ErrNoEmbedder) {
		// A primary older than the download on demand: it has the model,
		// or this device can't get it.
		if has, err := r.client.HasEmbedder(); err == nil && has {
			return smartModelStatus{ready: true}, nil
		}
		return smartModelStatus{unavailable: true}, nil
	}
	if err != nil {
		return smartModelStatus{}, err
	}
	return fromWire(has, st), nil
}

func (r *remoteModel) status() smartModelStatus {
	has, st, err := r.client.EmbedderStatus()
	if err != nil {
		log.Debug("smart search: could not ask the primary instance about the model:", err)
		return smartModelStatus{}
	}
	return fromWire(has, st)
}

func (r *remoteModel) embedder() modelserver.Embedder { return r.client }

// embedderBox holds the model an instance uses, which can appear while it
// runs (a download that ends).
type embedderBox struct{ e modelserver.Embedder }

// smartEmbedder is Smart search's model, nil while this instance has none.
func (mg *Manager) smartEmbedder() modelserver.Embedder {
	if mg == nil {
		return nil
	}
	if b := mg.embedder.Load(); b != nil {
		return b.e
	}
	return nil
}

func (mg *Manager) setEmbedder(e modelserver.Embedder) {
	mg.embedder.Store(&embedderBox{e: e})
}

// smartBackfill describes the photos with ses in the background
// (BackfillEmbeddings); tests replace it.
var smartBackfill = func(mg *Manager, ses *session.Session) { go mg.BackfillEmbeddings(ses) }

// smartWait is Smart search turned on before its model was there: the
// session to describe the photos with once it is, and a backfill left for
// the next sign-in.
type smartWait struct {
	mu   sync.Mutex
	ses  *session.Session
	turn uint64
	// gen is the password generation ses was held in: a session from
	// before a password change isn't used.
	gen uint64
	// pending: the model came with nobody's session to describe the photos
	// with; the next sign-in does it (SmartSearchSignedIn).
	pending atomic.Bool
}

// initSmartModel sets up Smart search's model on the primary: from its
// files when they are there - loaded when first needed - and otherwise
// downloaded once an instance turns Smart search on. low and run as
// newEmbedModels'.
func (mg *Manager) initSmartModel(low bool, run *sync.Mutex) {
	local := newLocalModel(smartModelConfig(), low, run, mg.adoptSmartModel)
	mg.smartLocal, mg.smartSrc = local, local
	switch {
	case local.ready():
		mg.setEmbedder(local.embedder())
		log.Info("Smart search available (its model loads when first needed)")
	case local.fetch == nil:
		log.Info("Smart search not available: its model isn't here, and there is nowhere to download it")
	default:
		log.Info("Smart search: its model is downloaded when Smart search is turned on")
	}
	go mg.resumeSmartModel()
}

// initRemoteSmartModel is a user's instance's: the primary's, when it has
// it, and asked for while this instance has Smart search on.
func (mg *Manager) initRemoteSmartModel(client *modelserver.Client) {
	mg.smartSrc = &remoteModel{client: client, who: os.Getenv(supervisor.ChildEnvVar)}
	if has, err := client.HasEmbedder(); err == nil && has {
		mg.setEmbedder(client)
	}
	go mg.watchSmartModel()
}

// resumeSmartModel starts the download at start when Smart search was left
// on without its model (a download that a restart or an update cut short).
func (mg *Manager) resumeSmartModel() {
	if mg.smartSrc == nil || mg.smartEmbedder() != nil || !mg.SmartSearchEnabled() {
		return
	}
	log.Info("smart search: on, without its model yet: downloading it")
	if st, err := mg.smartSrc.want(true); err == nil && st.ready {
		mg.adoptSmartModel()
	}
}

// watchSmartModel is a user's instance asking the primary for the model
// every cSmartPoll while its Smart search is on and it has none: that
// keeps the primary downloading it, and finds it once it is there.
func (mg *Manager) watchSmartModel() {
	for {
		if mg.smartEmbedder() == nil && mg.SmartSearchEnabled() {
			if st, err := mg.smartSrc.want(true); err != nil {
				log.Debug("smart search: could not ask the primary instance for the model:", err)
			} else if st.ready {
				mg.adoptSmartModel()
			}
		}
		time.Sleep(cSmartPoll)
	}
}

// serveSmartModel answers a user's instance asking for the model (who, its
// user) or saying it no longer wants it, over the model socket.
func (mg *Manager) serveSmartModel(who string, want bool) modelserver.EmbedderState {
	st := mg.smartLocal.wantFor(who, want)
	if st.ready {
		mg.adoptSmartModel()
	}
	return st.wire()
}

// smartModelWire is where the model is, for the model socket's "info".
func (mg *Manager) smartModelWire() modelserver.EmbedderState {
	st := mg.smartLocal.status()
	if st.ready {
		mg.adoptSmartModel()
	}
	return st.wire()
}

// adoptSmartModel makes the model this instance's once its source has it,
// and describes the photos when Smart search is on: with the session that
// turned it on, or at the next sign-in.
func (mg *Manager) adoptSmartModel() {
	if mg.smartSrc == nil {
		return
	}
	w := &mg.smart.wait
	w.mu.Lock()
	if mg.smartEmbedder() != nil {
		w.mu.Unlock()
		return
	}
	mg.setEmbedder(mg.smartSrc.embedder())
	ses, turn, gen := w.ses, w.turn, w.gen
	w.ses = nil
	w.mu.Unlock()
	log.Info("Smart search available: its model is here")
	if !mg.SmartSearchEnabled() {
		return
	}
	if ses != nil && turn == mg.smart.turn.Load() && gen == session.PasswordGeneration() {
		smartBackfill(mg, ses)
		return
	}
	w.pending.Store(true)
}

// holdForModel keeps ses to describe the photos with once the model is
// there; with the model there already it describes them now.
func (mg *Manager) holdForModel(ses *session.Session, turn uint64) {
	w := &mg.smart.wait
	w.mu.Lock()
	if mg.smartEmbedder() != nil {
		w.mu.Unlock()
		if ses != nil {
			smartBackfill(mg, ses)
		}
		return
	}
	w.ses, w.turn, w.gen = ses, turn, session.PasswordGeneration()
	w.mu.Unlock()
	log.Info("smart search: on, its model is downloaded first")
}

// dropHeld forgets the session held for the model (Smart search turned
// off).
func (mg *Manager) dropHeld() {
	w := &mg.smart.wait
	w.mu.Lock()
	w.ses = nil
	w.mu.Unlock()
	w.pending.Store(false)
}

// SmartSearchSignedIn is told of every sign-in: when the model came with
// nobody signed in to describe the photos with, ses does it.
func (mg *Manager) SmartSearchSignedIn(ses *session.Session) {
	if mg == nil || ses == nil || mg.smartSrc == nil || !mg.SmartSearchEnabled() {
		return
	}
	if mg.smartEmbedder() == nil {
		// Perhaps there by now (a user's instance hears of it at its next
		// ask).
		if !mg.smartSrc.status().ready {
			return
		}
		mg.adoptSmartModel()
	}
	if mg.smart.wait.pending.CompareAndSwap(true, false) {
		smartBackfill(mg, ses)
	}
}

// smartModelState is where the model is for this instance; while Smart
// search is on, asking for it (which keeps it downloading).
func (mg *Manager) smartModelState() smartModelStatus {
	if mg.smartEmbedder() != nil {
		return smartModelStatus{ready: true}
	}
	if mg == nil || mg.smartSrc == nil {
		return smartModelStatus{unavailable: true}
	}
	var st smartModelStatus
	if mg.SmartSearchEnabled() {
		var err error
		if st, err = mg.smartSrc.want(true); err != nil {
			log.Debug("smart search: could not ask for the model:", err)
			return smartModelStatus{}
		}
	} else {
		st = mg.smartSrc.status()
	}
	if st.ready {
		mg.adoptSmartModel()
	}
	return st
}

// SmartSearchModel is Settings.smart_search_model: whether the model is
// here, or how far its download is and why the last try failed, in lang.
func (mg *Manager) SmartSearchModel(lang string) *pb.SmartSearchModel {
	st := mg.smartModelState()
	out := &pb.SmartSearchModel{Ready: st.ready}
	if !st.ready && st.downloading {
		out.Downloading = true
		out.Percent = int32(min(max(st.percent, 0), 100))
		if st.problem != nil {
			out.Problem = smartModelProblem(st.problem).Render(lang)
		}
	}
	return out
}

// smartModelProblem is why the download's last try failed, in the device's
// words.
func smartModelProblem(p *modelfetch.Problem) i18n.Msg {
	switch p.Kind {
	case modelfetch.NoSpace:
		return i18n.DevFilesSmartSearchModelNoSpace(p.Need, p.Left)
	case modelfetch.Unreachable:
		return i18n.DevFilesSmartSearchModelUnreachable()
	}
	return i18n.DevFilesSmartSearchModelFailed()
}

// ErrSmartSearchDownloading is a text search while the model is still on
// its way (error_code "smart_search_downloading"); errors.As finds how far
// it is (SmartSearchDownloadPercent).
var ErrSmartSearchDownloading = errors.New("smart search's model is still downloading") // i18n-ignore: the reply's code and text are the websocket layer's

type smartDownloadingError struct{ percent int }

func (e *smartDownloadingError) Error() string        { return ErrSmartSearchDownloading.Error() }
func (e *smartDownloadingError) Is(target error) bool { return target == ErrSmartSearchDownloading }

// SmartSearchDownloadPercent is how far the model's download was when err
// (ErrSmartSearchDownloading) was answered.
func SmartSearchDownloadPercent(err error) int {
	var e *smartDownloadingError
	if errors.As(err, &e) {
		return e.percent
	}
	return 0
}

// smartSearchUsable is nil when a text search can run now - the model is
// here - and otherwise why it can't: still downloading, or unavailable.
// Smart search is on.
func (mg *Manager) smartSearchUsable() error {
	if mg.smartEmbedder() != nil {
		return nil
	}
	st := mg.smartModelState()
	switch {
	case st.ready:
		return nil
	case st.downloading:
		return &smartDownloadingError{percent: st.percent}
	}
	return ErrSmartSearchUnavailable
}
