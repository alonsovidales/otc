// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"fmt"
	"sync"
	"testing"

	"github.com/alonsovidales/otc/i18n"
	pb "github.com/alonsovidales/otc/proto/generated"
)

func workString(w []BackgroundTask) string {
	s := ""
	for _, t := range w {
		s += fmt.Sprintf("%s %d/%d;", t.Kind, t.Done, t.Total)
	}
	return s
}

// A pass is listed from Begin to End, one entry a kind in a fixed order,
// passes of one kind summed; a total under what is done is raised to it,
// 0 stays unknown.
func TestBackgroundWorkListsThePassesRunning(t *testing.T) {
	mg := &Manager{}
	if w := mg.BackgroundWork(); len(w) != 0 {
		t.Fatalf("nothing running: %v", w)
	}
	smart := mg.BeginBackground(BackgroundSmartSearch, 10)
	thumbs := mg.BeginBackground(BackgroundThumbnails, 0)
	places := mg.BeginBackground(BackgroundPlaces, 5)
	more := mg.BeginBackground(BackgroundPlaces, 2)
	other := mg.BeginBackground("something_new", 3)
	smart.Step(4)
	thumbs.Step(7)
	places.Step(5)
	places.Step(1) // more than it counted: new uploads came in
	more.Step(1)
	if got, want := workString(mg.BackgroundWork()), "thumbnails 7/0;places 7/8;smart_search 4/10;something_new 0/3;"; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	places.End()
	places.End()
	more.End()
	thumbs.SetTotal(9)
	smart.AddTotal(2)
	other.End()
	if got, want := workString(mg.BackgroundWork()), "thumbnails 7/9;smart_search 4/12;"; got != want {
		t.Errorf("after some ended: %s, want %s", got, want)
	}
	// In the request's language; a kind without words keeps its name.
	other = mg.BeginBackground("something_new", 0)
	tasks := mg.BackgroundTasks("es")
	if len(tasks) != 3 || tasks[1].Kind != BackgroundSmartSearch || tasks[1].Text != i18n.DevStatusBackgroundSmartSearch().Render("es") ||
		tasks[1].Done != 4 || tasks[1].Total != 12 || tasks[2].Text != "something_new" {
		t.Errorf("tasks %v", tasks)
	}
	if en := mg.BackgroundTasks("en"); en[0].Text != "Making previews" || en[1].Text != "Getting to know your photos" {
		t.Errorf("in English: %v", en)
	}
	smart.End()
	thumbs.End()
	other.End()
	if w := mg.BackgroundWork(); len(w) != 0 {
		t.Errorf("all ended: %v", w)
	}
	// Every kind has its words.
	for _, k := range backgroundOrder {
		if _, ok := backgroundText(k); !ok {
			t.Errorf("%s has no text", k)
		}
	}
	// Without a Manager, nothing - and no panic.
	var none *Manager
	p := none.BeginBackground(BackgroundQuality, 3)
	p.Step(1)
	p.AddTotal(1)
	p.SetTotal(2)
	p.End()
	if none.BackgroundWork() != nil || none.BackgroundTasks("en") != nil {
		t.Error("a nil Manager lists work")
	}
}

// The processing lanes' busy spell: listed from the first file queued
// while they were idle, total what was queued, done what came out - a
// thumbnail that failed included - and gone once they are idle again.
func TestTheLanesBusySpellIsListed(t *testing.T) {
	mg := &Manager{}
	release := make(chan struct{})
	var mu sync.Mutex
	analysed := 0
	mg.lanes = newMediaLanes(1, 1, func(j mediaJob) bool {
		<-release
		return j.file.Hash != "bad"
	}, func(mediaJob) {
		mu.Lock()
		analysed++
		mu.Unlock()
	})
	mg.lanes.finished = mg.analysisFinished
	mg.lanesOnce.Do(func() {})
	for _, h := range []string{"a", "bad", "c"} {
		mg.queueThumbnail(mediaJob{file: &pb.File{Hash: h}})
	}
	if got := workString(mg.BackgroundWork()); got != "analysis 0/3;" {
		t.Fatalf("queued: %s", got)
	}
	release <- struct{}{} // a
	release <- struct{}{} // bad: done, nothing to analyse
	waitFor(t, "the failed thumbnail", func() bool { return workString(mg.BackgroundWork()) == "analysis 1/3;" })
	// More while busy: the same spell.
	mg.queueAnalysis(mediaJob{file: &pb.File{Hash: "d"}})
	if got := workString(mg.BackgroundWork()); got != "analysis 1/4;" {
		t.Errorf("one more: %s", got)
	}
	release <- struct{}{} // c
	waitFor(t, "the lanes to be idle", func() bool { return len(mg.BackgroundWork()) == 0 })
	mu.Lock()
	if analysed != 3 {
		t.Errorf("analysed %d", analysed)
	}
	mu.Unlock()
	// A new spell starts from nothing.
	mg.queueAnalysis(mediaJob{file: &pb.File{Hash: "e"}})
	waitFor(t, "the second spell", func() bool { return len(mg.BackgroundWork()) == 0 })
	mg.spell.mu.Lock()
	if mg.spell.pass != nil || mg.spell.open != 0 {
		t.Errorf("spell left open %d", mg.spell.open)
	}
	mg.spell.mu.Unlock()
}

// The thumbnails pass is listed while it runs, with what is left to look
// at, and goes when it ends.
func TestThumbnailPassIsListed(t *testing.T) {
	mg := &Manager{}
	storage := t.TempDir()
	files := passLibrary(4)
	rec := &passRecorder{}
	p := testPass(t, storage, files, rec, nil)
	var seen []string
	p.begin = func(after string) *BackgroundPass {
		return mg.BeginBackground(BackgroundThumbnails, int64(len(files)))
	}
	fix := p.fix
	p.fix = func(hash string) thumbFix {
		seen = append(seen, workString(mg.BackgroundWork()))
		return fix(hash)
	}
	p.run()
	if len(seen) != 4 || seen[0] != "thumbnails 1/5;" || seen[3] != "thumbnails 4/5;" {
		t.Errorf("while running: %v", seen)
	}
	if w := mg.BackgroundWork(); len(w) != 0 {
		t.Errorf("after it ended: %v", w)
	}
}
