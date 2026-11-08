// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// testClip is an MP4 of seconds seconds, a keyframe every second, with a
// creation time and a place in its tags, as a phone's: skipped without
// ffmpeg.
func testClip(t *testing.T, seconds int) []byte {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	out := filepath.Join(t.TempDir(), "clip.mp4")
	n := strconv.Itoa(seconds)
	cmd := exec.Command("ffmpeg", "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=duration="+n+":size=160x120:rate=10",
		"-f", "lavfi", "-i", "sine=frequency=440:duration="+n,
		"-c:v", "libx264", "-g", "10", "-keyint_min", "10", "-sc_threshold", "0",
		"-c:a", "aac", "-shortest",
		"-metadata", "creation_time=2021-05-06T07:08:09Z",
		"-metadata", "location=+40.4168-003.7038/",
		"-movflags", "+faststart", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v: %s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// plainVideoSource stands in for the loopback stream: the stored video
// decrypted into a file ffmpeg reads.
func plainVideoSource(t *testing.T, mg *Manager) {
	mg.SetVideoSource(func(ses *session.Session, file *pb.File) (string, func(), error) {
		dst := filepath.Join(t.TempDir(), file.Hash)
		if err := copyPlaintext(ses, file.Hash, dst); err != nil {
			return "", nil, err
		}
		return dst, func() { os.Remove(dst) }, nil
	})
}

// probeStored is ffprobe's word on the stored content of hash: its length
// and its format's tags.
func probeStored(t *testing.T, ses *session.Session, hash string) (float64, map[string]string) {
	t.Helper()
	dst := filepath.Join(t.TempDir(), hash)
	if err := copyPlaintext(ses, hash, dst); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration:format_tags", "-of", "json", dst).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Format struct {
			Duration string            `json:"duration"`
			Tags     map[string]string `json:"tags"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	d, _ := strconv.ParseFloat(got.Format.Duration, 64)
	return d, got.Format.Tags
}

// A video cut in place keeps its name, date and tags, gets a hash of its
// own, keeps the original for Revert, and starts at the keyframe at or
// before the start asked; the copy goes next to it; a cut too short, of
// the whole video, of content changed meanwhile or of a file that can't
// be cut changes nothing; Revert puts the original back.
func TestCutAndRevertVideoMySQL(t *testing.T) {
	mg, ses, db := editTestDB(t)
	clip := testClip(t, 6)
	plainVideoSource(t, mg)
	orig := sha256Hex(clip)
	taken := timestamppb.New(time.Date(2021, 5, 6, 7, 8, 9, 0, time.UTC))
	if _, err := mg.UploadFile(ses, "/phone/VID_1.mp4", clip, false, taken, nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := mg.LinkFile(ses, "/backup/VID_1.mp4", orig, false, taken, nil, ""); err != nil {
		t.Fatal(err)
	}
	file, err := mg.dao.GetFileByPath("/phone/VID_1.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if cutFormatOf(file) != "mp4" {
		t.Fatalf("stored as %q", file.Mime)
	}

	// Too short, past the end, and the whole video: nothing changes.
	if _, err := mg.EditVideo(ses, &pb.EditVideo{Path: "/phone/VID_1.mp4", Hash: orig, StartSecs: 2, EndSecs: 2.3}, "en"); !IsCutTooShort(err) {
		t.Errorf("a cut of 0.3 s: %v", err)
	}
	if _, err := mg.EditVideo(ses, &pb.EditVideo{Path: "/phone/VID_1.mp4", Hash: orig, StartSecs: 9}, "en"); !IsCutTooShort(err) {
		t.Errorf("a cut past the end: %v", err)
	}
	same, err := mg.EditVideo(ses, &pb.EditVideo{Path: "/phone/VID_1.mp4", Hash: orig, EndSecs: 5.98}, "en")
	if err != nil || same.Hash != orig || same.OriginalHash != "" {
		t.Fatalf("a cut of the whole video: %v %v", same, err)
	}
	// Content other than the one shown.
	var changed *ContentChangedError
	if _, err := mg.EditVideo(ses, &pb.EditVideo{Path: "/phone/VID_1.mp4", Hash: "beef", StartSecs: 1}, "en"); !errors.As(err, &changed) {
		t.Errorf("a cut of content changed meanwhile: %v", err)
	}

	// Only this copy, from 2.5 s to 4.5 s: an MP4 keeps the frames from the
	// keyframe at 2 s, hidden by an edit list, so it shows 2 s.
	edited, err := mg.EditVideo(ses, &pb.EditVideo{Path: "/phone/VID_1.mp4", Hash: orig, StartSecs: 2.5, EndSecs: 4.5}, "en")
	if err != nil {
		t.Fatal(err)
	}
	if edited.Path != "/phone/VID_1.mp4" || edited.Hash == orig || edited.OriginalHash != orig || edited.Mime != file.Mime {
		t.Fatalf("edited %s %s %s (original %s)", edited.Path, edited.Hash, edited.Mime, edited.OriginalHash)
	}
	if rowHash(t, db, "/backup/VID_1.mp4") != orig {
		t.Error("the other copy was cut too")
	}
	if !mg.hasBlob(edited.Hash) || !mg.hasBlob(orig) {
		t.Fatal("the cut or the original isn't on the disk")
	}
	if got, _ := mg.dao.GetFileByPath("/phone/VID_1.mp4"); !got.Created.AsTime().Equal(taken.AsTime()) {
		t.Errorf("the cut's date is %v", got.Created.AsTime())
	}
	d, tags := probeStored(t, ses, edited.Hash)
	if d < 1.9 || d > 2.4 {
		t.Errorf("the cut runs %.2f s; want about 2 (2.5 s to 4.5 s)", d)
	}
	if tags["creation_time"] == "" || tags["location"] != "+40.4168-003.7038/" {
		t.Errorf("the cut's tags: %v", tags)
	}
	state, err := mg.EditState("/phone/VID_1.mp4")
	if err != nil || state.Copies != 1 || !state.Edited {
		t.Fatalf("EditState after: %v %v", state, err)
	}
	// No plain copy is left behind.
	if left, _ := filepath.Glob(filepath.Join(galleryPosts, ".cut-*")); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}

	// As a copy, next to the other one: "<name> (edited).mp4", its date.
	cp, err := mg.EditVideo(ses, &pb.EditVideo{Path: "/backup/VID_1.mp4", Hash: orig, StartSecs: 1, EndSecs: 3, AsCopy: true}, "en")
	if err != nil {
		t.Fatal(err)
	}
	if cp.Path != "/backup/VID_1 (edited).mp4" || cp.OriginalHash != orig {
		t.Fatalf("the copy is %s (original %s)", cp.Path, cp.OriginalHash)
	}
	if rowHash(t, db, "/backup/VID_1.mp4") != orig {
		t.Error("the copy replaced the video")
	}

	// Revert: the original back where the cut was, the cut gone.
	back, err := mg.RevertImage(ses, "/phone/VID_1.mp4", edited.Hash, false)
	if err != nil {
		t.Fatal(err)
	}
	if back.Path != "/phone/VID_1.mp4" || back.Hash != orig {
		t.Fatalf("reverted to %s %s", back.Path, back.Hash)
	}
	if mg.hasBlob(edited.Hash) {
		t.Error("the cut is still on the disk")
	}
	if _, err := mg.RevertImage(ses, "/phone/VID_1.mp4", orig, false); !IsNotEdited(err) || err.Error() != "this video has no edit to undo" {
		t.Errorf("a revert of a video with no cut: %v", err)
	}

	// A photo can't be cut.
	photo := cameraJPEG(t, 40, 20, 1)
	if _, err := mg.UploadFile(ses, "/phone/IMG_1.JPG", photo, false, taken, nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := mg.EditVideo(ses, &pb.EditVideo{Path: "/phone/IMG_1.JPG", StartSecs: 1}, "en"); !IsNotEditable(err) {
		t.Errorf("a cut of a photo: %v", err)
	}
}
