// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	imagesembedder "github.com/alonsovidales/otc/images_embedder"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/modelserver"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
)

// Smart search (beta): photos found by describing them. While the user has
// it on (settings.smart_search_enabled, on by default since release 152)
// every photo is described as a vector by SigLIP 2's image tower
// (images_embedder) from its big thumbnail - by the slow lane after tags,
// and in the background for what the device held before - and kept sealed
// with the user's key (photo_embeddings). A search's text goes through the
// text tower, and the photos the other filters keep are ranked by how close
// their vectors are to it (rankByText). The vectors are decrypted into
// memory at the first text search and dropped after cSmartIndexIdle
// without one.
//
// The switch works as Places' (places_switch.go): every write of a vector
// holds mu for reading and checks the switch under it (smartWrite); turning
// it off holds it for writing and deletes every vector in the same
// transaction, so none lands after.

const (
	// cEmbedModel names the model that made a vector (photo_embeddings.model):
	// another model's are made again by the backfill.
	cEmbedModel = "siglip2-base-p16-256"
	// cEmbedFormat is a packed vector's first byte (packVec).
	cEmbedFormat = 1
	// cEmbedIdle: the towers are released after this long unused (both
	// modes: Smart search is off on most devices, and on one where it is on
	// they are needed in bursts).
	cEmbedIdle = 10 * time.Minute
	// cSmartIndexIdle: the decrypted vectors are dropped after this long
	// without a text search or a burst check.
	cSmartIndexIdle = 15 * time.Minute
	// cEmbedTimeout bounds a photo's or a query's turn at the model, the
	// wait included.
	cEmbedTimeout = time.Minute
	// cSmartBackfillPage and cSmartBackfillPause are the backfill's page
	// and the least it rests after a photo (as long as describing it took
	// when that was longer: about 0.2-0.3 s on a Pi 5).
	cSmartBackfillPage  = 500
	cSmartBackfillPause = 30 * time.Millisecond
	// The cutoff of a text search: a photo is kept when its cosine is at
	// least cSmartFloor and cSmartShare of the best one's. Measured on the
	// demo library (98 queries, 7 languages): precision 0.84, recall 0.87.
	cSmartFloor = 0.05
	cSmartShare = 0.7
	// cMaxSmartText is the longest text a search takes, in bytes (the model
	// reads 63 tokens).
	cMaxSmartText = 1000
)

// Memories' bursts when Smart search is on (memories.go's burstOf): two
// photos at least cSameCos alike are the same photo whenever they were
// taken; at least cBurstCos alike and within cEmbedBurstWindow, shots of
// one burst. Variants of a photo (a crop, a shift, brighter, a JPEG saved
// again) scored 0.964 at the least, different photos 0.922 at the most.
const (
	cSameCos          = 0.95
	cBurstCos         = 0.92
	cEmbedBurstWindow = 60 * time.Second
)

var (
	// ErrSmartSearchOff is a text search while Smart search is off
	// (error_code "smart_search_off").
	ErrSmartSearchOff = errors.New("smart search is off") // i18n-ignore: the reply's code and text are the websocket layer's
	// ErrSmartSearchUnavailable is Smart search on a device that can't get
	// its model (error_code "smart_search_unavailable"): nowhere to
	// download it, or a primary instance older than the download on
	// demand that hasn't it.
	ErrSmartSearchUnavailable = errors.New("smart search is not available on this device") // i18n-ignore: the reply's code and text are the websocket layer's
)

// imageTower and textTower are a loaded tower: images_embedder's, or tests'.
type imageTower interface {
	EmbedResized(ctx context.Context, img *image.RGBA) ([]float32, error)
	Close()
}

type textTower interface {
	EmbedText(ctx context.Context, text string) ([]float32, error)
	Close()
}

// embedModels is Smart search's model on the device that has its files:
// each tower loaded when first needed and released after idle unused, run
// one call at a time - with the other models' calls too on a low-memory
// device (run is idleModels'). It serves the users' instances through the
// model socket as it serves this one.
type embedModels struct {
	mu        sync.Mutex
	run       *sync.Mutex
	idle      time.Duration
	now       func() time.Time
	loadImage func() (imageTower, error)
	loadText  func() (textTower, error)
	img       imageTower
	txt       textTower
	inUse     int
	lastUse   time.Time
}

// newEmbedModels is f's towers, loaded with lo; run nil: their own lock.
func newEmbedModels(f embedderFiles, lo imagesembedder.LoadOptions, run *sync.Mutex) *embedModels {
	if run == nil {
		run = &sync.Mutex{}
	}
	return &embedModels{
		run:  run,
		idle: cEmbedIdle,
		now:  time.Now,
		loadImage: func() (imageTower, error) {
			return imagesembedder.NewImageEncoder(f.image, lo)
		},
		loadText: func() (textTower, error) {
			return imagesembedder.NewTextEncoder(f.text, f.tokenizer, lo)
		},
	}
}

// begin marks a call, loading what get picks; done ends it.
func (e *embedModels) begin(get func() error) (done func(), err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := get(); err != nil {
		return nil, err
	}
	e.inUse++
	var once sync.Once
	return func() {
		once.Do(func() {
			e.mu.Lock()
			e.inUse--
			e.lastUse = e.now()
			e.mu.Unlock()
		})
	}, nil
}

// EmbedResized is the image tower's vector of img (through
// imagesembedder.Resize).
func (e *embedModels) EmbedResized(ctx context.Context, img *image.RGBA) ([]float32, error) {
	var tower imageTower
	done, err := e.begin(func() error {
		if e.img == nil {
			started := e.now()
			t, err := e.loadImage()
			if err != nil {
				return fmt.Errorf("loading Smart search's image model: %w", err) // i18n-ignore: internal, logged
			}
			e.img = t
			log.Info("smart search: image model loaded in", e.now().Sub(started).Round(time.Millisecond))
		}
		tower = e.img
		return nil
	})
	if err != nil {
		return nil, err
	}
	defer done()
	e.run.Lock()
	defer e.run.Unlock()
	return tower.EmbedResized(ctx, img)
}

// EmbedText is the text tower's vector of text.
func (e *embedModels) EmbedText(ctx context.Context, text string) ([]float32, error) {
	var tower textTower
	done, err := e.begin(func() error {
		if e.txt == nil {
			started := e.now()
			t, err := e.loadText()
			if err != nil {
				return fmt.Errorf("loading Smart search's text model: %w", err) // i18n-ignore: internal, logged
			}
			e.txt = t
			log.Info("smart search: text model loaded in", e.now().Sub(started).Round(time.Millisecond))
		}
		tower = e.txt
		return nil
	})
	if err != nil {
		return nil, err
	}
	defer done()
	e.run.Lock()
	defer e.run.Unlock()
	return tower.EmbedText(ctx, text)
}

// releaseIdle releases the towers once nothing has used them for idle.
func (e *embedModels) releaseIdle() (released []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.inUse > 0 || e.now().Sub(e.lastUse) < e.idle {
		return nil
	}
	if e.img != nil {
		e.img.Close()
		e.img = nil
		released = append(released, "image") // i18n-ignore: a log line
	}
	if e.txt != nil {
		e.txt.Close()
		e.txt = nil
		released = append(released, "text") // i18n-ignore: a log line
	}
	return released
}

func (e *embedModels) releaseWhenIdle() {
	for range time.Tick(time.Minute) {
		if r := e.releaseIdle(); len(r) > 0 {
			log.Info("smart search: released the idle model(s):", strings.Join(r, ", "))
		}
	}
}

// smartState is Smart search's switch, its backfill and the decrypted
// vectors.
type smartState struct {
	mu     sync.RWMutex
	loadMu sync.Mutex
	loaded atomic.Bool
	on     atomic.Bool
	turn   atomic.Uint64
	// backfillMu: one backfill at a time.
	backfillMu sync.Mutex
	// reading: on, and this turn's backfill hasn't described everything
	// the device held (ListOfFiles.smart_search_reading).
	reading atomic.Bool
	index   smartIndex
	// wait: turned on before the model was there (smart_model.go).
	wait smartWait
}

// loadSmart reads the switch from the database once. A database before
// the release that added it reads as off; an error reading it is off for
// now and read again next time.
func (mg *Manager) loadSmart() {
	if mg == nil || mg.dao == nil {
		return
	}
	s := &mg.smart
	if s.loaded.Load() {
		return
	}
	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	if s.loaded.Load() {
		return
	}
	on, err := mg.dao.SmartSearchEnabled()
	if err != nil && !errors.Is(err, dao.ErrSchemaBehind) {
		log.Error("smart search: could not read whether it is on, off for now:", err)
		return
	}
	s.mu.Lock()
	if !s.loaded.Load() {
		s.on.Store(on)
		// Until the first sign-in's backfill has looked.
		s.reading.Store(on)
		s.loaded.Store(true)
	}
	s.mu.Unlock()
}

// SmartSearchEnabled is whether Smart search is on for this library.
func (mg *Manager) SmartSearchEnabled() bool {
	if mg == nil {
		return false
	}
	mg.loadSmart()
	return mg.smart.on.Load()
}

// SmartSearchAvailable is whether this instance has Smart search's model
// now (its own, or the primary's).
func (mg *Manager) SmartSearchAvailable() bool {
	return mg != nil && mg.smartEmbedder() != nil
}

// SmartSearchReading is ListOfFiles.smart_search_reading.
func (mg *Manager) SmartSearchReading() bool {
	return mg.SmartSearchEnabled() && mg.smart.reading.Load()
}

// SetSmartSearchEnabled turns Smart search on or off for this library. Off
// deletes every vector before it returns, drops the decrypted ones and
// stops the model's download unless another instance wants it; on
// describes what the device holds in the background, with ses's key - once
// the model is there: without it, on starts its download (smart_model.go)
// and the photos are described after it. ErrSmartSearchUnavailable when
// the device can't get the model at all.
func (mg *Manager) SetSmartSearchEnabled(ses *session.Session, on bool) error {
	mg.loadSmart()
	asked := false
	if on && mg.smartEmbedder() == nil {
		if mg.smartSrc == nil {
			return ErrSmartSearchUnavailable
		}
		st, err := mg.smartSrc.want(true)
		if err != nil {
			return err
		}
		if st.unavailable {
			return ErrSmartSearchUnavailable
		}
		if st.ready {
			mg.adoptSmartModel()
		}
		asked = true
	}
	s := &mg.smart
	s.mu.Lock()
	wasOn := s.on.Load()
	if err := mg.dao.SetSmartSearchEnabled(on); err != nil {
		s.mu.Unlock()
		// The download was asked for this, which didn't happen.
		if asked && !wasOn {
			mg.smartSrc.want(false)
		}
		return err
	}
	s.on.Store(on)
	s.loaded.Store(true)
	turn := s.turn.Add(1)
	s.reading.Store(on)
	s.mu.Unlock()
	s.index.drop()
	// Memories' bursts are told apart another way now.
	mg.memories.changed()
	if on {
		log.Info("smart search: on, describing the photos")
		mg.holdForModel(ses, turn)
	} else {
		mg.dropHeld()
		if mg.smartSrc != nil {
			if _, err := mg.smartSrc.want(false); err != nil {
				log.Error("smart search: could not tell the primary instance it is off here:", err)
			}
		}
		log.Info("smart search: off, every description deleted")
	}
	return nil
}

// smartWrite runs write while Smart search is on and still in turn, under
// the switch's read lock, and is whether it ran.
func (mg *Manager) smartWrite(turn uint64, write func() error) (bool, error) {
	mg.loadSmart()
	s := &mg.smart
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.on.Load() || s.turn.Load() != turn {
		return false, nil
	}
	return true, write()
}

// packVec is v as kept: cEmbedFormat, a float32 scale (little-endian) and
// one signed byte a number (v/scale rounded): 773 bytes for SigLIP 2 base.
// Measured on the demo library, searching with int8 vectors gives the same
// precision as fp32 (0.944 against 0.943).
func packVec(v []float32) []byte {
	var peak float32
	for _, x := range v {
		peak = max(peak, float32(math.Abs(float64(x))))
	}
	scale := peak / 127
	if scale == 0 {
		scale = 1
	}
	out := make([]byte, 5+len(v))
	out[0] = cEmbedFormat
	binary.LittleEndian.PutUint32(out[1:5], math.Float32bits(scale))
	for i, x := range v {
		q := math.Round(float64(x / scale))
		out[5+i] = byte(int8(max(-127, min(127, q))))
	}
	return out
}

// unpackVec reads a packed vector back.
func unpackVec(b []byte) (scale float32, q []int8, err error) {
	if len(b) < 6 || b[0] != cEmbedFormat {
		return 0, nil, errors.New("not a vector this device writes") // i18n-ignore: internal, logged
	}
	scale = math.Float32frombits(binary.LittleEndian.Uint32(b[1:5]))
	if math.IsNaN(float64(scale)) || math.IsInf(float64(scale), 0) || scale <= 0 {
		return 0, nil, errors.New("a vector with a bad scale") // i18n-ignore: internal, logged
	}
	q = make([]int8, len(b)-5)
	for i, x := range b[5:] {
		q[i] = int8(x)
	}
	return scale, q, nil
}

// smartIndex is every vector of the library, decrypted, while text
// searches use it: about 0.8 KB a photo.
type smartIndex struct {
	mu     sync.Mutex
	loaded bool
	dim    int
	pos    map[string]int
	scales []float32
	vecs   []int8
	timer  *time.Timer
}

// drop forgets every vector.
func (x *smartIndex) drop() {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.dropLocked()
}

func (x *smartIndex) dropLocked() {
	x.loaded, x.dim, x.pos, x.scales, x.vecs = false, 0, nil, nil, nil
	if x.timer != nil {
		x.timer.Stop()
		x.timer = nil
	}
}

// addLocked adds or replaces hash's vector. x.mu held.
func (x *smartIndex) addLocked(hash string, scale float32, q []int8) {
	if x.dim == 0 {
		x.dim = len(q)
	}
	if len(q) != x.dim {
		return
	}
	if i, ok := x.pos[hash]; ok {
		x.scales[i] = scale
		copy(x.vecs[i*x.dim:], q)
		return
	}
	x.pos[hash] = len(x.scales)
	x.scales = append(x.scales, scale)
	x.vecs = append(x.vecs, q...)
}

// add adds a vector just made, when the index is in memory.
func (x *smartIndex) add(hash string, packed []byte) {
	scale, q, err := unpackVec(packed)
	if err != nil {
		return
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.loaded {
		x.addLocked(hash, scale, q)
	}
}

// remove forgets hash's vector (deleted content); its room stays until the
// index is read again.
func (x *smartIndex) remove(hash string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.loaded {
		delete(x.pos, hash)
	}
}

// touchLocked keeps the index for cSmartIndexIdle more. x.mu held.
func (x *smartIndex) touchLocked() {
	if x.timer != nil {
		x.timer.Reset(cSmartIndexIdle)
		return
	}
	x.timer = time.AfterFunc(cSmartIndexIdle, func() {
		x.drop()
		log.Debug("smart search: the decrypted descriptions dropped after", cSmartIndexIdle, "unused")
	})
}

// cosine is a·b for a stored vector and a normalised query. x.mu held.
func (x *smartIndex) cosineLocked(i int, q []float32) float32 {
	row := x.vecs[i*x.dim : (i+1)*x.dim]
	var s float32
	for j, v := range row {
		s += float32(v) * q[j]
	}
	return s * x.scales[i]
}

// pairLocked is the cosine of two stored vectors, false when either isn't
// there. x.mu held.
func (x *smartIndex) pairLocked(a, b string) (float32, bool) {
	i, okA := x.pos[a]
	j, okB := x.pos[b]
	if !okA || !okB {
		return 0, false
	}
	ra, rb := x.vecs[i*x.dim:(i+1)*x.dim], x.vecs[j*x.dim:(j+1)*x.dim]
	var s int32
	for k := range ra {
		s += int32(ra[k]) * int32(rb[k])
	}
	return float32(s) * x.scales[i] * x.scales[j], true
}

// withIndex runs fn on the index, read into memory with ses's key first
// when it isn't.
func (mg *Manager) withIndex(ses *session.Session, fn func(x *smartIndex)) error {
	x := &mg.smart.index
	x.mu.Lock()
	defer x.mu.Unlock()
	if !x.loaded {
		if ses == nil {
			return errors.New("no key to read the descriptions with") // i18n-ignore: internal, logged
		}
		started := time.Now()
		x.pos = map[string]int{}
		bad := 0
		err := mg.dao.EachPhotoEmbedding(cEmbedModel, func(hash string, sealed []byte) error {
			plain, err := ses.Decrypt(sealed)
			if err != nil {
				bad++
				return nil
			}
			scale, q, err := unpackVec(plain)
			if err != nil {
				bad++
				return nil
			}
			x.addLocked(hash, scale, q)
			return nil
		})
		if err != nil && !errors.Is(err, dao.ErrSchemaBehind) {
			x.dropLocked()
			return err
		}
		x.loaded = true
		log.Info("smart search: read", len(x.pos), "description(s) in", time.Since(started).Round(time.Millisecond), "-", bad, "unreadable")
	}
	x.touchLocked()
	fn(x)
	return nil
}

// thumbnailVector is hash's vector, from its big thumbnail.
func (mg *Manager) thumbnailVector(ses *session.Session, hash string) ([]float32, error) {
	data, err := blobstore.ReadAll(thumbnailPath(hash), ses)
	if err != nil {
		return nil, err
	}
	img, err := decodeThumbnail(data)
	if err != nil {
		return nil, err
	}
	e := mg.smartEmbedder()
	if e == nil {
		return nil, ErrSmartSearchUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), cEmbedTimeout)
	defer cancel()
	return e.EmbedResized(ctx, imagesembedder.Resize(img))
}

// describe makes and keeps hash's vector, in turn. Whether it was kept.
func (mg *Manager) describe(ses *session.Session, hash string, turn uint64) (bool, error) {
	vec, err := mg.thumbnailVector(ses, hash)
	if err != nil {
		return false, err
	}
	packed := packVec(vec)
	sealed := ses.Encrypt(packed)
	return mg.smartWrite(turn, func() error {
		if err := mg.dao.SetPhotoEmbedding(hash, cEmbedModel, sealed); err != nil {
			return err
		}
		mg.smart.index.add(hash, packed)
		return nil
	})
}

// storeEmbedding describes a photo the slow lane has just analysed, from
// the thumbnail the fast lane wrote, while Smart search is on. A failure
// leaves no row: the next start's backfill describes it.
func (mg *Manager) storeEmbedding(ses *session.Session, file *pb.File) {
	if ses == nil || mg.smartEmbedder() == nil || !strings.HasPrefix(file.Mime, "image") || !mg.SmartSearchEnabled() {
		return
	}
	turn := mg.smart.turn.Load()
	if _, err := mg.describe(ses, file.Hash, turn); err != nil && !errors.Is(err, dao.ErrSchemaBehind) {
		log.Debug("smart search: could not describe", file.Hash, ":", err)
		return
	}
	mg.memories.changed()
}

// forgetEmbedding forgets the vector of content nothing uses any more.
func (mg *Manager) forgetEmbedding(hash string) {
	if err := mg.dao.DelPhotoEmbedding(hash); err != nil {
		log.Error("could not forget the description of deleted content", hash, ":", err)
	}
	mg.smart.index.remove(hash)
}

// BackfillEmbeddings describes every photo the device held before Smart
// search was turned on, once, in the background: after the first sign-in
// (it needs the key), when it is turned on, and when its model arrives
// (smart_model.go's adoptSmartModel). One run at a time; a second
// caller leaves it to the one running. Only photos Images may show, with a
// thumbnail; another model's vectors are made again. It gives way to
// uploads and the processing lanes (waitForQuiet), stops for a full
// reprocess or when the switch turns, and a restart before the end goes on
// at the next: what has a vector is never listed again.
func (mg *Manager) BackfillEmbeddings(ses *session.Session) {
	if mg == nil || mg.dao == nil || ses == nil || mg.smartEmbedder() == nil || !cfg.HasSection("otc") {
		return
	}
	s := &mg.smart
	if !s.backfillMu.TryLock() {
		return
	}
	defer s.backfillMu.Unlock()
	if !mg.SmartSearchEnabled() {
		return
	}
	turn := s.turn.Load()
	excluded, err := mg.OutOfImagesFolders()
	if err != nil {
		log.Error("smart search: could not read the folders kept out of Images, nothing described:", err)
		return
	}
	// Listed in Status while there is something to describe (background.go).
	var bg *BackgroundPass
	if n, err := mg.dao.CountPhotosToEmbed(cEmbedModel, excluded); err != nil || n > 0 {
		bg = mg.BeginBackground(BackgroundSmartSearch, n)
	}
	defer bg.End()
	started := time.Now()
	described, failed := 0, 0
	after := ""
	for {
		batch, err := mg.dao.PhotosToEmbed(after, cSmartBackfillPage, cEmbedModel, excluded)
		if errors.Is(err, dao.ErrSchemaBehind) {
			log.Info("smart search: the database has nowhere to keep the descriptions yet (the release's migration), nothing described")
			return
		}
		if err != nil {
			log.Error("smart search: error listing the photos to describe:", err)
			return
		}
		for _, file := range batch {
			after = file.Hash
			bg.Step(1)
			if s.turn.Load() != turn || !mg.SmartSearchEnabled() {
				log.Info("smart search: turned off, the describing stops after", described, "photo(s)")
				return
			}
			if !mg.waitForQuiet() {
				log.Info("smart search: stopping for a reprocess after", described, "photo(s)")
				return
			}
			if _, err := os.Stat(thumbnailPath(file.Hash)); err != nil {
				continue
			}
			done := mg.beginProcessing(file.Hash, jobThumbFix)
			t := time.Now()
			kept, err := mg.describe(ses, file.Hash, turn)
			done()
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					failed++
					log.Debug("smart search: could not describe", file.Hash, ":", err)
				}
				continue
			}
			if !kept {
				return // the switch turned meanwhile
			}
			described++
			time.Sleep(max(cSmartBackfillPause, time.Since(t)))
		}
		if len(batch) < cSmartBackfillPage {
			break
		}
	}
	if failed == 0 && s.turn.Load() == turn {
		s.reading.Store(false)
	}
	if described > 0 || failed > 0 {
		mg.memories.changed()
		log.Info("smart search: described", described, "photo(s) in", time.Since(started).Round(time.Second), "-", failed, "to describe again at the next start")
	}
}

// smartText is a search's text as the model takes it: trimmed, "" for
// none, cut at cMaxSmartText bytes (on a rune).
func smartText(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= cMaxSmartText {
		return text
	}
	cut := cMaxSmartText
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// scoredPhoto is a candidate of a text search with its cosine.
type scoredPhoto struct {
	idx   int
	score float32
}

// rankScores is the candidates a text search keeps, best first: a cosine
// of at least cSmartFloor and cSmartShare of the best one's; ties by the
// order they came in (newest first).
func rankScores(scored []scoredPhoto) []scoredPhoto {
	if len(scored) == 0 {
		return nil
	}
	top := scored[0].score
	for _, s := range scored {
		top = max(top, s.score)
	}
	cut := max(float32(cSmartFloor), cSmartShare*top)
	kept := make([]scoredPhoto, 0, len(scored))
	for _, s := range scored {
		if s.score >= cut {
			kept = append(kept, s)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].score > kept[j].score })
	return kept
}

// rankByText is files (a search's photos, one per content, newest first)
// ranked by how well they match text, best first, cut at rankScores'
// cutoff; a photo without a vector isn't among them.
func (mg *Manager) rankByText(ses *session.Session, text string, files []*pb.File) ([]*pb.File, error) {
	if !mg.SmartSearchEnabled() {
		return nil, ErrSmartSearchOff
	}
	if err := mg.smartSearchUsable(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), cEmbedTimeout)
	q, err := mg.smartEmbedder().EmbedText(ctx, text)
	cancel()
	if errors.Is(err, modelserver.ErrNoEmbedder) {
		return nil, ErrSmartSearchUnavailable
	}
	if err != nil {
		return nil, err
	}
	var scored []scoredPhoto
	err = mg.withIndex(ses, func(x *smartIndex) {
		if x.dim != 0 && len(q) != x.dim {
			return
		}
		for i, f := range files {
			if p, ok := x.pos[f.Hash]; ok {
				scored = append(scored, scoredPhoto{idx: i, score: x.cosineLocked(p, q)})
			}
		}
	})
	if err != nil {
		return nil, err
	}
	kept := rankScores(scored)
	out := make([]*pb.File, len(kept))
	for i, s := range kept {
		out[i] = files[s.idx]
	}
	log.Debug("smart search: kept", len(out), "of", len(files), "photo(s)")
	return out, nil
}

// smartSimilarity is how alike two photos are by their vectors, for
// Memories' bursts: nil while Smart search is off, unavailable or its
// vectors can't be read.
func (mg *Manager) smartSimilarity(ses *session.Session) func(a, b string) (float32, bool) {
	if ses == nil || !mg.SmartSearchEnabled() || mg.smartEmbedder() == nil {
		return nil
	}
	if err := mg.withIndex(ses, func(*smartIndex) {}); err != nil {
		log.Debug("smart search: the descriptions couldn't be read for the memories:", err)
		return nil
	}
	return func(a, b string) (float32, bool) {
		var c float32
		var ok bool
		mg.withIndex(ses, func(x *smartIndex) { c, ok = x.pairLocked(a, b) })
		return c, ok
	}
}
