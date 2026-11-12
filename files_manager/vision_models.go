// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"context"
	"errors"
	"fmt"
	"image"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/modelfetch"
	"github.com/alonsovidales/otc/modelserver"
	petfinder "github.com/alonsovidales/otc/pet_finder"
	"github.com/alonsovidales/otc/supervisor"
	textreader "github.com/alonsovidales/otc/text_reader"
)

// Text in photos' and Pets' models (release 180): PP-OCRv6 small reads the
// text (text_reader), D-FINE small finds the dogs and cats and DINOv2 small
// tells them apart (pet_finder) - about 97 MB in five files, all
// Apache-2.0. Like Smart search's model (smart_model.go) they aren't fetched
// by an update: the primary instance downloads them once an instance wants
// them - one with Text in photos or Pets on, both on by default - in the
// background at Smart search's capped speed (package modelfetch), from the
// "models" release on GitHub, into [vision] download-dir (by default
// models/vision in the primary's working directory: /var/lib/otc on a
// Raspberry Pi, the state volume in the container). Each is loaded when
// first needed, without ONNX Runtime's arena, and released after
// cVisionIdle unused. A user's instance uses the primary's over the model
// socket ("text", "pets"), asking for the download ("vision-fetch") every
// cSmartPoll while it wants them and the primary hasn't them, as it asks
// for Smart search's.

// visionModelFiles are the models as the "models" release publishes them:
// each file's name here and there, its size and SHA-256 - the files of
// PaddlePaddle/PP-OCRv6_small_det_onnx and _rec_onnx (inference.onnx, and
// the reader's inference.yml, its list of characters),
// onnx-community/dfine_s_coco-ONNX (onnx/model.onnx) and
// onnx-community/dinov2-small (onnx/model_int8.onnx) on Hugging Face,
// unchanged.
var visionModelFiles = []modelfetch.File{
	{Name: "ppocrv6-small-det.onnx", Asset: "ppocrv6-small-det.onnx", Size: 9880512, SHA256: "d73e0058b7a8086bbd57f3d10b8bcd4ff95363f67e06e2762b5e814fe9c9410e"},
	{Name: "ppocrv6-small-rec.onnx", Asset: "ppocrv6-small-rec.onnx", Size: 21159378, SHA256: "5435fd747c9e0efe15a96d0b378d5bd157e9492ed8fd80edf08f30d02fa24634"},
	{Name: "ppocrv6-small-rec.yml", Asset: "ppocrv6-small-rec.yml", Size: 150579, SHA256: "ab078671bb49f06228eadccd34f1bb501e157f7a047095ffb943ba81512c77d1"},
	{Name: "dfine-s-coco.onnx", Asset: "dfine-s-coco.onnx", Size: 41535197, SHA256: "cd8a49a945feda6d28c6304ae8ae85c2759ba1d78a5a83a22c5ce8db82ef7238"},
	{Name: "dinov2-s-int8.onnx", Asset: "dinov2-s-int8.onnx", Size: 24446700, SHA256: "dfce54a839b491f395c516350ebb4a78f947e9170a6beac0f2bc5638e0f09d61"},
}

const (
	// cVisionModelDir is where visionModelFiles are downloaded, under the
	// primary's working directory, unless [vision] download-dir says
	// otherwise.
	cVisionModelDir = "models/vision"
	// cVisionIdle: a model is released after this long unused.
	cVisionIdle = 10 * time.Minute
	// cVisionTimeout bounds a photo's turn at a model, the wait included.
	cVisionTimeout = 3 * time.Minute
	// cTextMaxPixels and cPetMaxPixels: the most pixels of a photo the
	// models are given (shrinkTo: halved, thirded... until it fits). The
	// text reader cuts each line out of what it is given, so it gets the
	// photo itself up to 16 MP - a phone's 12 MP whole, a 48 MP one at
	// 12 - and the pet models, which look at 640 and 224 px, at most 4 MP.
	// Also what crosses the model socket from a user's instance.
	cTextMaxPixels = 16 << 20
	cPetMaxPixels  = 4 << 20
)

// errVisionUnavailable is a photo the vision models couldn't look at: they
// aren't on this device yet.
var errVisionUnavailable = errors.New("the text and pet models are not on this device yet") // i18n-ignore: internal, logged

// visionConf is [vision]'s settings (absent: the defaults).
type visionConf struct {
	// dir is where the models are downloaded, "" when there is nowhere.
	dir         string
	url         string
	bytesPerSec int64
}

// visionModelConfig reads [vision], as smartModelConfig reads [embedder].
func visionModelConfig() visionConf {
	c := visionConf{url: cSmartModelURL, bytesPerSec: cSmartModelKBps << 10}
	if cfg.HasSection("vision") {
		c.dir = strings.TrimSpace(cfg.GetStr("vision", "download-dir"))
		if v := strings.TrimSpace(cfg.GetStr("vision", "model-url")); v != "" {
			if u, err := url.Parse(v); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
				c.url = v
			} else {
				log.Error("[vision] model-url isn't an http(s) address, using the default")
			}
		}
		if v := strings.TrimSpace(cfg.GetStr("vision", "download-kbps")); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 && n < 1<<40 {
				c.bytesPerSec = n << 10
			} else {
				log.Error("[vision] download-kbps isn't a number of kilobytes a second, using", cSmartModelKBps)
			}
		}
	}
	if c.dir == "" {
		if wd, err := os.Getwd(); err == nil {
			c.dir = filepath.Join(wd, cVisionModelDir)
		}
	} else if abs, err := filepath.Abs(c.dir); err == nil {
		c.dir = abs
	} else {
		c.dir = ""
	}
	return c
}

// visionSource is where an instance's vision models come from: the
// device's own download (localVision, the primary), or the primary's
// (remoteVision, a user's instance).
type visionSource interface {
	// want says whether this instance has Text in photos or Pets on - wants
	// the models - and is where they are.
	want(on bool) smartModelStatus
	// models is what to use once want or status says they are ready.
	models() modelserver.Vision
	status() smartModelStatus
}

// localVision is the primary's: the files, their download, who wants them,
// and the models once loaded.
type localVision struct {
	dir   string
	fetch *modelfetch.Fetcher
	low   bool
	run   *sync.Mutex

	loadOnce sync.Once
	loaded   *visionModels

	// opMu: one change of who wants them - and the Start or Stop it makes -
	// at a time.
	opMu   sync.Mutex
	mu     sync.Mutex
	self   bool
	leases map[string]time.Time
	now    func() time.Time
}

// newLocalVision is the primary's models as c says, onReady called once a
// download has put every file in place. low: a low-memory device, whose
// models run one at a time under run (the idle models' lock).
func newLocalVision(c visionConf, low bool, run *sync.Mutex, onReady func()) *localVision {
	v := &localVision{dir: c.dir, low: low, run: run, leases: map[string]time.Time{}, now: time.Now}
	if c.dir != "" {
		v.fetch = modelfetch.New(modelfetch.Config{
			Name:           "the text and pet models", // i18n-ignore: a log line
			Dir:            c.dir,
			BaseURL:        c.url,
			Files:          visionModelFiles,
			BytesPerSecond: c.bytesPerSec,
			Room:           smartModelRoom,
			Wanted:         v.wanted,
			OnReady:        onReady,
		})
	}
	return v
}

func (v *localVision) ready() bool { return v.fetch != nil && v.fetch.Ready() }

func (v *localVision) wanted() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.wantedLocked()
}

func (v *localVision) wantedLocked() bool {
	if v.self {
		return true
	}
	now := v.now()
	wanted := false
	for who, at := range v.leases {
		if now.Sub(at) < cSmartLease {
			wanted = true
		} else {
			delete(v.leases, who)
		}
	}
	return wanted
}

// wantFor records whether who ("" for the primary itself, else a user's
// instance) wants the models, starts the download when they aren't there,
// and stops it once nobody wants them.
func (v *localVision) wantFor(who string, on bool) smartModelStatus {
	v.opMu.Lock()
	defer v.opMu.Unlock()
	v.mu.Lock()
	switch {
	case who == "":
		v.self = on
	case on:
		v.leases[who] = v.now()
	default:
		delete(v.leases, who)
	}
	wanted := v.wantedLocked()
	v.mu.Unlock()
	if v.fetch != nil && !v.ready() {
		if on {
			v.fetch.Start()
		} else if !wanted {
			v.fetch.Stop()
		}
	}
	return v.status()
}

func (v *localVision) want(on bool) smartModelStatus { return v.wantFor("", on) }

func (v *localVision) status() smartModelStatus {
	if v.ready() {
		return smartModelStatus{ready: true}
	}
	if v.fetch == nil {
		return smartModelStatus{unavailable: true}
	}
	st := v.fetch.State()
	return smartModelStatus{downloading: st.Downloading, percent: st.Percent(), problem: st.Problem}
}

// models is the models from their files, each loaded when first needed;
// one for the life of the process.
func (v *localVision) models() modelserver.Vision {
	v.loadOnce.Do(func() {
		in := func(name string) string { return filepath.Join(v.dir, name) }
		threads := 0
		if v.low {
			threads = 2
		}
		m := &visionModels{
			run:  v.run,
			idle: cVisionIdle,
			now:  time.Now,
			loadReader: func() (visionReader, error) {
				return textreader.Load(textreader.Files{
					Detector: in("ppocrv6-small-det.onnx"), Recognizer: in("ppocrv6-small-rec.onnx"), Dictionary: in("ppocrv6-small-rec.yml"),
				}, textreader.LoadOptions{NoArena: true, Threads: threads})
			},
			loadFinder: func() (visionFinder, error) {
				return petfinder.Load(petfinder.Files{Detector: in("dfine-s-coco.onnx"), Embedder: in("dinov2-s-int8.onnx")},
					petfinder.LoadOptions{NoArena: true, Threads: threads})
			},
		}
		if m.run == nil {
			m.run = &sync.Mutex{}
		}
		go m.releaseWhenIdle()
		v.loaded = m
	})
	return v.loaded
}

// remoteVision is a user's instance's view of the primary's models.
type remoteVision struct {
	client *modelserver.Client
	who    string
}

func (r *remoteVision) want(on bool) smartModelStatus {
	has, st, err := r.client.FetchVision(r.who, on)
	if errors.Is(err, modelserver.ErrNoVision) {
		// A primary older than them: never there.
		return smartModelStatus{unavailable: true}
	}
	if err != nil {
		log.Debug("text and pets: could not ask the primary instance for their models:", err)
		return smartModelStatus{}
	}
	return fromWire(has, st)
}

func (r *remoteVision) status() smartModelStatus {
	has, st, err := r.client.VisionStatus()
	if err != nil {
		return smartModelStatus{}
	}
	return fromWire(has, st)
}

func (r *remoteVision) models() modelserver.Vision { return r.client }

// visionReader and visionFinder are a loaded model: text_reader's and
// pet_finder's, or tests'.
type visionReader interface {
	Read(ctx context.Context, img image.Image) ([]textreader.Line, error)
	Close()
}

type visionFinder interface {
	Find(ctx context.Context, img image.Image) ([]petfinder.Pet, error)
	Close()
}

// visionModels is the primary's models from their files: each loaded when
// first needed and released after idle unused, run one call at a time -
// with the other models' calls too on a low-memory device (run). It serves
// the users' instances through the model socket as it serves this one.
type visionModels struct {
	mu         sync.Mutex
	run        *sync.Mutex
	idle       time.Duration
	now        func() time.Time
	loadReader func() (visionReader, error)
	loadFinder func() (visionFinder, error)
	reader     visionReader
	finder     visionFinder
	inUse      int
	lastUse    time.Time
}

// begin is get under mu, then one more use until done.
func (m *visionModels) begin(get func() error) (done func(), err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := get(); err != nil {
		return nil, err
	}
	m.inUse++
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			m.inUse--
			m.lastUse = m.now()
			m.mu.Unlock()
		})
	}, nil
}

// ReadText is the text reader's lines of img.
func (m *visionModels) ReadText(ctx context.Context, img image.Image) ([]textreader.Line, error) {
	var r visionReader
	done, err := m.begin(func() error {
		if m.reader == nil {
			started := m.now()
			loaded, err := m.loadReader()
			if err != nil {
				return fmt.Errorf("loading the text model: %w", err) // i18n-ignore: internal, logged
			}
			m.reader = loaded
			log.Info("text in photos: model loaded in", m.now().Sub(started).Round(time.Millisecond))
		}
		r = m.reader
		return nil
	})
	if err != nil {
		return nil, err
	}
	defer done()
	m.run.Lock()
	defer m.run.Unlock()
	return r.Read(ctx, img)
}

// FindPets is the pet models' pets in img.
func (m *visionModels) FindPets(ctx context.Context, img image.Image) ([]petfinder.Pet, error) {
	var f visionFinder
	done, err := m.begin(func() error {
		if m.finder == nil {
			started := m.now()
			loaded, err := m.loadFinder()
			if err != nil {
				return fmt.Errorf("loading the pet models: %w", err) // i18n-ignore: internal, logged
			}
			m.finder = loaded
			log.Info("pets: models loaded in", m.now().Sub(started).Round(time.Millisecond))
		}
		f = m.finder
		return nil
	})
	if err != nil {
		return nil, err
	}
	defer done()
	m.run.Lock()
	defer m.run.Unlock()
	return f.Find(ctx, img)
}

// releaseIdle releases the models once nothing has used them for idle.
func (m *visionModels) releaseIdle() (released []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inUse > 0 || m.now().Sub(m.lastUse) < m.idle {
		return nil
	}
	if m.reader != nil {
		m.reader.Close()
		m.reader = nil
		released = append(released, "text") // i18n-ignore: a log line
	}
	if m.finder != nil {
		m.finder.Close()
		m.finder = nil
		released = append(released, "pets") // i18n-ignore: a log line
	}
	return released
}

func (m *visionModels) releaseWhenIdle() {
	for range time.Tick(time.Minute) {
		if r := m.releaseIdle(); len(r) > 0 {
			log.Info("text and pets: released the idle model(s):", strings.Join(r, ", "))
		}
	}
}

// visionBox holds the models an instance uses, which appear while it runs
// (a download that ends).
type visionBox struct{ v modelserver.Vision }

// visionModel is the vision models, nil while this instance has none.
func (mg *Manager) visionModel() modelserver.Vision {
	if mg == nil {
		return nil
	}
	if b := mg.vision.Load(); b != nil {
		return b.v
	}
	return nil
}

func (mg *Manager) setVision(v modelserver.Vision) { mg.vision.Store(&visionBox{v: v}) }

// initVision sets up the vision models on the primary: from their files
// when they are there - loaded when first needed - and otherwise
// downloaded once an instance wants them. low and run as newLocalVision's.
func (mg *Manager) initVision(low bool, run *sync.Mutex) {
	local := newLocalVision(visionModelConfig(), low, run, mg.adoptVision)
	mg.visionLocal, mg.visionSrc = local, local
	switch {
	case local.ready():
		mg.setVision(local.models())
		log.Info("Text in photos and Pets available (their models load when first needed)")
	case local.fetch == nil:
		log.Info("Text in photos and Pets not available: their models aren't here, and there is nowhere to download them")
	}
}

// initRemoteVision is a user's instance's: the primary's, when it has them.
func (mg *Manager) initRemoteVision(client *modelserver.Client) {
	mg.visionSrc = &remoteVision{client: client, who: os.Getenv(supervisor.ChildEnvVar)}
	if has, _, err := client.VisionStatus(); err == nil && has {
		mg.setVision(client)
	}
}

// adoptVision is the primary's download ending: the models are there.
func (mg *Manager) adoptVision() {
	if mg.visionLocal != nil && mg.visionLocal.ready() {
		mg.setVision(mg.visionLocal.models())
		log.Info("text and pets: their models are here")
	}
}

// visionWanted is whether this instance wants the vision models: Text in
// photos or Pets on.
func (mg *Manager) visionWanted() bool {
	return mg.TextInPhotosEnabled() || mg.PetsEnabled()
}

// wantVision tells the models' source whether this instance wants them
// (its switches), at start and whenever one changes. A user's instance
// keeps asking every cSmartPoll while it wants them and they aren't there
// (watchVision).
func (mg *Manager) wantVision() {
	if mg == nil || mg.visionSrc == nil {
		return
	}
	on := mg.visionWanted()
	if !on && mg.visionModel() != nil {
		return // there already: nothing to stop
	}
	st := mg.visionSrc.want(on)
	if st.ready && mg.visionModel() == nil {
		mg.setVision(mg.visionSrc.models())
	}
	if on && !st.ready && !st.unavailable {
		mg.watchVisionOnce.Do(func() { go mg.watchVision() })
	}
}

// watchVision asks for the models every cSmartPoll while this instance
// wants them and hasn't them - for the primary's own download it only
// waits (the fetcher retries by itself); a user's instance renews its
// lease and adopts the primary's models once they are there.
func (mg *Manager) watchVision() {
	t := time.NewTicker(cSmartPoll)
	defer t.Stop()
	for range t.C {
		if mg.visionModel() != nil {
			return
		}
		on := mg.visionWanted()
		if !on {
			if _, remote := mg.visionSrc.(*remoteVision); remote {
				mg.visionSrc.want(false)
			}
			continue
		}
		if st := mg.visionSrc.want(true); st.ready {
			mg.setVision(mg.visionSrc.models())
			log.Info("text and pets: their models are here")
			return
		}
	}
}

// serveVision is "vision-fetch" for a user's instance (who).
func (mg *Manager) serveVision(who string, want bool) modelserver.EmbedderState {
	if mg.visionLocal == nil {
		return modelserver.EmbedderState{}
	}
	return mg.visionLocal.wantFor(who, want).wire()
}

// visionWire is where the primary's models are, for "info".
func (mg *Manager) visionWire() modelserver.EmbedderState {
	if mg.visionLocal == nil {
		return modelserver.EmbedderState{}
	}
	return mg.visionLocal.status().wire()
}

// readText is the lines of text in img, by this instance's text model
// (errVisionUnavailable without it), img brought down to cTextMaxPixels.
func (mg *Manager) readText(img image.Image) ([]textreader.Line, error) {
	v := mg.visionModel()
	if v == nil {
		return nil, errVisionUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), cVisionTimeout)
	defer cancel()
	small := shrinkTo(img, cTextMaxPixels)
	lines, err := v.ReadText(ctx, small)
	if err != nil {
		return nil, err
	}
	// In the photo's own pixels, as Info shows them later.
	if b, s := img.Bounds(), small.Bounds(); s.Dx() != b.Dx() && s.Dx() > 0 {
		k := float32(b.Dx()) / float32(s.Dx())
		for i := range lines {
			for j := range lines[i].Quad {
				lines[i].Quad[j] *= k
			}
		}
	}
	return lines, nil
}

// findPets is the pets in img, by this instance's pet models
// (errVisionUnavailable without them), img brought down to cPetMaxPixels;
// their boxes in img's own pixels.
func (mg *Manager) findPets(img image.Image) ([]petfinder.Pet, error) {
	v := mg.visionModel()
	if v == nil {
		return nil, errVisionUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), cVisionTimeout)
	defer cancel()
	small := shrinkTo(img, cPetMaxPixels)
	pets, err := v.FindPets(ctx, small)
	if err != nil {
		return nil, err
	}
	if b, s := img.Bounds(), small.Bounds(); s.Dx() != b.Dx() && s.Dx() > 0 {
		kx, ky := float64(b.Dx())/float64(s.Dx()), float64(b.Dy())/float64(s.Dy())
		for i := range pets {
			p := &pets[i]
			p.X, p.Y = int(float64(p.X)*kx), int(float64(p.Y)*ky)
			p.W, p.H = int(float64(p.W)*kx), int(float64(p.H)*ky)
		}
	}
	return pets, nil
}
