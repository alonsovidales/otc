// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"strings"

	"github.com/gabriel-vasile/mimetype"

	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// Which processing failures reach the owner's Alerts (issue #64). Two
// kinds of file used to raise one there for nothing wrong:
//
//   - an image/* file in a format this device makes no preview of - a
//     Windows cursor, a scanned DjVu book - "could not be processed:
//     image: unknown format". It gets no thumbnail and no analysis, and
//     that is logged (Debug), not alerted. A format the device does
//     preview (previewedMimes) that fails to decode - a truncated or
//     damaged JPEG, a corrupt HEIC - is still alerted: that is a real
//     problem with the file.
//   - content kept out of Images (issue #192): the owner asked the device
//     not to process it, so a failure making its thumbnail (or a crash
//     doing so) is logged, not alerted. Kept out is the rule
//     reconcileAnalysis applies - a row of it under a flagged folder and
//     none outside them all.
//
// Failures to read or write the disk ("could not be read back", "could
// not be written") are about the storage, not the file's format, and
// are alerted wherever the file is.

// previewedMimes is what the device makes previews of - its Go decoders
// (JPEG, PNG, GIF, WebP, BMP, TIFF, camera RAW/DNG being TIFF), HEIC/HEIF
// (goheif), and what decodeStill's ffmpeg fallback was added for: JPEG
// 2000 and Photoshop. Aliases included: a row's MIME is whatever the
// upload's sniffing wrote.
var previewedMimes = map[string]bool{
	"image/jpeg":                true,
	"image/pjpeg":               true,
	"image/png":                 true,
	"image/vnd.mozilla.apng":    true,
	"image/gif":                 true,
	"image/webp":                true,
	"image/bmp":                 true,
	"image/x-bmp":               true,
	"image/x-ms-bmp":            true,
	"image/tiff":                true,
	"image/heic":                true,
	"image/heic-sequence":       true,
	"image/heif":                true,
	"image/heif-sequence":       true,
	"image/jp2":                 true,
	"image/jpx":                 true,
	"image/vnd.adobe.photoshop": true,
	"image/x-psd":               true,
	"application/photoshop":     true,
}

// neverPreviewedMimes is image/* content no decoder here reads - neither
// Go's nor any ffmpeg build's: scanned documents (DjVu), CAD drawings,
// GIMP's own files. It isn't media to process (isMedia): not read, not
// queued, never handed to ffmpeg. Anything else ffmpeg is left to try -
// its decoders differ by build (an .ico decodes, an .svg does where it
// has librsvg) - and a failure is quiet unless the format is previewed.
var neverPreviewedMimes = map[string]bool{
	"image/vnd.djvu":   true,
	"image/vnd.dwg":    true,
	"image/x-dwg":      true,
	"image/vnd.dxf":    true,
	"image/x-xcf":      true,
	"image/x-gimp-pat": true,
	"image/x-gimp-gbr": true,
}

// baseMime is mime without parameters, lower case.
func baseMime(mime string) string {
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = mime[:i]
	}
	return strings.ToLower(strings.TrimSpace(mime))
}

// neverPreviewed is whether a file of this MIME type can't have a preview
// on any device: see neverPreviewedMimes.
func neverPreviewed(mime string) bool {
	return neverPreviewedMimes[baseMime(mime)]
}

// sniffedNeverPreviewed is neverPreviewed by content, for decodeStill,
// which has no row.
func sniffedNeverPreviewed(content []byte) bool {
	return len(content) > 0 && neverPreviewed(mimetype.Detect(content).String())
}

// previewExpected is whether file's content, which failed to convert or
// decode, is in a format the device previews: by its name (a ".HEIC" -
// what put it in processMedia's image branch - whatever its bytes sniff
// as: a zero-filled or truncated export is stored as
// application/octet-stream, a HEIC whose major brand mimetype doesn't
// list as video/mp4), by the row's MIME, or by the content itself - so a
// JPEG whose blob is damaged past recognition still counts as a JPEG.
func previewExpected(file *pb.File, content []byte) bool {
	if isHeicFile(file.Path, file.Mime) || previewedMimes[baseMime(file.Mime)] {
		return true
	}
	return len(content) > 0 && previewedMimes[baseMime(mimetype.Detect(content).String())]
}

// keptOutOfImages is whether hash's content is kept out of Images now -
// reconcileAnalysis' rule: a row of it under a flagged folder and none
// outside them all. Asked only when something failed. False when it
// can't be told (logged): the alert then goes out as it always did.
func (mg *Manager) keptOutOfImages(hash string) bool {
	if hash == "" || mg.dao == nil || mg.noOutOfImages() {
		return false
	}
	folders, err := mg.OutOfImagesFolders()
	if err != nil {
		log.Error("could not read the folders kept out of Images:", err)
		return false
	}
	if len(folders) == 0 {
		return false
	}
	hashes := []string{hash}
	visible, err := mg.dao.VisibleHashes(hashes, folders)
	if err != nil {
		log.Error("could not check", hash, "against the folders kept out of Images:", err)
		return false
	}
	if visible[hash] {
		return false
	}
	under, err := mg.dao.HashesWithRowsUnder(hashes, folders)
	if err != nil {
		log.Error("could not check", hash, "against the folders kept out of Images:", err)
		return false
	}
	return under[hash]
}

// processingAlert is alert for a failure processing file's content:
// logged instead when the content is kept out of Images - by its hash at
// Info, its path at Debug only (issue #156: at level=info the log never
// names a file path, and these are paths the owner chose to keep private).
func (mg *Manager) processingAlert(what string, file *pb.File, err error) {
	if mg.keptOutOfImages(file.Hash) {
		log.Info("content", file.Hash, what, "(kept out of Images, not alerted):", err)
		log.Debug(file.Path, "is content", file.Hash)
		return
	}
	mg.alert(what, file.Path, err)
}

// stillFailed is processingAlert for a still that could not be converted
// or decoded - content is what failed - quiet, too, when it isn't in a
// format the device previews.
func (mg *Manager) stillFailed(what string, file *pb.File, content []byte, err error) {
	if !previewExpected(file, content) {
		log.Debug(file.Path, "is in a format this device makes no preview of, skipped:", err)
		return
	}
	mg.processingAlert(what, file, err)
}
