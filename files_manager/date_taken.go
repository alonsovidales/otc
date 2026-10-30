// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/exifinfo"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
)

// Dates taken. A file's `created` is when it came to be: the phones send
// a photo's capture time, the sync clients a file's own creation time
// (issue #134) and anything else gets the time it arrived. A photo copied
// to a drive in 2018 and synced from there years later is a 2018 file -
// and Images orders, groups, scrubs and remembers (On this day) by
// `created`. So for a photo or a video whose own metadata says it was
// taken clearly earlier than a row's date, the row is a later copy and
// takes the date taken instead (createdFromTaken): when processing reads
// the metadata (processMedia, Reprocess included) and once, in the
// background, for what the device held before (BackfillDatesTaken). Every
// query that reads `created` - Images' order and month counts, the
// scrubber, `before`/`newer_than`, the oldest copy (issue #201), On this
// day, Places' covers - then has the date taken, unchanged. Files lists
// the same date in its Created column.

const (
	// cTakenMargin is how much earlier than a row's date the date taken
	// must be to move it: more than the widest gap between two zones'
	// clocks (UTC-12 to UTC+14), so a phone's exact capture time never
	// moves for an EXIF clock read without its zone.
	cTakenMargin = 26 * time.Hour

	// cDatesBackfillPage is how many contents the backfill lists at a
	// time; its place is saved after each page.
	cDatesBackfillPage = 500
	// The backfill's pause after each photo and each video, as Places':
	// a photo's metadata is a few kilobytes to decrypt, a video's an
	// ffprobe.
	cDatesBackfillPhotoPause = 20 * time.Millisecond
	cDatesBackfillVideoPause = 250 * time.Millisecond
)

// cTakenEarliest: a date taken before it is a camera that never had its
// clock set (1970, 1980, QuickTime's 1904), not a photo of then.
var cTakenEarliest = time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC)

// trustedTakenAt is ex's date taken, at the second, when the rule may use
// it: from 1990 to a day from now, and not a clock reset to a 1 January at
// midnight (2000-01-01 00:00:00 and the like, what cameras show until
// their clock is set).
func trustedTakenAt(ex *exifinfo.Info, now time.Time) (time.Time, bool) {
	if ex == nil || !ex.HasTakenAt || ex.TakenAt.IsZero() {
		return time.Time{}, false
	}
	t := ex.TakenAt.UTC().Truncate(time.Second)
	if t.Before(cTakenEarliest) || !t.Before(now.Add(24*time.Hour)) {
		return time.Time{}, false
	}
	clock := ex.TakenClock.UTC()
	if ex.TakenClock.IsZero() {
		clock = t
	}
	if clock.Month() == time.January && clock.Day() == 1 && clock.Hour() == 0 && clock.Minute() == 0 && clock.Second() == 0 {
		return time.Time{}, false
	}
	return t, true
}

// createdFromTaken is the rule for one row: the date it should carry, and
// whether that moves it - the date taken T, when the metadata has one the
// rule trusts and it is more than cTakenMargin before the row's date C (T <
// C - 26 h). Never later.
func createdFromTaken(ex *exifinfo.Info, created, now time.Time) (time.Time, bool) {
	t, ok := trustedTakenAt(ex, now)
	if !ok || !t.Before(created.Add(-cTakenMargin)) {
		return created, false
	}
	return t, true
}

// moveToDateTaken applies the rule to every files row of hash, from ex -
// the metadata processing or the backfill read; the database checks each
// row's date in the same statement (dao.MoveToDateTaken). It reports how
// many rows moved, and tells what keeps answers by date.
func (mg *Manager) moveToDateTaken(hash string, ex *exifinfo.Info) (int64, error) {
	t, ok := trustedTakenAt(ex, time.Now())
	if !ok {
		return 0, nil
	}
	moved, err := mg.dao.MoveToDateTaken(hash, t, t.Add(cTakenMargin))
	if err != nil || moved == 0 {
		return 0, err
	}
	mg.memories.changed()
	mg.places.changed()
	return moved, nil
}

// storeDateTaken is moveToDateTaken for processing, which goes on whatever
// it says.
func (mg *Manager) storeDateTaken(hash string, ex *exifinfo.Info) {
	if _, err := mg.moveToDateTaken(hash, ex); err != nil {
		log.Error("could not give", hash, "its date taken:", err)
	}
}

// errMetadataUnread is stored content whose metadata couldn't be read this
// time (its blob or ffprobe failed), as opposed to metadata that is there
// and says nothing.
var errMetadataUnread = errors.New("metadata not read") // i18n-ignore: logged only

// readStoredMetadata reads the metadata of stored content only, as "More
// info" does (GetFileInfo): a photo up to its EXIF, a video through ffprobe
// on its stream. Metadata that is missing or can't be parsed is (nil, nil);
// errMetadataUnread is a read to try again - os.ErrNotExist in it when the
// blob is gone.
func (mg *Manager) readStoredMetadata(ses *session.Session, file *pb.File) (ex *exifinfo.Info, err error) {
	if strings.HasPrefix(file.Mime, "video/") {
		src, done, srcErr := mg.videoSource(ses, file)
		if srcErr != nil {
			return nil, errors.Join(errMetadataUnread, srcErr)
		}
		ex, err = exifinfo.FromVideoSource(src)
		done()
		// ffprobe that ran and refused the file has read it; one that
		// didn't run, or didn't answer in time, hasn't.
		var exitErr *exec.ExitError
		if err != nil && !errors.As(err, &exitErr) {
			return nil, errors.Join(errMetadataUnread, err)
		}
	} else {
		blob, openErr := blobstore.Open(blobPath(file.Hash), ses)
		if openErr != nil {
			return nil, errors.Join(errMetadataUnread, openErr)
		}
		ex, err = mg.exifOfBlob(blob, file, mg.shownWholeLimit())
		blob.Close()
	}
	if err != nil {
		log.Debug("no metadata for", file.Path, ":", err)
		return nil, nil
	}
	return ex, nil
}

// datesBackfillMu lets one backfill run at a time.
var datesBackfillMu sync.Mutex

// BackfillDatesTaken gives the photos and videos the device held before
// dates taken their date taken, once per database, in the background:
// after the first sign-in (it needs the key to read), before On this day
// measures what it will choose by date. It reads every content's metadata
// only, paced, giving way to uploads and the processing lanes and stopping
// for a full reprocess (which applies the rule itself); its place is kept
// in settings.dates_taken_cursor, so a restart goes on from there, and
// settings.dates_taken_backfilled ends it. A database before those columns
// is left alone until its release's script has run. Content whose
// metadata couldn't be read this time is counted, not retried: processing
// it again (Reprocess) applies the rule.
func (mg *Manager) BackfillDatesTaken(ses *session.Session) {
	// blobPath needs the storage path: nothing to read without one (a test
	// binary's websocket layer has neither).
	if mg == nil || mg.dao == nil || ses == nil || !cfg.HasSection("otc") {
		return
	}
	if !datesBackfillMu.TryLock() {
		return
	}
	defer datesBackfillMu.Unlock()
	after, done, err := mg.dao.DatesTakenBackfill()
	if errors.Is(err, dao.ErrSchemaBehind) {
		log.Info("dates taken: the database has no place for this pass yet (the release's migration), nothing read")
		return
	}
	if err != nil {
		log.Error("dates taken: could not tell whether the pass has run:", err)
		return
	}
	if done {
		return
	}
	started := time.Now()
	read, photos, rows, unread := 0, 0, int64(0), 0
	finish := func(how string) {
		if read > 0 || unread > 0 || how != "" {
			log.Info(fmt.Sprintf("dates taken: read %d photo(s) and video(s) in %s; %d given their date taken (%d file row(s)), %d not read%s",
				read, time.Since(started).Round(time.Second), photos, rows, unread, how)) // i18n-ignore: a log line
		}
	}
	for {
		batch, err := mg.dao.ListMediaForReprocess(after, cDatesBackfillPage)
		if err != nil {
			log.Error("dates taken: error listing the photos and videos:", err)
			return
		}
		for _, file := range batch {
			if !mg.waitForQuiet() {
				finish(" - stopping for a reprocess, to go on at the next start")
				mg.saveDatesCursor(after)
				return
			}
			after = file.Hash
			if !isMedia(file) {
				continue
			}
			ex, err := mg.readMetadataSafely(ses, file)
			if errors.Is(err, os.ErrNotExist) {
				// Its blob is gone (alerted elsewhere): what brings it
				// back processes it, and that applies the rule.
				continue
			}
			if err != nil {
				unread++
				log.Debug("dates taken: could not read the metadata of", file.Path, ":", err)
				continue
			}
			read++
			moved, err := mg.moveToDateTaken(file.Hash, ex)
			if err != nil {
				log.Error("dates taken: could not move a date, stopping:", err)
				return
			}
			if moved > 0 {
				photos++
				rows += moved
			}
			if strings.HasPrefix(file.Mime, "video/") {
				time.Sleep(cDatesBackfillVideoPause)
			} else {
				time.Sleep(cDatesBackfillPhotoPause)
			}
		}
		if len(batch) < cDatesBackfillPage {
			break
		}
		if !mg.saveDatesCursor(after) {
			return
		}
	}
	finish("")
	if err := mg.dao.SetDatesTakenBackfilled(); err != nil {
		log.Error("dates taken: could not record that the pass is done:", err)
	}
}

// saveDatesCursor records the backfill's place; false when it couldn't
// (the pass then stops, to go on from the last place saved).
func (mg *Manager) saveDatesCursor(after string) bool {
	if err := mg.dao.SetDatesTakenCursor(after); err != nil {
		log.Error("dates taken: could not record how far the pass got:", err)
		return false
	}
	return true
}

// readMetadataSafely is readStoredMetadata with a panic in a metadata
// parser (malformed EXIF) taken as content not read, rather than the end
// of the process: the backfill runs outside the lanes' guard.
func (mg *Manager) readMetadataSafely(ses *session.Session, file *pb.File) (ex *exifinfo.Info, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("dates taken: recovered from a panic reading the metadata of", file.Hash, ":", r)
			ex, err = nil, errors.Join(errMetadataUnread, fmt.Errorf("%v", r))
		}
	}()
	return mg.readStoredMetadata(ses, file)
}
