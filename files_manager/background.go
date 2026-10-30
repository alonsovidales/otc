// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"sync"
	"sync/atomic"

	"github.com/alonsovidales/otc/i18n"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// Background work (release 152, Status.background_tasks): what this
// instance is working through on its own library, so the apps can say how
// far it is. Every long pass reports into it - BeginBackground with what it
// has to do (0 when it doesn't know), Step as it goes, End when it stops -
// and Status lists what is running, one entry a kind, in the request's
// language. A pass that waits (for the uploads, the processing lanes, the
// owner's key) stays listed; one that ends, for good or until the next
// start, goes. Reprocess Media isn't here: it has its own progress
// (GetReprocessStatus). Smart search's model download isn't either: it is
// bytes, not photos, and Settings.smart_search_model already says how far
// it is.

// The kinds, the apps' keys (BackgroundTask.kind).
const (
	// BackgroundAnalysis: what was uploaded, through the processing lanes
	// (thumbnails, tags, faces) - a busy spell, from the first file queued
	// until the lanes are idle again (lanes.go).
	BackgroundAnalysis = "analysis"
	// BackgroundThumbnails: thumbnails made again or fitted to the sizes
	// of this release (BackfillMissingThumbnails, FitStoredThumbnails).
	BackgroundThumbnails = "thumbnails"
	// BackgroundDates: the photos' dates put right.
	BackgroundDates = "dates"
	// BackgroundPlaces: where the photos were taken (BackfillLocations).
	BackgroundPlaces = "places"
	// BackgroundQuality: On this day's measures (BackfillQuality).
	BackgroundQuality = "quality"
	// BackgroundSmartSearch: Smart search's descriptions
	// (BackfillEmbeddings).
	BackgroundSmartSearch = "smart_search"
)

// backgroundOrder is the order Status lists them in: the uploads first,
// then the passes in the order startBackfillOnce runs them.
var backgroundOrder = []string{
	BackgroundAnalysis, BackgroundThumbnails, BackgroundDates, BackgroundPlaces, BackgroundQuality, BackgroundSmartSearch,
}

// backgroundText is what a kind is doing, in the device's words.
func backgroundText(kind string) (i18n.Msg, bool) {
	switch kind {
	case BackgroundAnalysis:
		return i18n.DevStatusBackgroundAnalysis(), true
	case BackgroundThumbnails:
		return i18n.DevStatusBackgroundThumbnails(), true
	case BackgroundDates:
		return i18n.DevStatusBackgroundDates(), true
	case BackgroundPlaces:
		return i18n.DevStatusBackgroundPlaces(), true
	case BackgroundQuality:
		return i18n.DevStatusBackgroundQuality(), true
	case BackgroundSmartSearch:
		return i18n.DevStatusBackgroundSmartSearch(), true
	}
	return i18n.Msg{}, false
}

// backgroundWork is the passes running now.
type backgroundWork struct {
	mu     sync.Mutex
	next   uint64
	passes map[uint64]*BackgroundPass
}

// BackgroundPass is one pass's entry. Its methods may be called on nil (a
// Manager without one, in tests) and from any goroutine.
type BackgroundPass struct {
	w     *backgroundWork
	id    uint64
	kind  string
	done  atomic.Int64
	total atomic.Int64
}

// BeginBackground lists a pass of kind with total photos or videos to go
// through (0: not known yet) until End.
func (mg *Manager) BeginBackground(kind string, total int64) *BackgroundPass {
	if mg == nil {
		return nil
	}
	return mg.background.begin(kind, total)
}

func (w *backgroundWork) begin(kind string, total int64) *BackgroundPass {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.passes == nil {
		w.passes = map[uint64]*BackgroundPass{}
	}
	w.next++
	p := &BackgroundPass{w: w, id: w.next, kind: kind}
	p.total.Store(max(total, 0))
	w.passes[p.id] = p
	return p
}

// Step counts n more photos or videos dealt with.
func (p *BackgroundPass) Step(n int64) {
	if p != nil {
		p.done.Add(n)
	}
}

// AddTotal counts n more to go through (a queue that grew).
func (p *BackgroundPass) AddTotal(n int64) {
	if p != nil {
		p.total.Add(n)
	}
}

// SetTotal is what it now knows it has to go through.
func (p *BackgroundPass) SetTotal(n int64) {
	if p != nil {
		p.total.Store(max(n, 0))
	}
}

// End takes the pass off the list; again is harmless.
func (p *BackgroundPass) End() {
	if p == nil {
		return
	}
	p.w.mu.Lock()
	delete(p.w.passes, p.id)
	p.w.mu.Unlock()
}

// BackgroundTask is one kind of work running, summed over its passes.
type BackgroundTask struct {
	Kind        string
	Done, Total int64
}

// BackgroundWork is what this instance is working through now, one entry a
// kind, in backgroundOrder (kinds it doesn't know after them). A total
// under what is done (more came in) is raised to it; 0 stays unknown.
func (mg *Manager) BackgroundWork() []BackgroundTask {
	if mg == nil {
		return nil
	}
	w := &mg.background
	w.mu.Lock()
	byKind := map[string]*BackgroundTask{}
	var kinds []string
	for _, p := range w.passes {
		t := byKind[p.kind]
		if t == nil {
			t = &BackgroundTask{Kind: p.kind}
			byKind[p.kind] = t
			kinds = append(kinds, p.kind)
		}
		done, total := p.done.Load(), p.total.Load()
		t.Done += done
		if total > 0 {
			t.Total += max(total, done)
		}
	}
	w.mu.Unlock()
	var out []BackgroundTask
	for _, k := range backgroundOrder {
		if t := byKind[k]; t != nil {
			out = append(out, *t)
			delete(byKind, k)
		}
	}
	for _, k := range kinds {
		if t := byKind[k]; t != nil {
			out = append(out, *t)
		}
	}
	return out
}

// BackgroundTasks is Status.background_tasks: BackgroundWork with each
// kind's words in lang.
func (mg *Manager) BackgroundTasks(lang string) []*pb.BackgroundTask {
	var out []*pb.BackgroundTask
	for _, t := range mg.BackgroundWork() {
		text := t.Kind
		if m, ok := backgroundText(t.Kind); ok {
			text = m.Render(lang)
		}
		out = append(out, &pb.BackgroundTask{Kind: t.Kind, Text: text, Done: t.Done, Total: t.Total})
	}
	return out
}

// analysisSpell is the processing lanes' busy spell as one pass: from the
// first upload queued while they were idle until they are idle again,
// total what was queued meanwhile and done what came out of them.
type analysisSpell struct {
	mu   sync.Mutex
	pass *BackgroundPass
	open int64
}

// analysisQueued counts a file going into the lanes.
func (mg *Manager) analysisQueued() {
	s := &mg.spell
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pass == nil {
		s.pass = mg.BeginBackground(BackgroundAnalysis, 0)
	}
	s.pass.AddTotal(1)
	s.open++
}

// analysisFinished counts a file coming out of them, done or not; the
// spell ends with the last.
func (mg *Manager) analysisFinished() {
	s := &mg.spell
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pass == nil {
		return
	}
	s.pass.Step(1)
	if s.open--; s.open <= 0 {
		s.pass.End()
		s.pass, s.open = nil, 0
	}
}
