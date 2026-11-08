// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

// Video edit (EditVideo in messages.proto): the viewers' Edit on a video
// cuts it down to a start and an end, with the post composer's trimmer.
// Unlike a post's cut (post_export.go), nothing is re-encoded: the copy
// stays in the library, so ffmpeg copies the streams between the two
// (-c copy) - nothing of the picture or the sound is lost, an HDR
// recording stays HDR, its date, place and rotation go with it, and a long
// 4K recording is cut in the time it takes to read it. A copied stream can
// only begin at a keyframe, so the cut keeps the frames from the last one
// at or before the start: an MP4 or a MOV (a phone's) hides them behind an
// edit list, which players read to start exactly at the start chosen (one
// that doesn't starts at the keyframe, the sound in step); an MKV, WebM or
// AVI, which have none, starts at the keyframe - a moment earlier, a
// second or less on a phone's recording. ffmpeg reads the stored video over
// the loopback stream and writes a plain copy into unenc-storage-path,
// sealed into the storage as new content; from there it is saved as a
// photo's edit is (saveEdit, edit_image.go): the original kept for Revert,
// the copy or the paths, the collections.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
)

// errNotCuttable is EditVideo's refusal of a video in a format it can't
// cut, or of a file that isn't a video (IsNotEditable).
var errNotCuttable = devtext.New(i18n.DevFilesNotCuttable())

// errCutTooShort is EditVideo's refusal of a cut that keeps less than
// cMinCutSecs, or starts past the end.
var errCutTooShort = devtext.New(i18n.DevFilesCutTooShort())

// errVideoNotEdited is ErrNotEdited for a video (IsNotEdited).
var errVideoNotEdited = devtext.New(i18n.DevFilesVideoNotEdited())

// IsCutTooShort is whether err is EditVideo's refusal of a cut too short.
func IsCutTooShort(err error) bool { return errors.Is(err, errCutTooShort) }

// IsNotEdited is whether err is a revert's of content that holds no edit.
func IsNotEdited(err error) bool {
	return errors.Is(err, ErrNotEdited) || errors.Is(err, errVideoNotEdited)
}

// cMinCutSecs is the shortest cut, the composer's trimmer's too.
const cMinCutSecs = 0.5

// cCutEndSlack: an end this close to the video's own is its end - a
// trimmer's handle left there, in a length rounded to its tenths.
const cCutEndSlack = 0.05

// cutFormats is ffmpeg's muxer for each type of video a cut can be copied
// into: the container the video already is.
var cutFormats = map[string]string{
	"video/mp4":        "mp4",
	"video/quicktime":  "mov",
	"video/x-m4v":      "mp4",
	"video/3gpp":       "3gp",
	"video/3gpp2":      "3g2",
	"video/x-matroska": "matroska",
	"video/webm":       "webm",
	"video/x-msvideo":  "avi",
}

// cutExtFormats is cutFormats by extension, for a video whose type was
// recorded otherwise (application/octet-stream from an old upload).
var cutExtFormats = map[string]string{
	".mp4": "mp4", ".mov": "mov", ".m4v": "mp4", ".3gp": "3gp", ".3g2": "3g2",
	".mkv": "matroska", ".webm": "webm", ".avi": "avi",
}

// cutFormatOf is the muxer a cut of file copies into; "" for a file that
// can't be cut.
func cutFormatOf(file *pb.File) string {
	mime, _, _ := strings.Cut(file.Mime, ";")
	if f, ok := cutFormats[strings.TrimSpace(strings.ToLower(mime))]; ok {
		return f
	}
	if strings.HasPrefix(mime, "video/") || mime == "" || mime == "application/octet-stream" {
		_, ext := splitExt(file.Path)
		return cutExtFormats[strings.ToLower(ext)]
	}
	return ""
}

// cutRange is the cut asked for, inside a video of duration seconds: the
// start at 0 at least, the end at the video's end at most - and there when
// end is 0, or within cCutEndSlack of it.
func cutRange(start, end, duration float64) (float64, float64) {
	if math.IsNaN(start) || start < 0 {
		start = 0
	}
	if math.IsNaN(end) || end <= 0 || end > duration-cCutEndSlack {
		end = duration
	}
	return start, end
}

// cutInfo is what a cut needs to know of a video: its length, and the
// streams it keeps.
type cutInfo struct {
	duration float64
	// The picture's and the sound's streams in codecs ffmpeg knows, by
	// index; none known keeps every picture and sound stream.
	streams []int
}

// probeCut is ffprobe's cutInfo of the video at src. A stream ffmpeg has
// no codec for is left out - an iPhone's spatial audio (APAC, beside its
// AAC track) or timed metadata: no container takes it back, and the cut
// would fail for it.
func probeCut(src string) (cutInfo, error) {
	cmd, cancel := command(cProbeTimeout, "ffprobe", "-v", "error",
		"-show_entries", "format=duration:stream=index,codec_type,codec_name", "-of", "json", src)
	defer cancel()
	out, err := cmd.Output()
	if err != nil {
		return cutInfo{}, err
	}
	return parseCutInfo(out)
}

// parseCutInfo reads probeCut's answer.
func parseCutInfo(out []byte) (cutInfo, error) {
	var probe struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			Index     int    `json:"index"`
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		return cutInfo{}, err
	}
	duration, err := strconv.ParseFloat(strings.TrimSpace(probe.Format.Duration), 64)
	if err != nil {
		return cutInfo{}, fmt.Errorf("no length: %q", probe.Format.Duration) // i18n-ignore: logged, never shown
	}
	info := cutInfo{duration: duration}
	video := false
	for _, st := range probe.Streams {
		if st.CodecName == "" || (st.CodecType != "video" && st.CodecType != "audio") {
			continue
		}
		video = video || st.CodecType == "video"
		info.streams = append(info.streams, st.Index)
	}
	if !video {
		info.streams = nil
	}
	return info, nil
}

// cutArgs is ffmpeg's command line for a cut of src from start to end of
// the video info describes into out, as format.
func cutArgs(src, out, format string, start, end float64, info cutInfo) []string {
	duration := info.duration
	secs := func(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }
	args := []string{"-y", "-v", "error"}
	if start > 0 {
		// Ahead of -i: ffmpeg seeks by the index, to the keyframe at or
		// before it, without reading what lies before.
		args = append(args, "-ss", secs(start))
	}
	args = append(args, ffmpegInput(src)...)
	if end < duration {
		// A length: after an input -ss, the output's clock starts at the
		// cut.
		args = append(args, "-t", secs(end-start))
	}
	// The picture and the sound (probeCut). Not a phone's other tracks,
	// which a container may not take back.
	if len(info.streams) == 0 {
		args = append(args, "-map", "0:v", "-map", "0:a?")
	}
	for _, i := range info.streams {
		args = append(args, "-map", "0:"+strconv.Itoa(i))
	}
	args = append(args, "-c", "copy", "-map_metadata", "0")
	switch format {
	case "mp4", "mov", "3gp", "3g2":
		// The index first, as the device's uploads are played from it; and
		// a phone's own tags (com.apple.quicktime.location...) kept as they
		// were, which Places and the date taken read. Its timestamps as
		// ffmpeg leaves them: the frames before the start are then hidden
		// by an edit list, not shifted into view.
		args = append(args, "-movflags", "+faststart+use_metadata_tags")
	default:
		// No edit lists: the cut starts at its keyframe, at 0.
		args = append(args, "-avoid_negative_ts", "make_zero")
	}
	return append(args, "-f", format, out)
}

// cutRun runs ffmpeg with args; a variable so tests can do without it.
var cutRun = func(args []string) error {
	cmd, cancel := command(cTranscodeTimeout, "ffmpeg", args...)
	defer cancel()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg cut: %w: %s", err, strings.TrimSpace(stderr.String())) // i18n-ignore: logged, never shown
	}
	return nil
}

// cutProbe is probeCut; a variable so tests can do without it.
var cutProbe = probeCut

// EditVideo cuts the video at req.Path (EditVideo in messages.proto) and
// answers the edited file: the path's own, or - as_copy - the copy's.
func (mg *Manager) EditVideo(ses *session.Session, req *pb.EditVideo, lang string) (*pb.File, error) {
	noteUploadActivity() // the thumbnails pass waits for writes to stop
	file, err := mg.dao.GetFileByPath(req.Path)
	if err != nil {
		return nil, err
	}
	if shownElsewhere(req.Hash, file.Hash) {
		return nil, &ContentChangedError{}
	}
	format := cutFormatOf(file)
	if format == "" {
		return nil, errNotCuttable
	}
	failed := func(err error) error {
		log.Error("could not cut a video:", err)
		return devtext.New(i18n.DevFilesEditFailed(i18n.DevFilesCutFailed().Render(lang)))
	}

	// The slot of a post's video first: one ffmpeg at a time reads a
	// stored video, and the stream's token must not age while this waits.
	transcodeSlots <- struct{}{}
	defer func() { <-transcodeSlots }()
	defer mg.beginProcessing("", jobTranscode)()
	src, done, err := mg.videoSource(ses, file)
	if err != nil {
		return nil, err
	}
	defer done()
	info, err := cutProbe(src)
	if err != nil || info.duration <= 0 {
		return nil, failed(fmt.Errorf("its length: %v", err)) // i18n-ignore: logged, never shown
	}
	duration := info.duration
	start, end := cutRange(req.StartSecs, req.EndSecs, duration)
	if end-start < cMinCutSecs {
		return nil, errCutTooShort
	}
	if start == 0 && end == duration {
		return file, nil // nothing cut
	}

	// The plain copy, beside the posts' (orphan_sweep.go takes one a power
	// cut left), no larger than the video itself.
	dir := cfg.GetStr("otc", "unenc-storage-path")
	if err := checkFreeSpace(dir, mg.budgetSize(file)); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dir, ".cut-*")
	if err != nil {
		return nil, err
	}
	tmp.Close()
	plain := tmp.Name()
	defer os.Remove(plain)
	if err := os.Chmod(plain, 0o600); err != nil {
		return nil, err
	}
	if err := cutRun(cutArgs(src, plain, format, start, end, info)); err != nil {
		return nil, failed(err)
	}
	edited, err := fileMeta(plain)
	if err != nil {
		return nil, failed(err)
	}
	// The same type as the video: the container is.
	edited.Mime, edited.Created, edited.Modified = file.Mime, nil, nil

	_, ext := splitExt(file.Path)
	result, err := mg.saveEdit(ses, file, editOut{
		edited:  edited,
		ext:     ext,
		store:   func() error { return mg.storeEditFile(ses, edited.Hash, plain, dao.FileSize(edited)) },
		renames: func(string) func(int) string { return nil }, // a cut keeps its name
	}, req.AsCopy, req.EveryCopy, lang)
	if err != nil {
		return nil, err
	}
	log.Info("cut a video: from", start, "to", end, "of", duration, "copy", req.AsCopy, "every copy", req.EveryCopy)
	return result, nil
}

// storeEditFile seals the plain file at plain (of hash, size bytes) into
// the storage, unless it is there: refused, with nothing written, when
// there is no room.
func (mg *Manager) storeEditFile(ses *session.Session, hash, plain string, size int64) error {
	if mg.hasBlob(hash) {
		return nil
	}
	target := blobPath(hash)
	if err := pendingUploads.roomFor(path.Dir(target), sealedSize(size)); err != nil {
		return err
	}
	unlock := lockBlob(hash)
	defer unlock()
	f, err := os.Open(plain)
	if err != nil {
		return err
	}
	defer f.Close()
	w, err := blobstore.Create(target, ses)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, f); err != nil {
		w.Abort()
		return err
	}
	return w.Commit()
}
