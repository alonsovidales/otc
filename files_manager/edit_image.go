// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math"
	"path"
	"runtime/debug"
	"slices"
	"strings"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/exifinfo"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"github.com/google/uuid"
	"github.com/jdeng/goheif"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Photo edits (EditImage, RevertImage and GetEditState in messages.proto):
// the viewers' Edit turns and crops a photo on the device. An edit is
// content of its own - its own hash, blob, thumbnails and analysis - and
// dao's edited_content names the photo it was made from, the original,
// which is kept with no path while an edit of it is held: RevertImage puts
// it back, HasFile and HasHashes count it, and a phone's sync sending it
// into the folder holding its edit gets the edit for an answer
// (editedAlready), so it is neither sent nor stored twice.
//
// Replaced in place, an edit takes the path (or, every copy, each path
// holding the content), with ".jpg" for a name whose extension no longer
// fits; saved as a copy, it goes next to the photo as "<name> (edited)".
// Its faces are the original's, their boxes moved by the edit (who is in
// it stays as the owner sorted it); its tags, quality, Smart search vector,
// date and place are worked out from it as from any new photo, its date
// and place from the camera details it keeps.

// ErrNotEditable is EditImage's refusal for content the device doesn't
// edit (RespEnvelope.error_code "not_editable").
var ErrNotEditable = devtext.New(i18n.DevFilesNotEditable())

// ErrNotEdited is RevertImage's for a path whose content is no edit
// (error_code "not_edited").
var ErrNotEdited = devtext.New(i18n.DevFilesNotEdited())

// errEditsUnavailable: the database has no edited_content yet (an update
// under way). Answered as not_editable, with its own message.
var errEditsUnavailable = devtext.New(i18n.DevFilesEditsUnavailable())

// IsNotEditable is whether err is a refusal to edit (error_code
// "not_editable").
func IsNotEditable(err error) bool {
	return errors.Is(err, ErrNotEditable) || errors.Is(err, errEditsUnavailable) || errors.Is(err, errNotCuttable)
}

// cEditQuality is the JPEG quality an edit is written at: near the camera's
// own, so a crop loses nothing anyone sees.
const cEditQuality = 92

// editKind is how the device writes an edit of some content.
type editKind int

const (
	editNone  editKind = iota
	editJPEG           // a JPEG: turned losslessly when only turned, else written as a JPEG
	editPNG            // a PNG, written as a PNG
	editPhoto          // HEIC, HEIF, AVIF, a still WebP: written as a JPEG
)

// editKindOf is what content is, for an edit, by its own bytes: camera
// raws (a JPEG preview inside a TIFF), animations and everything else that
// isn't a photo can't be edited.
func editKindOf(content []byte) editKind {
	switch {
	case len(content) < 16 || isCameraRaw(content):
		return editNone
	case isJPEGContent(content):
		return editJPEG
	case bytes.HasPrefix(content, []byte("\x89PNG\r\n\x1a\n")):
		if bytes.Contains(content[:min(len(content), 1<<16)], []byte("acTL")) {
			return editNone // an animated PNG
		}
		return editPNG
	case isHeifStill(content):
		return editPhoto
	case string(content[:4]) == "RIFF" && string(content[8:12]) == "WEBP":
		if bytes.Contains(content[:min(len(content), 1<<16)], []byte("ANIM")) {
			return editNone // an animated WebP
		}
		return editPhoto
	}
	return editNone
}

// cHeifStillBrands are the major brands of a HEIF holding a still photo:
// a HEIC, a HEIF, an AVIF - not a sequence (msf1, hevc, avis), nor an MP4
// or a QuickTime movie, which start with an ftyp box too.
var cHeifStillBrands = map[string]bool{"heic": true, "heix": true, "heim": true, "heis": true, "mif1": true, "mif2": true, "miaf": true, "MiHE": true, "MiHB": true, "avif": true}

// isHeifStill is whether b is a HEIF still photo, by its major brand.
func isHeifStill(b []byte) bool {
	return isHeif(b) && len(b) >= 12 && cHeifStillBrands[string(b[8:12])]
}

// editTurn is the EXIF orientation of quarter turns clockwise.
var editTurn = [4]int{1, 6, 3, 8}

// orientationMatrix is each EXIF orientation's linear part, as
// applyOrientation maps a pixel: [dx dy] = m * [x y] (+ a shift).
var orientationMatrix = [9][4]int{
	{}, {1, 0, 0, 1}, {-1, 0, 0, 1}, {-1, 0, 0, -1}, {1, 0, 0, -1},
	{0, 1, 1, 0}, {0, -1, 1, 0}, {0, -1, -1, 0}, {0, 1, -1, 0},
}

// thenTurned is the orientation that does o, then turns quarter turns
// clockwise: one pass over the pixels (applyOrientation) for both, and
// what a JPEG turned losslessly says in its tag.
func thenTurned(o, turns int) int {
	if o < 1 || o > 8 {
		o = 1
	}
	a, b := orientationMatrix[editTurn[((turns%4)+4)%4]], orientationMatrix[o]
	m := [4]int{a[0]*b[0] + a[1]*b[2], a[0]*b[1] + a[1]*b[3], a[2]*b[0] + a[3]*b[2], a[2]*b[1] + a[3]*b[3]}
	for k := 1; k <= 8; k++ {
		if orientationMatrix[k] == m {
			return k
		}
	}
	return 1
}

// editRect is crop in pixels of a w x h picture: whole when nil or empty,
// at least one pixel, inside the picture.
func editRect(crop *pb.EditCrop, w, h int) image.Rectangle {
	if crop == nil || crop.Width <= 0 || crop.Height <= 0 {
		return image.Rect(0, 0, w, h)
	}
	clamp := func(v float64) float64 { return math.Max(0, math.Min(1, v)) }
	x0 := int(math.Round(clamp(crop.X) * float64(w)))
	y0 := int(math.Round(clamp(crop.Y) * float64(h)))
	x1 := int(math.Round(clamp(crop.X+crop.Width) * float64(w)))
	y1 := int(math.Round(clamp(crop.Y+crop.Height) * float64(h)))
	x0, y0 = min(x0, w-1), min(y0, h-1)
	x1, y1 = max(x1, x0+1), max(y1, y0+1)
	return image.Rect(x0, y0, x1, y1)
}

// editResult is an edit's new content: its bytes, MIME type and the
// extension a name for it takes; turns and crop (in pixels of the photo
// turned), the photo's upright size before it and the edit's after, for
// moving its faces.
type editResult struct {
	content      []byte
	mime, ext    string
	turns        int
	crop         image.Rectangle
	fromW, fromH int
	toW, toH     int
}

// renderEdit edits content (of kind): its own orientation, then turns
// quarter turns clockwise, then crop of that.
func renderEdit(content []byte, kind editKind, turns int, crop *pb.EditCrop) (*editResult, error) {
	turns = ((turns % 4) + 4) % 4
	var exif []byte
	var icc [][]byte
	orientation := 1
	switch kind {
	case editJPEG:
		exif, icc = jpegMetadata(content)
		if info, err := exifinfo.FromJPEG(content); err == nil {
			orientation = info.Orientation
		}
	case editPhoto:
		if isHeif(content) {
			var profile []byte
			exif, profile = heicMetadata(content)
			icc = iccSegments(profile)
			fallback := 1
			if info, err := exifinfo.FromHEIC(content); err == nil {
				fallback = info.Orientation
			}
			orientation = fallback
			if r, mirror, axis := heifTransform(content); r != 0 || mirror {
				orientation = heifOrientation(r, mirror, axis)
			}
		}
	}
	whole := crop == nil || crop.Width <= 0 || crop.Height <= 0 ||
		(crop.X <= 0 && crop.Y <= 0 && crop.X+crop.Width >= 1 && crop.Y+crop.Height >= 1)

	// A JPEG only turned: its tag says the turn, nothing else changes.
	if kind == editJPEG && whole && exif != nil {
		if r, ok := losslessTurn(content, exif, orientation, turns); ok {
			return r, nil
		}
	}

	release, err := holdDecode(content)
	if err != nil {
		return nil, err
	}
	defer release()
	var img image.Image
	if isHeif(content) {
		img, err = decodeHeic(content)
		if errors.Is(err, errNotHevcStill) {
			img, err = goheif.Decode(bytes.NewReader(content))
		}
	} else {
		img, err = decodeFull(content)
	}
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	r := &editResult{turns: turns, fromW: b.Dx(), fromH: b.Dy()}
	if orientation >= 5 && orientation <= 8 {
		r.fromW, r.fromH = r.fromH, r.fromW
	}
	img = applyOrientation(img, thenTurned(orientation, turns))
	b = img.Bounds()
	r.crop = editRect(crop, b.Dx(), b.Dy())
	if !r.crop.Eq(image.Rect(0, 0, b.Dx(), b.Dy())) {
		sub, ok := img.(interface {
			SubImage(image.Rectangle) image.Image
		})
		if !ok {
			return nil, errors.New("this picture can't be cropped") // i18n-ignore: wrapped in dev.files.edit_failed
		}
		img = sub.SubImage(r.crop.Add(b.Min))
	}
	r.toW, r.toH = r.crop.Dx(), r.crop.Dy()

	var out bytes.Buffer
	if kind == editPNG {
		if err := png.Encode(&out, img); err != nil {
			return nil, err
		}
		r.content, r.mime, r.ext = out.Bytes(), "image/png", ".png"
		return r, nil
	}
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: cEditQuality}); err != nil {
		return nil, err
	}
	img = nil
	if exif != nil {
		edited, _, exifErr := editedExif(exif, 1, r.toW, r.toH, true)
		if exifErr != nil {
			log.Info("an edit leaves out its photo's camera details, which can't be read:", exifErr)
		}
		exif = edited
	}
	r.content, r.mime, r.ext = withMetadata(out.Bytes(), exif, icc), "image/jpeg", ".jpg"
	debug.FreeOSMemory() // the full-size decode, given back before the next one
	return r, nil
}

// losslessTurn is a JPEG turned by its orientation tag alone, when its
// EXIF has one and its size can be read.
func losslessTurn(content, exif []byte, orientation, turns int) (*editResult, bool) {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(content))
	if err != nil {
		return nil, false
	}
	edited, has, err := editedExif(exif, thenTurned(orientation, turns), 0, 0, false)
	if err != nil || !has {
		return nil, false
	}
	turned, err := jpegWithExif(content, edited)
	if err != nil {
		return nil, false
	}
	r := &editResult{content: turned, mime: "image/jpeg", ext: ".jpg", turns: turns, fromW: cfg.Width, fromH: cfg.Height}
	if orientation >= 5 && orientation <= 8 {
		r.fromW, r.fromH = r.fromH, r.fromW
	}
	r.toW, r.toH = r.fromW, r.fromH
	if turns%2 == 1 {
		r.toW, r.toH = r.fromH, r.fromW
	}
	r.crop = image.Rect(0, 0, r.toW, r.toH)
	return r, true
}

// workScale is how many pixels of a w x h photo one pixel of the picture
// its analysis worked on is (shrinkForWork): its faces' boxes are in that
// picture's pixels.
func workScale(w, h int) int {
	if int64(w)*int64(h) <= cWorkPixels {
		return 1
	}
	return shrinkFactor(w, h, cWorkPixels)
}

// moveFace is a face box of the photo edited by r, in the edit (both in
// their analysis' pixels, workScale); false when the edit cut most of it
// off.
func (r *editResult) moveFace(box dao.FaceBox) (dao.FaceBox, bool) {
	k := workScale(r.fromW, r.fromH)
	x0, y0 := box.X*k, box.Y*k
	x1, y1 := (box.X+box.W)*k, (box.Y+box.H)*k
	w, h := r.fromW, r.fromH
	turn := func(x, y int) (int, int) {
		switch r.turns {
		case 1:
			return h - y, x
		case 2:
			return w - x, h - y
		case 3:
			return y, w - x
		}
		return x, y
	}
	ax, ay := turn(x0, y0)
	bx, by := turn(x1, y1)
	tx0, tx1 := min(ax, bx), max(ax, bx)
	ty0, ty1 := min(ay, by), max(ay, by)
	area := (tx1 - tx0) * (ty1 - ty0)
	cx0, cy0 := max(tx0, r.crop.Min.X), max(ty0, r.crop.Min.Y)
	cx1, cy1 := min(tx1, r.crop.Max.X), min(ty1, r.crop.Max.Y)
	if area <= 0 || cx1 <= cx0 || cy1 <= cy0 || 2*(cx1-cx0)*(cy1-cy0) < area {
		return dao.FaceBox{}, false
	}
	k2 := workScale(r.toW, r.toH)
	return dao.FaceBox{
		X: (cx0 - r.crop.Min.X) / k2, Y: (cy0 - r.crop.Min.Y) / k2,
		W: max(1, (cx1-cx0)/k2), H: max(1, (cy1-cy0)/k2),
	}, true
}

// fits is whether a name with extension ext suits content of mime.
func fits(ext, mime string) bool {
	switch strings.ToLower(ext) {
	case ".jpg", ".jpeg", ".jpe":
		return mime == "image/jpeg"
	case ".png":
		return mime == "image/png"
	}
	return false
}

// splitExt is p without its extension, and the extension (".HEIC"; "" for
// a name without one).
func splitExt(p string) (stem, ext string) {
	ext = path.Ext(p)
	if ext == path.Base(p) || strings.ContainsAny(ext, " ") {
		return p, ""
	}
	return strings.TrimSuffix(p, ext), ext
}

// caseLike is ext in upper case when like is ("IMG_1.HEIC" becomes
// "IMG_1.JPG").
func caseLike(ext, like string) string {
	if like != "" && like == strings.ToUpper(like) && like != strings.ToLower(like) {
		return strings.ToUpper(ext)
	}
	return ext
}

// renamedNames is the names a path whose content is now of type mime
// (extension ext) tries, first free first: its own when its extension
// fits; else its name with ext, then "<name> (2)<ext>" and on.
func renamedNames(p, mime, ext string) func(n int) string {
	stem, own := splitExt(p)
	if fits(own, mime) {
		return nil
	}
	ext = caseLike(ext, own)
	return func(n int) string {
		if n == 0 {
			return stem + ext
		}
		return fmt.Sprintf("%s (%d)%s", stem, n+1, ext)
	}
}

// copyNames is the names an edit's copy of the photo at p tries: "<name>
// (edited)<ext>" in lang, then with a number.
func copyNames(p, ext, lang string) func(n int) string {
	stem, own := splitExt(p)
	dir, name := path.Split(stem)
	ext = caseLike(ext, own)
	base := i18n.DevFilesEditedCopyName(name).Render(lang)
	return func(n int) string {
		if n == 0 {
			return dir + base + ext
		}
		return fmt.Sprintf("%s%s %d%s", dir, base, n+1, ext)
	}
}

// editContent reads the content at file, as GetFile does.
func (mg *Manager) editContent(ses *session.Session, file *pb.File) ([]byte, error) {
	if mg.overShownWhole(file) {
		return nil, TooLarge(i18n.DevFilesTooLargeToOpen(mg.budgetSize(file)))
	}
	content, err := blobstore.ReadAll(blobPath(file.Hash), ses)
	if err != nil {
		return nil, devtext.New(i18n.DevFilesContentUnreadable(file.Path))
	}
	return content, nil
}

// storeEditContent writes content (of hash) to the disk, unless it is
// there: refused, with nothing written, when there is no room.
func (mg *Manager) storeEditContent(ses *session.Session, hash string, content []byte) error {
	if mg.hasBlob(hash) {
		return nil
	}
	target := blobPath(hash)
	if err := pendingUploads.roomFor(path.Dir(target), sealedSize(int64(len(content)))); err != nil {
		return err
	}
	unlock := lockBlob(hash)
	defer unlock()
	return blobstore.WriteBytes(target, ses, content)
}

// EditImage edits the photo at req.Path (EditImage in messages.proto) and
// answers the edited file: the path's own, or - as_copy - the copy's.
func (mg *Manager) EditImage(ses *session.Session, req *pb.EditImage, lang string) (*pb.File, error) {
	noteUploadActivity() // the thumbnails pass waits for writes to stop
	file, err := mg.dao.GetFileByPath(req.Path)
	if err != nil {
		return nil, err
	}
	if shownElsewhere(req.Hash, file.Hash) {
		return nil, &ContentChangedError{}
	}
	content, err := mg.editContent(ses, file)
	if err != nil {
		return nil, err
	}
	kind := editKindOf(content)
	if kind == editNone {
		return nil, ErrNotEditable
	}
	turns := ((int(req.QuarterTurns) % 4) + 4) % 4
	r, err := renderEdit(content, kind, turns, req.Crop)
	content = nil
	if err != nil {
		return nil, devtext.New(i18n.DevFilesEditFailed(devtext.Text(lang, err)))
	}
	sum := sha256.Sum256(r.content)
	edited := &pb.File{Hash: hex.EncodeToString(sum[:]), Mime: r.mime}
	dao.SetFileSize(edited, int64(len(r.content)))
	result, err := mg.saveEdit(ses, file, editOut{
		edited:  edited,
		ext:     r.ext,
		store:   func() error { return mg.storeEditContent(ses, edited.Hash, r.content) },
		renames: func(p string) func(int) string { return renamedNames(p, r.mime, r.ext) },
		move:    r.moveFace,
	}, req.AsCopy, req.EveryCopy, lang)
	if err != nil {
		return nil, err
	}
	log.Info("edited a photo: turns", turns, "copy", req.AsCopy, "every copy", req.EveryCopy)
	return result, nil
}

// editOut is what an edit saves: the edited content (its File: hash, mime,
// size), the extension a copy's name takes, how the content gets to the
// disk, the names a path holding it tries when it is renamed (nil: it
// keeps its own), and how a face's box moves with it (nil: its faces are
// found again by the analysis).
type editOut struct {
	edited  *pb.File
	ext     string
	store   func() error
	renames func(p string) func(n int) string
	move    func(dao.FaceBox) (dao.FaceBox, bool)
}

// saveEdit stores an edit of file and gives it its paths - file's own and,
// every, every other path holding the same content, or, asCopy, a copy
// next to it - and answers the edited file, with its original_hash. An
// edit that is the content itself changes nothing.
func (mg *Manager) saveEdit(ses *session.Session, file *pb.File, out editOut, asCopy, every bool, lang string) (*pb.File, error) {
	edited := out.edited
	if edited.Hash == file.Hash {
		return file, nil // nothing changed
	}

	// What it is an edit of: the original of an edit edited again.
	edit := &dao.EditedContent{Hash: edited.Hash, Original: file.Hash, OriginalMime: file.Mime, OriginalSize: dao.FileSize(file)}
	if prev, err := mg.dao.EditOf(file.Hash); err != nil {
		return nil, err
	} else if prev != nil {
		edit.Original, edit.OriginalMime, edit.OriginalSize, edit.OriginalExt = prev.Original, prev.OriginalMime, prev.OriginalSize, prev.OriginalExt
	}
	if edit.OriginalExt == "" && !asCopy && out.renames(file.Path) != nil {
		_, edit.OriginalExt = splitExt(file.Path)
	}
	if edited.Hash == edit.Original {
		edit = nil // turned back as it was: the original itself, no edit
	}

	if err := out.store(); err != nil {
		return nil, err
	}
	known := mg.contentKnown(edited.Hash)
	defer mg.places.changed()
	defer mg.memories.changed()

	var result *pb.File
	err := mg.withBlob(edited.Hash, func() (err error) {
		if asCopy {
			cp := &pb.File{Hash: edited.Hash, Mime: edited.Mime, Created: file.Created, Modified: timestamppb.Now()}
			dao.SetFileSize(cp, dao.FileSize(edited))
			result, err = mg.dao.AddEditCopy(cp, copyNames(file.Path, out.ext, lang), edit)
			return err
		}
		paths := []string{file.Path}
		if every {
			all, err := mg.dao.PathsOfHash(file.Hash)
			if err != nil {
				return err
			}
			paths = append(paths, slices.DeleteFunc(all, func(p string) bool { return p == file.Path })...)
		}
		targets := make([]dao.EditTarget, 0, len(paths))
		for _, p := range paths {
			targets = append(targets, dao.EditTarget{Path: p, Names: out.renames(p)})
		}
		done, err := mg.dao.ApplyEdit(targets, file.Hash, edited, edit)
		if err == nil {
			result = done[0]
		}
		return err
	})
	switch {
	case errors.Is(err, dao.ErrNoEdits):
		err = errEditsUnavailable
	case errors.Is(err, dao.ErrOtherContent):
		err = &ContentChangedError{}
	}
	if err != nil {
		if rmErr := mg.removeBlobIfUnused(edited.Hash); rmErr != nil {
			log.Error("could not remove an edit that wasn't saved", edited.Hash, ":", rmErr)
		}
		return nil, err
	}
	if edit != nil {
		result.OriginalHash = edit.Original
	}

	// Who is in it: the original's faces, moved with the edit - before its
	// analysis, which then finds them there and leaves them.
	if out.move != nil {
		if _, err := mg.dao.CopyFaces(file.Hash, edited.Hash, out.move, func() string { return uuid.New().String() }); err != nil {
			log.Error("could not give an edit its photo's faces:", err)
		}
	}
	if !asCopy {
		// The photo, when nothing holds it any more - an edit edited again
		// goes; an original stays, kept by its edit.
		if err := mg.removeBlobIfUnused(file.Hash); err != nil {
			log.Error("could not remove the content an edit replaced", file.Hash, ":", err)
		}
	}
	mg.reconcileAnalysis(ses, []string{file.Hash, edited.Hash})
	if !(known && mg.hasThumbnail(edited.Hash)) {
		mg.enqueueMedia(ses, result)
	}
	return result, nil
}

// RevertImage puts the original back at path (RevertImage in
// messages.proto) and answers the file there.
func (mg *Manager) RevertImage(ses *session.Session, p, shown string, everyCopy bool) (*pb.File, error) {
	file, err := mg.dao.GetFileByPath(p)
	if err != nil {
		return nil, err
	}
	if shownElsewhere(shown, file.Hash) {
		return nil, &ContentChangedError{}
	}
	edit, err := mg.dao.EditOf(file.Hash)
	if err != nil {
		return nil, err
	}
	if edit == nil {
		if strings.HasPrefix(file.Mime, "video/") {
			return nil, errVideoNotEdited
		}
		return nil, ErrNotEdited
	}
	if !mg.hasBlob(edit.Original) {
		return nil, devtext.New(i18n.DevFilesContentMissing(p))
	}
	paths := []string{p}
	if everyCopy {
		all, err := mg.dao.PathsOfHash(file.Hash)
		if err != nil {
			return nil, err
		}
		paths = append(paths, slices.DeleteFunc(all, func(q string) bool { return q == p })...)
	}
	targets := make([]dao.EditTarget, 0, len(paths))
	for _, q := range paths {
		t := dao.EditTarget{Path: q}
		if stem, ext := splitExt(q); edit.OriginalExt != "" && !strings.EqualFold(ext, edit.OriginalExt) {
			back := edit.OriginalExt
			t.Names = func(n int) string {
				if n == 0 {
					return stem + back
				}
				return fmt.Sprintf("%s (%d)%s", stem, n+1, back)
			}
		}
		targets = append(targets, t)
	}
	original := &pb.File{Hash: edit.Original, Mime: edit.OriginalMime}
	dao.SetFileSize(original, edit.OriginalSize)
	defer mg.places.changed()
	defer mg.memories.changed()
	var done []*pb.File
	if err := mg.withBlob(edit.Original, func() (err error) {
		done, err = mg.dao.ApplyEdit(targets, file.Hash, original, nil)
		return err
	}); errors.Is(err, dao.ErrOtherContent) {
		return nil, &ContentChangedError{}
	} else if err != nil {
		return nil, err
	}
	if err := mg.removeBlobIfUnused(file.Hash); err != nil {
		log.Error("could not remove a reverted edit", file.Hash, ":", err)
	}
	mg.reconcileAnalysis(ses, []string{file.Hash, edit.Original})
	log.Info("reverted an edit: every copy", everyCopy)
	return done[0], nil
}

// EditState is GetEditState's answer for the file at path.
func (mg *Manager) EditState(p string) (*pb.EditState, error) {
	file, err := mg.dao.GetFileByPath(p)
	if err != nil {
		return nil, err
	}
	counts, err := mg.dao.CountCopies([]string{file.Hash})
	if err != nil {
		return nil, err
	}
	edit, err := mg.dao.EditOf(file.Hash)
	if err != nil {
		return nil, err
	}
	return &pb.EditState{Copies: max(counts[file.Hash], 1), Edited: edit != nil}, nil
}

// editedAlready is the file a phone's sync means when it sends content into
// the folder of p: the edit of that content kept there, or - content being
// the edit itself - the file it renamed p into. nil when there is none:
// the content is stored as sent.
func (mg *Manager) editedAlready(p, content string) *pb.File {
	if content == "" {
		return nil
	}
	dir := path.Dir(p)
	if !strings.HasSuffix(dir, "/") {
		dir += "/"
	}
	stem, ext := splitExt(p)
	f, err := mg.dao.EditedFolderFile(dir, content, stem, ext)
	if err != nil {
		log.Error("could not look for an edit of what a sync sent:", err)
		return nil
	}
	if f != nil {
		log.Debug("a sync sent content whose edit is kept in its folder:", p, "->", f.Path)
	}
	return f
}

// setOriginals sets File.original_hash on the edits among files (a
// listing, a page of Images). A failure only leaves them unset.
func (mg *Manager) setOriginals(files []*pb.File) {
	hashes := make([]string, 0, len(files))
	for _, f := range files {
		if f.Hash != "" {
			hashes = append(hashes, f.Hash)
		}
	}
	if len(hashes) == 0 {
		return
	}
	originals, err := mg.dao.OriginalsOf(hashes)
	if err != nil {
		log.Error("could not say which files are edits:", err)
		return
	}
	for _, f := range files {
		if o, ok := originals[f.Hash]; ok {
			f.OriginalHash = o
		}
	}
}

// releaseEdit: hash is gone from the disk; when it was an edit, it is
// forgotten, and its original goes too once nothing else holds that.
func (mg *Manager) releaseEdit(hash string) {
	original, err := mg.dao.DelEdit(hash)
	if err != nil {
		log.Error("could not forget a deleted edit", hash, ":", err)
		return
	}
	if original == "" {
		return
	}
	removed, err := mg.removeUnusedBlob(original)
	if err != nil {
		log.Error("could not remove the original of a deleted edit", original, ":", err)
		return
	}
	if removed {
		// Its tags too: no file's delete took them, as it had no path.
		if err := mg.dao.DelTagsByHash(original); err != nil {
			log.Error("could not remove the tags of an edit's original", original, ":", err)
		}
		mg.releaseEdit(original)
	}
}

// heldOriginal is the original of an edit kept whole on the disk as
// content hash, with its type and size: nil when there is none.
func (mg *Manager) heldOriginal(hash string) *pb.File {
	held, err := mg.dao.HeldOriginals([]string{hash})
	if err != nil {
		log.Error("could not look for an edit's original:", err)
		return nil
	}
	o, ok := held[hash]
	if !ok || !mg.hasBlob(hash) {
		return nil
	}
	f := &pb.File{Hash: hash, Mime: o.Mime}
	dao.SetFileSize(f, o.Size)
	return f
}
