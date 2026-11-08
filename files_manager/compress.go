// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

// Compress (GetCompressOptions, CompressFiles and GetCompressJob in
// messages.proto; release 177): the owner picks photos and videos - in
// Settings › Largest files or in Images - and the device makes them
// smaller to free space: a 4K video re-encoded at a lower resolution as
// HEVC, a large photo shrunk to about 12 megapixels, a JPEG saved again at
// a lower quality, a PNG saved as a JPEG. Each is saved as an edit is
// (saveEdit, edit_image.go), in place of every copy - and then, unlike an
// edit, the original's bytes are deleted. Its hash is kept
// (edited_content.original_removed): the phones and computers that still
// hold the original are answered with the compressed file and never send
// it again, and Free up space never takes their copy (HasFile, HasHashes,
// LinkFile, the uploads). There is no revert.
//
// This file is the first step, GetCompressOptions: what can be done for
// each file and what it would weigh, worked out from its header - a
// video's size, codec, length and bit rates (ffprobe), a photo's pixels
// and format - never by compressing it, so a few hundred files are
// answered in seconds. The numbers are rough on purpose: the job keeps the
// original whenever a result isn't smaller (compress_job.go).

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"github.com/jdeng/goheif/heif"
)

// The presets (CompressChoice.preset), strongest first.
const (
	cPresetVideo720   = "video_720p"
	cPresetVideo1080  = "video_1080p"
	cPresetVideoHEVC  = "video_hevc"
	cPresetPhoto12MP  = "photo_12mp"
	cPresetJPEGResave = "jpeg_resave"
	cPresetPNGToJPEG  = "png_to_jpeg"
)

// The reason codes of an item nothing is offered for
// (CompressOption.reason_code).
const (
	cReasonChanged     = "changed"
	cReasonNotMedia    = "not_media"
	cReasonNothing     = "nothing_to_gain"
	cReasonUploadOnly  = "upload_only"
	cReasonKeptVersion = "kept_version"
	cReasonMissing     = "missing"
)

const (
	// cMaxCompressItems is the most items one GetCompressOptions (or
	// CompressFiles) takes: the apps' selection is cut there.
	cMaxCompressItems = 500
	// cCompressWorth: a preset is offered only when its estimate is at most
	// this much of the file - 15% smaller, or it isn't worth a generation
	// of loss.
	cCompressWorth = 0.85
	// cCompressPixels is photo_12mp's result, and cCompressOver the photos
	// it is offered for: over 13 megapixels.
	cCompressPixels = 12_000_000
	cCompressOver   = 13_000_000
	// What a JPEG weighs a pixel, roughly: one written at photo_12mp's
	// quality (88), jpeg_resave's (82) and png_to_jpeg's (90), and the
	// most a JPEG may weigh before saving it again is worth it.
	cBytesPerPixel12MP   = 0.32
	cBytesPerPixelResave = 0.24
	cBytesPerPixelPNG    = 0.30
	cResaveOver          = 0.45
	// The video bit rates of a 30 fps HEVC at 1080p and 720p (the shorter
	// side), video_hevc's share of the source's own, and the sound's when
	// it is re-encoded (AAC).
	cVideo1080Rate  = 6_000_000
	cVideo720Rate   = 3_000_000
	cVideoHEVCShare = 0.55
	cAudioRate      = 160_000
	// The shorter side a video must be over for each size to be offered.
	cVideo1080Over = 1200
	cVideo720Over  = 800
	// cCompressProbers is how many items' headers the options read at once.
	cCompressProbers = 4
	// cCompressHead is how much of a photo is read for its header: its
	// first segment (segcrypt's, 1 MB), which decrypts in one piece anyway.
	cCompressHead = 1 << 20
)

// errCompressTooMany refuses more than cMaxCompressItems at once
// (error_code "too_many").
var errCompressTooMany = devtext.New(i18n.DevFilesCompressTooMany())

// IsCompressTooMany is whether err is that refusal.
func IsCompressTooMany(err error) bool { return errors.Is(err, errCompressTooMany) }

// compressRefusal is why nothing can be done for an item: its reason_code
// and its words.
type compressRefusal struct {
	code string
	msg  i18n.Msg
}

func refusedFor(code string) *compressRefusal {
	r := &compressRefusal{code: code}
	switch code {
	case cReasonChanged:
		r.msg = i18n.DevFilesContentChanged()
	case cReasonNotMedia:
		r.msg = i18n.DevFilesCompressNotMedia()
	case cReasonUploadOnly:
		r.msg = i18n.DevFilesCompressUploadOnly()
	case cReasonKeptVersion:
		r.msg = i18n.DevFilesCompressKeptVersion()
	case cReasonMissing:
		r.msg = i18n.DevFilesCompressMissing()
	default:
		r.code, r.msg = cReasonNothing, i18n.DevFilesCompressNothingToGain()
	}
	return r
}

// compressPhotoExts and the cut's video extensions are what a file whose
// type was recorded as nothing in particular (application/octet-stream
// from an old upload) is taken for.
var compressPhotoExts = map[string]bool{".jpg": true, ".jpeg": true, ".jpe": true, ".png": true, ".heic": true, ".heif": true, ".hif": true, ".webp": true, ".avif": true}

// compressKind is "photo", "video", or "" for a file that is neither: by
// its type, or its name's extension when the type says nothing.
func compressKind(f *pb.File) string {
	mime := baseMime(f.Mime)
	switch {
	case isHeicFile(f.Path, f.Mime), strings.HasPrefix(mime, "image/"):
		return "photo"
	case strings.HasPrefix(mime, "video/"):
		return "video"
	case mime != "" && mime != "application/octet-stream":
		return ""
	}
	_, ext := splitExt(f.Path)
	ext = strings.ToLower(ext)
	switch {
	case compressPhotoExts[ext]:
		return "photo"
	case cutExtFormats[ext] != "":
		return "video"
	}
	return ""
}

// compressFormatOf is the muxer a compressed video is written with: its
// own container, of the MP4 family only - HEVC goes into an MP4 or a
// QuickTime movie (an M4V is an MP4), not a 3GP (ffmpeg's muxer refuses
// it), and the other containers stay as they are. "" for those.
func compressFormatOf(f *pb.File) string {
	switch format := cutFormatOf(f); format {
	case "mp4", "mov":
		return format
	}
	return ""
}

// compressFacts is what the options look up for every item at once: the
// paths holding each content, which an older version keeps, the
// upload-only folders, and whether the database can record a removed
// original at all.
type compressFacts struct {
	paths     map[string][]string
	versions  map[string]bool
	folders   []string
	supported bool
}

// compressFactsOf reads them for hashes.
func (mg *Manager) compressFactsOf(hashes []string) (*compressFacts, error) {
	supported, err := mg.dao.CompressSupported()
	if err != nil {
		return nil, err
	}
	paths, err := mg.dao.PathsOfHashes(hashes)
	if err != nil {
		return nil, err
	}
	versions, err := mg.dao.HashesInVersions(hashes)
	if err != nil {
		return nil, err
	}
	folders, err := mg.dao.GetUploadOnlyFolders()
	if err != nil {
		return nil, err
	}
	return &compressFacts{paths: paths, versions: versions, folders: folders, supported: supported}, nil
}

// refusal is why file (the row at the item's path, nil when there is none)
// can't be compressed at all, in the order the apps are told: the content
// shown isn't the path's any more; not a photo or video; a copy is in an
// upload-only folder, where nothing is replaced; an older version keeps
// the content, whose bytes would then stay; its content isn't on the disk;
// a database that can't record a removed original. nil: its presets
// decide.
func (mg *Manager) refusal(file *pb.File, shown string, facts *compressFacts) *compressRefusal {
	switch {
	case file == nil || shownElsewhere(shown, file.Hash):
		return refusedFor(cReasonChanged)
	case compressKind(file) == "":
		return refusedFor(cReasonNotMedia)
	}
	for _, p := range facts.paths[file.Hash] {
		if underFolders(p, false, facts.folders) {
			return refusedFor(cReasonUploadOnly)
		}
	}
	switch {
	case facts.versions[file.Hash]:
		return refusedFor(cReasonKeptVersion)
	case !mg.hasBlob(file.Hash):
		return refusedFor(cReasonMissing)
	case !facts.supported:
		return refusedFor(cReasonNothing)
	}
	return nil
}

// CompressOptions answers GetCompressOptions: for each item, in the order
// asked, its presets with their estimates, or why there are none.
func (mg *Manager) CompressOptions(ses *session.Session, items []*pb.CompressItem, lang string) (*pb.CompressOptions, error) {
	if len(items) > cMaxCompressItems {
		return nil, errCompressTooMany
	}
	paths := make([]string, 0, len(items))
	for _, it := range items {
		paths = append(paths, it.Path)
	}
	rows, err := mg.dao.GetFilesByPaths(paths)
	if err != nil {
		return nil, err
	}
	var hashes []string
	seen := map[string]bool{}
	for _, f := range rows {
		if !seen[f.Hash] {
			seen[f.Hash] = true
			hashes = append(hashes, f.Hash)
		}
	}
	copies, err := mg.dao.CountCopies(hashes)
	if err != nil {
		return nil, err
	}
	facts, err := mg.compressFactsOf(hashes)
	if err != nil {
		return nil, err
	}

	out := &pb.CompressOptions{Items: make([]*pb.CompressOption, len(items))}
	var todo []int
	for i, it := range items {
		o := &pb.CompressOption{Path: it.Path, Hash: it.Hash}
		out.Items[i] = o
		f := rows[it.Path]
		if f != nil {
			o.Hash, o.Size, o.Copies = f.Hash, dao.FileSize(f), max(copies[f.Hash], 1)
		}
		if r := mg.refusal(f, it.Hash, facts); r != nil {
			o.ReasonCode, o.Reason = r.code, r.msg.Render(lang)
			continue
		}
		todo = append(todo, i)
	}

	// The headers, a few at once: a video's probe waits on ffprobe, a
	// photo's on its first segment's read and decryption.
	var wg sync.WaitGroup
	next := make(chan int)
	for range min(cCompressProbers, len(todo)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				o := out.Items[i]
				o.Choices, _ = mg.compressChoices(ses, rows[items[i].Path])
				if len(o.Choices) == 0 {
					r := refusedFor(cReasonNothing)
					o.ReasonCode, o.Reason = r.code, r.msg.Render(lang)
				}
			}
		}()
	}
	for _, i := range todo {
		next <- i
	}
	close(next)
	wg.Wait()
	return out, nil
}

// compressChoices is what file can be compressed into, strongest first -
// each preset its header allows whose estimate is worth it - and what was
// read of it. None when the header can't be read (logged by its hash).
func (mg *Manager) compressChoices(ses *session.Session, file *pb.File) ([]*pb.CompressChoice, *probedContent) {
	p, err := mg.compressProbe(ses, file)
	if err != nil {
		log.Info("could not read what", file.Hash, "is, for its compress options:", err)
		return nil, nil
	}
	size := dao.FileSize(file)
	switch {
	case p.video != nil:
		return videoChoices(p.video, size, hasX265() && mg.profile.VideoCompressAllowed()), p
	case p.photo != nil:
		return photoChoices(p.photo, size), p
	}
	return nil, p
}

// worthIt is whether an estimate is at least cCompressWorth smaller than
// a file of size bytes.
func worthIt(estimate, size int64) bool {
	return estimate > 0 && float64(estimate) <= cCompressWorth*float64(size)
}

// probedContent is what the options read of a content, kept for the job
// (compressProbes): a photo's header or a video's probe.
type probedContent struct {
	photo *photoHeader
	video *videoProbe
}

// cMaxCompressProbes is the most contents compressProbes keeps; the
// oldest goes first.
const cMaxCompressProbes = 1000

// compressProbes keeps the contents the options read, by hash, for the
// job that follows: a few hundred, and never more than cMaxCompressProbes.
type compressProbes struct {
	mu     sync.Mutex
	byHash map[string]*probedContent
	order  []string
}

func (c *compressProbes) get(hash string) *probedContent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.byHash[hash]
}

func (c *compressProbes) put(hash string, p *probedContent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byHash == nil {
		c.byHash = map[string]*probedContent{}
	}
	if _, ok := c.byHash[hash]; !ok {
		c.order = append(c.order, hash)
	}
	c.byHash[hash] = p
	for len(c.order) > cMaxCompressProbes {
		delete(c.byHash, c.order[0])
		c.order = c.order[1:]
	}
}

// compressProbe is what the content of file is, for compressing it: read
// once, then from compressProbes.
func (mg *Manager) compressProbe(ses *session.Session, file *pb.File) (*probedContent, error) {
	if p := mg.compress.probes.get(file.Hash); p != nil {
		return p, nil
	}
	p := &probedContent{}
	var err error
	switch compressKind(file) {
	case "video":
		if compressFormatOf(file) == "" {
			break // a container HEVC doesn't go into: nothing to offer
		}
		p.video, err = mg.probeVideo(ses, file)
	case "photo":
		p.photo, err = mg.photoHeaderOf(ses, file)
	}
	if err != nil {
		return nil, err
	}
	mg.compress.probes.put(file.Hash, p)
	return p, nil
}

// --- Photos ---

// photoHeader is what a photo's header says, for compressing it: what
// kind of photo it is to the edits (editKindOf), its size in pixels, and
// whether it has no transparent pixel (a JPEG has none; a PNG or a WebP
// may).
type photoHeader struct {
	kind   editKind
	w, h   int
	opaque bool
}

func (p *photoHeader) pixels() int64 { return int64(p.w) * int64(p.h) }

// photoChoices is a photo's presets: photo_12mp over 13 megapixels, any
// format the device decodes; jpeg_resave for a JPEG heavier than
// cResaveOver bytes a pixel; png_to_jpeg for a PNG. A JPEG has no
// transparency, so a photo with transparent pixels gets none.
func photoChoices(p *photoHeader, size int64) []*pb.CompressChoice {
	if p.kind == editNone || !p.opaque || p.pixels() <= 0 {
		return nil
	}
	var out []*pb.CompressChoice
	offer := func(preset string, estimate float64) {
		if est := int64(estimate); worthIt(est, size) {
			out = append(out, &pb.CompressChoice{Preset: preset, EstimatedSize: est})
		}
	}
	px := float64(p.pixels())
	if px > cCompressOver {
		offer(cPresetPhoto12MP, cCompressPixels*cBytesPerPixel12MP)
	}
	switch {
	case p.kind == editJPEG && float64(size)/px > cResaveOver:
		offer(cPresetJPEGResave, px*cBytesPerPixelResave)
	case p.kind == editPNG:
		offer(cPresetPNGToJPEG, px*cBytesPerPixelPNG)
	}
	return out
}

// photoHeaderOf reads the header of file's stored photo: its first
// segment, or - a header longer than that, rare - the whole photo, within
// what the device holds of one at a time. A PNG whose header allows
// transparency (an alpha channel, a tRNS chunk) is decoded to see whether
// any pixel is - screenshots and exports often have the channel and no
// use for it - within the decode budget: one too large to decode is taken
// as transparent.
func (mg *Manager) photoHeaderOf(ses *session.Session, file *pb.File) (*photoHeader, error) {
	blob, err := blobstore.Open(blobPath(file.Hash), ses)
	if err != nil {
		return nil, err
	}
	head := make([]byte, min(blob.Size(), cCompressHead))
	_, err = blob.ReadAt(head, 0)
	whole := int64(len(head)) == blob.Size()
	blob.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	p, complete := readPhotoHeader(head)
	if !complete && !whole {
		content, err := mg.editContent(ses, file)
		if err != nil {
			return nil, err
		}
		head, whole = content, true
		p, _ = readPhotoHeader(head)
	}
	if p.kind == editPNG && !p.opaque && p.pixels() > 0 {
		if !whole {
			if head, err = mg.editContent(ses, file); err != nil {
				return nil, err
			}
		}
		p.opaque = decodesOpaque(head)
	}
	return p, nil
}

// readPhotoHeader is what b (a photo's first bytes, or all of them) says;
// complete is false when its header runs past b. A photo of a kind the
// device doesn't compress (a GIF, a TIFF, a camera raw, an animation)
// answers kind editNone.
func readPhotoHeader(b []byte) (p *photoHeader, complete bool) {
	p = &photoHeader{kind: editKindOf(b)}
	switch p.kind {
	case editJPEG:
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(b))
		if err != nil {
			return p, !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF)
		}
		p.w, p.h, p.opaque = cfg.Width, cfg.Height, true
	case editPNG:
		w, h, colour, transparent, ok := readPNGHeader(b)
		if !ok {
			return p, false
		}
		p.w, p.h = w, h
		p.opaque = (colour == 0 || colour == 2 || colour == 3) && !transparent
	case editPhoto:
		if isHeif(b) {
			it, err := heif.Open(bytes.NewReader(b)).PrimaryItem()
			if err != nil {
				return p, false
			}
			w, h, ok := it.SpatialExtents()
			if !ok {
				return p, false
			}
			p.w, p.h, p.opaque = w, h, true
			return p, true
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
		if err != nil {
			return p, false
		}
		p.w, p.h, p.opaque = cfg.Width, cfg.Height, !webpAlpha(b)
	}
	return p, true
}

// readPNGHeader reads a PNG's IHDR - its size and colour type - and whether a
// tRNS chunk (a colour key or a palette's alphas) comes before its pixels.
// ok is false when its chunks run past b before the pixels.
func readPNGHeader(b []byte) (w, h int, colour byte, transparent, ok bool) {
	if len(b) < 33 || string(b[12:16]) != "IHDR" {
		return 0, 0, 0, false, false
	}
	w, h = int(binary.BigEndian.Uint32(b[16:])), int(binary.BigEndian.Uint32(b[20:]))
	colour = b[25]
	for i := 8; i+8 <= len(b); {
		n := int64(binary.BigEndian.Uint32(b[i:]))
		switch string(b[i+4 : i+8]) {
		case "tRNS":
			transparent = true
		case "IDAT":
			return w, h, colour, transparent, true
		}
		next := int64(i) + 12 + n
		if next > int64(len(b)) {
			break
		}
		i = int(next)
	}
	return w, h, colour, transparent, false
}

// decodesOpaque is whether the photo b decodes, within the decode budget,
// with no pixel less than opaque.
func decodesOpaque(b []byte) bool {
	release, err := holdDecode(b)
	if err != nil {
		return false
	}
	defer release()
	img, err := decodeFull(b)
	if err != nil {
		return false
	}
	o, ok := img.(interface{ Opaque() bool })
	return ok && o.Opaque()
}

// pngMetadata is what a PNG saved as a JPEG keeps of it: its EXIF (an eXIf
// chunk, the TIFF structure) and its ICC profile (an iCCP chunk, inflated)
// as a JPEG's APP2 segments - each nil when it has none, or it can't be
// read.
func pngMetadata(b []byte) (exif []byte, icc [][]byte) {
	for i := 8; i+12 <= len(b); {
		n := int64(binary.BigEndian.Uint32(b[i:]))
		if int64(i)+12+n > int64(len(b)) {
			break
		}
		data := b[i+8 : i+8+int(n)]
		switch string(b[i+4 : i+8]) {
		case "eXIf":
			if bytes.HasPrefix(data, []byte("II*\x00")) || bytes.HasPrefix(data, []byte("MM\x00*")) {
				exif = data
			}
		case "iCCP":
			// A name, its NUL, the method (0, deflate), the profile.
			if z := bytes.IndexByte(data, 0); z > 0 && z+2 <= len(data) && data[z+1] == 0 {
				if r, err := zlib.NewReader(bytes.NewReader(data[z+2:])); err == nil {
					profile, err := io.ReadAll(io.LimitReader(r, 4<<20))
					r.Close()
					if err == nil && len(profile) >= 40 && string(profile[36:40]) == "acsp" {
						icc = iccSegments(profile)
					}
				}
			}
		case "IEND":
			return exif, icc
		}
		i += 12 + int(n)
	}
	return exif, icc
}

// webpAlpha is whether a WebP says it has an alpha channel: a lossless
// one's flag, or an extended one's.
func webpAlpha(b []byte) bool {
	if len(b) < 30 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return false
	}
	switch string(b[12:16]) {
	case "VP8L":
		// The 14-bit width and height, then the alpha_is_used bit.
		return len(b) >= 25 && b[24]&0x10 != 0
	case "VP8X":
		return b[20]&0x10 != 0
	}
	return false
}

// --- Videos ---

// videoProbe is what ffprobe says of a video, for compressing it.
type videoProbe struct {
	duration float64
	// The picture: its stream, size as stored (the rotation doesn't change
	// its shorter side), codec, pixel format, frame rate and bit rate (the
	// container's less the sound's when the stream has none), and colours.
	index         int
	width, height int
	codec, pixFmt string
	fps           float64
	bitRate       int64
	color         videoColor
	// The sound: each stream in a codec ffmpeg knows, as a cut keeps them.
	audio []audioStream
}

type audioStream struct {
	index   int
	codec   string
	bitRate int64
}

// copied is whether the stream goes into the compressed video as it is:
// AAC, which an MP4 and a phone take; any other sound is re-encoded.
func (a audioStream) copied() bool { return a.codec == "aac" }

// rate is what the stream adds to the compressed video, bits a second.
func (a audioStream) rate() int64 {
	if a.copied() && a.bitRate > 0 {
		return a.bitRate
	}
	return cAudioRate
}

// tenBit is whether the picture has more than 8 bits a sample (an iPhone's
// HDR): the compressed video keeps them.
func (v *videoProbe) tenBit() bool {
	return strings.Contains(v.pixFmt, "10") || strings.Contains(v.pixFmt, "12")
}

// shorterSide is the picture's shorter side, which a preset sizes: a
// portrait video's is its width as shown.
func (v *videoProbe) shorterSide() int { return min(v.width, v.height) }

// target is the picture's bit rate a preset aims at: 1080p's and 720p's
// for 30 fps, more for a higher frame rate (up to twice, at 60), and never
// over the source's own; video_hevc's a share of the source's.
func (v *videoProbe) target(preset string) int64 {
	fps := v.fps
	if fps <= 0 {
		fps = 30
	}
	scale := math.Min(2, math.Max(1, fps/30))
	var t float64
	switch preset {
	case cPresetVideo1080:
		t = cVideo1080Rate * scale
	case cPresetVideo720:
		t = cVideo720Rate * scale
	case cPresetVideoHEVC:
		t = cVideoHEVCShare * float64(v.bitRate)
	}
	if v.bitRate > 0 {
		t = math.Min(t, float64(v.bitRate))
	}
	return int64(t)
}

// estimate is the size of the video compressed as preset: its length at
// the picture's target and the sound's rates, and 1% for the container.
func (v *videoProbe) estimate(preset string) int64 {
	rate := v.target(preset)
	for _, a := range v.audio {
		rate += a.rate()
	}
	return int64(v.duration * float64(rate) / 8 * 1.01)
}

// cEfficientCodecs are pictures already in a codec HEVC isn't worth
// re-encoding into at their own size.
var cEfficientCodecs = map[string]bool{"hevc": true, "av1": true, "vp9": true}

// videoChoices is a video's presets: 720p for one whose shorter side is
// over 800, 1080p over 1200, HEVC at its own size for one in an older
// codec - none where the device's ffmpeg has no HEVC encoder.
func videoChoices(v *videoProbe, size int64, x265 bool) []*pb.CompressChoice {
	if !x265 || v.duration <= 0 {
		return nil
	}
	var out []*pb.CompressChoice
	offer := func(preset string) {
		if est := v.estimate(preset); worthIt(est, size) {
			out = append(out, &pb.CompressChoice{Preset: preset, EstimatedSize: est})
		}
	}
	if v.shorterSide() > cVideo720Over {
		offer(cPresetVideo720)
	}
	if v.shorterSide() > cVideo1080Over {
		offer(cPresetVideo1080)
	}
	if !cEfficientCodecs[v.codec] && v.bitRate > 0 {
		offer(cPresetVideoHEVC)
	}
	return out
}

// probeVideo is ffprobe's videoProbe of file's stored video, read over the
// loopback stream.
func (mg *Manager) probeVideo(ses *session.Session, file *pb.File) (*videoProbe, error) {
	src, done, err := mg.videoSource(ses, file)
	if err != nil {
		return nil, err
	}
	defer done()
	out, err := compressFFprobe(src)
	if err != nil {
		return nil, err
	}
	return parseVideoProbe(out, dao.FileSize(file))
}

// compressFFprobe runs ffprobe on src; a variable so tests can do without it.
var compressFFprobe = func(src string) ([]byte, error) {
	cmd, cancel := command(cProbeTimeout, "ffprobe", "-v", "error",
		"-show_entries", "format=duration,bit_rate:stream=index,codec_type,codec_name,width,height,pix_fmt,bit_rate,avg_frame_rate,r_frame_rate,color_primaries,color_transfer,color_space:stream_disposition=attached_pic",
		"-of", "json", src)
	defer cancel()
	return cmd.Output()
}

// parseVideoProbe reads compressFFprobe's answer for a video of size
// bytes.
func parseVideoProbe(out []byte, size int64) (*videoProbe, error) {
	var probe struct {
		Format struct {
			Duration string `json:"duration"`
			BitRate  string `json:"bit_rate"`
		} `json:"format"`
		Streams []struct {
			Index          int    `json:"index"`
			CodecType      string `json:"codec_type"`
			CodecName      string `json:"codec_name"`
			Width          int    `json:"width"`
			Height         int    `json:"height"`
			PixFmt         string `json:"pix_fmt"`
			BitRate        string `json:"bit_rate"`
			AvgFrameRate   string `json:"avg_frame_rate"`
			RFrameRate     string `json:"r_frame_rate"`
			ColorPrimaries string `json:"color_primaries"`
			ColorTransfer  string `json:"color_transfer"`
			ColorSpace     string `json:"color_space"`
			Disposition    struct {
				AttachedPic int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		return nil, err
	}
	duration, err := strconv.ParseFloat(strings.TrimSpace(probe.Format.Duration), 64)
	if err != nil || duration <= 0 {
		return nil, errors.New("no length") // i18n-ignore: logged, never shown
	}
	v := &videoProbe{duration: duration, index: -1}
	known := func(s string) string {
		if s == "unknown" {
			return ""
		}
		return s
	}
	for _, st := range probe.Streams {
		switch {
		case st.CodecType == "video" && v.index < 0 && st.Disposition.AttachedPic == 0 && st.Width > 0 && st.Height > 0:
			v.index, v.width, v.height, v.codec, v.pixFmt = st.Index, st.Width, st.Height, st.CodecName, st.PixFmt
			v.fps = frameRate(st.AvgFrameRate)
			if v.fps <= 0 {
				v.fps = frameRate(st.RFrameRate)
			}
			v.bitRate, _ = strconv.ParseInt(st.BitRate, 10, 64)
			v.color = videoColor{Transfer: known(st.ColorTransfer), Primaries: known(st.ColorPrimaries), Matrix: known(st.ColorSpace)}
		case st.CodecType == "audio" && st.CodecName != "" && st.CodecName != "none":
			a := audioStream{index: st.Index, codec: st.CodecName}
			a.bitRate, _ = strconv.ParseInt(st.BitRate, 10, 64)
			v.audio = append(v.audio, a)
		}
	}
	if v.index < 0 {
		return nil, errors.New("no picture") // i18n-ignore: logged, never shown
	}
	if v.bitRate <= 0 {
		// The container's rate, or its size over its length, less the sound.
		total, _ := strconv.ParseInt(probe.Format.BitRate, 10, 64)
		if total <= 0 {
			total = int64(float64(size) * 8 / duration)
		}
		for _, a := range v.audio {
			total -= a.bitRate
		}
		v.bitRate = max(total, 0)
	}
	return v, nil
}

// frameRate is ffprobe's "30000/1001" as frames a second; 0 when it says
// none.
func frameRate(s string) float64 {
	num, den, ok := strings.Cut(s, "/")
	n, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	if !ok {
		return n
	}
	d, err := strconv.ParseFloat(den, 64)
	if err != nil || d == 0 {
		return 0
	}
	return n / d
}

// hasX265 is whether the device's ffmpeg has libx265, the HEVC encoder
// videos are compressed with (in software: a Raspberry Pi 5 has no video
// encoder), asked once; a variable so tests can choose.
var hasX265 = func() bool {
	x265Once.Do(func() {
		cmd, cancel := command(cProbeTimeout, "ffmpeg", "-hide_banner", "-encoders")
		defer cancel()
		out, err := cmd.Output()
		x265Found = err == nil && bytes.Contains(out, []byte(" libx265 "))
		if !x265Found {
			log.Info("this device's ffmpeg has no HEVC encoder (libx265): videos can't be compressed")
		}
	})
	return x265Found
}

var (
	x265Once  sync.Once
	x265Found bool
)

// compressedName is the extension a compressed file's name takes, and how
// its paths are renamed: a photo becomes a JPEG ("IMG_1.PNG" ->
// "IMG_1.JPG"), a video keeps its name.
func compressedName(kind string) (ext string, renames func(p string) func(n int) string) {
	if kind == "video" {
		return "", func(string) func(int) string { return nil }
	}
	return ".jpg", func(p string) func(int) string { return renamedNames(p, "image/jpeg", ".jpg") }
}
