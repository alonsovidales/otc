// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"

	"github.com/alonsovidales/otc/log"
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
	cSocialVideoBitrate      = "1500k"
	cSocialVideoAudioBitrate = "128k"
)

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

	// scale='if(gt(iw,W),W,iw)':-2 only downscales a video wider than
	// cSocialVideoMaxWidth - a source already narrower keeps its own
	// size, never gets upscaled. -2 keeps the computed height even
	// (required by libx264) while preserving aspect ratio.
	scaleFilter := fmt.Sprintf("scale='if(gt(iw,%d),%d,iw)':-2", cSocialVideoMaxWidth, cSocialVideoMaxWidth)
	cmd := exec.Command(
		"ffmpeg", "-y", "-i", inPath,
		"-vf", scaleFilter,
		"-c:v", "libx264", "-preset", "veryfast", "-b:v", cSocialVideoBitrate,
		"-c:a", "aac", "-b:a", cSocialVideoAudioBitrate,
		"-movflags", "+faststart",
		outPath,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg transcode: %w: %s", err, stderr.String())
	}

	out, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("reading transcoded output: %w", err)
	}
	log.Debug("Compressed video for social:", len(content), "->", len(out), "bytes")
	return out, nil
}
