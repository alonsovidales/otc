// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

// Compress, step two (CompressFiles and GetCompressJob in messages.proto;
// compress.go is step one): the files the owner chose are compressed one
// at a time in the background - Status lists the job (kind "compress") -
// each through the memory admission every processing job waits on, and a
// video in a post transcode's slot as a cut is, as libx265 takes every
// core. Each task:
//
//   - checks again what the options checked: the content at its path is
//     still the one shown, no copy is in an upload-only folder, no older
//     version keeps it, its bytes are there, and the preset is still one
//     the device offers for it;
//   - compresses it - a video with ffmpeg, from the loopback stream into a
//     plain .compress-* in unenc-storage-path (checked for room first; the
//     orphan sweep takes one a power cut left); a photo in memory with the
//     edits' decoders - and drops a result that isn't smaller;
//   - saves it as an edit is (saveEdit), in place of every copy at once or
//     of none (dao.ReplaceEveryCopy), its edit's row saying the original is
//     gone: collections, Favourites and ratings move to it, a photo's faces
//     go with it, scaled, and the new content is analysed as any new file;
//   - deletes the original's bytes and everything worked out from them
//     (dropRemovedOriginal), once nothing but its edits' rows names it.
//
// One job per instance, in memory: tasks sent while it runs join it, and a
// finished job is kept until the next one starts. A restart forgets the
// tasks not started; one interrupted between its save and the deletion is
// finished by the next start (finishRemovedOriginals).

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"os"
	"path"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alonsovidales/otc/alerttext"
	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/exifinfo"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"github.com/google/uuid"
	"github.com/jdeng/goheif"
	"golang.org/x/image/draw"
)

// The states of a task (CompressResult.state).
const (
	cTaskWaiting = "waiting"
	cTaskRunning = "running"
	cTaskDone    = "done"
	cTaskSkipped = "skipped"
	cTaskFailed  = "failed"
)

const (
	// cCompressTempPrefix names a compressed video's plain copy while it
	// is written.
	cCompressTempPrefix = ".compress-"
	// cCompressKept: a result over this much of the original is dropped,
	// and the file left as it was.
	cCompressKept = 0.95
	// cCompressRoom is how much more than its estimate a result is given
	// room for on the disk.
	cCompressRoom = 1.2
	// cCompressSecsPerSec is how long a video may take to compress, a
	// second of it - a Raspberry Pi 5 does about 4 frames a second of a 4K
	// recording into 1080p - and never less than cTranscodeTimeout.
	cCompressSecsPerSec = 40
)

// The JPEG qualities: photo_12mp's (a smaller photo, as good as the
// camera's), jpeg_resave's (the same pixels, a lighter file) and
// png_to_jpeg's (a screenshot's text stays sharp).
const (
	cQuality12MP   = 88
	cQualityResave = 82
	cQualityPNG    = 90
)

// errCompressNoJob is GetCompressJob's answer when there is no such job
// (error_code "not_found").
var errCompressNoJob = devtext.New(i18n.DevFilesCompressNoJob())

// IsCompressNoJob is whether err is that answer.
func IsCompressNoJob(err error) bool { return errors.Is(err, errCompressNoJob) }

// compressState is the instance's job (the one running, or the last) and
// the contents the options read (compress.go).
type compressState struct {
	mu     sync.Mutex
	job    *compressJob
	probes compressProbes
}

// compressJob is one job: its tasks in the order they came, what it freed
// so far, and the language of the request that started it - its alert's
// sizes are written in it.
type compressJob struct {
	id       string
	lang     string
	tasks    []*compressTask
	saved    int64
	finished bool
	pass     *BackgroundPass
}

// compressTask is one file of a job. why says why it was skipped or
// failed, at when.
type compressTask struct {
	ses                *session.Session
	path, hash, preset string
	state              string
	percent            int32
	file               *pb.File
	why                i18n.Msg
	at                 time.Time
}

// snapshot is job as CompressJob, its words in lang. Called with the
// state's lock held.
func (job *compressJob) snapshot(lang string) *pb.CompressJob {
	out := &pb.CompressJob{Id: job.id, Total: int32(len(job.tasks)), Saved: job.saved, Finished: job.finished}
	for _, t := range job.tasks {
		r := &pb.CompressResult{Path: t.path, State: t.state, Percent: t.percent, File: t.file}
		if t.why.Key != "" {
			r.Message = t.why.Render(lang)
		}
		if t.state != cTaskWaiting && t.state != cTaskRunning {
			out.Done++
		}
		out.Results = append(out.Results, r)
	}
	return out
}

// CompressFiles queues tasks (CompressFiles in messages.proto): into the
// job running, or a new one when there is none, and answers that job. A
// path already waiting or running in it is not queued again.
func (mg *Manager) CompressFiles(ses *session.Session, tasks []*pb.CompressTask, lang string) (*pb.CompressJob, error) {
	if len(tasks) > cMaxCompressItems {
		return nil, errCompressTooMany
	}
	c := &mg.compress
	c.mu.Lock()
	defer c.mu.Unlock()
	job := c.job
	start := job == nil || job.finished
	if start {
		job = &compressJob{id: uuid.New().String(), lang: lang}
		c.job = job
	}
	queued := map[string]bool{}
	for _, t := range job.tasks {
		if t.state == cTaskWaiting || t.state == cTaskRunning {
			queued[t.path] = true
		}
	}
	for _, t := range tasks {
		if queued[t.Path] {
			continue
		}
		queued[t.Path] = true
		job.tasks = append(job.tasks, &compressTask{ses: ses, path: t.Path, hash: t.Hash, preset: t.Preset, state: cTaskWaiting})
	}
	log.Info("compress job", job.id, "has", len(job.tasks), "file(s)")
	if start {
		job.pass = mg.BeginBackground(BackgroundCompress, int64(len(job.tasks)))
		go mg.runCompressJob(job)
	} else {
		job.pass.SetTotal(int64(len(job.tasks)))
	}
	return job.snapshot(lang), nil
}

// CompressJob is GetCompressJob's answer: the job id names, "" for the
// one running or the last; errCompressNoJob when there is none.
func (mg *Manager) CompressJob(id, lang string) (*pb.CompressJob, error) {
	c := &mg.compress
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.job == nil || (id != "" && id != c.job.id) {
		return nil, errCompressNoJob
	}
	return c.job.snapshot(lang), nil
}

// runCompressJob is job's worker: its tasks one at a time, in the order
// they came, until none is waiting - then the job is over, and its alerts
// are written.
func (mg *Manager) runCompressJob(job *compressJob) {
	c := &mg.compress
	for {
		c.mu.Lock()
		var t *compressTask
		for _, w := range job.tasks {
			if w.state == cTaskWaiting {
				t = w
				break
			}
		}
		if t == nil {
			job.finished = true
			job.pass.End()
			c.mu.Unlock()
			break
		}
		t.state = cTaskRunning
		c.mu.Unlock()

		state, file, saved, why := mg.compressTaskSafely(job, t)

		c.mu.Lock()
		t.state, t.file, t.why, t.at, t.percent = state, file, why, time.Now(), 0
		if state == cTaskDone {
			t.percent = 100
			job.saved += saved
		}
		c.mu.Unlock()
		job.pass.Step(1)
	}
	mg.compressAlerts(job)
}

// compressTaskSafely is compressTask, a panic in it (a decoder's, on a
// damaged file) a failed task rather than the device down.
func (mg *Manager) compressTaskSafely(job *compressJob, t *compressTask) (state string, file *pb.File, saved int64, why i18n.Msg) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("recovered from a panic while compressing", t.hash+":", r, string(debug.Stack()))
			state, file, saved, why = cTaskFailed, nil, 0, i18n.DevFilesCompressFailed()
		}
	}()
	return mg.compressTask(job, t)
}

// compressTask compresses t's file: done, with the file now at its path
// and the bytes freed; skipped, when the result wasn't smaller; failed,
// with why. Nothing is changed unless it is done.
func (mg *Manager) compressTask(job *compressJob, t *compressTask) (state string, file *pb.File, saved int64, why i18n.Msg) {
	failed := func(m i18n.Msg) (string, *pb.File, int64, i18n.Msg) { return cTaskFailed, nil, 0, m }
	noteUploadActivity() // the thumbnails pass waits for writes to stop

	// What the options checked, again: it may have changed since.
	src, err := mg.dao.GetFileByPath(t.path)
	switch {
	case errors.Is(err, sql.ErrNoRows) || (err == nil && (t.hash == "" || src.Hash != t.hash)):
		// Never other content than the one the owner chose.
		return failed(i18n.DevFilesContentChanged())
	case err != nil:
		log.Error("could not read a file to compress:", err)
		return failed(i18n.DevFilesCompressFailed())
	}
	facts, err := mg.compressFactsOf([]string{src.Hash})
	if err != nil {
		log.Error("could not check a file to compress:", err)
		return failed(i18n.DevFilesCompressFailed())
	}
	if r := mg.refusal(src, t.hash, facts); r != nil {
		return failed(r.msg)
	}
	var choice *pb.CompressChoice
	choices, probe := mg.compressChoices(t.ses, src)
	for _, c := range choices {
		if c.Preset == t.preset {
			choice = c
		}
	}
	if choice == nil {
		return failed(i18n.DevFilesCompressUnknownPreset())
	}
	log.Info("compressing", src.Hash, "as", t.preset)
	log.Debug(src.Path, "is content", src.Hash)

	// Room beside the other processing (memadmit.go): a 4K video's decode
	// and encoder, or a large photo's decode, are a job's peak.
	defer mg.beginProcessing("", jobTranscode)()

	kind := compressKind(src)
	ext, renames := compressedName(kind)
	need := int64(float64(choice.EstimatedSize) * cCompressRoom)
	var out editOut
	if kind == "video" {
		plain, err := mg.compressVideo(t.ses, src, probe.video, t.preset, need, func(p int32) {
			mg.compress.mu.Lock()
			t.percent = p
			mg.compress.mu.Unlock()
		})
		if plain != "" {
			defer os.Remove(plain)
		}
		if err != nil {
			return failed(compressWhy(src, err))
		}
		edited, err := fileMeta(plain)
		if err != nil {
			return failed(compressWhy(src, err))
		}
		// The same type as the video: the container is.
		edited.Mime, edited.Created, edited.Modified = src.Mime, nil, nil
		_, ext = splitExt(src.Path)
		out = editOut{
			edited:          edited,
			ext:             ext,
			store:           func() error { return mg.storeEditFile(t.ses, edited.Hash, plain, dao.FileSize(edited)) },
			renames:         renames,
			removesOriginal: true,
		}
	} else {
		if err := checkFreeSpace(path.Dir(blobPath(src.Hash)), need); err != nil {
			return failed(compressWhy(src, err))
		}
		content, err := mg.editContent(t.ses, src)
		if err != nil {
			return failed(compressWhy(src, err))
		}
		r, err := renderCompress(content, t.preset)
		content = nil
		if err != nil {
			return failed(compressWhy(src, err))
		}
		sum := sha256.Sum256(r.content)
		edited := &pb.File{Hash: hex.EncodeToString(sum[:]), Mime: "image/jpeg"}
		dao.SetFileSize(edited, int64(len(r.content)))
		out = editOut{
			edited:          edited,
			ext:             ext,
			store:           func() error { return mg.storeEditContent(t.ses, edited.Hash, r.content) },
			renames:         renames,
			move:            r.move,
			removesOriginal: true,
		}
	}
	if size := dao.FileSize(out.edited); float64(size) >= cCompressKept*float64(dao.FileSize(src)) {
		log.Info("a compress of", src.Hash, "came out at", size, "bytes, no smaller than", dao.FileSize(src), "- left as it was")
		return cTaskSkipped, nil, 0, i18n.DevFilesCompressNotSmaller()
	}

	result, err := mg.saveEdit(t.ses, src, out, false, true, job.lang)
	if err != nil {
		return failed(compressWhy(src, err))
	}
	// The source's bytes went with saveEdit when it was an edit itself (no
	// file holds it now); the first original's go now.
	removed, freed := mg.dropRemovedOriginal(result.OriginalHash)
	if removed {
		saved = freed - dao.FileSize(result)
		if result.OriginalHash != src.Hash {
			saved += dao.FileSize(src)
		}
		saved = max(saved, 0)
	} else {
		result.OriginalRemoved = false
	}
	if err := mg.setCopies([]*pb.File{result}); err != nil {
		log.Error("could not count the copies of a compressed file:", err)
	}
	log.Info("compressed", src.Hash, "into", result.Hash, "as", t.preset+":", saved, "bytes freed")
	return cTaskDone, result, saved, i18n.Msg{}
}

// compressWhy is what a task that failed with err says: the disk full, the
// file changed meanwhile, or that it could not be compressed (the error
// itself logged by content).
func compressWhy(src *pb.File, err error) i18n.Msg {
	var full *storageFullError
	var changed *ContentChangedError
	switch {
	case errors.As(err, &full):
		return full.msg()
	case errors.As(err, &changed):
		return changed.msg()
	}
	log.Error("could not compress", src.Hash+":", err)
	return i18n.DevFilesCompressFailed()
}

// --- Videos ---

// compressSides is the shorter side each video preset sizes a video to.
var compressSides = map[string]int{cPresetVideo1080: 1080, cPresetVideo720: 720}

// compressVideoArgs is ffmpeg's command line for compressing the video v
// describes, at src, into out as format, as preset: its picture re-encoded
// as HEVC (libx265, whose settings in faster are a Raspberry Pi's best
// trade of time for size) at the preset's bit rate, capped at 1.5 times it
// in a buffer of two, tagged hvc1 for Apple's players; sized down to the
// preset's shorter side, the other in proportion and even, on the picture
// as shown - ffmpeg turns the frames upright as it re-encodes, so a
// portrait video is sized by its width and comes out with no rotation to
// apply; 10 bits a sample kept (an iPhone's HDR, as Main 10) with the
// colours' primaries, transfer and matrix. AAC sound is copied, any other
// re-encoded to AAC; the date, place and a phone's own tags are kept
// (-map_metadata 0, use_metadata_tags), and the index goes first, as the
// device's uploads are played from it. Progress goes to stdout
// (compressRun).
func compressVideoArgs(src, out, format string, v *videoProbe, preset string) []string {
	args := []string{"-y", "-v", "error", "-nostats", "-progress", "pipe:1"}
	args = append(args, ffmpegInput(src)...)
	args = append(args, "-map", "0:"+strconv.Itoa(v.index))
	for _, a := range v.audio {
		args = append(args, "-map", "0:"+strconv.Itoa(a.index))
	}
	if side := compressSides[preset]; side > 0 {
		args = append(args, "-vf", fmt.Sprintf("scale=w='if(lte(iw,ih),%d,-2)':h='if(lte(iw,ih),-2,%d)'", side, side))
	}
	rate := v.target(preset)
	kbps := func(r int64) string { return strconv.FormatInt(max(r/1000, 1), 10) + "k" }
	pixFmt := "yuv420p"
	if v.tenBit() {
		pixFmt = "yuv420p10le"
	}
	args = append(args, "-c:v", "libx265", "-preset", "faster",
		"-b:v", kbps(rate), "-maxrate", kbps(rate*3/2), "-bufsize", kbps(rate*2),
		// x265's own pool size: limiting it (pools=N) measured no less
		// memory in a container held to 4 CPUs (745 MB at the peak of a 4K
		// recording either way), and asks the kernel for a NUMA node's
		// affinity, which a container refuses.
		"-pix_fmt", pixFmt, "-x265-params", "log-level=error", "-tag:v", "hvc1")
	if c := v.color; c.Primaries != "" {
		args = append(args, "-color_primaries", c.Primaries)
	}
	if c := v.color; c.Transfer != "" {
		args = append(args, "-color_trc", c.Transfer)
	}
	if c := v.color; c.Matrix != "" {
		args = append(args, "-colorspace", c.Matrix)
	}
	for i, a := range v.audio {
		n := strconv.Itoa(i)
		if a.copied() {
			args = append(args, "-c:a:"+n, "copy")
		} else {
			args = append(args, "-c:a:"+n, "aac", "-b:a:"+n, kbps(cAudioRate))
		}
	}
	return append(args, "-map_metadata", "0", "-movflags", "+faststart+use_metadata_tags", "-f", format, out)
}

// compressTimeout is how long compressing a video of duration seconds
// may take.
func compressTimeout(duration float64) time.Duration {
	return max(cTranscodeTimeout, time.Duration(duration*cCompressSecsPerSec*float64(time.Second)))
}

// compressRun runs ffmpeg with args, compressing a video of duration
// seconds, and calls progress with how far it is (0-99) as ffmpeg says
// (-progress: out_time_us, the output's clock); a variable so tests can do
// without it.
var compressRun = func(args []string, duration float64, progress func(int32)) error {
	cmd, cancel := command(compressTimeout(duration), "ffmpeg", args...)
	defer cancel()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// The clock is the last packet written's, the sound's or the
	// picture's: it can step back a little, and the percent never does.
	var shown int32
	lines := bufio.NewScanner(stdout)
	for lines.Scan() {
		k, v, ok := strings.Cut(lines.Text(), "=")
		if !ok || (k != "out_time_us" && k != "out_time_ms") || duration <= 0 {
			continue // out_time_ms is in microseconds too
		}
		us, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if p := int32(min(99, float64(us)/1e6/duration*100)); err == nil && p > shown {
			shown = p
			progress(p)
		}
	}
	if err := cmd.Wait(); err != nil {
		msg := stderr.String()
		if len(msg) > 2048 {
			msg = msg[len(msg)-2048:]
		}
		return fmt.Errorf("ffmpeg compress: %w: %s", err, strings.TrimSpace(msg)) // i18n-ignore: logged, never shown
	}
	return nil
}

// compressVideo writes file's video compressed as preset into a plain
// .compress-* in unenc-storage-path - refused when the disk hasn't need
// bytes to spare - and answers its path (removed by the caller; "" when
// none was made). One ffmpeg at a time reads a stored video: the slot
// first, so the stream's token doesn't age while this waits.
func (mg *Manager) compressVideo(ses *session.Session, file *pb.File, v *videoProbe, preset string, need int64, progress func(int32)) (string, error) {
	format := compressFormatOf(file)
	if v == nil || format == "" {
		return "", errors.New("not a video the device compresses") // i18n-ignore: logged, never shown
	}
	transcodeSlots <- struct{}{}
	defer func() { <-transcodeSlots }()
	src, done, err := mg.videoSource(ses, file)
	if err != nil {
		return "", err
	}
	defer done()
	dir := cfg.GetStr("otc", "unenc-storage-path")
	if err := checkFreeSpace(dir, need); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, cCompressTempPrefix+"*")
	if err != nil {
		return "", err
	}
	tmp.Close()
	plain := tmp.Name()
	if err := os.Chmod(plain, 0o600); err != nil {
		return plain, err
	}
	start := time.Now()
	if err := compressRun(compressVideoArgs(src, plain, format, v, preset), v.duration, progress); err != nil {
		return plain, err
	}
	log.Info("compressed a video of", fmt.Sprintf("%.0f s", v.duration), "in", time.Since(start).Round(time.Second)) // i18n-ignore: a log line
	return plain, nil
}

// --- Photos ---

// compressedPhoto is a photo compressed: the JPEG, and how a face's box
// moves into it.
type compressedPhoto struct {
	content []byte
	move    func(dao.FaceBox) (dao.FaceBox, bool)
}

// renderCompress compresses the photo content as preset into a JPEG,
// decoded as an edit's is (holdDecode, decodeHeic or decodeFull): its
// camera's EXIF and its colour profile carried over (editedExif,
// withMetadata). photo_12mp shrinks it to about cCompressPixels - by a
// whole factor first when that is 2 or more, each pixel the average of the
// block it stands for (shrinkBy, the analysis' own shrink: one pass and a
// picture a quarter of the size at most, where a resampler would hold the
// full decode and its own copy), then Catmull-Rom for the rest, on the
// smaller picture - and turns it upright, orientation 1. jpeg_resave and
// png_to_jpeg keep the pixels and their orientation as they were.
func renderCompress(content []byte, preset string) (*compressedPhoto, error) {
	kind := editKindOf(content)
	if kind == editNone {
		return nil, ErrNotEditable
	}
	var exif []byte
	var icc [][]byte
	orientation := 1
	switch {
	case kind == editJPEG:
		exif, icc = jpegMetadata(content)
		if info, err := exifinfo.FromJPEG(content); err == nil {
			orientation = info.Orientation
		}
	case kind == editPNG:
		exif, icc = pngMetadata(content)
	case isHeif(content):
		var profile []byte
		exif, profile = heicMetadata(content)
		icc = iccSegments(profile)
		if info, err := exifinfo.FromHEIC(content); err == nil {
			orientation = info.Orientation
		}
		if r, mirror, axis := heifTransform(content); r != 0 || mirror {
			orientation = heifOrientation(r, mirror, axis)
		}
	}
	if orientation < 1 || orientation > 8 {
		orientation = 1
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
	upright := func(b image.Rectangle, o int) (int, int) {
		if o >= 5 && o <= 8 {
			return b.Dy(), b.Dx()
		}
		return b.Dx(), b.Dy()
	}
	fromW, fromH := upright(img.Bounds(), orientation)

	quality := cQualityResave
	switch preset {
	case cPresetPhoto12MP:
		quality = cQuality12MP
		shrunk := shrinkToAbout(img, cCompressPixels)
		if shrunk != img {
			img = shrunk
			debug.FreeOSMemory() // the full decode, given back before what follows
		}
		img = applyOrientation(img, orientation)
		orientation = 1
	case cPresetPNGToJPEG:
		quality = cQualityPNG
	case cPresetJPEGResave:
	default:
		return nil, devtext.New(i18n.DevFilesCompressUnknownPreset())
	}
	toW, toH := upright(img.Bounds(), orientation)

	var out bytes.Buffer
	if err := jpeg.Encode(&out, opaqueRGBA(img), &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	img = nil
	if exif != nil {
		w, h := 0, 0 // the pixels kept: the Exif dimensions are still right
		if preset == cPresetPhoto12MP {
			w, h = toW, toH
		}
		edited, _, exifErr := editedExif(exif, orientation, w, h, true)
		if exifErr != nil {
			log.Info("a compressed photo leaves out its camera details, which can't be read:", exifErr)
		}
		exif = edited
	}
	r := &compressedPhoto{content: withMetadata(out.Bytes(), exif, icc), move: scaledFace(fromW, fromH, toW, toH)}
	debug.FreeOSMemory()
	return r, nil
}

// shrinkToAbout is img brought down to about target pixels: by the largest
// whole factor that leaves it at least that large (shrinkBy), then the rest
// of the way by Catmull-Rom. img itself when it isn't larger.
func shrinkToAbout(img image.Image, target int64) image.Image {
	b := img.Bounds()
	px := int64(b.Dx()) * int64(b.Dy())
	if px <= target {
		return img
	}
	if k := int(math.Sqrt(float64(px) / float64(target))); k >= 2 {
		img = shrinkBy(img, k)
		b = img.Bounds()
		px = int64(b.Dx()) * int64(b.Dy())
	}
	if float64(px) <= float64(target)*1.02 {
		return img // within a few pixels of it: no second pass
	}
	s := math.Sqrt(float64(target) / float64(px))
	w, h := max(1, int(math.Round(float64(b.Dx())*s))), max(1, int(math.Round(float64(b.Dy())*s)))
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

// opaqueRGBA is img as the JPEG encoder takes it fastest: an NRGBA of
// opaque pixels (a shrunk or turned photo; compress offers nothing for one
// with transparency) is the same bytes as an RGBA, so it is read as one.
func opaqueRGBA(img image.Image) image.Image {
	if n, ok := img.(*image.NRGBA); ok && n.Opaque() {
		return &image.RGBA{Pix: n.Pix, Stride: n.Stride, Rect: n.Rect}
	}
	return img
}

// scaledFace moves a face's box from a photo fromW x fromH (shown upright)
// into the same photo at toW x toH, both in their analysis' pixels
// (workScale): its people stay as the owner sorted them.
func scaledFace(fromW, fromH, toW, toH int) func(dao.FaceBox) (dao.FaceBox, bool) {
	k, k2 := float64(workScale(fromW, fromH)), float64(workScale(toW, toH))
	sx, sy := float64(toW)/float64(max(fromW, 1))*k/k2, float64(toH)/float64(max(fromH, 1))*k/k2
	return func(b dao.FaceBox) (dao.FaceBox, bool) {
		return dao.FaceBox{
			X: int(math.Round(float64(b.X) * sx)), Y: int(math.Round(float64(b.Y) * sy)),
			W: max(1, int(math.Round(float64(b.W)*sx))), H: max(1, int(math.Round(float64(b.H)*sy))),
		}, true
	}
}

// --- The original ---

// dropRemovedOriginal deletes what is left of original, the original of a
// compressed file - its blob, thumbnails, tags, faces, place, measures and
// Smart search vector, as a delete of its last file would (removeUnusedBlob,
// dropOrphan: those spare it, as its edits' rows name it) - once no file
// or kept version holds it, checked under its lock, which a link of it
// takes too. Its edits' rows stay, saying it is gone: they are the record
// the phones and computers are answered by. A file holding it after all (a
// copy linked while it was compressed) keeps it, and its edits are
// ordinary ones again, which can be reverted. Content that isn't such an
// original is left alone. removed reports whether its bytes are gone now;
// freed is what they were (0 when they were gone already).
func (mg *Manager) dropRemovedOriginal(original string) (removed bool, freed int64) {
	if original == "" {
		return false, 0
	}
	unlock := lockBlob(original)
	defer unlock()
	held, err := mg.dao.HeldOriginals([]string{original})
	if err != nil {
		log.Error("could not look for a compressed file's original", original+":", err)
		return false, 0
	}
	o, ok := held[original]
	if !ok || !o.Removed {
		return false, 0
	}
	inUse, err := mg.dao.ContentHeld(original)
	if err != nil {
		log.Error("could not check what holds a compressed file's original", original+", kept for now:", err)
		return false, 0
	}
	if inUse {
		if err := mg.dao.SetOriginalRemoved(original, false); err != nil {
			log.Error("could not record that a compressed file's original is kept", original+":", err)
		}
		log.Info("a copy of", original, "was linked while it was compressed: kept")
		return false, 0
	}
	// Every edit of it first: none can be reverted once its bytes are gone.
	if err := mg.dao.SetOriginalRemoved(original, true); err != nil {
		log.Error("could not record that a compressed file's original is gone", original+", kept for now:", err)
		return false, 0
	}
	full := blobPath(original)
	if _, err := os.Stat(full); err == nil {
		freed = o.Size
	}
	mg.donePendingAnalysis(original)
	if err := mg.dao.DelTagsByHash(original); err != nil {
		log.Error("could not remove the tags of a compressed file's original", original+":", err)
	}
	mg.dropFacesOfHash(original)
	if err := mg.dao.DelSkippedAnalysis(original); err != nil {
		log.Error("could not forget the skipped analysis of a compressed file's original", original+":", err)
	}
	if err := mg.dao.DelPhotoLocation(original); err != nil {
		log.Error("could not forget where a compressed file's original was taken", original+":", err)
	}
	if err := mg.dao.DelPhotoQuality(original); err != nil {
		log.Error("could not forget how a compressed file's original looked", original+":", err)
	}
	mg.forgetEmbedding(original)
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		log.Error("could not delete a compressed file's original", original+":", err)
		return false, 0
	}
	removeThumbnails(full)
	blobstore.SyncDir(filepath.Dir(full))
	if freed > 0 {
		log.Info("deleted the original of a compressed file:", original)
	}
	return true, freed
}

// compressedFor is the file the device holds in place of hash when hash is
// a compressed file's original - one whose bytes a compress deleted, or is
// about to, which no file holds itself: its newest compressed content, at
// its first path. nil for any other content (or when it can't be told,
// logged).
func (mg *Manager) compressedFor(hash string) *pb.File {
	f, err := mg.dao.RemovedOriginalEdit(hash)
	if err != nil {
		log.Error("could not look for the compressed copy of", hash+":", err)
		return nil
	}
	return f
}

// finishRemovedOriginals deletes the originals a compress saved and a
// restart kept it from deleting: each still on the disk, once nothing
// holds it (dropRemovedOriginal). Once at start, after the orphan sweep.
func (mg *Manager) finishRemovedOriginals() {
	originals, err := mg.dao.RemovedOriginals()
	if err != nil {
		log.Error("could not list the originals of compressed files:", err)
		return
	}
	n := 0
	for _, o := range originals {
		if _, err := os.Stat(blobPath(o)); err != nil {
			continue
		}
		if removed, _ := mg.dropRemovedOriginal(o); removed {
			n++
		}
	}
	if n > 0 {
		log.Info("deleted", n, "original(s) of compressed files a restart had kept")
	}
}

// --- The alerts ---

// compressAlerts tells the owner how job went, once it is over, in Alerts
// - never pushed: they asked for it and may be watching: what was
// compressed and freed, and what failed, a line a file with its name and
// why. A file skipped, as it came out no smaller, is neither.
func (mg *Manager) compressAlerts(job *compressJob) {
	mg.compress.mu.Lock()
	done, saved := 0, job.saved
	var lines []dao.AlertLine
	for _, t := range job.tasks {
		switch t.state {
		case cTaskDone:
			done++
		case cTaskFailed:
			lines = append(lines, dao.AlertLine{Alert: alerttext.New(i18n.DevAlertFileNotProcessed(path.Base(t.path)), t.why), At: t.at})
		}
	}
	mg.compress.mu.Unlock()
	if done > 0 {
		a := alerttext.New(i18n.DevAlertCompressDone(done, i18n.FormatBytes(job.lang, saved)), i18n.Msg{})
		if err := mg.dao.AddStandaloneAlert(a, nil); err != nil {
			log.Error("error recording the alert:", err)
		}
	}
	if len(lines) > 0 {
		if err := mg.dao.AddStandaloneAlert(alerttext.New(i18n.DevAlertCompressFailed(len(lines)), i18n.Msg{}), lines); err != nil {
			log.Error("error recording the alert:", err)
		}
	}
	log.Info("compress job", job.id, "is over:", done, "compressed,", len(lines), "failed,", saved, "bytes freed")
}
