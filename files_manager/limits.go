// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"os/exec"
	"runtime/debug"
	"time"

	"github.com/alonsovidales/otc/log"
)

// Issue #165: what one file may cost the device.
const (
	// cMaxImagePixels: an image is only decoded when its header says it's
	// at most this big. A 40,000 x 40,000 PNG can be a few kilobytes and
	// decode to 6.4 GB; 120 MP (~480 MB decoded) is above any phone or
	// camera photo and most panoramas.
	cMaxImagePixels = 120_000_000

	// Timeouts for ffprobe and ffmpeg: a call that hangs held its upload
	// processing slot forever.
	cProbeTimeout     = time.Minute
	cFrameTimeout     = 2 * time.Minute
	cTranscodeTimeout = 30 * time.Minute
)

// checkImageSize refuses an image whose header says it's too large to
// decode. A header Go can't read is let through: the decoder then fails on
// its own, or ffmpeg is tried (and its output is checked here too).
func checkImageSize(b []byte) error {
	c, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil
	}
	if int64(c.Width)*int64(c.Height) > cMaxImagePixels {
		return fmt.Errorf("the image is %dx%d, too large to process (over %d megapixels)", c.Width, c.Height, cMaxImagePixels/1_000_000)
	}
	return nil
}

// decodeImage is image.Decode behind checkImageSize.
func decodeImage(b []byte) (image.Image, error) {
	if err := checkImageSize(b); err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	return img, err
}

// command is exec.CommandContext with a deadline; the returned cancel must
// be called once the command is done.
func command(timeout time.Duration, name string, args ...string) (*exec.Cmd, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	return exec.CommandContext(ctx, name, args...), cancel
}

// safely runs background work on a file so that a panic in it (a decoder
// bug on a malformed file, say) is logged and raised to the owner instead
// of taking the whole service down - and, on restart, doing it again.
func (mg *Manager) safely(what, path string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("recovered from a panic while", what, path, ":", r, string(debug.Stack()))
			// Telling the owner must not be what brings the process down.
			defer func() {
				if r2 := recover(); r2 != nil {
					log.Error("could not raise the alert either:", r2)
				}
			}()
			mg.alert("could not be processed (it crashed the processing)", path, fmt.Errorf("%v", r))
		}
	}()
	fn()
}
