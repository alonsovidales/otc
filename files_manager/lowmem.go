// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bufio"
	"context"
	"fmt"
	"image"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alonsovidales/otc/cfg"
	facerecognition "github.com/alonsovidales/otc/face_recognition"
	imagestagger "github.com/alonsovidales/otc/images_tagger"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/modelserver"
)

// The low-memory profile: a device with less memory than the product is
// built for (8 GB) runs the same features with less at once. Measured for
// one 12 MP photo (ONNX Runtime 1.24.3, OpenCV 4.10, arm64):
//
//   - the RAM++ tagging model: ~0.85-1.0 GB resident once loaded, ~1.4 GB
//     while it loads; a run needs ~650 MB more, and with ONNX Runtime's
//     memory arena on (the default) that stays allocated after it - three
//     runs at once, the lanes' three analysis workers, added ~1.3 GB;
//   - face detection (YuNet at 1600 px): ~100 MB its detector keeps;
//   - the Go side: a HEIC's decode ~45 MB, its JPEG ~30 MB, the JPEG's
//     decode ~18 MB, an orientation ~47 MB, the big thumbnail's scaler
//     ~96 MB, the tagger's 384 px scale ~37 MB, the faces' BGR copy ~35 MB;
//   - ffmpeg for a 4K HEVC frame, its own process: ~260-280 MB with one
//     or two decoding threads, ~680 MB with as many as it likes (16 cores).
//
// With all of that at once - three thumbnails, three analyses, three runs
// of the model, a Go heap allowed 40% of the memory - a 4 GB Raspberry Pi
// was killed by the kernel within two minutes of every start. Low-memory
// mode does one photo at a time, frees what the model and the detector
// hold once they have been idle a while, and gives Go a smaller share.

// cLowMemoryBelow: a device with less memory than this is low-memory unless
// [otc] low-memory says otherwise. Raspberry Pi 5s come with 2, 4, 8 or 16
// GB (MemTotal a little under: 3.7 GB on a 4 GB board, 7.9 on an 8 GB
// one), so 6 GB sits between the two that matter: everything the product
// runs at once on 8 GB adds up to more than 4 GB, while one photo at a
// time fits a 4 GB board with room for MariaDB and the system.
const cLowMemoryBelow = 6 << 30

// EnvLowMemory carries the primary instance's low-memory choice to the
// instances it supervises when [otc] low-memory set it: their own config
// files don't have the key.
const EnvLowMemory = "OTC_LOW_MEMORY"

// cLowGoMemoryShare is the Go runtime's soft memory limit in low-memory
// mode, as a share of the memory: 20% (~760 MB on a 4 GB board) holds one
// photo's processing (~200 MB), the content budget's downloads (256 MB),
// the uploads arriving and the rest, while the models' native memory -
// which no Go limit sees - stays within what is left. The normal profile
// gives Go 40% (bin/otc.go).
const cLowGoMemoryShare = 0.20

// cLowContentBudgetShare: the content budget (membudget.go) in low-memory
// mode is a sixteenth of the memory, at least the usual 256 MB floor -
// the floor on a 4 GB board.
const cLowContentBudgetShare = 16

// cModelIdle is how long a low-memory device keeps the models loaded with
// nothing to analyse. Loading RAM++ takes ~15 s on a Raspberry Pi 5, once
// per batch of uploads.
const cModelIdle = 10 * time.Minute

// cFFmpegLowThreads: ffmpeg decodes a frame with this many threads in
// low-memory mode (each 4K thread holds its own frames).
const cFFmpegLowThreads = "2"

// MemoryProfile is how much this device does at once.
type MemoryProfile struct {
	// Low: one photo at a time, the models released when idle - see the
	// top of this file.
	Low bool
	// Total is the machine's memory (MemTotal), 0 when unknown.
	Total int64
	// Source is why: "auto" (by Total), "config" ([otc] low-memory) or
	// "primary" (the supervising instance's choice, EnvLowMemory).
	Source string
}

// memoryProfileFor decides the profile from the machine's memory, the
// [otc] low-memory setting (auto, on, off; anything else is auto) and the
// primary instance's choice (EnvLowMemory: "on"/"off", or empty).
func memoryProfileFor(total int64, setting, inherited string) MemoryProfile {
	p := MemoryProfile{Total: total, Source: "auto", Low: total > 0 && total < cLowMemoryBelow}
	switch strings.ToLower(strings.TrimSpace(inherited)) {
	case "on":
		p.Low, p.Source = true, "primary"
		return p
	case "off":
		p.Low, p.Source = false, "primary"
		return p
	}
	switch strings.ToLower(strings.TrimSpace(setting)) {
	case "on", "true", "1", "yes":
		p.Low, p.Source = true, "config"
	case "off", "false", "0", "no":
		p.Low, p.Source = false, "config"
	}
	return p
}

var (
	profileOnce sync.Once
	profile     MemoryProfile
)

// CurrentMemoryProfile is this process's profile, decided once - after
// cfg.Init, since [otc] low-memory can set it.
func CurrentMemoryProfile() MemoryProfile {
	profileOnce.Do(func() {
		setting := ""
		if cfg.HasSection("otc") {
			setting = cfg.GetStr("otc", "low-memory")
		}
		profile = memoryProfileFor(memTotalBytes(), setting, os.Getenv(EnvLowMemory))
	})
	return profile
}

// GoMemoryLimit is the Go runtime's soft limit in low-memory mode.
func (p MemoryProfile) GoMemoryLimit() int64 {
	return int64(float64(p.Total) * cLowGoMemoryShare)
}

// lowContentBudget is the content budget in low-memory mode.
func (p MemoryProfile) lowContentBudget() int64 {
	const floor = 256 << 20
	return max(p.Total/cLowContentBudgetShare, floor)
}

// NominalGB is the memory as the device is sold: MemTotal is a little
// under (3.7 GB on a 4 GB board), never a whole GB under.
func (p MemoryProfile) NominalGB() int {
	return int(math.Ceil(float64(p.Total) / (1 << 30)))
}

// Describe is the line the start logs; "" for a device with the memory
// the product is built for and no setting, which logs nothing new.
func (p MemoryProfile) Describe() string {
	size := "an unknown amount of memory"
	if p.Total > 0 {
		size = fmt.Sprintf("%d GB of memory (%d MB)", p.NominalGB(), p.Total>>20)
	}
	switch {
	case p.Low && p.Source == "auto":
		return fmt.Sprintf("low-memory mode: %s - photos are processed one at a time, the tagging and face models are released after %d idle minutes, Go is limited to %d MB (8 GB recommended)", size, int(cModelIdle.Minutes()), p.GoMemoryLimit()>>20)
	case p.Low:
		return fmt.Sprintf("low-memory mode (set by %s): %s - photos are processed one at a time, the tagging and face models are released after %d idle minutes, Go is limited to %d MB", p.Source, size, int(cModelIdle.Minutes()), p.GoMemoryLimit()>>20)
	case p.Source != "auto":
		return fmt.Sprintf("low-memory mode off (set by %s): %s", p.Source, size)
	}
	return ""
}

// ExportForChildren passes a configured choice on to the supervised
// instances (EnvLowMemory); an automatic one they make the same way.
func (p MemoryProfile) ExportForChildren() {
	if p.Source != "config" {
		return
	}
	v := "off"
	if p.Low {
		v = "on"
	}
	os.Setenv(EnvLowMemory, v)
}

// jobKind is what a processing job is, for the guard.
type jobKind int

const (
	// jobThumbnail: an upload's thumbnail (the fast lane). Never paused: a
	// new photo stays hidden until it has one (#147), and a post of it
	// waits for it.
	jobThumbnail jobKind = iota
	// jobAnalysis: tags and faces (the slow lane).
	jobAnalysis
	// jobBackfill: BackfillMissingThumbnails' rebuild of a file.
	jobBackfill
	// jobReprocess: a file of the owner's full Reprocess.
	jobReprocess
	// jobThumbFix: a small thumbnail made from the big one (the queue and
	// the pass, which wait for the pause on their own: waitForQuiet).
	jobThumbFix
	// jobTranscode: a post's video re-encoded. Not tracked: nothing about
	// it is retried by itself.
	jobTranscode
)

// paused: the kinds that wait out the pause after a death.
func (k jobKind) paused() bool {
	return k == jobAnalysis || k == jobBackfill || k == jobReprocess
}

// processingGuard makes media processing gentler: on a low-memory device
// (one job at a time, the content in flight recorded), and for the first
// hour of a run that follows one that died (the same, after a pause when
// the death looked like processing's - runstate.go). nil - every device
// with the memory the product is built for, after a clean stop - leaves
// processing as it was.
type processingGuard struct {
	// resumeAt: no paused kind of job starts before it.
	resumeAt time.Time
	// slot lets one job run at a time while limited is set.
	slot    chan struct{}
	limited atomic.Bool
	// track, while tracking is set, records the content being processed
	// (on) and done with (off), for runstate.go to know what a death
	// interrupted.
	track    func(hash string, on bool)
	tracking atomic.Bool
	// after, when set, runs after each job (the memory log).
	after func()
	// sleep is time.Sleep, replaced by tests.
	sleep func(time.Duration)
	// lowMemory: the low-memory profile's guard, whose turn a post's
	// transcode takes too (a recovery's doesn't: minutes of it would hold
	// every thumbnail back).
	lowMemory bool
}

func newProcessingGuard(track func(hash string, on bool), lowMemory bool) *processingGuard {
	hw := &memoryHighWater{}
	g := &processingGuard{slot: make(chan struct{}, 1), track: track, sleep: time.Sleep, after: hw.check, lowMemory: lowMemory}
	g.limited.Store(true)
	g.tracking.Store(track != nil)
	return g
}

// relax ends a recovery's limits: jobs run as many at once as before and
// nothing more is recorded. Jobs already running finish as they began.
func (g *processingGuard) relax() {
	g.limited.Store(false)
	g.tracking.Store(false)
}

// cPausePoll is how often a paused job that can be called off looks.
const cPausePoll = time.Second

// beginProcessing waits for the guard to let a job of kind on hash start
// and returns the call that ends it. Never nil.
func (mg *Manager) beginProcessing(hash string, kind jobKind) (done func()) {
	done, _ = mg.beginProcessingUnless(hash, kind, nil)
	return done
}

// beginProcessingUnless is beginProcessing for a job that can be called
// off while it waits out a pause (a Reprocess, by Cancel): ok is false,
// and done a no-op, when stop said so.
func (mg *Manager) beginProcessingUnless(hash string, kind jobKind, stop func() bool) (done func(), ok bool) {
	g := mg.guard
	if g == nil {
		return func() {}, true
	}
	if kind.paused() {
		for d := time.Until(g.resumeAt); d > 0; d = time.Until(g.resumeAt) {
			if stop == nil {
				g.sleep(d)
				break
			}
			if stop() {
				return func() {}, false
			}
			g.sleep(min(d, cPausePoll))
		}
	}
	held := g.limited.Load() && (kind != jobTranscode || g.lowMemory)
	if held {
		g.slot <- struct{}{}
	}
	tracked := g.track != nil && g.tracking.Load() && kind != jobTranscode && hash != ""
	if tracked {
		g.track(hash, true)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			if tracked {
				g.track(hash, false)
			}
			if held {
				<-g.slot
			}
			if g.after != nil {
				g.after()
			}
		})
	}, true
}

// processingDeferred: processing is paused after a run that died (the
// thumbnails pass and queue wait for it too).
func (mg *Manager) processingDeferred() bool {
	return mg.guard != nil && time.Now().Before(mg.guard.resumeAt)
}

// ffmpegThreads, when set, is how many threads ffmpeg decodes a frame with
// ("-threads"); ffmpeg's own choice when empty.
var ffmpegThreads string

// ffmpegInput is the arguments that open path for ffmpeg: "-i path", after
// "-threads n" in low-memory mode.
func ffmpegInput(path string) []string {
	if ffmpegThreads == "" {
		return []string{"-i", path}
	}
	return []string{"-threads", ffmpegThreads, "-i", path}
}

// taggerModel and faceModel are loaded models that can be let go:
// *imagestagger.RAMTagger and *facerecognition.Recognizer, or tests' fakes.
type taggerModel interface {
	TagsResized(ctx context.Context, img *image.RGBA, opt imagestagger.RAMOptions) ([]imagestagger.RAMTag, error)
	Close()
}

type faceModel interface {
	DetectFaces(img image.Image) ([]facerecognition.FaceDetection, error)
	Close()
}

// idleModels holds a low-memory device's tagging and face models: loaded
// when processing first needs them, released once neither has been used
// for idle, and run one at a time - tags or faces, for this instance or
// the ones it supervises (modelserver) - so two runs never add up.
type idleModels struct {
	mu       sync.Mutex // the fields below
	run      sync.Mutex // one model call at a time
	idle     time.Duration
	now      func() time.Time
	loadTags func() (taggerModel, error)
	loadFace func() (faceModel, error) // nil: no face models
	tags     taggerModel
	faces    faceModel
	inUse    int
	lastUse  time.Time
	// loaded, when set, runs after a model loads (the memory log).
	loaded func(what string, took time.Duration)
	// loading, when set, is told a load starts (true) and ends: the run
	// marker's, so a death during a load - its peak - is put down to the
	// load, not to the file being processed (runstate.go).
	loading func(on bool)
}

// load runs f, telling m.loading.
func (m *idleModels) load(f func() error) error {
	if m.loading != nil {
		m.loading(true)
		defer m.loading(false)
	}
	return f()
}

func newIdleModels(loadTags func() (taggerModel, error), loadFace func() (faceModel, error), idle time.Duration) *idleModels {
	return &idleModels{loadTags: loadTags, loadFace: loadFace, idle: idle, now: time.Now}
}

// use returns the model get picks, loaded, and the call that says it is no
// longer in use.
func (m *idleModels) use(get func() (any, error)) (any, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	model, err := get()
	if err != nil {
		return nil, nil, err
	}
	m.inUse++
	var once sync.Once
	return model, func() {
		once.Do(func() {
			m.mu.Lock()
			m.inUse--
			m.lastUse = m.now()
			m.mu.Unlock()
		})
	}, nil
}

// taggerLocked is the tagging model, loading it if it isn't. m.mu held.
func (m *idleModels) taggerLocked() (any, error) {
	if m.tags == nil {
		started := m.now()
		var t taggerModel
		err := m.load(func() (err error) { t, err = m.loadTags(); return err })
		if err != nil {
			return nil, fmt.Errorf("loading the tagging model: %w", err)
		}
		m.tags = t
		if m.loaded != nil {
			m.loaded("tagging model", m.now().Sub(started))
		}
	}
	return m.tags, nil
}

// facesLocked is the face models, loading them if they aren't. m.mu held.
func (m *idleModels) facesLocked() (any, error) {
	if m.faces == nil {
		started := m.now()
		var f faceModel
		err := m.load(func() (err error) { f, err = m.loadFace(); return err })
		if err != nil {
			return nil, fmt.Errorf("loading the face models: %w", err)
		}
		m.faces = f
		if m.loaded != nil {
			m.loaded("face models", m.now().Sub(started))
		}
	}
	return m.faces, nil
}

// Tags is imagestagger.RAMTagger.Tags: the photo is scaled to the model's
// input outside the model's turn.
func (m *idleModels) Tags(ctx context.Context, img image.Image, opt imagestagger.RAMOptions) ([]imagestagger.RAMTag, error) {
	if opt.ImageSize == 0 {
		opt.ImageSize = imagestagger.DefaultRAMOptions().ImageSize
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return m.TagsResized(ctx, imagestagger.Resize(img, opt.ImageSize), opt)
}

// TagsResized is imagestagger.RAMTagger.TagsResized, in the model's turn.
func (m *idleModels) TagsResized(ctx context.Context, img *image.RGBA, opt imagestagger.RAMOptions) ([]imagestagger.RAMTag, error) {
	model, done, err := m.use(m.taggerLocked)
	if err != nil {
		return nil, err
	}
	defer done()
	m.run.Lock()
	defer m.run.Unlock()
	return model.(taggerModel).TagsResized(ctx, img, opt)
}

// DetectFaces is facerecognition.Recognizer.DetectFaces, in the model's
// turn.
func (m *idleModels) DetectFaces(img image.Image) ([]facerecognition.FaceDetection, error) {
	model, done, err := m.use(m.facesLocked)
	if err != nil {
		return nil, err
	}
	defer done()
	m.run.Lock()
	defer m.run.Unlock()
	return model.(faceModel).DetectFaces(img)
}

// adoptFaces takes face models already loaded (Init checks they load).
func (m *idleModels) adoptFaces(f faceModel) {
	m.mu.Lock()
	m.faces = f
	m.lastUse = m.now()
	m.mu.Unlock()
}

// releaseIdle lets the models go when nothing has used them for m.idle;
// it reports what it released.
func (m *idleModels) releaseIdle() (released []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inUse > 0 || m.now().Sub(m.lastUse) < m.idle {
		return nil
	}
	if m.tags != nil {
		m.tags.Close()
		m.tags = nil
		released = append(released, "tagging model")
	}
	if m.faces != nil {
		m.faces.Close()
		m.faces = nil
		released = append(released, "face models")
	}
	return released
}

// releaseWhenIdle checks every minute, for good.
func (m *idleModels) releaseWhenIdle() {
	for range time.Tick(time.Minute) {
		if released := m.releaseIdle(); len(released) > 0 {
			log.Info(fmt.Sprintf("released the %s after %d idle minutes (%s)", strings.Join(released, " and "), int(m.idle.Minutes()), memoryInUse()))
		}
	}
}

// ensureTagger loads the tagging model if it isn't (an error is the next
// call's to report).
func (m *idleModels) ensureTagger() {
	if _, done, err := m.use(m.taggerLocked); err == nil {
		done()
	}
}

// initIdleModels is Init's models on a low-memory device: the tagging
// model loads when the first photo needs it - not at start, which is when
// a phone that was waiting starts sending its backlog - in ONNX Runtime
// without its memory arena; the face models load at start, as they
// always have, to know whether there are any. Both go after cModelIdle
// unused, and the instances this one supervises get them one request at
// a time.
func (mg *Manager) initIdleModels() {
	models := newIdleModels(func() (taggerModel, error) {
		t, err := imagestagger.NewRAMTaggerWith(
			cfg.GetStr("tagger", "model-path"),
			cfg.GetStr("tagger", "tags-path"),
			cfg.GetStr("tagger", "thresholds-path"),
			imagestagger.DefaultRAMOptions(),
			imagestagger.LoadOptions{NoArena: true},
		)
		if err != nil {
			log.Error("could not load the tagging model:", err)
			return nil, err
		}
		return t, nil
	}, nil, cModelIdle)
	models.loaded = func(what string, took time.Duration) {
		log.Info(what, "loaded in", took.Round(time.Millisecond), "-", memoryInUse())
	}
	if mg.run != nil {
		models.loading = mg.run.setLoading
	}
	mg.tagger = models
	close(mg.taggerReady)

	// As Init does it (see there).
	if cfg.HasSection("faces") {
		det, rec := cfg.GetStr("faces", "detector-model-path"), cfg.GetStr("faces", "recognizer-model-path")
		r, err := facerecognition.NewRecognizer(det, rec)
		if err != nil {
			log.Info("Face recognition not available (issue #52 stays off until this is configured):", err)
		} else {
			models.loadFace = func() (faceModel, error) {
				r, err := facerecognition.NewRecognizer(det, rec)
				if err != nil {
					return nil, err
				}
				return r, nil
			}
			models.adoptFaces(r)
			mg.faceRecognizer = models
		}
	} else {
		log.Info("Face recognition not available ([faces] section not configured)")
	}

	go models.releaseWhenIdle()
	go func() {
		if err := serveModels(modelserver.SocketPath(), mg.waitForTagger, mg.faceRecognizer, 1); err != nil {
			log.Error("could not serve the models to the other instances:", err)
		}
	}()
}

// serveModels is modelserver.ServeLimited, replaced by tests.
var serveModels = modelserver.ServeLimited

// procStatusKB is a "Name:  N kB" line of a /proc status-like file.
func procStatusKB(path, name string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == name+":" {
			if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
				return kb
			}
		}
	}
	return 0
}

// memoryInUse is this process's resident memory and the device's
// available memory, for the log: what to look at on a low-memory device.
func memoryInUse() string {
	rss := procStatusKB("/proc/self/status", "VmRSS")
	avail := procStatusKB("/proc/meminfo", "MemAvailable")
	if rss == 0 {
		return "memory in use unknown"
	}
	return fmt.Sprintf("this process holds %d MB, %d MB available on the device", rss>>10, avail>>10)
}

// memoryHighWater logs each time the process holds 128 MB more than it
// ever did (low-memory mode, after each job).
type memoryHighWater struct {
	mu   sync.Mutex
	high int64
}

func (h *memoryHighWater) check() {
	rss := procStatusKB("/proc/self/status", "VmRSS") << 10
	h.mu.Lock()
	defer h.mu.Unlock()
	if rss == 0 || rss < h.high+128<<20 {
		return
	}
	h.high = rss
	log.Info("memory high-water mark:", memoryInUse())
}
