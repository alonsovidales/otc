// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"os"
	"strconv"
	"strings"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"golang.org/x/image/draw"
)

// Every photo and video has two thumbnails, JPEGs sealed under the owner's
// key next to its blob, both made from the picture as it is shown (EXIF
// orientation applied) and never scaled up:
//
//   - <hash>_thumbnail, the big one: its longest side at most
//     ThumbnailMaxSide() (1000 px), quality 80: the viewer's placeholder,
//     Social's posts, shared galleries, and every answer that doesn't ask
//     for the small one.
//   - <hash>_thumbnail_small, a grid's tile: its shorter side
//     cSmallThumbnailSide (400 px), its longer side at most twice that (a
//     panorama stays small), quality 75. Sent instead of the big one by
//     SearchPhotos, GetThumbnails, ListImageGroups and CreateImageGroup
//     when the request sets small_thumbnails. Its aspect is the picture's:
//     the clients crop it to a square, the device never does.
//
// Thumbnails travel inline in those answers, a page of 12 or 30 at a time,
// over mobile data: a 1000 px one is ~105 KB, a small one ~38 KB (32
// photos measured; see CLAUDE.md).
//
// Processing writes the small one first and the big one last: the big one
// is what says a file is processed (hasThumbnail, ImageSearch's #147
// rule), so whoever sees it finds the small one too. Both are removed
// together, under the hash's lock (removeBlobIfUnused, dropIfOrphaned).
// Content from before the small ones existed gets them from the big one -
// never the original - by the one-time pass (thumbnail_pass.go) or, when a
// grid asks for one first, the queue in thumbnail_fix.go.

const (
	cThumbnailSuffix      = "_thumbnail"
	cSmallThumbnailSuffix = "_thumbnail_small"

	// cDefaultThumbnailMaxSide is the cap when [otc] max-thumbnail-width-px
	// is missing or not a positive number (a scale to 0 pixels used to
	// follow).
	cDefaultThumbnailMaxSide = 1000
	cThumbnailQuality        = 80

	// cSmallThumbnailSide is a small thumbnail's shorter side: a grid tile
	// is at most ~375 device pixels on the phones (three across at 390 pt
	// @3x, the Fold's wide grid of 120 dp tiles), 300-310 in the web's
	// Images at 1440 px and DPR 2, ~370 at 390 px and DPR 3. The Files
	// grid's and Collections' bigger tiles on a phone (up to ~540 px) scale
	// it up a little, which a 3x screen hardly shows.
	cSmallThumbnailSide = 400
	// cSmallThumbnailMaxLong bounds the longer side: a 4:1 panorama is
	// 800x200 rather than 1600x400.
	cSmallThumbnailMaxLong = 2 * cSmallThumbnailSide
	cSmallThumbnailQuality = 75
)

// thumbnailPath is where hash's big thumbnail is.
func thumbnailPath(hash string) string { return blobPath(hash) + cThumbnailSuffix }

// smallThumbnailPath is where hash's small thumbnail is.
func smallThumbnailPath(hash string) string { return blobPath(hash) + cSmallThumbnailSuffix }

// ThumbnailMaxSide is the longest side, in pixels, of every big thumbnail
// the device makes (photos, video posters, posts' and shared galleries'
// copies): [otc] max-thumbnail-width-px. The key keeps its old name - every
// device has it in its ini - but it bounds the longest side, not the
// width: a portrait photo capped by width alone was 1000x1333, and a long
// screenshot 1000 pixels wide and ten times as tall.
func ThumbnailMaxSide() int {
	if !cfg.HasSection("otc") {
		return cDefaultThumbnailMaxSide
	}
	raw := strings.TrimSpace(cfg.GetStr("otc", "max-thumbnail-width-px"))
	if raw == "" {
		return cDefaultThumbnailMaxSide
	}
	if n, err := strconv.Atoi(raw); err == nil && n > 0 {
		return n
	}
	log.Error("[otc] max-thumbnail-width-px is not a positive number, using", cDefaultThumbnailMaxSide)
	return cDefaultThumbnailMaxSide
}

// fitWithin is the size w x h scales down to so that neither side is over
// maxSide, keeping the aspect ratio (rounded, never under one pixel). A
// size that already fits - or maxSide <= 0 - is returned as it is: a
// thumbnail is never scaled up.
func fitWithin(w, h, maxSide int) (int, int) {
	if maxSide <= 0 || (w <= maxSide && h <= maxSide) {
		return w, h
	}
	if w >= h {
		return maxSide, max(1, int((int64(h)*int64(maxSide)+int64(w)/2)/int64(w)))
	}
	return max(1, int((int64(w)*int64(maxSide)+int64(h)/2)/int64(h))), maxSide
}

// smallThumbnailSize is the size of the small thumbnail of a w x h picture
// (its big thumbnail's size): the shorter side cSmallThumbnailSide, unless
// that makes the longer one over cSmallThumbnailMaxLong, which then bounds
// it. Never larger than w x h.
func smallThumbnailSize(w, h int) (int, int) {
	short, long := min(w, h), max(w, h)
	if short <= 0 {
		return w, h
	}
	// The scale is num/den, kept as integers so the pass computes exactly
	// the size processing wrote.
	num, den := int64(cSmallThumbnailSide), int64(short)
	if int64(long)*num > int64(cSmallThumbnailMaxLong)*den {
		num, den = cSmallThumbnailMaxLong, int64(long)
	}
	if num >= den {
		return w, h
	}
	return max(1, int((int64(w)*num+den/2)/den)), max(1, int((int64(h)*num+den/2)/den))
}

// scaleTo is img at w x h, or img itself when it already is that size.
func scaleTo(img image.Image, w, h int) image.Image {
	b := img.Bounds()
	if w == b.Dx() && h == b.Dy() {
		return img
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	return dst
}

// thumbnailSource returns the image a big thumbnail is encoded from: img
// scaled down so its longest side is maxSide, or img itself, unchanged,
// when it already fits. Always returns something to encode - a thumbnail
// must exist once a file is uploaded, full stop (see processMedia), so "no
// resize needed" must never mean "no thumbnail" - a gap that used to leave
// nothing on disk for an image already small enough.
//
// img must already be the way the photo is shown (EXIF orientation
// applied): a portrait photo stored sideways is capped by its height.
func thumbnailSource(img image.Image, maxSide int) image.Image {
	b := img.Bounds()
	w, h := fitWithin(b.Dx(), b.Dy(), maxSide)
	return scaleTo(img, w, h)
}

// smallThumbnailSource is the image a small thumbnail is encoded from,
// given the big thumbnail's.
func smallThumbnailSource(big image.Image) image.Image {
	b := big.Bounds()
	w, h := smallThumbnailSize(b.Dx(), b.Dy())
	return scaleTo(big, w, h)
}

func encodeJPEG(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// encodeThumbnails makes both thumbnails from shown, the picture as it is
// shown: the small one from the big one's pixels, a fraction of the work
// of scaling the original twice.
func encodeThumbnails(shown image.Image, maxSide int) (big, small []byte, err error) {
	bigImg := thumbnailSource(shown, maxSide)
	if big, err = encodeJPEG(bigImg, cThumbnailQuality); err != nil {
		return nil, nil, err
	}
	if small, err = encodeJPEG(smallThumbnailSource(bigImg), cSmallThumbnailQuality); err != nil {
		return nil, nil, err
	}
	return big, small, nil
}

// writeThumbnails stores both thumbnails of file (at targetPath, its blob)
// from shown: the small one first, the big one last (see the top of this
// file). The two writes hold the hash's lock, so a fix of the old ones
// (fixThumbnails, which checks under it that the big one is unchanged)
// lands before both or not at all - never a small one from the old big
// one next to the new one. Content deleted meanwhile is still the
// deferred dropIfOrphaned's to clean up, as for the rest of processing.
func (mg *Manager) writeThumbnails(ses *session.Session, file *pb.File, targetPath string, shown image.Image) {
	big, small, err := encodeThumbnails(shown, ThumbnailMaxSide())
	if err != nil {
		mg.processingAlert("has no thumbnail (it could not be encoded)", file, err)
		return
	}
	unlock := lockBlob(file.Hash)
	smallErr := blobstore.WriteBytes(targetPath+cSmallThumbnailSuffix, ses, small)
	bigErr := blobstore.WriteBytes(targetPath+cThumbnailSuffix, ses, big)
	unlock()
	if err := bigErr; err != nil {
		mg.alert("has no thumbnail (it could not be written)", file.Path, errors.Join(err, smallErr))
		return
	}
	if smallErr != nil {
		// The grids get the big one meanwhile, and queue it again.
		mg.alert("has no small thumbnail (it could not be written)", file.Path, smallErr)
	}
}

// readGridThumbnail is the thumbnail a grid shows for f: the small one when
// small is asked for and there is one, otherwise the big one. A small one
// that is missing - content processed before they existed, until the pass
// reaches it - is queued to be made from the big one (thumbnail_fix.go),
// and the big one is answered meanwhile, as stored: one made before
// release 111 is capped by width only (1000x1333, or 1000 px wide and any
// height) until the queue or the pass rescales it. Either way only content whose big
// thumbnail exists is shown (#147: processed).
func (mg *Manager) readGridThumbnail(ses *session.Session, f *pb.File, small bool) ([]byte, error) {
	if small {
		content, err := blobstore.ReadAll(smallThumbnailPath(f.Hash), ses)
		if err == nil {
			// The big one goes first when content is removed: a small one
			// alone is a deletion half done, not a processed file.
			if _, statErr := os.Stat(thumbnailPath(f.Hash)); statErr != nil {
				return nil, statErr
			}
			return content, nil
		}
		big, bigErr := mg.readThumbnail(ses, f)
		if bigErr == nil {
			mg.queueThumbnailFix(ses, f.Hash)
		}
		return big, bigErr
	}
	return mg.readThumbnail(ses, f)
}
