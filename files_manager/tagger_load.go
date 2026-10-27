// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"context"
	"image"
	"sync"
	"time"

	"github.com/alonsovidales/otc/alerttext"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/i18n"
	imagestagger "github.com/alonsovidales/otc/images_tagger"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/modelserver"
)

// Outside low-memory mode the primary loads RAM++ in the background at
// start (Init). A model that failed to load - a damaged or missing file,
// ONNX Runtime refusing it - used to end the process (log.Fatal), and
// systemd started it again three seconds later, for good: the apps could
// no longer reach the device, nor update it from Settings to repair it.
// Now the device keeps serving without a tagger: photos and videos are
// processed as usual, with their place tags only (the model's answer
// modelserver.ErrNoTagger, which processing skips quietly); one Alert
// says so, and the load is tried again with a growing pause.

// loadedTagger is a loaded tagging model: *imagestagger.RAMTagger, or a
// test's.
type loadedTagger interface {
	Tags(ctx context.Context, img image.Image, opt imagestagger.RAMOptions) ([]imagestagger.RAMTag, error)
	TagsResized(ctx context.Context, img *image.RGBA, opt imagestagger.RAMOptions) ([]imagestagger.RAMTag, error)
}

// retryTagger is the primary's tagging model outside low-memory mode: the
// one loaded, or none yet (modelserver.ErrNoTagger) while it is tried
// again. The instances it supervises get the same answer through the
// model socket.
type retryTagger struct {
	mu sync.RWMutex
	t  loadedTagger
}

func (r *retryTagger) model() loadedTagger {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.t
}

func (r *retryTagger) set(t loadedTagger) {
	r.mu.Lock()
	r.t = t
	r.mu.Unlock()
}

func (r *retryTagger) Tags(ctx context.Context, img image.Image, opt imagestagger.RAMOptions) ([]imagestagger.RAMTag, error) {
	t := r.model()
	if t == nil {
		return nil, modelserver.ErrNoTagger
	}
	return t.Tags(ctx, img, opt)
}

// TagsResized is what the model socket serves the supervised instances.
func (r *retryTagger) TagsResized(ctx context.Context, img *image.RGBA, opt imagestagger.RAMOptions) ([]imagestagger.RAMTag, error) {
	t := r.model()
	if t == nil {
		return nil, modelserver.ErrNoTagger
	}
	return t.TagsResized(ctx, img, opt)
}

// The pause before trying the model again: a minute, doubling, at most an
// hour.
const (
	cTaggerRetryFirst = time.Minute
	cTaggerRetryMax   = time.Hour
)

// loadRAMTagger is the model Init loads outside low-memory mode.
func loadRAMTagger() (loadedTagger, error) {
	t, err := imagestagger.NewRAMTagger(
		cfg.GetStr("tagger", "model-path"),
		cfg.GetStr("tagger", "tags-path"),
		cfg.GetStr("tagger", "thresholds-path"),
		imagestagger.DefaultRAMOptions(),
	)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// loadTaggerRetrying loads rt's model with load until it works, waiting
// with wait between tries (time.Sleep outside tests). ready is closed once
// the first try is over, whether it worked or not: processing waits for
// that much (waitForTagger), never for the retries. The first failure is
// alerted, once.
func (mg *Manager) loadTaggerRetrying(rt *retryTagger, load func() (loadedTagger, error), ready chan struct{}, wait func(time.Duration)) {
	started := time.Now()
	pause := cTaggerRetryFirst
	for attempt := 1; ; attempt++ {
		t, err := load()
		if err == nil {
			rt.set(t)
			if attempt == 1 {
				close(ready)
				log.Info("image tagger ready after", time.Since(started).Round(time.Millisecond))
			} else {
				log.Info("image tagger ready after", attempt, "tries; photos processed meanwhile have no tags until Reprocess Media")
			}
			return
		}
		log.Error("could not load the image tagging model (try", attempt, "), the device goes on without tags and tries again in", pause, ":", err)
		if attempt == 1 {
			mg.raise(alerttext.New(i18n.DevAlertTaggerNotLoaded(), i18n.DevAlertTaggerNotLoadedDetail(err.Error())))
			close(ready)
		}
		wait(pause)
		pause = min(2*pause, cTaggerRetryMax)
	}
}
