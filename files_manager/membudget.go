// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bufio"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/container"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
)

// A download holds its whole file in memory several times over - the
// encrypted blob, the decrypted content, and the reply packed for the wire
// - and a sync client fetches several files at once. A Mac re-downloading
// a large folder took a device from 3.8 to 7 GB in ten seconds and the
// kernel killed it. memBudget bounds the file content in flight: a download
// that doesn't fit waits for others to finish instead of adding to them.
type memBudget struct {
	mu   sync.Mutex
	cond *sync.Cond
	used int64
	max  int64
}

// cDownloadCopies is how many copies of a file's content a download holds
// at its peak (see memBudget).
const cDownloadCopies = 3

// cHeicDecodeFactor is roughly how many bytes of memory decoding a HEIC to
// JPEG takes per byte of the file (see ReserveForDownload).
const cHeicDecodeFactor = 36

func newMemBudget(max int64) *memBudget {
	b := &memBudget{max: max}
	b.cond = sync.NewCond(&b.mu)
	return b
}

// acquire reserves n bytes, waiting while they don't fit, and returns the
// release. A request larger than the whole budget still runs - alone - so
// no file is ever too big to download.
func (b *memBudget) acquire(n int64) func() {
	if n <= 0 {
		return func() {}
	}
	if n > b.max {
		n = b.max
	}
	b.mu.Lock()
	for b.used > 0 && b.used+n > b.max {
		b.cond.Wait()
	}
	b.used += n
	b.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			b.used -= n
			b.mu.Unlock()
			b.cond.Broadcast()
		})
	}
}

// contentBudgetBytes is a fifth of the machine's memory (1.6 GB on an 8 GB
// Raspberry Pi), at least 256 MB.
func contentBudgetBytes() int64 {
	const floor = 256 << 20
	total := memTotalBytes()
	if total <= 0 {
		return 1 << 30
	}
	if b := total / 5; b > floor {
		return b
	}
	return floor
}

// MemTotalBytes is MemTotal from /proc/meminfo, or 0 off Linux.
func MemTotalBytes() int64 { return memTotalBytes() }

// memTotalBytes is MemTotal from /proc/meminfo, or 0 off Linux. In a
// container it is the container's memory instead (container.MemoryTotal:
// its limit, or 8 GB without one), which the profile and every budget
// sized from this follow.
func memTotalBytes() int64 {
	if container.Enabled() {
		return container.MemoryTotal()
	}
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, err := strconv.ParseInt(fields[1], 10, 64)
			if err == nil {
				return kb << 10
			}
		}
	}
	return 0
}

// budgetMax is the content budget's size (contentBudgetBytes for a
// Manager without one, as in tests).
func (mg *Manager) budgetMax() int64 {
	if mg.contentBudget != nil {
		return mg.contentBudget.max
	}
	return contentBudgetBytes()
}

// What one reply may hold whole. A reply holds its content several times
// (cDownloadCopies), and acquire lets one larger than the whole budget
// run alone: a 1.5 GB TIFF opened in a viewer took ~3 GB, and a friend
// syncing a post's multi-GB video took more, every two minutes - the
// kernel killed the device each time. Beyond these the content is read in
// parts (ReadFile), streamed (GetMediaURL), or shown by its thumbnail.
const (
	// cMaxPublicationMedia is the most a post's file served whole may be.
	cMaxPublicationMedia = 256 << 20
	// cMinShownWhole is what GetFile always sends whole, however small the
	// budget.
	cMinShownWhole = 64 << 20
)

// ErrTooLarge is a reply refused because its content is over what one
// reply may hold whole (GetFile, GetPublicationMedia): the reply carries
// RespEnvelope.error_code "too_large", and the apps read the file in parts,
// stream it or keep showing its thumbnail.
var ErrTooLarge = errors.New("too large to send whole") // i18n-ignore: a sentinel for errors.Is; the reply shows TooLarge's text

// tooLargeError is TooLarge's refusal, worded by msg.
type tooLargeError struct{ msg i18n.Msg }

func (e *tooLargeError) Error() string { return devtext.New(e.msg).Error() }

// Localized is the refusal in lang (devtext.Text, for the reply).
func (e *tooLargeError) Localized(lang string) string { return devtext.New(e.msg).Localized(lang) }

func (e *tooLargeError) Is(target error) bool { return target == ErrTooLarge }

// TooLarge is a refusal (ErrTooLarge) that msg words.
func TooLarge(msg i18n.Msg) error { return &tooLargeError{msg: msg} }

// MaxPublicationMedia is the largest post file GetPublicationMedia sends
// whole: cMaxPublicationMedia, or a third of the content budget when that
// is less (85 MB on a 4 GB Raspberry Pi). The apps play a post's video
// from its stream (GetMediaURL) whatever its size; a friend's device
// keeps the thumbnail of a larger one.
func (mg *Manager) MaxPublicationMedia() int64 {
	return min(cMaxPublicationMedia, mg.budgetMax()/cDownloadCopies)
}

// shownWholeLimit is the largest file GetFile sends whole: a third of the
// content budget (533 MB on an 8 GB Raspberry Pi, 85 MB on a 4 GB one), at
// least cMinShownWhole.
func (mg *Manager) shownWholeLimit() int64 {
	return max(cMinShownWhole, mg.budgetMax()/cDownloadCopies)
}

// overShownWhole is whether GetFile refuses f as too large to send whole
// (shownWholeLimit). A HEIC is sent as its JPEG, which the decode budget
// bounds (heicToJpeg). A video isn't capped yet: the web composer's
// trimmer fetches one whole with GetFile and has no other way to read it,
// while every viewer streams videos and downloads read in parts.
func (mg *Manager) overShownWhole(f *pb.File) bool {
	if isHeicFile(f.Path, f.Mime) || strings.HasPrefix(f.Mime, "video/") {
		return false
	}
	return mg.budgetSize(f) > mg.shownWholeLimit()
}

// What processing reads whole: an image's decoders take all of it at once
// (a video is streamed to ffmpeg instead). It used to be read with no limit
// and held from no budget: a 2-3 GB TIFF scan or Photoshop file synced from
// a computer took a 4 GB device down - again at every start, until it was
// set aside. Processing (the lanes, Reprocess and the thumbnail backfill)
// now holds what it reads from the content budget while it works on it,
// and reads nothing larger than a decode may hold (processedWholeLimit):
// such a picture never fits its decode anyway - only a camera raw's
// preview would, and its raw that large is beyond any camera's. The
// ffmpeg fallback still gets its own copy, a temporary file.

// errTooLargeToProcess is readForProcessing's refusal, for errors.Is.
var errTooLargeToProcess = errors.New("too large to process") // i18n-ignore: a sentinel for errors.Is; the alert says tooLargeToProcess's text

// tooLargeToProcess is the refusal of content of size bytes, over limit.
func tooLargeToProcess(size, limit int64) error {
	m := i18n.DevFilesTooLargeToProcess(size, limit)
	return &englishErr{english: devtext.New(m).Error(), msg: m, cause: errTooLargeToProcess}
}

// processedWholeLimit is the largest content processing reads whole: the
// decode budget's size (limits.go: a fifth of the memory, ~760 MB on a
// 4 GB Raspberry Pi, 1.6 GB on an 8 GB one). A variable for the tests.
var processedWholeLimit = func() int64 { return stillDecodes().max }

// readForProcessing is the content of the blob at target, read whole for
// processing and held from the content budget until release (never nil) -
// or tooLargeToProcess, before anything is read, when it is over
// processedWholeLimit.
func (mg *Manager) readForProcessing(ses *session.Session, target string) (content []byte, release func(), err error) {
	b, err := blobstore.Open(target, ses)
	if err != nil {
		return nil, func() {}, err
	}
	defer b.Close()
	size := b.Size()
	if limit := processedWholeLimit(); size > limit {
		return nil, func() {}, tooLargeToProcess(size, limit)
	}
	release = mg.ReserveBytes(size)
	content = make([]byte, size)
	if _, err := b.ReadAt(content, 0); err != nil && !errors.Is(err, io.EOF) {
		release()
		return nil, func() {}, err
	}
	return content, release, nil
}

// ReserveBytes holds n bytes of the content budget, waiting for room.
// Never nil.
func (mg *Manager) ReserveBytes(n int64) func() {
	if mg.contentBudget == nil {
		return func() {}
	}
	return mg.contentBudget.acquire(n)
}

// ReservePublicationMedia holds the content budget for serving a post's
// media file (issue #166: read whole, for the owner's feed and friends).
func (mg *Manager) ReservePublicationMedia(hash string) func() {
	if !dao.IsContentHash(hash) {
		return func() {}
	}
	info, err := os.Stat(filepath.Join(cfg.GetStr("otc", "unenc-storage-path"), hash))
	if err != nil || info.Size() > mg.MaxPublicationMedia() {
		return func() {} // nothing to read, or refused as too large
	}
	return mg.ReserveBytes(info.Size() * cDownloadCopies)
}

// ReserveForDownload holds a share of the content budget for serving the
// file at path (a version, with versionHash), waiting if the device is
// already holding as much file content as it should. Call the returned
// release once the reply has been sent - not before, the reply is one of
// the copies. Never nil.
func (mg *Manager) ReserveForDownload(path, versionHash string) func() {
	if mg.contentBudget == nil {
		return func() {}
	}
	var file *pb.File
	var err error
	if versionHash != "" {
		file, err = mg.dao.GetFileVersion(path, versionHash)
	} else {
		file, err = mg.dao.GetFileByPath(path)
	}
	if err != nil || file == nil {
		return func() {}
	}
	if mg.overShownWhole(file) {
		return func() {} // refused by GetFile: nothing is read
	}
	sz := mg.budgetSize(file)
	need := sz * cDownloadCopies
	// Issue #168: a HEIC is served converted to JPEG, and the decode is what
	// costs memory, not the file: ~1.3 bits a pixel, so a 2 MB iPhone photo
	// is 12 MP - ~48 MB as RGBA, plus the decoder's YCbCr and the JPEG being
	// built. The sync clients read originals with ReadFile, never this.
	if isHeicFile(file.Path, file.Mime) {
		need += sz * cHeicDecodeFactor
	}
	if need > mg.contentBudget.max/2 {
		log.Debug("download of", path, "waits for", need>>20, "MB of the", mg.contentBudget.max>>20, "MB content budget")
	}
	return mg.contentBudget.acquire(need)
}

// SharedLinkKnown: a share link with this uuid exists, so a request for one
// of its parts is worth a share of the content budget. A cheap lookup only:
// the download itself still checks expiry and the secret.
func (mg *Manager) SharedLinkKnown(uuid string) bool {
	_, _, err := mg.dao.GetArchiveLinkExpiry(uuid)
	return err == nil
}

// SharedGalleryKnown is SharedLinkKnown for a gallery (issue #180).
func (mg *Manager) SharedGalleryKnown(id, secret string) bool {
	if !galleryUUID.MatchString(id) || !gallerySecret.MatchString(secret) {
		return false
	}
	_, _, err := mg.dao.GetSharedLinkExpiry(id)
	return err == nil
}

// budgetSize is how many bytes of content f holds, for the budget: the
// row's size (issue #187). Until the size backfill has run, a row stored
// before release 93 for content of 2 GiB or more still holds the size
// wrapped to int32 - negative at 2-4 GiB, which reserved nothing - so a
// size an int32 could hold is checked against the blob on disk (the
// content plus its header and 16 bytes a MiB, segcrypt: a little more
// than the content in either format) and the blob's used when it is
// larger than MaxInt32.
func (mg *Manager) budgetSize(f *pb.File) int64 {
	n := dao.FileSize(f)
	// No storage configured (a test's Manager): nothing on disk to ask.
	if n > math.MaxInt32 || mg.sizesBackfilled.Load() || !cfg.HasSection("otc") {
		return n
	}
	if fi, err := os.Stat(blobPath(f.Hash)); err == nil && fi.Size() > math.MaxInt32 {
		n = fi.Size()
	}
	return n
}
