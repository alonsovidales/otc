// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"context"
	"fmt"
	"os"

	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
)

// cReprocessBatchSize is how many files Reprocess's worker loop lists (and
// persists progress for) per round - small enough that a crash between
// two batches loses very little re-work, large enough that a personal
// library's file count (thousands, not millions - see ListFaceEmbeddings'
// own doc comment for the same assumption elsewhere) doesn't turn this
// into thousands of tiny, mostly-empty round trips to the DB.
const cReprocessBatchSize = 20

// ReprocessStatus returns the current (persisted) state of issue #73's
// full-library reprocess run, for the Settings screen's progress bar to
// poll (GetReprocessStatus) the same way StatusWidget polls GetStatus.
func (mg *Manager) ReprocessStatus() (status string, total, processed int32, err error) {
	st, err := mg.dao.GetReprocessState()
	if err != nil {
		return "", 0, 0, err
	}
	return st.Status, int32(st.Total), int32(st.Processed), nil
}

// Reprocess kicks off (or resumes, or no-ops) issue #73's full-library
// reprocess: every image/video already uploaded gets its tags and faces
// recalculated from scratch - "when a change in a model is done... we
// should be able to run a full re-process of all the media uploaded", per
// the issue - run in one background goroutine detached from whichever
// connection triggered it, so it keeps going even if the owner closes the
// app/browser tab right after starting it. ses is held only for that
// goroutine's own lifetime - once the run finishes, the reference is
// dropped and the key goes with it ("using the secret key and discarding
// this at the end", per the issue). Runs entirely with a network
// connection to nothing else: no request/response happens mid-run, just
// GetReprocessStatus polls for a progress bar.
//
// Three distinct cases, told apart by mg.reprocessing (true only while a
// goroutine from *this process* is actively working), the persisted
// reprocess_state.status, and forceRestart:
//   - mg.reprocessing is true: a run is already active right here - no-op
//     (the caller polls GetReprocessStatus, same as if it had started).
//     forceRestart is ignored here too - cancel the active run first.
//   - mg.reprocessing is false, status is "running" or "stopped", and
//     forceRestart is false: the DB says a run was in progress, but
//     nothing here is driving it - the previous run was either
//     interrupted (a server restart, taking its goroutine and session
//     key down with it) or explicitly cancelled (see CancelReprocess) -
//     either way, resume from last_hash rather than wiping and starting
//     over. "If the process is interrupted it should be able to continue
//     from where it was left" applies to a deliberate stop exactly the
//     same as an accidental one.
//   - anything else (idle/completed/failed/never run, or forceRestart is
//     true even over a resumable "stopped" run - the owner explicitly
//     choosing to start over rather than continue): a fresh start - wipe
//     file_tags/faces/people ("strip pre-existing data so it can be
//     recalculated") and begin from hash "".
func (mg *Manager) Reprocess(ses *session.Session, forceRestart bool) (err error) {
	mg.reprocessMu.Lock()
	if mg.reprocessing {
		mg.reprocessMu.Unlock()
		return nil
	}
	mg.reprocessing = true
	mg.reprocessMu.Unlock()

	// If anything below fails before the worker goroutine actually starts,
	// release the guard - otherwise a failed WipeTagsAndFaces/StartReprocess
	// would wedge mg.reprocessing true forever, with no goroutine left to
	// ever clear it.
	started := false
	defer func() {
		if !started {
			mg.reprocessMu.Lock()
			mg.reprocessing = false
			mg.reprocessMu.Unlock()
		}
	}()

	state, err := mg.dao.GetReprocessState()
	if err != nil {
		return fmt.Errorf("reading reprocess state: %w", err)
	}

	if !forceRestart && (state.Status == "running" || state.Status == "stopped") {
		if err := mg.dao.ResumeReprocess(); err != nil {
			return fmt.Errorf("resuming reprocess: %w", err)
		}
	} else {
		if err := mg.dao.WipeTagsAndFaces(); err != nil {
			return fmt.Errorf("wiping tags/faces before reprocess: %w", err)
		}
		total, err := mg.dao.CountMediaFiles()
		if err != nil {
			return fmt.Errorf("counting media for reprocess: %w", err)
		}
		if err := mg.dao.StartReprocess(total); err != nil {
			return fmt.Errorf("starting reprocess: %w", err)
		}
		state.LastHash = ""
		state.Processed = 0
	}

	ctx, cancel := context.WithCancel(context.Background())
	mg.reprocessMu.Lock()
	mg.reprocessCancel = cancel
	mg.reprocessMu.Unlock()

	started = true
	go mg.runReprocess(ctx, ses, state.LastHash, state.Processed)
	return nil
}

// CancelReprocess stops the active run, if any - a no-op otherwise. The
// worker goroutine notices between files (see runReprocess) and leaves
// reprocess_state as 'stopped' rather than 'completed'/'failed', so
// Reprocess's own resume logic picks it back up on the next "Reprocess"
// click instead of wiping and starting over.
func (mg *Manager) CancelReprocess() {
	mg.reprocessMu.Lock()
	defer mg.reprocessMu.Unlock()
	if mg.reprocessCancel != nil {
		mg.reprocessCancel()
	}
}

// runReprocess is Reprocess's worker loop - always started in its own
// goroutine, never called directly (see Reprocess's doc comment for the
// fresh-vs-resume decision that precedes it).
func (mg *Manager) runReprocess(ctx context.Context, ses *session.Session, lastHash string, processed int) {
	defer func() {
		mg.reprocessMu.Lock()
		mg.reprocessing = false
		mg.reprocessCancel = nil
		mg.reprocessMu.Unlock()
	}()

	storagePath := cfg.GetStr("otc", "storage-path")

	stop := func() {
		if err := mg.dao.FinishReprocess("stopped"); err != nil {
			log.Error("reprocess: error marking run stopped:", err)
		}
		log.Info("reprocess: stopped by request at", processed, "file(s)")
	}

	for {
		if ctx.Err() != nil {
			stop()
			return
		}

		batch, err := mg.dao.ListMediaForReprocess(lastHash, cReprocessBatchSize)
		if err != nil {
			log.Error("reprocess: error listing next batch:", err)
			if ferr := mg.dao.FinishReprocess("failed"); ferr != nil {
				log.Error("reprocess: error marking run failed:", ferr)
			}
			return
		}
		if len(batch) == 0 {
			break
		}

		for _, file := range batch {
			// Checked per-file (not just per-batch) so a cancellation is
			// noticed within one file's own processing time, not up to a
			// whole batch's worth.
			if ctx.Err() != nil {
				stop()
				return
			}

			mg.reprocessOneFile(ses, file, storagePath)
			processed++
			lastHash = file.Hash
			// Persisted after every single file, deliberately - the whole
			// point of last_hash is to lose as little work as possible to
			// an interruption (see reprocess_state's doc comment).
			if err := mg.dao.UpdateReprocessProgress(processed, lastHash); err != nil {
				log.Error("reprocess: error saving progress:", err)
			}
		}
	}

	if err := mg.dao.FinishReprocess("completed"); err != nil {
		log.Error("reprocess: error marking run completed:", err)
	}
	log.Info("reprocess: completed,", processed, "file(s) processed")
}

// reprocessOneFile re-runs the tag/face pipeline (processMediaContent -
// the same code a fresh upload runs) against one already-stored file's
// original bytes, read straight off disk and decrypted with the owner's
// key. Logged and skipped on error (a corrupt file, a read failure)
// rather than aborting the whole run - same "don't let one bad thing take
// down the rest" reasoning as DetectFaces' own per-face handling.
func (mg *Manager) reprocessOneFile(ses *session.Session, file *pb.File, storagePath string) {
	targetPath := fmt.Sprintf("%s/%s", storagePath, file.Hash)
	encContent, err := os.ReadFile(targetPath)
	if err != nil {
		log.Error("reprocess: error reading", file.Path, ":", err)
		return
	}
	content, err := ses.Decrypt(encContent)
	if err != nil {
		log.Error("reprocess: error decrypting", file.Path, ":", err)
		return
	}
	mg.processMediaContent(ses, file, targetPath, content)
}
