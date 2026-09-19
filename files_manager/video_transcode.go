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
	"os"
	"os/exec"
	"strconv"

	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/gabriel-vasile/mimetype"
	"golang.org/x/image/draw"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// cSocialVideoMaxWidth caps the output width of a social-compressed
	// video (issue #60) - 854px (roughly 480p for 16:9) keeps a typical
	// phone-shot clip comfortably watchable in a social feed while cutting
	// file size dramatically versus a modern phone's original 1080p/4K
	// capture. Only ever downscales - see the scale filter below.
	cSocialVideoMaxWidth = 854
	// cSocialVideoBitrate/cSocialVideoAudioBitrate are ffmpeg's target
	// output bitrates - a target, not a hard cap (ffmpeg's -b:v doesn't
	// guarantee an exact output size), chosen to land a typical short,
	// phone-shot clip well under the size that triggered compression in
	// the first place. Re-encoding again just to hit an exact byte count
	// wasn't worth the added complexity here.
	cSocialVideoBitrate = "1500k"
	// cSocialTrimCRF is used when a video is only being trimmed, not
	// compressed (issue #108) - the source is already small enough to
	// distribute, so the re-encode a frame-accurate cut requires should
	// give back something visually indistinguishable rather than hitting
	// a size target. 20 is a common "looks like the source" x264 setting.
	cSocialTrimCRF           = "20"
	cSocialVideoAudioBitrate = "128k"
)

// TrimRange is a [Start, End) cut of a video, in seconds from the start of
// the clip (issue #108). An End at or below Start means "run to the end of
// the clip", so a caller that only wants to drop an intro sends just a
// Start.
type TrimRange struct {
	Start float64
	End   float64
}

// Duration reports the length of the cut, or 0 when the range is open-ended
// (see TrimRange).
func (t TrimRange) Duration() float64 {
	if t.End <= t.Start {
		return 0
	}
	return t.End - t.Start
}

// CompressVideoForSocial re-encodes content (any container ffmpeg
// understands) into a lower-resolution/bitrate H.264/AAC MP4, for
// publishing a video that's too large to distribute to friends at its
// original quality (issue #60: "we can perhaps create a low resolution of
// the video if it is too large and then publish this"). Never touches the
// original file on disk - the caller is expected to store the *returned*
// bytes as a brand new file and publish that instead, leaving whatever the
// owner already has in Files/the gallery untouched. Requires ffmpeg on
// PATH (already a runtime dependency - see video_frames.go).
func (mg *Manager) CompressVideoForSocial(content []byte) ([]byte, error) {
	return mg.transcodeForSocial(content, nil, true)
}

// TrimVideoForSocial cuts content down to trim (issue #108) without
// touching its resolution - someone posting a 6-second highlight out of a
// 30-second clip asked to lose the other 24 seconds, not the picture
// quality. Same contract as CompressVideoForSocial otherwise: the original
// file on disk is never modified, and the caller publishes the returned
// bytes as a new file.
//
// downscale folds issue #60's compression into this same single ffmpeg
// pass, for a source that was going to be re-encoded anyway. Doing the two
// as separate passes would re-encode a 4K source at 4K first only to
// immediately throw that away - a meaningful cost on a Raspberry Pi, and
// a second generation of lossy encoding for nothing.
func (mg *Manager) TrimVideoForSocial(content []byte, trim TrimRange, downscale bool) ([]byte, error) {
	return mg.transcodeForSocial(content, &trim, downscale)
}

// transcodeForSocial is the one place that actually shells out to ffmpeg
// for a publication's video. trim nil means "the whole clip"; downscale
// false keeps the source resolution (and picks quality-targeted CRF over a
// fixed bitrate, since there's no size problem to solve in that case).
func (mg *Manager) transcodeForSocial(content []byte, trim *TrimRange, downscale bool) ([]byte, error) {
	inTmp, err := os.CreateTemp("", "otc-video-social-in-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp input file: %w", err)
	}
	inPath := inTmp.Name()
	defer os.Remove(inPath)
	if _, err := inTmp.Write(content); err != nil {
		inTmp.Close()
		return nil, fmt.Errorf("writing temp input file: %w", err)
	}
	if err := inTmp.Close(); err != nil {
		return nil, fmt.Errorf("closing temp input file: %w", err)
	}

	outPath := inPath + "-out.mp4"
	defer os.Remove(outPath)

	args := []string{"-y"}
	if trim != nil && trim.Start > 0 {
		// -ss ahead of -i seeks by index before decoding anything, which
		// is what keeps trimming the tail off a long clip fast. Combined
		// with a re-encode (never -c copy) the cut lands on the requested
		// frame rather than the nearest preceding keyframe, which can be
		// seconds away - visibly wrong when someone trimmed to a specific
		// moment.
		args = append(args, "-ss", strconv.FormatFloat(trim.Start, 'f', 3, 64))
	}
	args = append(args, "-i", inPath)
	if d := trimDuration(trim); d > 0 {
		// -t (a duration) rather than -to (an absolute timestamp): after
		// an input -ss the output clock has already been rebased to the
		// cut point, so a duration is what's unambiguous here.
		args = append(args, "-t", strconv.FormatFloat(d, 'f', 3, 64))
	}

	if downscale {
		// scale='if(gt(iw,W),W,iw)':-2 only downscales a video wider than
		// cSocialVideoMaxWidth - a source already narrower keeps its own
		// size, never gets upscaled. -2 keeps the computed height even
		// (required by libx264) while preserving aspect ratio.
		scaleFilter := fmt.Sprintf("scale='if(gt(iw,%d),%d,iw)':-2", cSocialVideoMaxWidth, cSocialVideoMaxWidth)
		args = append(args,
			"-vf", scaleFilter,
			"-c:v", "libx264", "-preset", "veryfast", "-b:v", cSocialVideoBitrate,
			"-c:a", "aac", "-b:a", cSocialVideoAudioBitrate,
		)
	} else {
		args = append(args,
			"-c:v", "libx264", "-preset", "veryfast", "-crf", cSocialTrimCRF,
			"-c:a", "aac", "-b:a", cSocialVideoAudioBitrate,
		)
	}
	args = append(args, "-movflags", "+faststart", outPath)

	cmd := exec.Command("ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg transcode: %w: %s", err, stderr.String())
	}

	out, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("reading transcoded output: %w", err)
	}
	log.Debug("Transcoded video for social:", len(content), "->", len(out), "bytes, trimmed:", trim != nil, "downscaled:", downscale)
	return out, nil
}

func trimDuration(trim *TrimRange) float64 {
	if trim == nil {
		return 0
	}
	return trim.Duration()
}

// BuildTransientFile computes the same Hash/Mime/Size metadata UploadFile
// does, but does none of UploadFile's persistence: no `files` DB row, no
// write to the encrypted storage path, no tag/thumbnail/face background
// processing. For content that exists purely to support something else -
// today, only NewPublication's compressed video-for-social copy - rather
// than something the owner chose to keep: the same "never shows up in
// Files, never gets tagged" treatment a photo's own thumbnail already
// gets, just for a case that needs the full pb.File shape (Content
// included), not a bare hash.
func BuildTransientFile(content []byte) *pb.File {
	mimeType := mimetype.Detect(content)
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	now := timestamppb.Now()
	return &pb.File{
		Created:  now,
		Modified: now,
		Mime:     mimeType.String(),
		Hash:     hash,
		Size:     int32(len(content)),
		Content:  content,
	}
}

// GenerateVideoThumbnail extracts a representative frame from raw video
// bytes and returns it JPEG-encoded, downscaled to maxWidth the same way a
// regular upload's own video thumbnail is (see processMediaContent's video
// branch, which passes cfg's "max-thumbnail-width-px" - kept as a plain
// parameter here rather than read from cfg directly, same convention as
// thumbnailSource, so this stays unit-testable without a config file).
// Factored out so a transient file (see BuildTransientFile above) can get
// a thumbnail without going through the whole UploadFile pipeline. Unlike
// processMediaContent's own version, this always encodes a thumbnail
// regardless of the frame's width (that unconditional part already had to
// be fixed once for the image side - see processMediaContent's own
// comment on it - no reason to reproduce the same gap here in new code).
func (mg *Manager) GenerateVideoThumbnail(content []byte, maxWidth int) ([]byte, error) {
	frames, err := extractVideoFrames(content, cVideoSampleFrames)
	if err != nil {
		return nil, fmt.Errorf("extracting video frames: %w", err)
	}
	if len(frames) == 0 {
		return nil, errors.New("no frames extracted from video")
	}

	thumbSrc := frames[0]
	b := thumbSrc.Bounds()
	var thumbImg image.Image = thumbSrc
	if b.Dx() > maxWidth {
		newH := int(float64(b.Dy()) * float64(maxWidth) / float64(b.Dx()))
		dst := image.NewRGBA(image.Rect(0, 0, maxWidth, newH))
		draw.CatmullRom.Scale(dst, dst.Bounds(), thumbSrc, thumbSrc.Bounds(), draw.Over, nil)
		thumbImg = dst
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, thumbImg, &jpeg.Options{Quality: 80}); err != nil {
		return nil, fmt.Errorf("encoding thumbnail: %w", err)
	}
	return buf.Bytes(), nil
}
