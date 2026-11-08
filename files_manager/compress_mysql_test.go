// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fourKClip is a phone's 4K recording: HEVC at 10 bits in HLG, 40 Mbps,
// seconds long, held upright (its picture stored 3840x2160, turned 90
// degrees), with its date and place in its tags - skipped without an
// ffmpeg that has libx265.
func fourKClip(t *testing.T, seconds int) []byte {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil || !hasX265() {
		t.Skip("no ffmpeg with libx265")
	}
	dir := t.TempDir()
	n := strconv.Itoa(seconds)
	run := func(args ...string) {
		if b, err := exec.Command("ffmpeg", append([]string{"-v", "error", "-y"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v: %s", err, b)
		}
	}
	run("-f", "lavfi", "-i", "testsrc2=duration="+n+":size=3840x2160:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:duration="+n,
		"-vf", "format=yuv420p10le,setparams=color_primaries=bt2020:color_trc=arib-std-b67:colorspace=bt2020nc:range=tv",
		"-c:v", "libx265", "-preset", "ultrafast", "-b:v", "40M", "-x265-params", "log-level=error", "-tag:v", "hvc1",
		"-c:a", "aac", "-b:a", "128k", "-shortest",
		"-metadata", "creation_time=2021-05-06T07:08:09Z", "-metadata", "location=+40.4168-003.7038/",
		"-movflags", "+faststart", filepath.Join(dir, "flat.mov"))
	run("-display_rotation:v:0", "90", "-i", filepath.Join(dir, "flat.mov"), "-c", "copy", "-map_metadata", "0",
		"-movflags", "+faststart+use_metadata_tags", filepath.Join(dir, "clip.mov"))
	b, err := os.ReadFile(filepath.Join(dir, "clip.mov"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// probeVideoStored is ffprobe's word on the stored video of hash: its
// picture's size, codec, pixel format, colours and rotation, and its tags.
func probeVideoStored(t *testing.T, ses *session.Session, hash string) (map[string]any, map[string]string) {
	t.Helper()
	dst := filepath.Join(t.TempDir(), hash)
	if err := copyPlaintext(ses, hash, dst); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_streams", "-show_entries", "format_tags", "-of", "json", dst).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Streams []map[string]any `json:"streams"`
		Format  struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &got); err != nil || len(got.Streams) != 1 {
		t.Fatalf("%s: %v", out, err)
	}
	return got.Streams[0], got.Format.Tags
}

// compressTestDB is editTestDB with image tagging off - the test's
// Manager has no model, which an analysis would wait for - and the
// processing lanes left to run.
func compressTestDB(t *testing.T) (*Manager, *session.Session, *sql.DB) {
	t.Helper()
	mg, ses, db := editTestDB(t)
	if _, err := db.Exec("insert into `settings` (`device_uuid`, `subdomain`, `bridge_secret`, `image_tagging_enabled`) values ('t', 't', 't', 0)"); err != nil {
		t.Fatal(err)
	}
	return mg, ses, db
}

// waitUpTo polls cond every 20 ms for up to d.
func waitUpTo(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(d); !cond(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("waited %v for %s", d, what)
		}
	}
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// The whole of Compress against MariaDB and a real ffmpeg: the options for
// a 4K video, a 48 MP photo and what can't be compressed; the job - its
// progress, every copy replaced, the originals' bytes and thumbnails gone,
// the edits' rows saying so, faces scaled, collections and Favourites
// moved, a result no smaller skipped, refusals failed - and the alerts;
// then what the device answers for an original that is gone: held to
// HasFile, never to HasHashes, an upload or a link of it answered with
// the compressed file and never stored, no revert, an edit keeping the
// mark.
func TestCompressMySQL(t *testing.T) {
	mg, ses, db := compressTestDB(t)
	clip := fourKClip(t, 3)
	plainVideoSource(t, mg)
	taken := timestamppb.New(time.Date(2021, 5, 6, 7, 8, 9, 0, time.UTC))
	upload := func(p string, b []byte) string {
		t.Helper()
		f, err := mg.UploadFile(ses, p, b, false, taken, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		return f.Hash
	}
	link := func(p, h string) {
		t.Helper()
		if _, err := mg.LinkFile(ses, p, h, false, taken, nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	video := upload("/phone/VID_1.MOV", clip)
	link("/backup/VID_1.MOV", video)
	big := noisyJPEG(t, 8000, 6000, 4, 92, 1)
	photo := upload("/phone/IMG_1.JPG", big)
	link("/mac/IMG_1.JPG", photo)
	heavy := upload("/phone/IMG_2.JPG", noisyJPEG(t, 1200, 900, 64, 82, 1))
	pdf := upload("/docs/a.pdf", append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("x"), 4096)...))
	locked := upload("/phone/IMG_3.JPG", noisyJPEG(t, 1200, 900, 100, 98, 1))
	link("/locked/IMG_3.JPG", locked)
	if err := mg.SetUploadOnly("/locked/", true); err != nil {
		t.Fatal(err)
	}
	if err := mg.SetFavourite([]string{"/phone/IMG_1.JPG"}, true); err != nil {
		t.Fatal(err)
	}
	trip, err := mg.dao.CreateImageGroup("Trip")
	if err != nil {
		t.Fatal(err)
	}
	if err := mg.dao.AddPathsToImageGroup(trip, []string{"/phone/VID_1.MOV"}); err != nil {
		t.Fatal(err)
	}
	if err := mg.dao.CreatePerson("person-1"); err != nil {
		t.Fatal(err)
	}
	if err := mg.dao.AddFace("face-1", photo, "person-1", 1000, 2000, 400, 600, []byte("emb"), []byte("thumb"), 0.9); err != nil {
		t.Fatal(err)
	}
	// The originals processed, as on a device: their thumbnails are what
	// must go with them.
	waitUpTo(t, time.Minute, "the thumbnails", func() bool {
		return exists(blobPath(photo)+cThumbnailSuffix) && exists(blobPath(video)+cThumbnailSuffix)
	})

	// Largest files: each content once, at its first path, with its copies.
	page, token, err := mg.ListLargestFiles("", 2)
	if err != nil || len(page) != 2 || token == "" {
		t.Fatalf("largest: %v %q %v", page, token, err)
	}
	if page[0].Hash != video || page[0].Path != "/backup/VID_1.MOV" || page[0].GetCopies() != 2 || page[1].Hash != photo || page[1].Path != "/mac/IMG_1.JPG" {
		t.Errorf("largest first page: %s %s %d, %s %s", page[0].Hash[:8], page[0].Path, page[0].GetCopies(), page[1].Hash[:8], page[1].Path)
	}
	rest, token, err := mg.ListLargestFiles(token, 50)
	if err != nil || len(rest) != 3 || token != "" {
		t.Errorf("largest second page: %d files, token %q, %v", len(rest), token, err)
	}

	// The options.
	other := sha256Hex([]byte("not what is there"))
	opts, err := mg.CompressOptions(ses, []*pb.CompressItem{
		{Path: "/phone/VID_1.MOV", Hash: video}, {Path: "/phone/IMG_1.JPG", Hash: photo}, {Path: "/phone/IMG_2.JPG", Hash: heavy},
		{Path: "/docs/a.pdf", Hash: pdf}, {Path: "/phone/IMG_3.JPG", Hash: locked}, {Path: "/mac/IMG_1.JPG", Hash: other}, {Path: "/gone.jpg", Hash: photo},
	}, "en")
	if err != nil {
		t.Fatal(err)
	}
	o := opts.Items
	if c := choiceText(o[0].Choices); c != "video_720p,video_1080p" || o[0].Copies != 2 || o[0].Size != int64(len(clip)) {
		t.Errorf("the video: %s, %d copies, %d bytes (%s)", c, o[0].Copies, o[0].Size, o[0].Reason)
	}
	if c := choiceText(o[1].Choices); c != "photo_12mp" || o[1].Choices[0].EstimatedSize != 3_840_000 || o[1].Copies != 2 {
		t.Errorf("the photo: %s (%s)", c, o[1].Reason)
	}
	if c := choiceText(o[2].Choices); c != "jpeg_resave" {
		t.Errorf("the heavy JPEG: %s (%s)", c, o[2].Reason)
	}
	for i, code := range map[int]string{3: cReasonNotMedia, 4: cReasonUploadOnly, 5: cReasonChanged, 6: cReasonChanged} {
		if o[i].ReasonCode != code || len(o[i].Choices) != 0 || o[i].Reason == "" {
			t.Errorf("%s: %q %q %v, want %s", o[i].Path, o[i].ReasonCode, o[i].Reason, o[i].Choices, code)
		}
	}
	if o[4].Reason != "this file is in an upload-only folder, where nothing is replaced; turn upload only off there to compress it" {
		t.Errorf("upload only reads %q", o[4].Reason)
	}
	if es, _ := mg.CompressOptions(ses, []*pb.CompressItem{{Path: "/docs/a.pdf", Hash: pdf}}, "es"); es.Items[0].Reason == o[3].Reason {
		t.Error("the reason isn't in the request's language")
	}
	tooMany := make([]*pb.CompressItem, cMaxCompressItems+1)
	if _, err := mg.CompressOptions(ses, tooMany, "en"); !IsCompressTooMany(err) {
		t.Errorf("501 items: %v", err)
	}
	if _, err := mg.CompressJob("", "en"); !IsCompressNoJob(err) {
		t.Errorf("a job before any: %v", err)
	}

	// The job: the video, the photo, the heavy JPEG (no smaller), the
	// upload-only copy and content changed meanwhile (refused).
	before := time.Now()
	job, err := mg.CompressFiles(ses, []*pb.CompressTask{
		{Path: "/phone/VID_1.MOV", Hash: video, Preset: cPresetVideo1080},
		{Path: "/phone/IMG_1.JPG", Hash: photo, Preset: cPresetPhoto12MP},
		{Path: "/phone/VID_1.MOV", Hash: video, Preset: cPresetVideo720}, // asked twice: once is enough
	}, "en")
	if err != nil || job.Total != 2 || job.Finished {
		t.Fatalf("the job: %v %v", job, err)
	}
	// Tasks sent while it runs join it.
	job, err = mg.CompressFiles(ses, []*pb.CompressTask{
		{Path: "/phone/IMG_2.JPG", Hash: heavy, Preset: cPresetJPEGResave},
		{Path: "/phone/IMG_3.JPG", Hash: locked, Preset: cPresetJPEGResave},
		{Path: "/mac/IMG_1.JPG", Hash: other, Preset: cPresetPhoto12MP},
	}, "en")
	if err != nil || job.Total != 5 {
		t.Fatalf("joined: %v %v", job, err)
	}
	id := job.Id
	var percents []int32
	background := false
	waitUpTo(t, 3*time.Minute, "the job", func() bool {
		j, err := mg.CompressJob(id, "en")
		if err != nil {
			t.Fatal(err)
		}
		if r := j.Results[0]; r.State == cTaskRunning && r.Percent > 0 && (len(percents) == 0 || percents[len(percents)-1] != r.Percent) {
			percents = append(percents, r.Percent)
		}
		for _, w := range mg.BackgroundWork() {
			background = background || w.Kind == BackgroundCompress
		}
		job = j
		return j.Finished
	})
	t.Logf("the video's progress: %v; the job took %v", percents, time.Since(before).Round(time.Millisecond))
	if len(percents) == 0 || !slices.IsSorted(percents) {
		t.Errorf("the video's progress: %v", percents)
	}
	if !background {
		t.Error("the job wasn't in Background work")
	}
	if job.Done != 5 || job.Id != id {
		t.Fatalf("done %d of %d", job.Done, job.Total)
	}
	states := make([]string, len(job.Results))
	for i, r := range job.Results {
		states[i] = r.State + ":" + r.Message
	}
	want := []string{"done:", "done:", "skipped:compressed, it came out no smaller, so the file was left as it was",
		"failed:this file is in an upload-only folder, where nothing is replaced; turn upload only off there to compress it",
		"failed:this file has changed since it was shown, so nothing was deleted: refresh and try again"}
	if strings.Join(states, "|") != strings.Join(want, "|") {
		t.Errorf("results:\n%s\nwant\n%s", strings.Join(states, "\n"), strings.Join(want, "\n"))
	}
	vid, img := job.Results[0].File, job.Results[1].File
	if vid == nil || img == nil {
		t.Fatal("no files in the results")
	}

	// Every copy replaced, at both paths, with the original named and gone.
	for p, h := range map[string]string{"/phone/VID_1.MOV": vid.Hash, "/backup/VID_1.MOV": vid.Hash, "/phone/IMG_1.JPG": img.Hash, "/mac/IMG_1.JPG": img.Hash} {
		if got := rowHash(t, db, p); got != h {
			t.Errorf("%s holds %s, want %s", p, got, h)
		}
	}
	if vid.OriginalHash != video || !vid.OriginalRemoved || img.OriginalHash != photo || !img.OriginalRemoved || vid.Path != "/phone/VID_1.MOV" || vid.GetCopies() != 2 {
		t.Errorf("the results: %v / %v", vid, img)
	}
	for _, h := range []string{video, photo} {
		if mg.hasBlob(h) || exists(blobPath(h)+cThumbnailSuffix) || exists(blobPath(h)+cSmallThumbnailSuffix) {
			t.Errorf("%s's bytes or thumbnails are still on the disk", h[:8])
		}
		if n := countRows(t, db, "select count(*) from `edited_content` where `original_hash` = ? and `original_removed` = 1", h); n != 1 {
			t.Errorf("%s: %d edit rows saying it is gone", h[:8], n)
		}
	}
	if n := countRows(t, db, "select count(*) from `faces` where `hash` = ?", photo); n != 0 {
		t.Errorf("the original's faces are still there: %d", n)
	}
	var saved int64 = int64(len(clip)) + int64(len(big)) - dao.FileSize(vid) - dao.FileSize(img)
	if job.Saved != saved {
		t.Errorf("saved %d, want %d", job.Saved, saved)
	}
	t.Logf("the 4K clip %d -> %d bytes, the 48 MP photo %d -> %d", len(clip), dao.FileSize(vid), len(big), dao.FileSize(img))
	// The heavy JPEG and the refused ones, as they were.
	if rowHash(t, db, "/phone/IMG_2.JPG") != heavy || !mg.hasBlob(heavy) || rowHash(t, db, "/locked/IMG_3.JPG") != locked {
		t.Error("a file not compressed changed")
	}
	if left, _ := filepath.Glob(filepath.Join(galleryPosts, cCompressTempPrefix+"*")); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}

	// The video: 1080 wide (it is held upright), HEVC at 10 bits in HLG,
	// shown as it was - no rotation left to apply - with its tags.
	st, tags := probeVideoStored(t, ses, vid.Hash)
	if st["codec_name"] != "hevc" || st["width"] != float64(1080) || st["height"] != float64(1920) || st["pix_fmt"] != "yuv420p10le" ||
		st["color_transfer"] != "arib-std-b67" || st["color_primaries"] != "bt2020" || st["codec_tag_string"] != "hvc1" {
		t.Errorf("the compressed video: %v", st)
	}
	if sd, _ := st["side_data_list"].([]any); len(sd) > 0 {
		for _, d := range sd {
			if m, _ := d.(map[string]any); m["rotation"] != nil && m["rotation"] != float64(0) {
				t.Errorf("the compressed video still says it is turned: %v", sd)
			}
		}
	}
	if tags["location"] != "+40.4168-003.7038/" || !strings.HasPrefix(tags["creation_time"], "2021-05-06T07:08:09") {
		t.Errorf("its tags: %v", tags)
	}
	// The photo: 12 MP, its face scaled with it, in its person; the
	// collection and Favourites hold the compressed files.
	var x, y, w, h int
	var person string
	if err := db.QueryRow("select `bbox_x`, `bbox_y`, `bbox_w`, `bbox_h`, `person_id` from `faces` where `hash` = ?", img.Hash).Scan(&x, &y, &w, &h, &person); err != nil {
		t.Fatalf("the compressed photo has no face: %v", err)
	}
	if x != 500 || y != 1000 || w != 200 || h != 300 || person != "person-1" {
		t.Errorf("its face is %d,%d %dx%d of %s", x, y, w, h, person)
	}
	if n := countRows(t, db, "select count(*) from `image_group_files` where `hash` = ?", img.Hash); n != 1 {
		t.Errorf("Favourites hold the compressed photo %d times", n)
	}
	if n := countRows(t, db, "select count(*) from `image_group_files` where `group_id` = ? and `hash` = ?", trip, vid.Hash); n != 1 {
		t.Error("the collection lost the video")
	}
	if n := countRows(t, db, "select count(*) from `image_group_files` where `hash` in (?, ?)", photo, video); n != 0 {
		t.Errorf("%d memberships still name the originals", n)
	}

	// The alerts: what was done, and what failed with a line a file.
	rows, err := db.Query("select `title`, `details`, `msg_key` from `notifications` where `type` = 'Error' and `actor_domain` = '(standalone)' order by `title`")
	if err != nil {
		t.Fatal(err)
	}
	var alerts []string
	for rows.Next() {
		var title, details, key string
		rows.Scan(&title, &details, &key)
		alerts = append(alerts, key+": "+title+" | "+details)
	}
	rows.Close()
	if len(alerts) != 2 || !strings.HasPrefix(alerts[0], "dev.alert.compress.done: 2 files compressed, ") || !strings.HasSuffix(alerts[0], " freed | ") ||
		!strings.HasPrefix(alerts[1], "dev.alert.compress.failed: 2 files couldn't be compressed | ") ||
		!strings.Contains(alerts[1], "IMG_3.JPG could not be processed: this file is in an upload-only folder") ||
		!strings.Contains(alerts[1], "\n") || !strings.Contains(alerts[1], "IMG_1.JPG could not be processed: this file has changed") {
		t.Errorf("alerts:\n%s", strings.Join(alerts, "\n"))
	}
	notifications, _, err := mg.dao.ListNotifications(10)
	if err != nil {
		t.Fatal(err)
	}
	if err := mg.dao.LocalizeNotifications("es", notifications); err != nil {
		t.Fatal(err)
	}
	for _, n := range notifications {
		if strings.Contains(n.Title, "compressed") || strings.Contains(n.Details, "could not be processed") {
			t.Errorf("an alert in Spanish reads %q / %q", n.Title, n.Details)
		}
	}

	// What the device answers for an original that is gone.
	if has, err := mg.HasFile(photo, ""); err != nil || !has {
		t.Errorf("HasFile of the photo's original: %v %v", has, err)
	}
	if found, err := mg.HasHashes([]string{photo, video, heavy}); err != nil || strings.Join(found, ",") != heavy {
		t.Errorf("HasHashes: %v %v", found, err)
	}
	listed, err := mg.ListFiles(ses, "/phone/", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range listed {
		if f.Path == "/phone/IMG_1.JPG" && (!f.OriginalRemoved || f.OriginalHash != photo) {
			t.Errorf("the listing: %v", f)
		}
	}
	if state, err := mg.EditState("/phone/IMG_1.JPG"); err != nil || !state.Edited || !state.OriginalRemoved || state.Copies != 2 {
		t.Errorf("EditState: %v %v", state, err)
	}
	if _, err := mg.RevertImage(ses, "/phone/IMG_1.JPG", img.Hash, false); !errors.Is(err, ErrOriginalRemoved) {
		t.Errorf("a revert: %v", err)
	}
	// The phone's own sync, sending the photo it took again: the
	// compressed file, nothing stored - whole, and in chunks.
	if got, err := mg.UploadFile(ses, "/phone/IMG_1.JPG", big, false, taken, nil, ""); err != nil || got.Hash != img.Hash || got.Path != "/phone/IMG_1.JPG" {
		t.Errorf("the photo uploaded again: %v %v", got, err)
	}
	if got, err := mg.UploadFile(ses, "/phone/IMG_1 (1).JPG", big, false, taken, nil, ""); err != nil || got.Hash != img.Hash || got.Path != "/phone/IMG_1.JPG" {
		t.Errorf("the photo uploaded again under an alt name: %v %v", got, err)
	}
	upID, _, err := mg.BeginUpload(ses, "/phone/VID_1.MOV", int64(len(clip)), false, taken, nil, "", video, "test")
	if err != nil {
		t.Fatal(err)
	}
	for off := 0; off < len(clip); off += MaxChunk {
		if _, err := mg.UploadChunk(ses, upID, int64(off), clip[off:min(off+MaxChunk, len(clip))], "test"); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := mg.FinishUpload(ses, upID, video, "test"); err != nil || got.Hash != vid.Hash {
		t.Errorf("the video sent again in chunks: %v %v", got, err)
	}
	// Anywhere else, the compressed content is linked there.
	if got, err := mg.UploadFile(ses, "/elsewhere/IMG_1.JPG", big, false, taken, nil, ""); err != nil || got.Hash != img.Hash || rowHash(t, db, "/elsewhere/IMG_1.JPG") != img.Hash {
		t.Errorf("the photo uploaded elsewhere: %v %v", got, err)
	}
	if got, err := mg.LinkFile(ses, "/other/VID_1.MOV", video, false, taken, nil, ""); err != nil || got.Hash != vid.Hash || rowHash(t, db, "/other/VID_1.MOV") != vid.Hash {
		t.Errorf("the video's original linked elsewhere: %v %v", got, err)
	}
	if mg.hasBlob(photo) || mg.hasBlob(video) {
		t.Error("an original came back to the disk")
	}
	if n := countRows(t, db, "select count(*) from `files` where `hash` in (?, ?)", photo, video); n != 0 {
		t.Errorf("%d rows hold an original again", n)
	}

	// An edit of the compressed photo keeps the mark: still nothing to
	// revert to.
	turned, err := mg.EditImage(ses, &pb.EditImage{Path: "/phone/IMG_1.JPG", Hash: img.Hash, QuarterTurns: 1, EveryCopy: true}, "en")
	if err != nil {
		t.Fatal(err)
	}
	if turned.OriginalHash != photo || !turned.OriginalRemoved {
		t.Errorf("the edit of a compressed photo: %v", turned)
	}
	if _, err := mg.RevertImage(ses, "/phone/IMG_1.JPG", turned.Hash, false); !errors.Is(err, ErrOriginalRemoved) {
		t.Errorf("a revert of its edit: %v", err)
	}
	if has, _ := mg.HasFile(photo, ""); !has {
		t.Error("the original isn't held after an edit of its compressed file")
	}

	// The next job replaces the finished one.
	next, err := mg.CompressFiles(ses, nil, "en")
	if err != nil || next.Id == id {
		t.Fatalf("a new job: %v %v", next, err)
	}
	waitUpTo(t, 10*time.Second, "the empty job", func() bool { j, _ := mg.CompressJob("", "en"); return j.Finished })
	if _, err := mg.CompressJob(id, "en"); !IsCompressNoJob(err) {
		t.Errorf("the old job: %v", err)
	}
}

// A compress interrupted between its save and the deletion of the
// original is finished at the next start; a copy linked while it ran
// keeps the original, whose edit is then an ordinary one.
func TestCompressFinishesAndKeepsOriginalsMySQL(t *testing.T) {
	mg, ses, db := compressTestDB(t)
	orig := sha256Hex([]byte("an interrupted compress"))
	kept := sha256Hex([]byte("a compress with a copy linked meanwhile"))
	for i, h := range []string{orig, kept} {
		if err := os.WriteFile(blobPath(h), []byte("sealed"), 0o600); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(blobPath(h)+cThumbnailSuffix, []byte("thumb"), 0o600)
		t.Cleanup(func() { os.Remove(blobPath(h)); os.Remove(blobPath(h) + cThumbnailSuffix) })
		if _, err := db.Exec("insert into `edited_content` (`hash`, `original_hash`, `original_mime`, `original_size`, `original_ext`, `edited`, `original_removed`) values (?, ?, 'image/jpeg', 6, '', now(), 1)",
			sha256Hex([]byte{byte(i)}), h); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("insert into `files` (`hash`, `mime`, `created`, `modified`, `path`, `size`) values (?, 'image/jpeg', now(), now(), '/linked.jpg', 6)", kept); err != nil {
		t.Fatal(err)
	}
	mg.finishRemovedOriginals()
	if exists(blobPath(orig)) || exists(blobPath(orig)+cThumbnailSuffix) {
		t.Error("the interrupted compress's original is still on the disk")
	}
	if !exists(blobPath(kept)) || countRows(t, db, "select count(*) from `edited_content` where `original_hash` = ? and `original_removed` = 0", kept) != 1 {
		t.Error("the original a copy holds was deleted, or its edit still says so")
	}
	// Content that is no compressed file's original is left alone.
	other := sha256Hex([]byte("an ordinary photo"))
	os.WriteFile(blobPath(other), []byte("sealed"), 0o600)
	t.Cleanup(func() { os.Remove(blobPath(other)) })
	if removed, _ := mg.dropRemovedOriginal(other); removed || !exists(blobPath(other)) {
		t.Error("an ordinary blob was deleted")
	}
	_ = ses
}
