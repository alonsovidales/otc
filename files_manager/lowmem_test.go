// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"context"
	"errors"
	"image"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	facerecognition "github.com/alonsovidales/otc/face_recognition"
	imagestagger "github.com/alonsovidales/otc/images_tagger"
	"github.com/alonsovidales/otc/modelserver"
	pb "github.com/alonsovidales/otc/proto/generated"
)

const (
	pi4GB = 3789 << 20      // a 4 GB Raspberry Pi 5's MemTotal (ceres)
	pi8GB = 8_245_000 << 10 // an 8 GB one's, roughly
)

func TestMemoryProfileFor(t *testing.T) {
	for _, c := range []struct {
		total              int64
		setting, inherited string
		low                bool
		source             string
	}{
		{pi4GB, "", "", true, "auto"},
		{pi8GB, "", "", false, "auto"},
		{6<<30 - 1, "", "", true, "auto"},
		{6 << 30, "", "", false, "auto"},
		{0, "", "", false, "auto"}, // unknown (not Linux): as it always was
		{pi4GB, "auto", "", true, "auto"},
		{pi4GB, "garbage", "", true, "auto"},
		{pi4GB, "off", "", false, "config"},
		{pi4GB, " OFF ", "", false, "config"},
		{pi8GB, "on", "", true, "config"},
		{pi8GB, "true", "", true, "config"},
		{pi8GB, "", "on", true, "primary"},
		{pi4GB, "on", "off", false, "primary"}, // the primary's choice wins
		{pi4GB, "", "bogus", true, "auto"},
	} {
		p := memoryProfileFor(c.total, c.setting, c.inherited)
		if p.Low != c.low || p.Source != c.source || p.Total != c.total {
			t.Errorf("memoryProfileFor(%d, %q, %q) = %+v, want low %v from %s", c.total, c.setting, c.inherited, p, c.low, c.source)
		}
	}
}

func TestLowMemoryShares(t *testing.T) {
	p := memoryProfileFor(pi4GB, "", "")
	total := int64(pi4GB)
	if got, want := p.GoMemoryLimit(), int64(float64(total)*0.2); got != want {
		t.Errorf("Go limit %d, want %d (20%%)", got, want)
	}
	if got := p.GoMemoryLimit() >> 20; got != 757 {
		t.Errorf("Go limit on a 4 GB Pi %d MB, want 757", got)
	}
	// A sixteenth, at least the usual 256 MB floor - the floor at 4 GB.
	if got := p.lowContentBudget(); got != 256<<20 {
		t.Errorf("content budget on a 4 GB Pi %d MB, want 256", got>>20)
	}
	if got := memoryProfileFor(5<<30+512<<20, "", "").lowContentBudget(); got != (5<<30+512<<20)/16 {
		t.Errorf("content budget at 5.5 GB %d MB, want a sixteenth", got>>20)
	}
	for total, want := range map[int64]int{pi4GB: 4, pi8GB: 8, 1900 << 20: 2, 15_900 << 20: 16} {
		if got := (MemoryProfile{Total: total}).NominalGB(); got != want {
			t.Errorf("NominalGB(%d MB) = %d, want %d", total>>20, got, want)
		}
	}
}

func TestMemoryProfileDescribe(t *testing.T) {
	if got := memoryProfileFor(pi8GB, "", "").Describe(); got != "" {
		t.Errorf("an 8 GB device logs %q, want nothing new", got)
	}
	got := memoryProfileFor(pi4GB, "", "").Describe()
	if !strings.HasPrefix(got, "low-memory mode: 4 GB of memory (3789 MB)") || !strings.Contains(got, "one at a time") || !strings.Contains(got, "757 MB") {
		t.Errorf("4 GB: %q", got)
	}
	if got := memoryProfileFor(pi4GB, "off", "").Describe(); !strings.HasPrefix(got, "low-memory mode off (set by config): 4 GB") {
		t.Errorf("4 GB, off by config: %q", got)
	}
	if got := memoryProfileFor(pi8GB, "on", "").Describe(); !strings.HasPrefix(got, "low-memory mode (set by config): 8 GB") {
		t.Errorf("8 GB, on by config: %q", got)
	}
}

// The normal profile leaves processing exactly as it was: no guard, as
// many workers per lane as before, ffmpeg's arguments unchanged.
func TestNormalProfileChangesNothing(t *testing.T) {
	mg := &Manager{}
	if mg.profileGuard(memoryProfileFor(pi8GB, "", ""), nil) != nil {
		t.Fatal("an 8 GB device got a processing guard")
	}
	start := time.Now()
	for _, k := range []jobKind{jobThumbnail, jobAnalysis, jobBackfill, jobReprocess, jobThumbFix, jobTranscode} {
		mg.beginProcessing("h", k)()
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Error("beginProcessing waited without a guard")
	}
	if got := ffmpegInput("/v"); !slices.Equal(got, []string{"-i", "/v"}) {
		t.Errorf("ffmpeg input %q", got)
	}
}

func TestFFmpegThreadsInLowMemory(t *testing.T) {
	ffmpegThreads = cFFmpegLowThreads
	defer func() { ffmpegThreads = "" }()
	if got := ffmpegInput("/v"); !slices.Equal(got, []string{"-threads", "2", "-i", "/v"}) {
		t.Errorf("ffmpeg input %q", got)
	}
}

// concurrency records how many calls are inside at once.
type concurrency struct {
	now, peak atomic.Int32
}

func (c *concurrency) enter() {
	n := c.now.Add(1)
	for {
		p := c.peak.Load()
		if n <= p || c.peak.CompareAndSwap(p, n) {
			return
		}
	}
}

func (c *concurrency) leave() { c.now.Add(-1) }

// In low-memory mode the lanes - thumbnails and analysis alike - and the
// backfill or a reprocess run one job at a time, and the content being
// processed is tracked.
func TestLowMemoryProcessesOneJobAtATime(t *testing.T) {
	mg := &Manager{}
	var mu sync.Mutex
	var tracked []string
	mg.guard = newProcessingGuard(func(hash string, on bool) {
		mu.Lock()
		defer mu.Unlock()
		if on {
			tracked = append(tracked, "+"+hash)
		} else {
			tracked = append(tracked, "-"+hash)
		}
	}, true)

	var c concurrency
	var wg sync.WaitGroup
	job := func(kind jobKind) func(j mediaJob) {
		return func(j mediaJob) {
			defer wg.Done()
			defer mg.beginProcessing(j.file.Hash, kind)()
			c.enter()
			time.Sleep(5 * time.Millisecond)
			c.leave()
		}
	}
	thumb, analyse := job(jobThumbnail), job(jobAnalysis)
	ls := newMediaLanes(processingWorkers(), func(j mediaJob) bool { thumb(j); return true }, analyse)
	for _, h := range []string{"a", "b", "c", "d"} {
		wg.Add(2) // its thumbnail and its analysis
		ls.fast.push(mediaJob{file: &pb.File{Hash: h}})
	}
	// A backfill alongside.
	for _, h := range []string{"x", "y"} {
		wg.Add(1)
		go job(jobBackfill)(mediaJob{file: &pb.File{Hash: h}})
	}
	wg.Wait()
	if p := c.peak.Load(); p != 1 {
		t.Errorf("%d jobs ran at once, want 1", p)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(tracked) != 20 {
		t.Fatalf("tracked %v, want 10 jobs in and out", tracked)
	}
	for i := 0; i < len(tracked); i += 2 {
		if tracked[i][0] != '+' || tracked[i+1] != "-"+tracked[i][1:] {
			t.Fatalf("tracked %v: a job started before the last ended", tracked)
		}
	}
}

// After a run that died, the analysis, the backfill and Reprocess wait
// until resumeAt; an upload's thumbnail, a thumbnail fix and a post's
// transcode never do.
func TestProcessingWaitsToResume(t *testing.T) {
	mg := &Manager{}
	g := mg.profileGuard(MemoryProfile{Low: true}, nil)
	var slept time.Duration
	g.sleep = func(d time.Duration) { slept += d }
	g.resumeAt = time.Now().Add(2 * time.Minute)
	mg.guard = g
	if !mg.processingDeferred() {
		t.Error("processing isn't deferred before resumeAt")
	}
	for _, k := range []jobKind{jobThumbnail, jobThumbFix, jobTranscode} {
		mg.beginProcessing("h", k)()
	}
	if slept != 0 {
		t.Fatalf("a thumbnail waited %v", slept)
	}
	for _, k := range []jobKind{jobAnalysis, jobBackfill} {
		slept = 0
		mg.beginProcessing("h", k)()
		if slept < 119*time.Second || slept > 2*time.Minute {
			t.Errorf("kind %d slept %v, want ~2m", k, slept)
		}
	}
	g.resumeAt = time.Now().Add(-time.Second)
	if mg.processingDeferred() {
		t.Error("processing still deferred after resumeAt")
	}
}

// Cancel ends a Reprocess waiting out the pause.
func TestReprocessPauseCanBeCalledOff(t *testing.T) {
	mg := &Manager{}
	g := newProcessingGuard(nil, true)
	polls := 0
	g.sleep = func(d time.Duration) {
		polls++
		if d > cPausePoll {
			t.Errorf("slept %v at once", d)
		}
	}
	g.resumeAt = time.Now().Add(30 * time.Minute)
	mg.guard = g
	cancelled := false
	done, ok := mg.beginProcessingUnless("h", jobReprocess, func() bool { return cancelled || polls >= 3 })
	if ok {
		t.Fatal("a cancelled Reprocess got its turn")
	}
	done()
	if len(g.slot) != 0 {
		t.Error("a cancelled Reprocess holds the slot")
	}
}

// relax - an hour after a death, on a device with memory to spare - lets
// jobs run at once again and records nothing more; jobs that began limited
// end as they began.
func TestGuardRelaxes(t *testing.T) {
	mg := &Manager{}
	var tracked atomic.Int32
	g := newProcessingGuard(func(string, bool) { tracked.Add(1) }, false)
	mg.guard = g
	first := mg.beginProcessing("a", jobAnalysis) // holds the slot
	g.relax()
	second := mg.beginProcessing("b", jobAnalysis) // doesn't wait for it
	second()
	first()
	if n := tracked.Load(); n != 2 {
		t.Errorf("%d track calls, want the first job's 2", n)
	}
	if len(g.slot) != 0 {
		t.Error("the slot is still held")
	}
}

// A transcode and a thumbnail fix take the one-job turn on a low-memory
// device; the fix is recorded as in flight, the transcode isn't. In an
// 8 GB device's hour after a death the transcode doesn't hold the turn.
func TestTranscodeAndThumbFixTakeTheTurn(t *testing.T) {
	mg := &Manager{}
	var mu sync.Mutex
	var tracked []string
	mg.guard = newProcessingGuard(func(h string, on bool) {
		mu.Lock()
		defer mu.Unlock()
		if on {
			tracked = append(tracked, h)
		}
	}, true)
	done := mg.beginProcessing("", jobTranscode)
	got := make(chan struct{})
	go func() {
		mg.beginProcessing("f", jobThumbFix)()
		close(got)
	}()
	select {
	case <-got:
		t.Fatal("a thumbnail fix ran during a transcode")
	case <-time.After(50 * time.Millisecond):
	}
	done()
	<-got
	mu.Lock()
	if !slices.Equal(tracked, []string{"f"}) {
		t.Errorf("tracked %v, want only the fix", tracked)
	}
	mu.Unlock()

	mg.guard = newProcessingGuard(nil, false)
	done = mg.beginProcessing("", jobTranscode)
	if len(mg.guard.slot) != 0 {
		t.Error("a recovery's transcode holds the turn")
	}
	done()
}

type fakeTagModel struct {
	c      *concurrency
	closed atomic.Bool
	got    image.Rectangle
}

func (f *fakeTagModel) TagsResized(ctx context.Context, img *image.RGBA, opt imagestagger.RAMOptions) ([]imagestagger.RAMTag, error) {
	f.c.enter()
	defer f.c.leave()
	f.got = img.Rect
	time.Sleep(5 * time.Millisecond)
	return []imagestagger.RAMTag{{Name: "dog", Score: 0.9}}, ctx.Err()
}

func (f *fakeTagModel) Close() { f.closed.Store(true) }

type fakeFaceModel struct {
	c      *concurrency
	closed atomic.Bool
}

func (f *fakeFaceModel) DetectFaces(img image.Image) ([]facerecognition.FaceDetection, error) {
	f.c.enter()
	defer f.c.leave()
	time.Sleep(5 * time.Millisecond)
	return nil, nil
}

func (f *fakeFaceModel) Close() { f.closed.Store(true) }

type fakeModels struct {
	c                   concurrency
	tagLoads, faceLoads atomic.Int32
	tags                []*fakeTagModel
	faces               []*fakeFaceModel
	mu                  sync.Mutex
	failTags            atomic.Bool
	clock               time.Time
	models              *idleModels
}

func newFakeModels() *fakeModels {
	f := &fakeModels{clock: time.Unix(1_700_000_000, 0)}
	f.models = newIdleModels(func() (taggerModel, error) {
		f.tagLoads.Add(1)
		if f.failTags.Load() {
			return nil, errors.New("no model")
		}
		t := &fakeTagModel{c: &f.c}
		f.mu.Lock()
		f.tags = append(f.tags, t)
		f.mu.Unlock()
		return t, nil
	}, func() (faceModel, error) {
		f.faceLoads.Add(1)
		m := &fakeFaceModel{c: &f.c}
		f.mu.Lock()
		f.faces = append(f.faces, m)
		f.mu.Unlock()
		return m, nil
	}, cModelIdle)
	f.models.now = func() time.Time {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.clock
	}
	return f
}

func (f *fakeModels) advance(d time.Duration) {
	f.mu.Lock()
	f.clock = f.clock.Add(d)
	f.mu.Unlock()
}

// The tagging model loads at first use, once, and the photo is scaled to
// the model's input before its turn.
func TestIdleModelsLoadOnFirstUse(t *testing.T) {
	f := newFakeModels()
	if f.tagLoads.Load() != 0 {
		t.Fatal("loaded before any use")
	}
	img := image.NewRGBA(image.Rect(0, 0, 4032, 3024))
	for i := 0; i < 3; i++ {
		tags, err := f.models.Tags(context.Background(), img, imagestagger.DefaultRAMOptions())
		if err != nil || len(tags) != 1 {
			t.Fatalf("Tags = %v, %v", tags, err)
		}
	}
	if n := f.tagLoads.Load(); n != 1 {
		t.Errorf("loaded %d times, want 1", n)
	}
	if got := f.tags[0].got; got != image.Rect(0, 0, 384, 384) {
		t.Errorf("the model got %v, want 384x384", got)
	}
}

// Tags and faces, from this instance and the ones it supervises, never
// run at once.
func TestIdleModelsRunOneAtATime(t *testing.T) {
	f := newFakeModels()
	small := image.NewRGBA(image.Rect(0, 0, 384, 384))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			f.models.TagsResized(context.Background(), small, imagestagger.DefaultRAMOptions())
		}()
		go func() {
			defer wg.Done()
			f.models.DetectFaces(small)
		}()
	}
	wg.Wait()
	if p := f.c.peak.Load(); p != 1 {
		t.Errorf("%d model calls at once, want 1", p)
	}
}

// Idle for cModelIdle, both models go; the next use loads them again.
func TestIdleModelsReleaseWhenIdle(t *testing.T) {
	f := newFakeModels()
	small := image.NewRGBA(image.Rect(0, 0, 384, 384))
	f.models.TagsResized(context.Background(), small, imagestagger.DefaultRAMOptions())
	f.models.DetectFaces(small)

	f.advance(cModelIdle - time.Second)
	if got := f.models.releaseIdle(); got != nil {
		t.Fatalf("released %v before the idle time", got)
	}
	// In use: kept, however long.
	_, done, err := f.models.use(f.models.taggerLocked)
	if err != nil {
		t.Fatal(err)
	}
	f.advance(time.Hour)
	if got := f.models.releaseIdle(); got != nil {
		t.Fatalf("released %v while in use", got)
	}
	done()
	f.advance(cModelIdle)
	got := f.models.releaseIdle()
	if !slices.Equal(got, []string{"tagging model", "face models"}) {
		t.Fatalf("released %v", got)
	}
	if !f.tags[0].closed.Load() || !f.faces[0].closed.Load() {
		t.Error("a released model wasn't closed")
	}
	if got := f.models.releaseIdle(); got != nil {
		t.Errorf("released %v again", got)
	}

	f.models.TagsResized(context.Background(), small, imagestagger.DefaultRAMOptions())
	f.models.DetectFaces(small)
	if f.tagLoads.Load() != 2 || f.faceLoads.Load() != 2 {
		t.Errorf("loads after a release: tags %d, faces %d, want 2 and 2", f.tagLoads.Load(), f.faceLoads.Load())
	}
}

// A model that won't load is the call's error (the photo's alert), and
// the next call tries again.
func TestIdleModelsLoadError(t *testing.T) {
	f := newFakeModels()
	f.failTags.Store(true)
	small := image.NewRGBA(image.Rect(0, 0, 384, 384))
	if _, err := f.models.TagsResized(context.Background(), small, imagestagger.DefaultRAMOptions()); err == nil {
		t.Fatal("no error from a model that won't load")
	}
	f.failTags.Store(false)
	if _, err := f.models.TagsResized(context.Background(), small, imagestagger.DefaultRAMOptions()); err != nil {
		t.Fatal(err)
	}
	if n := f.tagLoads.Load(); n != 2 {
		t.Errorf("%d loads, want 2", n)
	}
}

// A load is announced to the run marker, so a death during it isn't put
// down to the file in flight.
func TestIdleModelsMarkTheLoad(t *testing.T) {
	f := newFakeModels()
	var seq []bool
	f.models.loading = func(on bool) { seq = append(seq, on) }
	inner := f.models.loadTags
	f.models.loadTags = func() (taggerModel, error) {
		if len(seq) != 1 || !seq[0] {
			t.Error("loading without telling the marker")
		}
		return inner()
	}
	f.models.ensureTagger()
	f.models.ensureTagger() // loaded: nothing to tell
	if !slices.Equal(seq, []bool{true, false}) {
		t.Errorf("loading %v", seq)
	}
}

// Init's low-memory models: the idle models as the tagger, nothing loaded
// yet, the load wired to the run marker, faces unavailable without
// [faces], and the model server answering one request at a time.
func TestInitIdleModelsWiring(t *testing.T) {
	galleryTestEnv(t) // a config without [faces]
	k := newFakeKernel(t)
	served := make(chan int, 1)
	prev := serveModels
	serveModels = func(_ string, tagger func() modelserver.Tagger, faces modelserver.FaceDetector, limit int) error {
		if faces != nil {
			t.Error("faces served without [faces]")
		}
		served <- limit
		return nil
	}
	mg := &Manager{taggerReady: make(chan struct{})}
	mg.guard = mg.checkPreviousRun(k.env, memoryProfileFor(pi4GB, "", ""))
	mg.initIdleModels()
	var limit int
	select {
	case limit = <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("the models aren't served")
	}
	serveModels = prev
	if limit != 1 {
		t.Errorf("served with limit %d, want 1", limit)
	}
	m, ok := mg.tagger.(*idleModels)
	if !ok {
		t.Fatalf("tagger %T", mg.tagger)
	}
	select {
	case <-mg.taggerReady:
	default:
		t.Error("taggerReady still open")
	}
	if m.tags != nil || m.loading == nil || mg.faceRecognizer != nil {
		t.Errorf("loaded %v, loading hook %v, faces %v", m.tags != nil, m.loading != nil, mg.faceRecognizer)
	}
	mg.Stopped()
}

// waitForTagger loads the model before the caller's deadline starts: a
// load takes longer than a photo's 10 s.
func TestWaitForTaggerLoadsTheIdleModel(t *testing.T) {
	f := newFakeModels()
	mg := &Manager{tagger: f.models, taggerReady: make(chan struct{})}
	close(mg.taggerReady)
	if mg.waitForTagger() != f.models {
		t.Fatal("not the idle models")
	}
	if n := f.tagLoads.Load(); n != 1 {
		t.Errorf("%d loads, want 1", n)
	}
}
