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
	"strconv"
	"strings"

	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/gabriel-vasile/mimetype"
	"golang.org/x/image/draw"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// cSocialVideoMaxWidth caps the output width of a social-compressed
	// video (issue #60) - only ever downscales, see the scale filter
	// below.
	//
	// Issue #112 raised this by 30% (854 -> 1110): 854px was roughly
	// 480p, which is noticeably soft once a clip is watched full-width on
	// a phone. Note that is a 30% wider picture but ~69% more pixels, so
	// cSocialVideoBitrate had to go up with it - holding the bitrate
	// still would have spread the same bits over half as many again and
	// handed back a larger, softer video rather than a better one.
	cSocialVideoMaxWidth = 1110
	// cSocialVideoBitrate/cSocialVideoAudioBitrate are ffmpeg's target
	// output bitrates - a target, not a hard cap (ffmpeg's -b:v doesn't
	// guarantee an exact output size), chosen to land a typical short,
	// phone-shot clip well under the size that triggered compression in
	// the first place. Re-encoding again just to hit an exact byte count
	// wasn't worth the added complexity here.
	//
	// Issue #112: scaled with the pixel count when cSocialVideoMaxWidth
	// went up (1500k * 1.69 ~= 2500k), so the extra resolution is
	// actually visible instead of being cancelled out by coarser
	// compression. Videos published from here on are correspondingly
	// larger - the cost of the sharper picture that was asked for, and
	// still a fraction of the originals this exists to avoid
	// distributing.
	cSocialVideoBitrate = "2500k"
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
	transcodeSlots <- struct{}{}
	defer func() { <-transcodeSlots }()
	if err := transcodeFile(inPath, outPath, trim, downscale); err != nil {
		return nil, err
	}
	out, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("reading transcoded output: %w", err)
	}
	log.Debug("Transcoded video for social:", len(content), "->", len(out), "bytes, trimmed:", trim != nil, "downscaled:", downscale)
	return out, nil
}

// transcodeSlots: one post video is re-encoded at a time (issue #166) - it
// takes every core, and more at once only made each slower and added
// their memory together.
var transcodeSlots = make(chan struct{}, 1)

// transcodeFile re-encodes the video at inPath - a file, or a URL ffmpeg
// reads with range requests (a stored video's loopback stream) - into
// outPath as an MP4.
// The caller holds a transcodeSlots slot.
func transcodeFile(inPath, outPath string, trim *TrimRange, downscale bool) error {
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

	var filters []string
	if downscale {
		// scale='if(gt(iw,W),W,iw)':-2 only downscales a video wider than
		// cSocialVideoMaxWidth - a source already narrower keeps its own
		// size, never gets upscaled. -2 keeps the computed height even
		// (required by libx264) while preserving aspect ratio.
		filters = append(filters, fmt.Sprintf("scale='if(gt(iw,%d),%d,iw)':-2", cSocialVideoMaxWidth, cSocialVideoMaxWidth))
	}
	rate := []string{"-crf", cSocialTrimCRF}
	if downscale {
		rate = []string{"-b:v", cSocialVideoBitrate}
	}
	encode := func(color []string) []string {
		chain := append(append([]string{}, filters...), color...)
		vf := strings.Join(append(chain, cTagBT709), ",")
		a := append(append([]string{}, args...), "-vf", vf,
			"-c:v", "libx264", "-preset", "veryfast")
		a = append(a, rate...)
		a = append(a, playableEverywhere...)
		return append(a, "-c:a", "aac", "-b:a", cSocialVideoAudioBitrate, "-movflags", "+faststart", outPath)
	}

	// An HDR source (every iPhone recording, by default) is brought down
	// to standard colour first. Without a tone-mapping filter in this
	// ffmpeg (it needs zscale), the plain 8-bit conversion still plays -
	// just flatter-looking - which beats a video that doesn't play at all.
	var runErr error
	var stderr bytes.Buffer
	for _, color := range [][]string{hdrToSDR(probeColor(inPath)), {"format=yuv420p"}} {
		if color == nil {
			continue
		}
		stderr.Reset()
		cmd, cancel := command(cTranscodeTimeout, "ffmpeg", encode(color)...)
		cmd.Stderr = &stderr
		runErr = cmd.Run()
		cancel()
		if runErr == nil {
			break
		}
		log.Info("ffmpeg transcode failed with", strings.Join(color, ","), "- trying the next conversion:", runErr)
	}
	if runErr != nil {
		return fmt.Errorf("ffmpeg transcode: %w: %s", runErr, stderr.String())
	}
	return nil
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
	// One frame: this is only the post's thumbnail - the tags come from
	// processMediaContent's own frames. It decoded cVideoSampleFrames full
	// frames and kept the first (issue #173).
	frames, err := extractVideoFrames(content, 1)
	if err != nil {
		return nil, fmt.Errorf("extracting video frames: %w", err)
	}
	return videoThumbnail(frames, maxWidth)
}

// GenerateVideoThumbnailFrom is GenerateVideoThumbnail for a video at src
// (a file or a loopback stream URL), without loading it.
func GenerateVideoThumbnailFrom(src string, maxWidth int) ([]byte, error) {
	frames, err := extractVideoFramesFrom(src, 1)
	if err != nil {
		return nil, fmt.Errorf("extracting video frames: %w", err)
	}
	return videoThumbnail(frames, maxWidth)
}

func videoThumbnail(frames []image.Image, maxWidth int) ([]byte, error) {
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

// playableEverywhere pins the H.264 a post is re-encoded to (issue: videos
// not playing on Android). Left to itself libx264 keeps the source's pixel
// format, so a 10-bit iPhone recording came out as H.264 "High 10" - which
// iPhones and browsers decode in software, but Android phones' hardware
// decoders reject outright ("Decoder failed: c2.qti.avc.decoder"). 8-bit
// 4:2:0 High profile in standard BT.709 colour (cTagBT709) plays on
// anything.
var playableEverywhere = []string{"-pix_fmt", "yuv420p", "-profile:v", "high"}

// cTagBT709 ends every filter chain: the frames carry the source's colour
// tags through the conversion otherwise (an HDR source's HLG tag on what is
// now standard colour), and the encoder writes the frames' tags, not its
// own options.
const cTagBT709 = "setparams=color_primaries=bt709:color_trc=bt709:colorspace=bt709"

// videoColor is what ffprobe says about a video stream's colour.
type videoColor struct {
	Transfer  string // color_transfer, e.g. "arib-std-b67" (HLG) or "smpte2084" (PQ)
	Primaries string // color_primaries, e.g. "bt2020"
	Matrix    string // color_space, e.g. "bt2020nc"
}

// probeColor reads the first video stream's colour tags; an empty result
// (no ffprobe, no tags) reads as standard colour.
func probeColor(path string) videoColor {
	cmd, cancel := command(cProbeTimeout, "ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=color_transfer,color_primaries,color_space",
		"-of", "default=noprint_wrappers=1", path)
	defer cancel()
	out, err := cmd.Output()
	if err != nil {
		return videoColor{}
	}

	return parseProbeColor(string(out))
}

func parseProbeColor(out string) videoColor {
	var c videoColor
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || v == "unknown" {
			continue
		}
		switch k {
		case "color_transfer":
			c.Transfer = v
		case "color_primaries":
			c.Primaries = v
		case "color_space":
			c.Matrix = v
		}
	}

	return c
}

// hdrToSDR is the filter chain that tone-maps an HDR video (HLG or PQ) to
// standard 8-bit BT.709, or nil for a video that isn't HDR.
func hdrToSDR(c videoColor) []string {
	if c.Transfer != "arib-std-b67" && c.Transfer != "smpte2084" {
		return nil
	}
	primaries, matrix := c.Primaries, c.Matrix
	if primaries == "" {
		primaries = "bt2020"
	}
	if matrix == "" {
		matrix = "bt2020nc"
	}

	return []string{
		// Every property of the first step spelled out, input and output:
		// zscale refuses ("no path between colorspaces") whatever it has
		// to guess, and phones don't always tag all of them.
		fmt.Sprintf("zscale=tin=%s:pin=%s:min=%s:rin=tv:t=linear:npl=100:p=%s:m=gbr", c.Transfer, primaries, matrix, primaries),
		"format=gbrpf32le",
		"zscale=p=bt709",
		"tonemap=tonemap=hable:desat=0",
		"zscale=t=bt709:m=bt709:r=tv",
		"format=yuv420p",
	}
}
