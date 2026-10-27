// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"github.com/gabriel-vasile/mimetype"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ExportVideoForPost writes the copy of a stored video a post distributes
// into dir, named by its hash, and returns that copy's File (no Content)
// and whether it is a new, transient file rather than the original.
//
// Issue #166: a post's video used to be read and decrypted whole, then
// written to a temporary file for ffmpeg, then read back whole - several
// copies of a video of any size in memory. Now, with a trim or a
// downscale, ffmpeg reads the stored video over the loopback stream and
// writes into dir; otherwise the original is decrypted into dir a segment
// at a time.
//
// A re-encode has as long as the video needs (transcodeTimeoutFor) and,
// when it runs out of time, a second, faster try (transcodePlan.fast). If
// that fails too, the original is published in its place only when no
// cut was asked for (publishing it would show what the owner cut out) and
// it is small enough for friends to fetch (MaxPublicationMedia): a
// multi-GB original sent to every friend's sync was what ran devices out
// of memory. Otherwise the post fails, saying why (errVideoNotReencoded).
func (mg *Manager) ExportVideoForPost(ses *session.Session, file *pb.File, trim *TrimRange, downscale bool, dir string) (*pb.File, bool, error) {
	if trim != nil || downscale {
		out, err := mg.transcodeStored(ses, file, trim, downscale, dir)
		if err == nil {
			return out, true, nil
		}
		if IsStorageFull(err) {
			return nil, false, err
		}
		if size := mg.budgetSize(file); trim != nil || size > mg.MaxPublicationMedia() {
			log.Error("could not re-encode a video for a post, and its original is not published in its place:", err)
			return nil, false, errVideoNotReencoded(trim != nil, size)
		}
		log.Error("error re-encoding a video for a post, publishing the original instead:", err)
	}
	if err := checkFreeSpace(dir, mg.budgetSize(file)); err != nil {
		return nil, false, err
	}
	if err := copyPlaintext(ses, file.Hash, filepath.Join(dir, file.Hash)); err != nil {
		return nil, false, err
	}
	return file, false, nil
}

// errVideoNotReencoded is the post's failure when its video couldn't be
// re-encoded and the original isn't published instead: cut says a cut was
// asked for, size is the original's. ffmpeg's own words are only logged.
func errVideoNotReencoded(cut bool, size int64) error {
	if cut {
		return devtext.New(i18n.DevFilesVideoNotCut())
	}
	return devtext.New(i18n.DevFilesVideoNotReduced(size))
}

// transcodeTimeoutFor is how long one ffmpeg run of a post's video may
// take for outSecs seconds of output (0: not known): cTranscodeTimeout at
// least, and cTranscodeSecsPerSec for each second of video beyond that.
// A Raspberry Pi decodes a 4K HEVC recording in software at well under
// real time, so a fixed half hour cut off every long one.
func transcodeTimeoutFor(outSecs float64) time.Duration {
	if outSecs <= 0 {
		return cTranscodeTimeout
	}
	return max(cTranscodeTimeout, time.Duration(outSecs*cTranscodeSecsPerSec*float64(time.Second)))
}

// cTranscodeSecsPerSec is the time a second of a post's video may take to
// re-encode.
const cTranscodeSecsPerSec = 6

// outputSeconds is how long the re-encoded video runs: the cut, or the
// whole video after its start (duration is the video's own, 0 unknown).
func outputSeconds(duration float64, trim *TrimRange) float64 {
	if trim == nil {
		return duration
	}
	if d := trim.Duration(); d > 0 {
		return d
	}
	if duration > trim.Start {
		return duration - trim.Start
	}
	return duration
}

// transcodeEstimate is at most what a re-encode of outSecs seconds writes
// (the bitrates it aims at, and half again: a target, not a cap) - never
// more than twice the source's size, the most a cut at the source's
// resolution comes to.
func transcodeEstimate(outSecs float64, sourceSize int64, downscale bool) int64 {
	if !downscale || outSecs <= 0 {
		return 2 * sourceSize
	}
	const bitsPerSec = 2_500_000 + 128_000 // cSocialVideoBitrate + cSocialVideoAudioBitrate
	return min(2*sourceSize, int64(outSecs*bitsPerSec/8*1.5)+(1<<20))
}

// transcodeRun is transcodeFileWith; a variable so tests can fail or slow
// down the re-encode.
var transcodeRun = transcodeFileWith

func (mg *Manager) transcodeStored(ses *session.Session, file *pb.File, trim *TrimRange, downscale bool, dir string) (*pb.File, error) {
	// The slot first: the stream's token must not age while this waits.
	transcodeSlots <- struct{}{}
	defer func() { <-transcodeSlots }()
	// And the processing guard's turn on a low-memory device (lowmem.go).
	defer mg.beginProcessing("", jobTranscode)()
	src, done, err := mg.videoSource(ses, file)
	if err != nil {
		return nil, err
	}
	defer done()
	duration, probeErr := probeVideoDuration(src)
	if probeErr != nil {
		log.Info("could not read a post video's length, re-encoding with the shortest time limit:", probeErr)
	}
	outSecs := outputSeconds(duration, trim)
	if err := checkFreeSpace(dir, transcodeEstimate(outSecs, mg.budgetSize(file), downscale)); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dir, ".post-*.mp4")
	if err != nil {
		return nil, err
	}
	tmp.Close()
	outPath := tmp.Name()
	defer os.Remove(outPath) // a no-op once renamed
	plan := transcodePlan{timeout: transcodeTimeoutFor(outSecs)}
	err = transcodeRun(src, outPath, trim, downscale, plan)
	if errors.Is(err, errTranscodeTimedOut) {
		log.Info("a post video took longer than", plan.timeout, "to re-encode; trying once more, faster")
		plan.fast = true
		err = transcodeRun(src, outPath, trim, downscale, plan)
	}
	if err != nil {
		return nil, err
	}
	out, err := fileMeta(outPath)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(outPath, 0o600); err != nil {
		return nil, err
	}
	// ffmpeg wrote it and exited: on the disk before it takes its name, so
	// a power cut can't leave a post with an empty video - kept for good
	// by every friend that fetched it.
	if err := syncFile(outPath); err != nil {
		return nil, err
	}
	if err := os.Rename(outPath, filepath.Join(dir, out.Hash)); err != nil {
		return nil, err
	}
	blobstore.SyncDir(dir)
	return out, nil
}

// syncFile flushes the file at path to the disk.
func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// fileMeta is BuildTransientFile's metadata for a file on disk, read as a
// stream.
func fileMeta(path string) (*pb.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return nil, err
	}
	mt, err := mimetype.DetectFile(path)
	if err != nil {
		return nil, err
	}
	now := timestamppb.Now()
	file := &pb.File{
		Created:  now,
		Modified: now,
		Mime:     mt.String(),
		Hash:     hex.EncodeToString(h.Sum(nil)),
	}
	dao.SetFileSize(file, n)
	return file, nil
}

// CopyForPost decrypts the stored content of file into dir under its hash,
// a segment at a time - a post's copy of a photo that needs no converting
// (a HEIC does: GetFile) - refused when the disk can't take it.
func (mg *Manager) CopyForPost(ses *session.Session, file *pb.File, dir string) error {
	if err := checkFreeSpace(dir, mg.budgetSize(file)); err != nil {
		return err
	}
	return copyPlaintext(ses, file.Hash, filepath.Join(dir, file.Hash))
}

// copyPlaintext decrypts the blob of hash into dst (0600), a segment at a
// time, through a temporary file synced and renamed into place.
func copyPlaintext(ses *session.Session, hash, dst string) error {
	blob, err := blobstore.Open(blobPath(hash), ses)
	if err != nil {
		return devtext.Wrap(i18n.DevFilesPostContentUnavailable(hash, err.Error()), err)
	}
	defer blob.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".post-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, io.NewSectionReader(blob, 0, blob.Size())); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	// Whole on the disk before it takes the name, as transcodeStored's.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return err
	}
	blobstore.SyncDir(filepath.Dir(dst))
	return nil
}
