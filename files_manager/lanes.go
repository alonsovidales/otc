// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/alonsovidales/otc/container"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/i18n"
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
//
// Issue #206: a job is its content's hash, the row it processes read when
// it runs (jobFile) - not a file row held from the upload until then: a
// big sync queues tens of thousands, ~90 bytes each now. And a file
// deleted while its job waited is passed over, never read.

type mediaJob struct {
	ses  *session.Session
	hash string
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
	// finished, when set, is told of each file that leaves the lanes: the
	// thumbnail lane's that don't go on, and every analysis
	// (Status.background_tasks' "analysis", background.go). Set before
	// anything is pushed.
	finished func()
}

// newMediaLanes starts both lanes, with fast and analysis workers.
// thumbnail reports whether the file should go on to the analysis lane.
func newMediaLanes(fast, analysis int, thumbnail func(mediaJob) bool, analyse func(mediaJob)) *mediaLanes {
	ls := &mediaLanes{}
	ls.fast = newLane(fast, func(j mediaJob) {
		if thumbnail(j) {
			ls.analysis.push(j)
		} else if ls.finished != nil {
			ls.finished()
		}
	}, nil)
	ls.analysis = newLane(analysis, func(j mediaJob) {
		analyse(j)
		if ls.finished != nil {
			ls.finished()
		}
	}, func() bool { return !ls.fast.busy() })
	ls.fast.onIdle = ls.analysis.wake
	return ls
}

// How many jobs each lane runs at once. Both leave one CPU free for
// serving requests (and for the Pi's power budget), as the single upload
// queue did before the lanes; on a Raspberry Pi 5 that is three, which the
// memory was measured with. Until release 133 a PC got one per CPU but
// one, 15 or 31 a lane with 16 or 32 threads, and what a job holds doesn't
// shrink with the cores: that could be more than 32 GB hold, and a Linux
// Mint PC with 32 GB ran out of memory (2026-10-23).
//
//   - The fast lane (a photo's decode and scale, ~300 MB; a 4K video's
//     frame with ffmpeg at a Pi's four threads, ~370 MB more) runs one job
//     per cMemoryPerWorker of memory at most: three on an 8 GB Pi (four by
//     memory, its CPUs less one), 16 on a 32 GB PC with 32 threads.
//   - The analysis lane runs at most cMaxAnalysisWorkers: its runs share
//     the tagging model's threads, so more barely help, and each keeps
//     ~650 MB in ONNX Runtime's memory arena after it.
const (
	cMemoryPerWorker    = 2 // GB
	cMaxAnalysisWorkers = 3
)

// processingWorkers is how many jobs the fast and analysis lanes run at
// once on this machine.
func processingWorkers() (fast, analysis int) {
	return processingWorkersFor(cpuCount(), CurrentMemoryProfile())
}

// cpuCount is the CPUs processing is sized by: the machine's, or in a
// container the ones the Go runtime was given - its CPU limit
// (runtime.GOMAXPROCS, which bin/otc.go leaves to the runtime there), not
// the host's.
func cpuCount() int {
	if container.Enabled() {
		return runtime.GOMAXPROCS(0)
	}
	return runtime.NumCPU()
}

// processingWorkersFor is processingWorkers for cpus and p. Memory unknown:
// the fast lane stops where the analysis lane does.
func processingWorkersFor(cpus int, p MemoryProfile) (fast, analysis int) {
	n := max(1, cpus-1)
	analysis = min(n, cMaxAnalysisWorkers)
	if p.Total <= 0 {
		return analysis, analysis
	}
	return max(1, min(n, p.NominalGB()/cMemoryPerWorker)), analysis
}

func (mg *Manager) mediaLanes() *mediaLanes {
	mg.lanesOnce.Do(func() {
		fast, analysis := processingWorkers()
		log.Info(fmt.Sprintf("processing: %d thumbnails and %d analyses at once (%d CPUs, %d GB of memory)", fast, analysis, cpuCount(), CurrentMemoryProfile().NominalGB())) // i18n-ignore: a log line
		mg.lanes = newMediaLanes(fast, analysis, mg.thumbnailJob, mg.analysisJob)
		mg.lanes.finished = mg.analysisFinished
	})
	return mg.lanes
}

// queueThumbnail and queueAnalysis put a file in the lanes - the thumbnail
// lane (which goes on to the analysis) or the analysis lane - counted in
// the busy spell Status lists (background.go).
func (mg *Manager) queueThumbnail(j mediaJob) {
	ls := mg.mediaLanes()
	mg.analysisQueued()
	ls.fast.push(j)
}

func (mg *Manager) queueAnalysis(j mediaJob) {
	ls := mg.mediaLanes()
	mg.analysisQueued()
	ls.analysis.push(j)
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
func (mg *Manager) enqueueMedia(ses *session.Session, file *pb.File) {
	if !isMedia(file) {
		return
	}
	if err := mg.dao.AddPendingAnalysis(file.Hash); err != nil {
		log.Error("could not record the analysis to do for", file.Hash, ":", err)
	}
	mg.queueThumbnail(mediaJob{ses: ses, hash: file.Hash})
}

// jobFile is the row a job of hash processes: its smallest path that is
// media (isMedia), as ResumePendingAnalysis picks them. None - every row
// gone, or none media - is nothing to do: its analysis isn't pending any
// more. A row that can't be read leaves it pending, for the next start.
func (mg *Manager) jobFile(hash string) (*pb.File, bool) {
	files, err := mg.dao.FileRowsOfHash(hash)
	if err != nil {
		log.Error("could not read the file of", hash, "to process it, left for the next start:", err)
		return nil, false
	}
	for _, f := range files {
		if isMedia(f) {
			return f, true
		}
	}
	mg.donePendingAnalysis(hash)
	return nil, false
}

// processStoredStages runs stages of processing on a file already on
// disk. A video isn't read whole: ffmpeg streams what it needs. Anything
// else is, within the content budget (readForProcessing): content too
// large to hold is alerted and not processed, as one too large to decode.
func (mg *Manager) processStoredStages(ses *session.Session, file *pb.File, target string, stages mediaStages) bool {
	// Issue #192: decided before the file is read - an analysis job for
	// content kept out of Images has nothing to do, and nothing failed.
	if stages = mg.guardAnalysis(file.Hash, stages); stages == 0 {
		return true
	}
	if strings.HasPrefix(file.Mime, "video/") && mg.videoSourceFn != nil {
		return mg.processMedia(ses, file, target, nil, stages)
	}
	content, release, err := mg.readForProcessing(ses, target)
	if errors.Is(err, errTooLargeToProcess) {
		mg.stillFailed(i18n.DevAlertFileNotProcessed, file, nil, err)
		return false
	}
	if err != nil {
		mg.alert(i18n.DevAlertFileNotReadBack, file.Path, err)
		return false
	}
	defer release()
	return mg.processMedia(ses, file, target, content, stages)
}

func (mg *Manager) thumbnailJob(j mediaJob) bool {
	// One job at a time on a low-memory device and after a death
	// (lowmem.go); never paused.
	defer mg.beginProcessing(j.hash, jobThumbnail)()
	file, ok := mg.jobFile(j.hash)
	if !ok {
		return false
	}
	return mg.thumbnailOf(j.ses, file)
}

// thumbnailOf is a thumbnail job's work on file: whether it goes on to the
// analysis.
func (mg *Manager) thumbnailOf(ses *session.Session, file *pb.File) bool {
	ok := false
	mg.safelyOn("processing", file, func() {
		ok = mg.processStoredStages(ses, file, blobPath(file.Hash), stageThumbnail)
	})
	if !ok {
		// Undecodable: the analysis would fail the same way.
		mg.donePendingAnalysis(file.Hash)
	}
	return ok
}

func (mg *Manager) analysisJob(j mediaJob) {
	defer mg.beginProcessing(j.hash, jobAnalysis)()
	// Done meanwhile by a reprocess or the backfill.
	if pending, err := mg.dao.IsPendingAnalysis(j.hash); err == nil && !pending {
		return
	}
	// Deleted while it waited: nothing to analyse.
	if referenced, err := mg.dao.HashReferenced(j.hash); err == nil && !referenced {
		mg.donePendingAnalysis(j.hash)
		return
	}
	file, ok := mg.jobFile(j.hash)
	if !ok {
		return
	}
	mg.safelyOn("analysing", file, func() {
		mg.processStoredStages(j.ses, file, blobPath(file.Hash), stageAnalysis)
	})
	// Done even when it failed: a file that crashes the analysis must not
	// be retried at every start.
	mg.donePendingAnalysis(file.Hash)
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
	j := mediaJob{ses: ses, hash: hash}
	if mg.hasThumbnail(hash) {
		mg.queueAnalysis(j)
	} else {
		mg.queueThumbnail(j)
	}
}

func (mg *Manager) donePendingAnalysis(hash string) {
	if err := mg.dao.DelPendingAnalysis(hash); err != nil {
		log.Error("could not clear the pending analysis of", hash, ":", err)
	}
}

// cResumeChunk is how many pending hashes ResumePendingAnalysis reads at
// once, and the files of (dao.FilesOfHashes).
const cResumeChunk = 500

// ResumePendingAnalysis puts back in the lanes what a restart interrupted:
// a file that still has no thumbnail goes through both lanes, one that has
// one only through the analysis. Called once per process at the first
// sign-in, before BackfillMissingThumbnails, which leaves these files to
// the lanes. pending_analysis is read cResumeChunk hashes at a time in the
// order they were queued (dao.PendingPage), and their files in one query a
// page (issue #206: one query a hash was minutes of a Pi's work after a big
// sync, and every pending row and file was held at once); only the hashes
// go into the lanes. A page whose files can't be read stays pending, for
// the next start.
func (mg *Manager) ResumePendingAnalysis(ses *session.Session) {
	if mg == nil || mg.dao == nil {
		return
	}
	resumed := 0
	var after *dao.PendingMark
	for {
		hashes, next, err := mg.dao.PendingPage(after, cResumeChunk)
		if err != nil {
			log.Error("could not list the pending analyses:", err)
			break
		}
		files, err := mg.dao.FilesOfHashes(hashes)
		if err != nil {
			log.Error("could not read the files of the pending analyses, left for the next start:", err)
			files = nil
		}
		for _, hash := range hashes {
			if files == nil {
				break
			}
			file := files[hash]
			if file == nil {
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
			j := mediaJob{ses: ses, hash: hash}
			if _, statErr := os.Stat(blobPath(hash) + "_thumbnail"); statErr == nil {
				mg.queueAnalysis(j)
			} else {
				mg.queueThumbnail(j)
			}
			resumed++
		}
		if next == nil {
			break
		}
		after = next
	}
	if resumed > 0 {
		log.Info(fmt.Sprintf("resumed the processing of %d upload(s) a restart interrupted", resumed))
	}
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
