// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"database/sql"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
)

// Upload processing runs in two lanes. The fast lane makes the thumbnail
// (a decode and a resize; one frame of a video) so a new photo shows up in
// the gallery within seconds. The slow lane works out tags and faces - the
// tagging model is most of the time a photo takes - and only takes work
// while the fast lane has none: during a big first sync every thumbnail
// comes first, and the analysis catches up once the uploads stop.
//
// The slow lane's queue is also in pending_analysis (hashes only - nothing
// secret, nothing that could be decrypted without the owner), so a restart
// doesn't lose it: ResumePendingAnalysis puts it back at the first sign-in
// after one, the first moment the owner's key is in memory again.

type mediaJob struct {
	ses    *session.Session
	file   *pb.File
	target string
}

// lane is a queue served by a fixed number of workers. gate, when set, is
// checked under the lane's lock before a job is taken; whoever makes it
// true must call wake.
type lane struct {
	mu      sync.Mutex
	cond    *sync.Cond
	jobs    []mediaJob
	running int
	gate    func() bool
	run     func(mediaJob)
	onIdle  func()
}

func newLane(workers int, run func(mediaJob), gate func() bool) *lane {
	l := &lane{run: run, gate: gate}
	l.cond = sync.NewCond(&l.mu)
	for i := 0; i < workers; i++ {
		go l.work()
	}
	return l
}

func (l *lane) push(j mediaJob) {
	l.mu.Lock()
	l.jobs = append(l.jobs, j)
	l.mu.Unlock()
	l.cond.Signal()
}

// wake re-checks the gate; taking the lock first means a worker can't miss
// it between checking the gate and waiting.
func (l *lane) wake() {
	l.mu.Lock()
	l.mu.Unlock()
	l.cond.Broadcast()
}

// busy is whether the lane has work queued or running.
func (l *lane) busy() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.jobs) > 0 || l.running > 0
}

func (l *lane) work() {
	for {
		l.mu.Lock()
		for len(l.jobs) == 0 || (l.gate != nil && !l.gate()) {
			l.cond.Wait()
		}
		j := l.jobs[0]
		l.jobs[0] = mediaJob{}
		l.jobs = l.jobs[1:]
		l.running++
		l.mu.Unlock()

		l.run(j)

		l.mu.Lock()
		l.running--
		idle := len(l.jobs) == 0 && l.running == 0
		l.mu.Unlock()
		if idle && l.onIdle != nil {
			l.onIdle()
		}
	}
}

type mediaLanes struct {
	fast, analysis *lane
}

// newMediaLanes starts both lanes. thumbnail reports whether the file
// should go on to the analysis lane.
func newMediaLanes(workers int, thumbnail func(mediaJob) bool, analyse func(mediaJob)) *mediaLanes {
	ls := &mediaLanes{}
	ls.fast = newLane(workers, func(j mediaJob) {
		if thumbnail(j) {
			ls.analysis.push(j)
		}
	}, nil)
	ls.analysis = newLane(workers, analyse, func() bool { return !ls.fast.busy() })
	ls.fast.onIdle = ls.analysis.wake
	return ls
}

// processingWorkers leaves one CPU free for serving requests (and for the
// Pi's power budget), as the single upload queue did before the lanes.
func processingWorkers() int {
	return max(1, runtime.NumCPU()-1)
}

func (mg *Manager) mediaLanes() *mediaLanes {
	mg.lanesOnce.Do(func() {
		mg.lanes = newMediaLanes(processingWorkers(), mg.thumbnailJob, mg.analysisJob)
	})
	return mg.lanes
}

// isMedia is whether processing has anything to do with file - not an
// image/* type no decoder reads (a DjVu document: neverPreviewedMimes).
func isMedia(file *pb.File) bool {
	if neverPreviewed(file.Mime) {
		return false
	}
	return strings.HasPrefix(file.Mime, "image") || strings.HasPrefix(file.Mime, "video/") || strings.HasSuffix(file.Path, ".HEIC")
}

// enqueueMedia queues a just-stored upload: thumbnail first, analysis
// later. The analysis is recorded before anything runs, so a restart at
// any point leaves it to ResumePendingAnalysis.
func (mg *Manager) enqueueMedia(ses *session.Session, file *pb.File, target string) {
	if !isMedia(file) {
		return
	}
	if err := mg.dao.AddPendingAnalysis(file.Hash); err != nil {
		log.Error("could not record the analysis to do for", file.Hash, ":", err)
	}
	mg.mediaLanes().fast.push(mediaJob{ses: ses, file: file, target: target})
}

// processStoredStages runs stages of processing on a file already on
// disk. A video isn't read whole: ffmpeg streams what it needs.
func (mg *Manager) processStoredStages(ses *session.Session, file *pb.File, target string, stages mediaStages) bool {
	// Issue #192: decided before the file is read - an analysis job for
	// content kept out of Images has nothing to do, and nothing failed.
	if stages = mg.guardAnalysis(file.Hash, stages); stages == 0 {
		return true
	}
	if strings.HasPrefix(file.Mime, "video/") && mg.videoSourceFn != nil {
		return mg.processMedia(ses, file, target, nil, stages)
	}
	content, err := blobstore.ReadAll(target, ses)
	if err != nil {
		mg.alert("could not be read back for processing", file.Path, err)
		return false
	}
	return mg.processMedia(ses, file, target, content, stages)
}

func (mg *Manager) thumbnailJob(j mediaJob) bool {
	// One job at a time on a low-memory device and after a death
	// (lowmem.go); never paused.
	defer mg.beginProcessing(j.file.Hash, jobThumbnail)()
	ok := false
	mg.safelyOn("processing", j.file, func() {
		ok = mg.processStoredStages(j.ses, j.file, j.target, stageThumbnail)
	})
	if !ok {
		// Undecodable: the analysis would fail the same way.
		mg.donePendingAnalysis(j.file.Hash)
	}
	return ok
}

func (mg *Manager) analysisJob(j mediaJob) {
	defer mg.beginProcessing(j.file.Hash, jobAnalysis)()
	// Done meanwhile by a reprocess or the backfill.
	if pending, err := mg.dao.IsPendingAnalysis(j.file.Hash); err == nil && !pending {
		return
	}
	// Deleted while it waited: nothing to analyse.
	if referenced, err := mg.dao.HashReferenced(j.file.Hash); err == nil && !referenced {
		mg.donePendingAnalysis(j.file.Hash)
		return
	}
	mg.safelyOn("analysing", j.file, func() {
		mg.processStoredStages(j.ses, j.file, j.target, stageAnalysis)
	})
	// Done even when it failed: a file that crashes the analysis must not
	// be retried at every start.
	mg.donePendingAnalysis(j.file.Hash)
}

// enqueueAnalysis queues the analysis of content that was kept out of
// Images and no longer is (issue #192), already recorded in
// pending_analysis (TakeSkippedAnalysis moved it there): the analysis
// lane - or the fast lane, which goes on to it, when there is no thumbnail
// yet. Content only kept versions hold isn't in Images: it isn't queued,
// and the next start's ResumePendingAnalysis drops its row.
func (mg *Manager) enqueueAnalysis(ses *session.Session, hash string) {
	file, err := mg.dao.GetFileByHash(hash)
	if err == sql.ErrNoRows {
		return
	}
	if err != nil {
		// Recorded as skipped again too: ResumePendingAnalysis drops a
		// pending row whose file it can't read, but ReconcileOutOfImages,
		// which runs first, moves this one back.
		log.Error("could not queue the analysis of", hash, ":", err)
		if err := mg.dao.AddSkippedAnalysis([]string{hash}); err != nil {
			log.Error("could not record the analysis of", hash, "as still to do:", err)
		}
		return
	}
	if !isMedia(file) {
		// Nothing to analyse: a lane would only fail to decode it.
		mg.donePendingAnalysis(hash)
		return
	}
	j := mediaJob{ses: ses, file: file, target: blobPath(hash)}
	if mg.hasThumbnail(hash) {
		mg.mediaLanes().analysis.push(j)
	} else {
		mg.mediaLanes().fast.push(j)
	}
}

func (mg *Manager) donePendingAnalysis(hash string) {
	if err := mg.dao.DelPendingAnalysis(hash); err != nil {
		log.Error("could not clear the pending analysis of", hash, ":", err)
	}
}

// ResumePendingAnalysis puts back in the lanes what a restart interrupted:
// a file that still has no thumbnail goes through both lanes, one that has
// one only through the analysis. Called once per process at the first
// sign-in, before BackfillMissingThumbnails, which leaves these files to
// the lanes.
func (mg *Manager) ResumePendingAnalysis(ses *session.Session) {
	if mg == nil || mg.dao == nil {
		return
	}
	hashes, err := mg.dao.PendingAnalysis()
	if err != nil {
		log.Error("could not list the pending analyses:", err)
		return
	}
	if len(hashes) == 0 {
		return
	}
	lanes := mg.mediaLanes()
	resumed := 0
	for _, hash := range hashes {
		file, err := mg.dao.GetFileByHash(hash)
		if err != nil {
			// Its file is gone.
			mg.donePendingAnalysis(hash)
			continue
		}
		if !isMedia(file) {
			// Nothing decodes it (a DjVu scan: neverPreviewedMimes) - a
			// row an older build queued, or skipped content shown again
			// (cMediaRows has no such exclusion): not read.
			mg.donePendingAnalysis(hash)
			continue
		}
		target := blobPath(hash)
		j := mediaJob{ses: ses, file: file, target: target}
		if _, statErr := os.Stat(target + "_thumbnail"); statErr == nil {
			lanes.analysis.push(j)
		} else {
			lanes.fast.push(j)
		}
		resumed++
	}
	log.Info(fmt.Sprintf("resumed the processing of %d upload(s) a restart interrupted", resumed))
}

// pendingAnalysisSet is the hashes the lanes own, for the backfill to skip.
func (mg *Manager) pendingAnalysisSet() map[string]bool {
	hashes, err := mg.dao.PendingAnalysis()
	if err != nil {
		return nil
	}
	set := make(map[string]bool, len(hashes))
	for _, h := range hashes {
		set[h] = true
	}
	return set
}
