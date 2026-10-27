// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/i18n"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
)

// postVideo is a stored video for the post tests, of size bytes as its row
// says, and a Manager whose budget lets 10 MB be served whole
// (MaxPublicationMedia) and streams the video to "ffmpeg" from its blob.
func postVideo(t *testing.T, size int64) (*Manager, *session.Session, *pb.File, string) {
	t.Helper()
	_, ses := galleryTestEnv(t)
	content := bytes.Repeat([]byte("video "), 1000)
	file := libraryFile(t, ses, "clip.mov", "video/quicktime", content)
	file.Path = "/videos/clip.mov"
	dao.SetFileSize(file, size)
	mg := &Manager{contentBudget: newMemBudget(30 << 20)}
	mg.sizesBackfilled.Store(true)
	mg.SetVideoSource(func(*session.Session, *pb.File) (string, func(), error) { return blobPath(file.Hash), func() {}, nil })
	withDisk(t, 4_000_000_000_000, 1_000_000_000_000) // room, whatever this machine has
	return mg, ses, file, t.TempDir()
}

// withTranscode replaces the re-encode for the rest of the test.
func withTranscode(t *testing.T, run func(in, out string, trim *TrimRange, downscale bool, plan transcodePlan) error) {
	t.Helper()
	prev := transcodeRun
	transcodeRun = run
	t.Cleanup(func() { transcodeRun = prev })
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// A large video whose downscale fails is never published as its original
// - every friend's sync read that whole, and a multi-GB one took devices
// down: the post fails, saying why, and nothing is left in the posts'
// folder.
func TestExportVideoForPostNeverPublishesALargeOriginal(t *testing.T) {
	mg, ses, file, dir := postVideo(t, 11<<20)
	withTranscode(t, func(string, string, *TrimRange, bool, transcodePlan) error { return errors.New("ffmpeg: no") })
	_, _, err := mg.ExportVideoForPost(ses, file, nil, true, dir)
	if err == nil || !strings.Contains(err.Error(), "could not be made smaller") {
		t.Fatalf("got %v, want the post refused", err)
	}
	if want := i18n.DevFilesVideoNotReduced(11 << 20).Render("en"); err.Error() != want {
		t.Errorf("English %q, want %q", err.Error(), want)
	}
	if names := dirNames(t, dir); len(names) != 0 {
		t.Fatalf("left in the posts' folder: %v", names)
	}
}

// A small one is published as it is when its downscale fails, as before.
func TestExportVideoForPostPublishesASmallOriginal(t *testing.T) {
	mg, ses, file, dir := postVideo(t, 9<<20)
	withTranscode(t, func(string, string, *TrimRange, bool, transcodePlan) error { return errors.New("ffmpeg: no") })
	out, transient, err := mg.ExportVideoForPost(ses, file, nil, true, dir)
	if err != nil || transient || out.Hash != file.Hash {
		t.Fatalf("got %+v, %v, %v; want the original", out, transient, err)
	}
	body, err := os.ReadFile(filepath.Join(dir, file.Hash))
	if err != nil || !bytes.Equal(body, bytes.Repeat([]byte("video "), 1000)) {
		t.Fatalf("the original's copy: %v", err)
	}
}

// A cut that fails never publishes the whole video: that would share what
// the owner cut out.
func TestExportVideoForPostNeverUndoesACut(t *testing.T) {
	mg, ses, file, dir := postVideo(t, 2<<20)
	withTranscode(t, func(string, string, *TrimRange, bool, transcodePlan) error { return errors.New("ffmpeg: no") })
	_, _, err := mg.ExportVideoForPost(ses, file, &TrimRange{Start: 2, End: 4}, false, dir)
	if err == nil || err.Error() != i18n.DevFilesVideoNotCut().Render("en") {
		t.Fatalf("got %v, want the post refused", err)
	}
	if names := dirNames(t, dir); len(names) != 0 {
		t.Fatalf("left in the posts' folder: %v", names)
	}
}

// A re-encode that runs out of time is tried once more, faster, with the
// same time; what it writes is the post's copy, named by its hash.
func TestExportVideoForPostTriesAgainFasterAfterATimeout(t *testing.T) {
	mg, ses, file, dir := postVideo(t, 900<<20)
	var plans []transcodePlan
	withTranscode(t, func(in, out string, trim *TrimRange, downscale bool, plan transcodePlan) error {
		plans = append(plans, plan)
		if !plan.fast {
			return errTranscodeTimedOut
		}
		return os.WriteFile(out, []byte("the faster copy"), 0o600)
	})
	out, transient, err := mg.ExportVideoForPost(ses, file, nil, true, dir)
	if err != nil || !transient {
		t.Fatalf("got %+v, %v, %v", out, transient, err)
	}
	if len(plans) != 2 || plans[0].fast || !plans[1].fast || plans[0].timeout != plans[1].timeout || plans[0].timeout < cTranscodeTimeout {
		t.Fatalf("plans %+v", plans)
	}
	sum := sha256.Sum256([]byte("the faster copy"))
	if out.Hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("hash %s", out.Hash)
	}
	if names := dirNames(t, dir); len(names) != 1 || names[0] != out.Hash {
		t.Fatalf("the posts' folder holds %v, want only the copy", names)
	}
}

// Refused before ffmpeg starts when the disk can't take the copy.
func TestExportVideoForPostRefusedWhenTheDiskIsFull(t *testing.T) {
	mg, ses, file, dir := postVideo(t, 900<<20)
	ran := false
	withTranscode(t, func(string, string, *TrimRange, bool, transcodePlan) error { ran = true; return nil })
	withDisk(t, 4_000_000_000_000, 1<<30)
	if _, _, err := mg.ExportVideoForPost(ses, file, nil, true, dir); !IsStorageFull(err) {
		t.Fatalf("got %v, want storage_full", err)
	}
	if ran {
		t.Fatal("the re-encode ran")
	}
}

// A post's video gets as long as it needs: half an hour at least, six
// seconds a second of it.
func TestTranscodeTimeoutGrowsWithTheVideo(t *testing.T) {
	for _, c := range []struct {
		secs float64
		want time.Duration
	}{
		{0, cTranscodeTimeout},
		{60, cTranscodeTimeout},
		{600, time.Hour},
		{3600, 6 * time.Hour},
	} {
		if got := transcodeTimeoutFor(c.secs); got != c.want {
			t.Errorf("%v s of video: %v, want %v", c.secs, got, c.want)
		}
	}
	if got := outputSeconds(600, &TrimRange{Start: 10, End: 40}); got != 30 {
		t.Errorf("a cut's length: %v", got)
	}
	if got := outputSeconds(600, &TrimRange{Start: 100}); got != 500 {
		t.Errorf("an open-ended cut's length: %v", got)
	}
	if got := outputSeconds(600, nil); got != 600 {
		t.Errorf("the whole video: %v", got)
	}
}

// What a re-encode may write: its bitrates' worth (and half again), never
// more than twice the source.
func TestTranscodeEstimate(t *testing.T) {
	if got := transcodeEstimate(600, 4<<30, true); got < 190_000_000 || got > 300_000_000 {
		t.Errorf("10 minutes downscaled: %d", got)
	}
	if got := transcodeEstimate(600, 50<<20, true); got != 100<<20 {
		t.Errorf("capped by the source: %d", got)
	}
	if got := transcodeEstimate(30, 8<<20, false); got != 16<<20 {
		t.Errorf("a cut at the source's resolution: %d", got)
	}
}

// ffmpeg running past its time is a timeout the caller can tell apart
// (and try faster), not a failed conversion.
func TestTranscodeFileWithTimesOut(t *testing.T) {
	requireFFmpeg(t)
	src := filepath.Join(t.TempDir(), "in.mp4")
	if err := os.WriteFile(src, makeTestVideoSized(t, 20, 1920, 1080), 0o600); err != nil {
		t.Fatal(err)
	}
	err := transcodeFileWith(src, filepath.Join(t.TempDir(), "out.mp4"), nil, true, transcodePlan{timeout: 50 * time.Millisecond})
	if !errors.Is(err, errTranscodeTimedOut) {
		t.Fatalf("got %v, want a timeout", err)
	}
}
