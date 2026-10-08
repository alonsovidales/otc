// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"image"
	"io"
	"os"
	"sync"
	"time"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/log"
)

// Stored thumbnails brought to the sizes thumbnail_size.go describes,
// always from the big thumbnail - never the original, a full decode of a
// photo for a few hundred kilobytes: a small one where there is none (or
// one of another size), and a big one whose longest side is over the cap
// (made before it bounded the height) scaled down. Used by the one-time
// pass (thumbnail_pass.go) and by the queue below.

type thumbFix int

const (
	thumbOK      thumbFix = iota // both there, at their sizes
	thumbFixed                   // a small one made, the big one scaled down, or both
	thumbSkipped                 // no big one (not processed yet), unreadable, too large, or changed or removed meanwhile
)

// Memory a fix holds from the content budget (membudget.go), per pixel of
// each buffer it allocates - measured against what it really allocates in
// TestThumbFixReservationCoversWhatItAllocates:
//   - cThumbDecodeBytesPerPixel for the decoded big thumbnail: the
//     decoder's YCbCr, at most 3 bytes a pixel (4:4:4).
//   - cThumbScaleTmpBytesPerPixel for each scale: x/image/draw's kernel
//     scaler keeps a [4]float64 for every destination column of every
//     source row (draw.kernelScaler.makeTmpBuf), 32 bytes times dstW x
//     srcH - for an old 1000x1333 portrait, ~10x what its decode takes.
//   - cThumbRGBABytesPerPixel for each scaled copy (an *image.RGBA), and
//     once more for the encoders' buffers.
//   - cThumbFixOverhead whatever the size: the sealed file's read buffers
//     and the decoder's own.
const (
	cThumbDecodeBytesPerPixel   = 3
	cThumbScaleTmpBytesPerPixel = 32
	cThumbRGBABytesPerPixel     = 4
	cThumbFixOverhead           = 1 << 20
)

// thumbFixReserve is what fixing a bw x bh big thumbnail holds from the
// content budget: its decode, and for each scale (the big one shrunk to
// maxSide when over it, then the small one) the scaler's buffer and the
// copy it makes. A 1000x30000 thumbnail - a long screenshot capped by width
// only - holds ~124 MB and waits for downloads to leave that much room
// rather than adding to them; an old 1000x1333 portrait ~58 MB.
func thumbFixReserve(bw, bh, maxSide int) int64 {
	tw, th := fitWithin(bw, bh, maxSide)
	n := int64(bw) * int64(bh) * cThumbDecodeBytesPerPixel
	srcW, srcH := int64(bw), int64(bh)
	if tw != bw || th != bh {
		n += int64(tw)*srcH*cThumbScaleTmpBytesPerPixel + 2*int64(tw)*int64(th)*cThumbRGBABytesPerPixel
		srcW, srcH = int64(tw), int64(th)
	}
	sw, sh := smallThumbnailSize(tw, th)
	if int64(sw) != srcW || int64(sh) != srcH {
		n += int64(sw)*srcH*cThumbScaleTmpBytesPerPixel + int64(sw)*int64(sh)*cThumbRGBABytesPerPixel
	}
	return n + int64(sw)*int64(sh)*cThumbRGBABytesPerPixel + cThumbFixOverhead
}

// beforeThumbFixWrite is a test hook: called with the hash about to be
// written, before its lock is taken.
var beforeThumbFixWrite func(hash string)

// imageSizeOf reads the size of the image sealed at path from its header:
// only the first segment is decrypted.
func imageSizeOf(path string, keys blobstore.Keys) (int, int, error) {
	b, err := blobstore.Open(path, keys)
	if err != nil {
		return 0, 0, err
	}
	defer b.Close()
	conf, _, err := image.DecodeConfig(io.NewSectionReader(b, 0, b.Size()))
	if err != nil {
		return 0, 0, err
	}
	return conf.Width, conf.Height, nil
}

// fixThumbnails brings hash's thumbnails to their sizes (see the top of
// this file). It reads the headers first and decodes the big thumbnail
// only when something has to change. What it writes is decided under the
// hash's lock (issue #141): the content may have been deleted meanwhile
// (both thumbnails went with it and must not come back) or processed
// again (new thumbnails, already right: kept).
func (mg *Manager) fixThumbnails(keys blobstore.Keys, hash string, maxSide int) thumbFix {
	if !dao.IsContentHash(hash) {
		return thumbSkipped
	}
	bigPath, smallPath := thumbnailPath(hash), smallThumbnailPath(hash)
	before, err := os.Stat(bigPath)
	if err != nil {
		return thumbSkipped // none yet (still in the lanes, or undecodable)
	}
	bw, bh, err := imageSizeOf(bigPath, keys)
	if err != nil {
		log.Error("thumbnails: could not read the thumbnail of", hash, ":", err)
		return thumbSkipped
	}
	tw, th := fitWithin(bw, bh, maxSide)
	sw, sh := smallThumbnailSize(tw, th)
	shrink := tw != bw || th != bh
	if !shrink {
		if w, h, err := imageSizeOf(smallPath, keys); err == nil && w == sw && h == sh {
			return thumbOK
		}
	}
	if int64(bw)*int64(bh) > cMaxImagePixels {
		log.Error("thumbnails: the thumbnail of", hash, "is too large to decode:", bw, "x", bh)
		return thumbSkipped
	}

	release := mg.ReserveBytes(thumbFixReserve(bw, bh, maxSide))
	big, small, err := remakeThumbnails(bigPath, keys, maxSide, shrink)
	release()
	if err != nil {
		log.Error("thumbnails: could not make the thumbnails of", hash, ":", err)
		return thumbSkipped
	}

	if beforeThumbFixWrite != nil {
		beforeThumbFixWrite(hash)
	}
	unlock := lockBlob(hash)
	defer unlock()
	now, err := os.Stat(bigPath)
	if err != nil || now.Size() != before.Size() || !now.ModTime().Equal(before.ModTime()) || !mg.hasBlob(hash) {
		return thumbSkipped
	}
	if err := blobstore.WriteBytes(smallPath, keys, small); err != nil {
		log.Error("thumbnails: could not write the small thumbnail of", hash, ":", err)
		return thumbSkipped
	}
	if big != nil {
		if err := blobstore.WriteBytes(bigPath, keys, big); err != nil {
			log.Error("thumbnails: could not rewrite the thumbnail of", hash, ":", err)
			return thumbSkipped
		}
	}
	return thumbFixed
}

// remakeThumbnails decodes the big thumbnail at path and makes the small
// one from it, and - when shrink - the big one again within maxSide (big
// is nil otherwise).
func remakeThumbnails(path string, keys blobstore.Keys, maxSide int, shrink bool) (big, small []byte, err error) {
	b, err := blobstore.Open(path, keys)
	if err != nil {
		return nil, nil, err
	}
	img, _, err := image.Decode(io.NewSectionReader(b, 0, b.Size()))
	b.Close()
	if err != nil {
		return nil, nil, err
	}
	if shrink {
		img = thumbnailSource(img, maxSide)
		if big, err = encodeJPEG(img, cThumbnailQuality); err != nil {
			return nil, nil, err
		}
	}
	if small, err = encodeJPEG(smallThumbnailSource(img), cSmallThumbnailQuality); err != nil {
		return nil, nil, err
	}
	return big, small, nil
}

// A grid that asks for small thumbnails gets the big one of content that
// has none yet, and the hash goes in this queue: one worker, in the
// background, makes them from the big ones - ~14 ms each on an M-series
// Mac, ~60 ms on a Pi 5 - so the next look at that page gets small ones.
// Making them while the request waits would put that time, times twelve
// or thirty, before the first tile, right when the owner opens Images, and
// compete with the upload lanes during a sync; answering the big one is
// exactly what the device did before. The one-time pass makes the rest.
//
// The worker gives way exactly as the pass does, or scrolling through an
// old library during a sync would keep the one core the lanes leave for
// requests busy for minutes: before each file it waits until no upload is
// arriving and the lanes are idle (waitForQuiet), and after a fix it rests
// as long as the fix took, at least cThumbPassMinPause. A full reprocess
// empties the queue - it makes every thumbnail again.

// cThumbFixQueueMax bounds the queue: more than this many hashes waiting
// are left to the pass.
const cThumbFixQueueMax = 1024

// thumbFixWaitQuiet is what the queue's worker waits on before each file:
// (*Manager).waitForQuiet, replaced by tests.
var thumbFixWaitQuiet = (*Manager).waitForQuiet

type thumbFixJob struct {
	keys blobstore.Keys
	hash string
}

type thumbFixQueue struct {
	mu      sync.Mutex
	jobs    []thumbFixJob
	queued  map[string]bool
	running bool
}

// queueThumbnailFix queues hash's thumbnails to be fixed, once.
func (mg *Manager) queueThumbnailFix(keys blobstore.Keys, hash string) {
	if keys == nil || !dao.IsContentHash(hash) {
		return
	}
	q := &mg.thumbFixes
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.queued[hash] || len(q.jobs) >= cThumbFixQueueMax {
		return
	}
	if q.queued == nil {
		q.queued = map[string]bool{}
	}
	q.queued[hash] = true
	q.jobs = append(q.jobs, thumbFixJob{keys: keys, hash: hash})
	if !q.running {
		q.running = true
		go mg.runThumbnailFixes()
	}
}

// runThumbnailFixes is the queue's worker, until the queue is empty.
func (mg *Manager) runThumbnailFixes() {
	q := &mg.thumbFixes
	for {
		quiet := thumbFixWaitQuiet(mg)
		q.mu.Lock()
		if !quiet {
			// A full reprocess makes every thumbnail again.
			log.Info("thumbnails: dropping", len(q.jobs), "queued small thumbnail(s) for a reprocess")
			q.jobs, q.queued = nil, nil
		}
		if len(q.jobs) == 0 {
			q.running = false
			q.mu.Unlock()
			return
		}
		j := q.jobs[0]
		q.jobs[0] = thumbFixJob{}
		q.jobs = q.jobs[1:]
		q.mu.Unlock()

		t := time.Now()
		res := mg.fixThumbnailsSafely(j.keys, j.hash, ThumbnailMaxSide())

		// Forgotten only once done: a page asked again meanwhile doesn't
		// queue it twice.
		q.mu.Lock()
		delete(q.queued, j.hash)
		q.mu.Unlock()
		if res == thumbFixed {
			time.Sleep(max(cThumbPassMinPause, time.Since(t)))
		}
	}
}

// fixThumbnailsSafely is fixThumbnails in background work: a panic (a
// decoder bug) is logged and the file skipped, rather than taking the
// service down. Logged only: these are the device's own JPEGs, and the
// grids show the big one meanwhile.
func (mg *Manager) fixThumbnailsSafely(keys blobstore.Keys, hash string, maxSide int) (res thumbFix) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("thumbnails: recovered from a panic on", hash, ":", r)
			res = thumbSkipped
		}
	}()
	return mg.fixThumbnails(keys, hash, maxSide)
}
