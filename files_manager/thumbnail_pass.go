// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
)

// Release 111 added the small thumbnails and bounded the big ones' height
// (thumbnail_size.go). This pass, once per device, brings what was stored
// before to that: for every photo and video it reads the thumbnails'
// headers and, only where needed, makes the small one and scales the big
// one down - from the big thumbnail, never the original (fixThumbnails).
// It is resumable, goes one file at a time, and gives way to everything
// else: it waits while an upload is arriving or the processing lanes have
// work, keeps to about half a core, and stops when a full reprocess starts
// (which makes every thumbnail again anyway).

const (
	// cThumbPassState, in the storage path, records the pass:
	// "<cap> <small side> <last hash done>" while it runs,
	// "<cap> <small side> done" once it has. The storage path rather than
	// a settings column: it describes the thumbnails on this disk, and
	// needs no migration.
	cThumbPassState = ".thumbnails-sized"
	cThumbPassDone  = "done"
	// cThumbPassCheckPause is between two files that needed nothing (two
	// headers read); after one fixed the pass rests as long as the fix
	// took, and at least cThumbPassMinPause.
	cThumbPassCheckPause = 10 * time.Millisecond
	cThumbPassMinPause   = 50 * time.Millisecond
	// cThumbPassIdlePoll is how often a pass waiting for the uploads and
	// the processing lanes to go quiet looks again.
	cThumbPassIdlePoll = 10 * time.Second
	// cUploadQuiet: an upload this recent means a sync is running.
	cUploadQuiet = time.Minute
)

// lastUploadActivity is when an upload last arrived (unix nanoseconds), for
// background work that waits for a sync to finish.
var lastUploadActivity atomic.Int64

func noteUploadActivity() { lastUploadActivity.Store(time.Now().UnixNano()) }

func uploadedWithin(d time.Duration) bool {
	last := lastUploadActivity.Load()
	return last != 0 && time.Since(time.Unix(0, last)) < d
}

// FitStoredThumbnails is the pass, called once per process after the
// first sign-in (the thumbnails are under the owner's key), after the
// missing thumbnails are rebuilt (BackfillMissingThumbnails).
func (mg *Manager) FitStoredThumbnails(ses *session.Session) {
	if mg == nil || mg.dao == nil || ses == nil || !cfg.HasSection("otc") {
		return
	}
	maxSide := ThumbnailMaxSide()
	p := &thumbPass{
		storage:    cfg.GetStr("otc", "storage-path"),
		maxSide:    maxSide,
		list:       mg.dao.ListMediaForReprocess,
		idle:       mg.waitForQuiet,
		fix:        func(hash string) thumbFix { return mg.fixThumbnailsSafely(ses, hash, maxSide) },
		checkPause: cThumbPassCheckPause,
		minPause:   cThumbPassMinPause,
	}
	p.run()
}

// waitForQuiet blocks until no upload is arriving and both processing
// lanes are idle; false when the pass should stop (a full reprocess).
func (mg *Manager) waitForQuiet() bool {
	for {
		if mg.isReprocessing() {
			return false
		}
		ls := mg.mediaLanes()
		if !uploadedWithin(cUploadQuiet) && !ls.fast.busy() && !ls.analysis.busy() && !mg.processingDeferred() {
			return true
		}
		time.Sleep(cThumbPassIdlePoll)
	}
}

type thumbPass struct {
	storage string
	maxSide int
	// list pages through the library's media by hash
	// (dao.ListMediaForReprocess).
	list func(after string, n int) ([]*pb.File, error)
	// idle waits until the pass may work; false stops it.
	idle func() bool
	// fix is fixThumbnails for one hash.
	fix                  func(hash string) thumbFix
	checkPause, minPause time.Duration
}

type thumbPassState struct {
	maxSide, small int
	after          string
	done           bool
}

func (p *thumbPass) statePath() string { return filepath.Join(p.storage, cThumbPassState) }

// readThumbPassState parses the state file; ok is false when there is none
// or it can't be read - the pass then runs from the start, which changes
// nothing already right.
func readThumbPassState(path string) (st thumbPassState, ok bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return st, false
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 3 {
		return st, false
	}
	if st.maxSide, err = strconv.Atoi(fields[0]); err != nil || st.maxSide <= 0 {
		return st, false
	}
	if st.small, err = strconv.Atoi(fields[1]); err != nil || st.small <= 0 {
		return st, false
	}
	if fields[2] == cThumbPassDone {
		st.done = true
	} else {
		st.after = fields[2]
	}
	return st, true
}

// saveState writes the state file whole (writeBlob: a temp file renamed
// into place), so a crash leaves the old state or the new one.
func (p *thumbPass) saveState(after string, done bool) {
	mark := after
	if done {
		mark = cThumbPassDone
	}
	if mark == "" {
		mark = "-" // nothing done yet: still three fields
	}
	content := fmt.Sprintf("%d %d %s\n", p.maxSide, cSmallThumbnailSide, mark)
	if err := writeBlob(p.statePath(), []byte(content)); err != nil {
		log.Error("thumbnails pass: could not record its progress:", err)
	}
}

// start is where the pass begins, or false when it has nothing to do.
func (p *thumbPass) start() (after string, run bool) {
	st, ok := readThumbPassState(p.statePath())
	switch {
	case !ok, st.small != cSmallThumbnailSide, p.maxSide < st.maxSide:
		// Never ran, or the sizes changed (a release with another small
		// size, a lower cap in the ini): everything is checked again.
		return "", true
	case st.done:
		if p.maxSide != st.maxSide {
			// A higher cap: what fits the lower one fits it too.
			p.saveState("", true)
		}
		return "", false
	case st.after == "-":
		return "", true
	default:
		return st.after, true
	}
}

func (p *thumbPass) run() {
	after, run := p.start()
	if !run {
		return
	}
	started := time.Now()
	checked, fixed := 0, 0
	for {
		batch, err := p.list(after, cBackfillBatchSize)
		if err != nil {
			log.Error("thumbnails pass: error listing media:", err)
			p.saveState(after, false)
			return
		}
		if len(batch) == 0 {
			break
		}
		for _, f := range batch {
			if !p.idle() {
				log.Info("thumbnails pass: stopping for a reprocess after", checked, "file(s)")
				p.saveState(after, false)
				return
			}
			if isMedia(f) {
				t := time.Now()
				res := p.fix(f.Hash)
				checked++
				if res == thumbFixed {
					fixed++
					time.Sleep(max(p.minPause, time.Since(t)))
				} else {
					time.Sleep(p.checkPause)
				}
			}
			after = f.Hash
		}
		p.saveState(after, false)
	}
	p.saveState("", true)
	log.Info("thumbnails pass: fixed", fixed, "of", checked, "file(s) (small thumbnails, longest side", p.maxSide, "px) in", time.Since(started))
}
