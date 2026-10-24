// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"os/exec"
	"runtime/debug"
	"sync"
	"time"

	"github.com/alonsovidales/otc/alerttext"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// Issue #165: what one file may cost the device.
const (
	// cMaxImagePixels: no image larger than this is decoded, whatever the
	// device's memory - a 40,000 x 40,000 PNG can be a few kilobytes and
	// decode to 6.4 GB. 500 MP is above any camera's pixel-shift mode
	// (400 MP). Below it, what an image may cost is the device's memory's
	// say (decodeBudgetFor): until 2026-10-24 the limit was 120 MP on
	// every device, and a phone's 200 MP photos were refused.
	cMaxImagePixels = 500_000_000

	// cWorkPixels: a photo larger than this is brought down to it as it
	// is decoded (shrinkForWork) - its thumbnails, tags, faces and a
	// gallery's preview need far less, and the orientation, the scalers
	// and the face detector's copy each take a few bytes a pixel of what
	// they are given. 50 MP keeps every phone photo as it was (48 and 50
	// MP sensors); a 200 MP one is worked on at 50 MP.
	cWorkPixels = 50_000_000

	// Timeouts for ffprobe and ffmpeg: a call that hangs held its upload
	// processing slot forever.
	cProbeTimeout     = time.Minute
	cFrameTimeout     = 2 * time.Minute
	cTranscodeTimeout = 30 * time.Minute
)

// errImageTooLarge is checkImageSize's refusal.
var errImageTooLarge = errors.New("too large to process") // i18n-ignore: part of imageTooLarge's English; the catalog has the whole sentence

// imageTooLarge is the refusal of a w x h image whose kind may have at
// most limit pixels on this device (stillCost.maxPixels), wrapping
// errImageTooLarge.
func imageTooLarge(w, h, limit int64) error {
	mp := int(limit / 1_000_000)
	return &englishErr{
		english: fmt.Sprintf("the image is %dx%d, %s (over %d megapixels)", w, h, errImageTooLarge, mp), // i18n-ignore: the English as it always read; the catalog has the other languages
		msg:     i18n.DevFilesImageTooLarge(int(w), int(h), mp),
		cause:   errImageTooLarge,
	}
}

// What a decode holds is reserved from a budget of its own, sized by the
// machine's memory: decodes that don't fit wait for others to finish, and
// an image whose decode alone would take more than all of it is refused
// (checkImageSize). It is a fifth of the memory, the content budget's
// share for downloads (membudget.go) - 1.6 GB on an 8 GB Raspberry Pi,
// ~760 MB on a 4 GB one, at least 256 MB, 1 GB when the memory isn't
// known. Separate from the content budget: a download and a gallery
// preview hold that one while they decode (always in that order, never
// the other).
//
// What that lets through, by what each kind of image costs (stillCost,
// TestDecodeLimitGoesByMemory):
//
//	                         4 GB     8 GB
//	JPEG 4:2:0 (phones)      311 MP   500 MP
//	JPEG 4:2:2 (cameras)     259 MP   500 MP
//	JPEG 4:4:4               194 MP   419 MP
//	progressive JPEG 4:2:0    91 MP   197 MP
//	PNG, 8-bit RGBA          155 MP   335 MP
//	HEIC                      86 MP   186 MP
var decodeBudgetBytes = func() int64 { return decodeBudgetFor(memTotalBytes()) }

// decodeBudgetFor is the decode budget of a machine with total bytes of
// memory (0: unknown).
func decodeBudgetFor(total int64) int64 {
	const floor = 256 << 20
	if total <= 0 {
		return 1 << 30
	}
	return max(total/5, floor)
}

var (
	stillDecodesOnce sync.Once
	stillDecodesB    *memBudget
)

// stillDecodes is the decode budget, sized at its first use.
func stillDecodes() *memBudget {
	stillDecodesOnce.Do(func() { stillDecodesB = newMemBudget(decodeBudgetBytes()) })
	return stillDecodesB
}

// cDecodeOverhead is what a decode holds whatever the image's size: the
// decoder's own buffers, the header's parse.
const cDecodeOverhead = 16 << 20

// stillCost is what decoding a kind of image holds at its peak: perPixel
// bytes for each of its pixels and, when shrunk is set and it is over
// cWorkPixels, the copy shrinkForWork makes - a quarter of its pixels at
// most, 4 bytes each, so one more byte a pixel. The decoded image itself
// is let go once shrunk: what the caller goes on to do costs what a photo
// of cWorkPixels at most costs, as before.
type stillCost struct {
	perPixel float64
	shrunk   bool
}

// perPixelAt is the bytes a pixel of a w x h image costs.
func (c stillCost) perPixelAt(w, h int64) float64 {
	if c.shrunk && w*h > cWorkPixels {
		return c.perPixel + 1
	}
	return c.perPixel
}

// bytes is what decoding a w x h image holds.
func (c stillCost) bytes(w, h int64) int64 {
	return int64(float64(w*h)*c.perPixelAt(w, h)) + cDecodeOverhead
}

// maxPixels is the most pixels an image of this kind may have within
// budget bytes, and never over cMaxImagePixels: the "over N megapixels"
// of the refusal.
func (c stillCost) maxPixels(budget int64) int64 {
	room := float64(budget - cDecodeOverhead)
	n := int64(room / c.perPixel)
	if c.shrunk && n > cWorkPixels {
		// Past cWorkPixels the shrunk copy is held too; at least the
		// photos that aren't shrunk fit.
		n = max(cWorkPixels, int64(room/(c.perPixel+1)))
	}
	return max(0, min(n, cMaxImagePixels))
}

// check refuses a w x h image over cMaxImagePixels or whose decode alone
// would take more than the whole decode budget.
func (c stillCost) check(w, h int64) error {
	budget := stillDecodes().max
	if w*h > cMaxImagePixels || c.bytes(w, h) > budget {
		return imageTooLarge(w, h, c.maxPixels(budget))
	}
	return nil
}

// cHeicBytesPerPixel: a HEIC's decode, libde265's picture (two bytes a
// sample at 10 bits) and goheif's copy of it, then brought to 8 bits
// (decodeHeic) - and in heicToJpeg, once decoded, the oriented copy (an
// NRGBA, 4 bytes) and the JPEG it is encoded to.
const cHeicBytesPerPixel = 8

// stillSize reads b's header: the size it decodes to - a HEIC grid's
// canvas, which its header doesn't bound (checkHeifGrid) - and what its
// decode costs (decodeBytesPerPixel), refusing it when that is too much
// for this device (stillCost.check). ok is false, and nothing refused,
// when Go can't read the header: the decoder then fails on its own, or
// ffmpeg is tried (and its output is checked here too).
func stillSize(b []byte) (w, h int64, cost stillCost, ok bool, err error) {
	c, format, cerr := image.DecodeConfig(bytes.NewReader(b))
	if cerr != nil {
		return 0, 0, stillCost{}, false, nil
	}
	w, h = int64(c.Width), int64(c.Height)
	cost = stillCost{perPixel: decodeBytesPerPixel(b, format, c.ColorModel), shrunk: true}
	if w*h > cMaxImagePixels {
		return w, h, cost, true, imageTooLarge(w, h, cost.maxPixels(stillDecodes().max))
	}
	if isHeif(b) {
		gw, gh, gerr := checkHeifGrid(b, cost)
		if gerr != nil {
			return w, h, cost, true, gerr
		}
		if gw > 0 {
			w, h = gw, gh
		}
	}
	return w, h, cost, true, cost.check(w, h)
}

// isHeif: "????ftyp" is the signature goheif registers, so the file's
// name doesn't matter.
func isHeif(b []byte) bool {
	return len(b) >= 8 && string(b[4:8]) == "ftyp"
}

// checkImageSize refuses an image whose header says it's too large to
// decode on this device (stillSize). A header Go can't read is let
// through.
func checkImageSize(b []byte) error {
	_, _, _, _, err := stillSize(b)
	return err
}

// holdDecode checks b (stillSize) and holds what decoding it costs from
// the decode budget, waiting for room; release gives it back. Never nil.
func holdDecode(b []byte) (release func(), err error) {
	w, h, cost, ok, err := stillSize(b)
	if err != nil || !ok {
		return func() {}, err
	}
	return stillDecodes().acquire(cost.bytes(w, h)), nil
}

// decodeImage is image.Decode behind checkImageSize - a HEIC's HEVC
// still or grid through decodeHeic, which is right at 10 bits a sample
// where goheif's decoder isn't. The decode holds its cost from the
// decode budget while it runs (holdDecode), and a photo over cWorkPixels
// comes back shrunk to it (shrinkForWork): every caller makes thumbnails,
// tags, faces or a preview of it, never the full size (a HEIC's is
// heicToJpeg's).
func decodeImage(b []byte) (image.Image, error) {
	release, err := holdDecode(b)
	if err != nil {
		return nil, err
	}
	defer release()
	img, err := decodeFull(b)
	if err != nil {
		return nil, err
	}
	if shrunk := shrinkForWork(img); shrunk != img {
		// The full decode - hundreds of MB - is given back before the
		// next decode is let in, not whenever the GC gets to it.
		img = shrunk
		debug.FreeOSMemory()
	}
	return img, nil
}

// decodeFull is decodeImage's decode, at full size and unchecked.
func decodeFull(b []byte) (image.Image, error) {
	if isHeif(b) {
		if img, err := decodeHeic(b); !errors.Is(err, errNotHevcStill) {
			return img, err
		}
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	return img, err
}

// stillFFmpeg is decodeWithFFmpeg; a variable so tests can tell whether
// decodeStill reached it.
var stillFFmpeg = decodeWithFFmpeg

// decodeStill is decodeImage, then ffmpeg for what Go's decoders can't
// read - JPEG 2000, Photoshop, a JPEG cut short or with a damaged marker.
// A camera raw is its own JPEG preview (rawPreview), first: no decoder
// here reads its raw, and where its first image is one Go reads that is a
// thumbnail. Any other TIFF Go can't read is tried for one before ffmpeg. Never for anything checkImageSize refuses (issue
// #188): ffmpeg would decode it anyway, out of process and unbounded (its
// own HEIC grid reading included), taking the memory the limit keeps -
// too large, and also a HEIF grid that doesn't match its header. Nor for
// content no ffmpeg reads (a DjVu document: neverPreviewedMimes), which
// would only be copied to a temporary file to be refused. When ffmpeg
// fails too, or isn't tried, the decoder's own error is returned; path is
// only for the log.
func decodeStill(content []byte, path string) (image.Image, error) {
	if isCameraRaw(content) {
		if img, err := decodeRawPreview(content); err == nil {
			return img, nil
		}
	}
	img, err := decodeImage(content)
	if err == nil || errors.Is(err, errImageTooLarge) || checkImageSize(content) != nil {
		return img, err
	}
	if sniffedNeverPreviewed(content) {
		return nil, err
	}
	if isTIFF(content) {
		if preview, perr := decodeRawPreview(content); perr == nil {
			return preview, nil
		}
	}
	fallback, ffErr := stillFFmpeg(content)
	if ffErr != nil {
		log.Debug("ffmpeg could not decode", path, "either:", ffErr)
		return nil, err
	}
	return fallback, nil
}

// decodeRawPreview is a camera raw's JPEG preview (rawPreview), decoded
// behind checkImageSize as any JPEG is.
func decodeRawPreview(content []byte) (image.Image, error) {
	jpg, err := rawPreview(content)
	if err != nil {
		return nil, err
	}
	return decodeImage(jpg)
}

// command is exec.CommandContext with a deadline; the returned cancel must
// be called once the command is done.
func command(timeout time.Duration, name string, args ...string) (*exec.Cmd, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	return exec.CommandContext(ctx, name, args...), cancel
}

// safely runs background work so that a panic in it (a decoder bug on a
// malformed file, say) is logged and raised to the owner - crashed is the
// alert, given the panic's text - instead of taking the whole service
// down, and, on restart, doing it again.
func (mg *Manager) safely(what string, crashed func(panicked string) alerttext.Alert, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("recovered from a panic while", what+":", r, string(debug.Stack()))
			// Telling the owner must not be what brings the process down.
			defer func() {
				if r2 := recover(); r2 != nil {
					log.Error("could not raise the alert either:", r2)
				}
			}()
			mg.raise(crashed(fmt.Sprint(r)))
		}
	}()
	fn()
}

// safelyOn is safely for work on one file's content: a panic on content
// kept out of Images (issue #192) is logged, not alerted - the owner
// asked the device not to process it.
func (mg *Manager) safelyOn(what string, file *pb.File, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("recovered from a panic while", what, "a file:", r, string(debug.Stack()))
			// Telling the owner must not be what brings the process down.
			defer func() {
				if r2 := recover(); r2 != nil {
					log.Error("could not raise the alert either:", r2)
				}
			}()
			mg.processingAlert(i18n.DevAlertFileCrashed, file, fmt.Errorf("%v", r))
		}
	}()
	fn()
}
