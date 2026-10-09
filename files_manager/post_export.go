// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/dao"
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
// at a time. A failed re-encode publishes the original, as before.
func (mg *Manager) ExportVideoForPost(ses *session.Session, file *pb.File, trim *TrimRange, downscale bool, dir string) (*pb.File, bool, error) {
	if trim != nil || downscale {
		out, err := mg.transcodeStored(ses, file, trim, downscale, dir)
		if err == nil {
			return out, true, nil
		}
		log.Error("error re-encoding a video for a post, publishing the original instead:", err)
	}
	if err := copyPlaintext(ses, file.Hash, filepath.Join(dir, file.Hash)); err != nil {
		return nil, false, err
	}
	return file, false, nil
}

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
	tmp, err := os.CreateTemp(dir, ".post-*.mp4")
	if err != nil {
		return nil, err
	}
	tmp.Close()
	outPath := tmp.Name()
	defer os.Remove(outPath) // a no-op once renamed
	if err := transcodeFile(src, outPath, trim, downscale); err != nil {
		return nil, err
	}
	out, err := fileMeta(outPath)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(outPath, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(outPath, filepath.Join(dir, out.Hash)); err != nil {
		return nil, err
	}
	return out, nil
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

// copyPlaintext decrypts the blob of hash into dst (0600), a segment at a
// time, through a temporary file renamed into place.
func copyPlaintext(ses *session.Session, hash, dst string) error {
	blob, err := blobstore.Open(blobPath(hash), ses)
	if err != nil {
		return fmt.Errorf("the content of %s is missing or unreadable: %w", hash, err)
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
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}
