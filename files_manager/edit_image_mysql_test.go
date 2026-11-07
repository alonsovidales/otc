// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alonsovidales/otc/dao"
	otcdb "github.com/alonsovidales/otc/db"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	_ "github.com/go-sql-driver/mysql"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Photo edits against a real MariaDB: OTC_TEST_DEVICE_MYSQL_DSN names a
// server whose user may create databases (user:pass@tcp(127.0.0.1:3306)/),
// in which each test makes one of its own from db.sql and drops it after.
// Skipped without it.
func editTestDB(t *testing.T) (*Manager, *session.Session, *sql.DB) {
	t.Helper()
	server := os.Getenv("OTC_TEST_DEVICE_MYSQL_DSN")
	if server == "" {
		t.Skip("OTC_TEST_DEVICE_MYSQL_DSN not set")
	}
	admin, err := sql.Open("mysql", server)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("otc_edittest_%d", time.Now().UnixNano())
	if _, err := admin.Exec("create database " + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec("drop database " + name)
		admin.Close()
	})
	opts := "parseTime=true&charset=utf8mb4,utf8&interpolateParams=true&time_zone=%27%2B00%3A00%27"
	schemaDB, err := sql.Open("mysql", server+name+"?multiStatements=true&"+opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := schemaDB.Exec(otcdb.Schema); err != nil {
		t.Fatalf("db.sql: %v", err)
	}
	schemaDB.Close()
	db, err := sql.Open("mysql", server+name+"?"+opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, ses := galleryTestEnv(t)
	return keptOut(NewWithDAO(dao.NewWithDB(db))), ses, db
}

func rowHash(t *testing.T, db *sql.DB, path string) string {
	t.Helper()
	var h string
	if err := db.QueryRow("select `hash` from `files` where `path` = ?", path).Scan(&h); err != nil {
		if err == sql.ErrNoRows {
			return ""
		}
		t.Fatal(err)
	}
	return h
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// An edit replaces one copy, keeps the original hidden - held for the
// phones, linked back where its edit is, found by Free up space - moves
// the faces, and a revert puts the original back and drops the edit.
func TestEditAndRevertMySQL(t *testing.T) {
	mg, ses, db := editTestDB(t)
	photo := cameraJPEG(t, 80, 40, 6)
	orig := sha256Hex(photo)
	taken := timestamppb.New(time.Date(2021, 5, 6, 7, 8, 9, 0, time.UTC))
	if _, err := mg.UploadFile(ses, "/phone/IMG_1.JPG", photo, false, taken, nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := mg.LinkFile(ses, "/backup/IMG_1.JPG", orig, false, taken, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := mg.SetFavourite([]string{"/phone/IMG_1.JPG"}, true); err != nil {
		t.Fatal(err)
	}
	// A face in the photo shown upright (40x80): 4..14 x 4..14, top left.
	if err := mg.dao.CreatePerson("person-1"); err != nil {
		t.Fatal(err)
	}
	if err := mg.dao.AddFace("face-1", orig, "person-1", 4, 4, 10, 10, []byte("emb"), []byte("thumb"), 0.9); err != nil {
		t.Fatal(err)
	}

	state, err := mg.EditState("/phone/IMG_1.JPG")
	if err != nil || state.Copies != 2 || state.Edited {
		t.Fatalf("EditState before: %v %v", state, err)
	}

	// Only this copy, turned: a lossless turn of the JPEG.
	edited, err := mg.EditImage(ses, &pb.EditImage{Path: "/phone/IMG_1.JPG", Hash: orig, QuarterTurns: 1}, "en")
	if err != nil {
		t.Fatal(err)
	}
	if edited.Path != "/phone/IMG_1.JPG" || edited.Hash == orig || edited.OriginalHash != orig {
		t.Fatalf("edited %s %s (original %s)", edited.Path, edited.Hash, edited.OriginalHash)
	}
	if rowHash(t, db, "/backup/IMG_1.JPG") != orig {
		t.Error("the other copy was edited too")
	}
	if !mg.hasBlob(edited.Hash) || !mg.hasBlob(orig) {
		t.Fatal("the edit or the original isn't on the disk")
	}
	// Its date, as the photo's.
	if got, _ := mg.dao.GetFileByPath("/phone/IMG_1.JPG"); !got.Created.AsTime().Equal(taken.AsTime()) {
		t.Errorf("the edit's date is %v", got.Created.AsTime())
	}
	// The face, turned clockwise into the 80x40 edit: 66..76 x 4..14.
	var x, y, w, h int
	var person string
	if err := db.QueryRow("select `bbox_x`, `bbox_y`, `bbox_w`, `bbox_h`, `person_id` from `faces` where `hash` = ?", edited.Hash).Scan(&x, &y, &w, &h, &person); err != nil {
		t.Fatalf("the edit has no face: %v", err)
	}
	if x != 66 || y != 4 || w != 10 || h != 10 || person != "person-1" {
		t.Errorf("the edit's face is %d,%d %dx%d of %s", x, y, w, h, person)
	}
	// Favourites keep the photo: another copy still holds it.
	if n := countRows(t, db, "select count(*) from `image_group_files` where `hash` = ?", orig); n != 1 {
		t.Errorf("the photo is in %d collections", n)
	}

	// A listing says which is an edit.
	listed, err := mg.ListFiles(ses, "/phone/", false)
	if err != nil || len(listed) != 1 || listed[0].OriginalHash != orig {
		t.Fatalf("listing: %v %v", listed, err)
	}

	// The phone's sync sending the photo it took: there already.
	if has, err := mg.HasFile(orig, ""); err != nil || !has {
		t.Errorf("HasFile of the original: %v %v", has, err)
	}
	linked, err := mg.LinkFile(ses, "/phone/IMG_1 (1).JPG", orig, false, taken, nil, "")
	if err != nil || linked.Path != "/phone/IMG_1.JPG" || linked.Hash != edited.Hash {
		t.Errorf("the phone linking the photo it took got %v %v", linked, err)
	}
	if rowHash(t, db, "/phone/IMG_1 (1).JPG") != "" {
		t.Error("the photo was linked again next to its edit")
	}
	up, err := mg.UploadFile(ses, "/phone/IMG_1.JPG", photo, true, taken, nil, "")
	if err != nil || up.Hash != edited.Hash || rowHash(t, db, "/phone/IMG_1.JPG") != edited.Hash {
		t.Errorf("a sync sending the photo again over its edit: %v %v", up, err)
	}

	// Every copy now: the backup's copy, cropped. The only file of the
	// photo goes over, and the photo's place in Favourites with it? No -
	// the favourite was the phone's copy's content, the photo, held by
	// the backup's alone until now.
	cropped, err := mg.EditImage(ses, &pb.EditImage{Path: "/backup/IMG_1.JPG", Hash: orig, Crop: &pb.EditCrop{X: 0, Y: 0, Width: 1, Height: 0.5}, EveryCopy: true}, "en")
	if err != nil {
		t.Fatal(err)
	}
	if cropped.OriginalHash != orig || rowHash(t, db, "/backup/IMG_1.JPG") != cropped.Hash {
		t.Fatalf("cropped %v", cropped)
	}
	if n := countRows(t, db, "select count(*) from `image_group_files` where `hash` = ?", cropped.Hash); n != 1 {
		t.Errorf("the favourite didn't go with the photo's last copy: %d", n)
	}
	// Free up space: the original is held whole.
	found, err := mg.HasHashes([]string{orig})
	if err != nil || len(found) != 1 {
		t.Errorf("HasHashes of the original: %v %v", found, err)
	}

	// Revert the phone's copy: the original back, the turned edit gone.
	reverted, err := mg.RevertImage(ses, "/phone/IMG_1.JPG", edited.Hash, false)
	if err != nil || reverted.Hash != orig {
		t.Fatalf("revert: %v %v", reverted, err)
	}
	if mg.hasBlob(edited.Hash) || countRows(t, db, "select count(*) from `edited_content` where `hash` = ?", edited.Hash) != 0 {
		t.Error("the reverted edit is still kept")
	}
	if countRows(t, db, "select count(*) from `faces` where `hash` = ?", edited.Hash) != 0 {
		t.Error("the reverted edit's faces are still there")
	}

	// Deleting the last edit: it goes, and the original stays - the phone's
	// copy holds it again.
	if err := mg.DelFile(ses, "/backup/IMG_1.JPG"); err != nil {
		t.Fatal(err)
	}
	if mg.hasBlob(cropped.Hash) || !mg.hasBlob(orig) {
		t.Error("the edit's delete kept the edit or took the photo")
	}
	if countRows(t, db, "select count(*) from `edited_content`") != 0 {
		t.Error("an edit row is left")
	}
}

// A HEIC replaced in place becomes a JPEG named so; an iPhone relinking
// either is answered with it; the revert gives the name back; an edit of
// the edit keeps the first original; deleting it takes the original too.
func TestEditOfAHEICMySQL(t *testing.T) {
	mg, ses, db := editTestDB(t)
	heic, err := os.ReadFile("testdata/eightbit-grid.heic")
	if err != nil {
		t.Fatal(err)
	}
	orig := sha256Hex(heic)
	if _, err := mg.UploadFile(ses, "/iphone/IMG_2.HEIC", heic, false, nil, nil, "cloud-2"); err != nil {
		t.Fatal(err)
	}
	edited, err := mg.EditImage(ses, &pb.EditImage{Path: "/iphone/IMG_2.HEIC", QuarterTurns: 3}, "en")
	if err != nil {
		t.Fatal(err)
	}
	if edited.Path != "/iphone/IMG_2.JPG" || edited.Mime != "image/jpeg" || rowHash(t, db, "/iphone/IMG_2.HEIC") != "" {
		t.Fatalf("edited %s %s", edited.Path, edited.Mime)
	}
	var ext string
	db.QueryRow("select `original_ext` from `edited_content` where `hash` = ?", edited.Hash).Scan(&ext)
	if ext != ".HEIC" {
		t.Errorf("original_ext %q", ext)
	}
	// The iPhone, by its cloud id, links the edit's content to the HEIC's
	// name; by the asset's own hash, the HEIC.
	for _, h := range []string{edited.Hash, orig} {
		got, err := mg.LinkFile(ses, "/iphone/IMG_2.HEIC", h, false, nil, nil, "cloud-2")
		if err != nil || got.Path != "/iphone/IMG_2.JPG" {
			t.Errorf("linking %s to the HEIC's name: %v %v", h[:8], got, err)
		}
	}
	if n := countRows(t, db, "select count(*) from `files`"); n != 1 {
		t.Errorf("%d files after the relinks", n)
	}
	// Elsewhere the original is a photo of its own again.
	if got, err := mg.LinkFile(ses, "/elsewhere/IMG_2.HEIC", orig, false, nil, nil, ""); err != nil || got.Mime != "image/heic" && !strings.Contains(got.Mime, "hei") {
		t.Errorf("the original linked elsewhere: %v %v", got, err)
	}
	if err := mg.DelFile(ses, "/elsewhere/IMG_2.HEIC"); err != nil {
		t.Fatal(err)
	}
	if !mg.hasBlob(orig) {
		t.Fatal("deleting the original's other path took the original")
	}

	// Edited again: the first original stays the original, the middle edit
	// goes.
	again, err := mg.EditImage(ses, &pb.EditImage{Path: "/iphone/IMG_2.JPG", Crop: &pb.EditCrop{X: 0.1, Y: 0.1, Width: 0.5, Height: 0.5}}, "en")
	if err != nil {
		t.Fatal(err)
	}
	if again.OriginalHash != orig || again.Path != "/iphone/IMG_2.JPG" || mg.hasBlob(edited.Hash) {
		t.Fatalf("edited again: %v (the middle edit kept: %v)", again, mg.hasBlob(edited.Hash))
	}
	db.QueryRow("select `original_ext` from `edited_content` where `hash` = ?", again.Hash).Scan(&ext)
	if ext != ".HEIC" {
		t.Errorf("the edit of the edit lost the original's extension: %q", ext)
	}

	// Revert: the HEIC, under its name.
	back, err := mg.RevertImage(ses, "/iphone/IMG_2.JPG", "", false)
	if err != nil || back.Path != "/iphone/IMG_2.HEIC" || back.Hash != orig {
		t.Fatalf("revert: %v %v", back, err)
	}

	// As a copy: next to it, the photo as it was.
	cp, err := mg.EditImage(ses, &pb.EditImage{Path: "/iphone/IMG_2.HEIC", QuarterTurns: 1, AsCopy: true}, "en")
	if err != nil {
		t.Fatal(err)
	}
	if cp.Path != "/iphone/IMG_2 (edited).JPG" || rowHash(t, db, "/iphone/IMG_2.HEIC") != orig || cp.OriginalHash != orig {
		t.Fatalf("the copy: %v", cp)
	}
	// The phone sending its photo where the photo still is: the photo.
	if got, err := mg.LinkFile(ses, "/iphone/IMG_2.HEIC", orig, false, nil, nil, ""); err != nil || got.Path != "/iphone/IMG_2.HEIC" {
		t.Errorf("relinking the photo next to its copy: %v %v", got, err)
	}

	// Deleting the photo and its copy: nothing of either is left.
	for _, p := range []string{"/iphone/IMG_2.HEIC", "/iphone/IMG_2 (edited).JPG"} {
		if err := mg.DelFile(ses, p); err != nil {
			t.Fatal(err)
		}
	}
	if mg.hasBlob(orig) || mg.hasBlob(cp.Hash) || countRows(t, db, "select count(*) from `edited_content`") != 0 {
		t.Error("something of the photo is left")
	}

	// Not a photo: refused, nothing changed.
	video := append([]byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"), bytes.Repeat([]byte{0}, 64)...)
	if _, err := mg.UploadFile(ses, "/v/clip.mp4", video, false, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := mg.EditImage(ses, &pb.EditImage{Path: "/v/clip.mp4", QuarterTurns: 1}, "en"); !IsNotEditable(err) {
		t.Errorf("editing a video: %v", err)
	}
	// Shown with other content: refused.
	if _, err := mg.EditImage(ses, &pb.EditImage{Path: "/v/clip.mp4", Hash: orig, QuarterTurns: 1}, "en"); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Errorf("an edit of content that changed: %v", err)
	}
}
