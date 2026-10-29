// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"errors"
	"math"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/exifinfo"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
)

// Places, the map in Images: every photo and video gets a photo_locations
// row when the device processes it (storeLocation, from processMedia's
// metadata read), and what the device held before Places gets one once,
// in the background, from its metadata alone (backfillLocations).

const (
	// cLocationBackfillPage is how many contents the backfill lists at a
	// time.
	cLocationBackfillPage = 500
	// The backfill's pause after each photo and each video: it runs while
	// the device serves its people, and a photo's metadata is a few
	// kilobytes to decrypt while a video's is an ffprobe.
	cLocationBackfillPhotoPause = 20 * time.Millisecond
	cLocationBackfillVideoPause = 250 * time.Millisecond
)

// locationOf is the position ex holds, ok false for none (or a reading
// outside the globe, which no map can show).
func locationOf(ex *exifinfo.Info) (lat, lon float64, ok bool) {
	if ex == nil || !ex.HasGPS {
		return 0, 0, false
	}
	lat, lon = ex.Latitude, ex.Longitude
	if math.IsNaN(lat) || math.IsNaN(lon) || math.Abs(lat) > 90 || math.Abs(lon) > 180 {
		return 0, 0, false
	}
	return lat, lon, true
}

// storeLocation records hash's position from ex, its metadata as
// processing read it (nil: none could be), or that it has none.
func (mg *Manager) storeLocation(hash string, ex *exifinfo.Info) {
	lat, lon, ok := locationOf(ex)
	if err := mg.dao.SetPhotoLocation(hash, lat, lon, ok); err != nil {
		// Before the release's migration there is nowhere to keep it; the
		// backfill reads it once the table is there.
		if !errors.Is(err, dao.ErrSchemaBehind) {
			log.Error("could not record where", hash, "was taken:", err)
		}
		return
	}
	mg.places.changed()
}

// errLocationUnread is a content whose metadata couldn't be read this
// time (its blob or ffprobe failed): no row, so a later pass reads it.
var errLocationUnread = errors.New("metadata not read") // i18n-ignore: logged only

// readLocation reads the position of stored content from its metadata
// only, as "More info" does (GetFileInfo): a photo up to its EXIF, a
// video through ffprobe on its stream. Metadata that is there but holds
// no position, or can't be parsed, is ok false with no error - read, and
// never read again; errLocationUnread is a read to try again.
func (mg *Manager) readLocation(ses *session.Session, file *pb.File) (lat, lon float64, ok bool, err error) {
	var ex *exifinfo.Info
	if strings.HasPrefix(file.Mime, "video/") {
		src, done, srcErr := mg.videoSource(ses, file)
		if srcErr != nil {
			return 0, 0, false, errors.Join(errLocationUnread, srcErr)
		}
		ex, err = exifinfo.FromVideoSource(src)
		done()
		// ffprobe that ran and refused the file has read it; one that
		// didn't run, or didn't answer in time, hasn't.
		var exitErr *exec.ExitError
		if err != nil && !errors.As(err, &exitErr) {
			return 0, 0, false, errors.Join(errLocationUnread, err)
		}
	} else {
		blob, openErr := blobstore.Open(blobPath(file.Hash), ses)
		if openErr != nil {
			return 0, 0, false, errors.Join(errLocationUnread, openErr)
		}
		ex, err = mg.exifOfBlob(blob, file, mg.shownWholeLimit())
		blob.Close()
	}
	if err != nil {
		log.Debug("no location metadata for", file.Path, ":", err)
	}
	lat, lon, ok = locationOf(ex)
	return lat, lon, ok, nil
}

// BackfillLocations gives every photo and video the device held before
// Places its photo_locations row, once per database
// (settings.locations_backfilled), in the background after the first
// sign-in (it needs the key to read). Only content Images may show is
// read - one with a thumbnail; the processing that makes one writes the
// row of the rest - and only its metadata, paced. A restart before the
// end goes on at the next: what has a row is never listed again. Content
// that couldn't be read this time (errLocationUnread) leaves the flag
// unset, and is read at the next start.
func (mg *Manager) BackfillLocations(ses *session.Session) {
	// thumbnailPath needs the storage path: nothing to read without one
	// (a test binary's websocket layer has neither).
	if mg == nil || mg.dao == nil || ses == nil || !cfg.HasSection("otc") {
		return
	}
	done, err := mg.dao.LocationsBackfilled()
	if errors.Is(err, dao.ErrSchemaBehind) {
		log.Info("places: the database has no photo positions yet (the release's migration), nothing read")
		return
	}
	if err != nil {
		log.Error("places: could not tell whether the positions were read:", err)
		return
	}
	if done {
		return
	}
	started := time.Now()
	read, located, unread := 0, 0, 0
	after := ""
	for {
		batch, err := mg.dao.MediaWithoutLocation(after, cLocationBackfillPage)
		if err != nil {
			log.Error("places: error listing the photos without a position:", err)
			return
		}
		for _, file := range batch {
			after = file.Hash
			if _, err := os.Stat(thumbnailPath(file.Hash)); err != nil {
				continue
			}
			lat, lon, ok, err := mg.readLocation(ses, file)
			if errors.Is(err, os.ErrNotExist) {
				// Its blob is gone (alerted elsewhere): what brings it
				// back processes it, and that writes the row.
				continue
			}
			if err != nil {
				unread++
				log.Debug("places: could not read the metadata of", file.Path, ":", err)
				continue
			}
			if err := mg.dao.SetPhotoLocation(file.Hash, lat, lon, ok); err != nil {
				log.Error("places: could not record a position, stopping:", err)
				return
			}
			mg.places.changed()
			read++
			if ok {
				located++
			}
			if strings.HasPrefix(file.Mime, "video/") {
				time.Sleep(cLocationBackfillVideoPause)
			} else {
				time.Sleep(cLocationBackfillPhotoPause)
			}
		}
		if len(batch) < cLocationBackfillPage {
			break
		}
	}
	if read > 0 || unread > 0 {
		log.Info("places: read the metadata of", read, "photo(s) and video(s),", located, "with a position, in", time.Since(started), "-", unread, "to read again at the next start")
	}
	if unread > 0 {
		return
	}
	if err := mg.dao.SetLocationsBackfilled(); err != nil {
		log.Error("places: could not record that the positions were read:", err)
	}
}
