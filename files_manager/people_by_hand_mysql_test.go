// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"database/sql"
	"sort"
	"strings"
	"testing"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// byHandOf is who was added by hand to the content hash, sorted.
func byHandOf(t *testing.T, db *sql.DB, hash string) string {
	t.Helper()
	rows, err := db.Query("select `person_id` from `people_by_hand` where `hash` = ?", hash)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// infoOf is Info's list for the file at p: "person" for a face found,
// "person+" for one added by hand, in order.
func infoOf(t *testing.T, mg *Manager, p string) string {
	t.Helper()
	faces, err := mg.PhotoFaces(nil, p, true)
	if err != nil {
		t.Fatal(err)
	}
	if !faces.AddsPeople {
		t.Error("the device doesn't say it takes AddToPerson")
	}
	var out []string
	for _, f := range faces.Faces {
		s := f.PersonId
		if f.ByHand {
			s += "+"
		}
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

// People added by hand against a real MariaDB (OTC_TEST_DEVICE_MYSQL_DSN,
// a database made from db.sql): Info lists them after the faces found; an
// edit keeps them, as it keeps the faces, and the original keeps its own;
// a revert gives the original those added to the edit since; a face of
// theirs found takes their place; the content's last copy deleted takes
// them with it.
func TestEditsKeepPeopleAddedByHandMySQL(t *testing.T) {
	mg, ses, db := editTestDB(t)
	photo := cameraJPEG(t, 80, 40, 1)
	orig := sha256Hex(photo)
	taken := timestamppb.New(time.Date(2021, 5, 6, 7, 8, 9, 0, time.UTC))
	if _, err := mg.UploadFile(ses, "/phone/IMG_1.JPG", photo, false, taken, nil, ""); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ana", "bo", "cy"} {
		if err := mg.dao.CreatePerson(id); err != nil {
			t.Fatal(err)
		}
	}
	if err := mg.dao.AddFace("face-bo", orig, "bo", 4, 4, 10, 10, []byte("emb"), []byte("thumb"), 0.9); err != nil {
		t.Fatal(err)
	}
	if err := mg.AddToPerson("ana", []string{orig}); err != nil {
		t.Fatal(err)
	}
	// Bo's face is there: he isn't added.
	if err := mg.AddToPerson("bo", []string{orig}); err != nil {
		t.Fatal(err)
	}
	if got := infoOf(t, mg, "/phone/IMG_1.JPG"); got != "bo,ana+" {
		t.Errorf("Info %s", got)
	}

	// Turned: the face and Ana go with it, and stay on the original.
	edited, err := mg.EditImage(ses, &pb.EditImage{Path: "/phone/IMG_1.JPG", Hash: orig, QuarterTurns: 1}, "en")
	if err != nil {
		t.Fatal(err)
	}
	if got := byHandOf(t, db, edited.Hash); got != "ana" {
		t.Errorf("the edit's people added by hand: %q", got)
	}
	if got := byHandOf(t, db, orig); got != "ana" {
		t.Errorf("the original's: %q", got)
	}
	time.Sleep(1100 * time.Millisecond) // the next second: Info's order
	if err := mg.AddToPerson("cy", []string{edited.Hash}); err != nil {
		t.Fatal(err)
	}
	if got := infoOf(t, mg, "/phone/IMG_1.JPG"); got != "bo,ana+,cy+" {
		t.Errorf("the edit's Info %s", got)
	}

	// Reverted: the original back, with Cy, added to the edit since.
	if _, err := mg.RevertImage(ses, "/phone/IMG_1.JPG", edited.Hash, false); err != nil {
		t.Fatal(err)
	}
	if got := byHandOf(t, db, orig); got != "ana,cy" {
		t.Errorf("the original's people added by hand after the revert: %q", got)
	}
	if got := byHandOf(t, db, edited.Hash); got != "" {
		t.Errorf("the reverted edit keeps people added by hand: %q", got)
	}

	// A face of Cy found takes the place of his row.
	if err := mg.dao.AddFace("face-cy", orig, "cy", 40, 4, 10, 10, []byte("emb"), []byte("thumb"), 0.9); err != nil {
		t.Fatal(err)
	}
	if got := infoOf(t, mg, "/phone/IMG_1.JPG"); got != "bo,cy,ana+" {
		t.Errorf("Info after Cy's face %s", got)
	}

	// The photo deleted: its people added by hand go with it.
	if err := mg.DelFile(ses, "/phone/IMG_1.JPG"); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, "select count(*) from `people_by_hand`"); n != 0 {
		t.Errorf("%d rows left after the photo went", n)
	}
}

// A video: added by hand (no face is looked for in it), listed by Info,
// kept by its cut.
func TestVideoCutKeepsPeopleAddedByHandMySQL(t *testing.T) {
	mg, ses, db := editTestDB(t)
	clip := testClip(t, 4)
	plainVideoSource(t, mg)
	orig := sha256Hex(clip)
	taken := timestamppb.New(time.Date(2021, 5, 6, 7, 8, 9, 0, time.UTC))
	if _, err := mg.UploadFile(ses, "/phone/VID_1.mp4", clip, false, taken, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := mg.dao.CreatePerson("ana"); err != nil {
		t.Fatal(err)
	}
	if err := mg.AddToPerson("ana", []string{orig}); err != nil {
		t.Fatal(err)
	}
	if got := infoOf(t, mg, "/phone/VID_1.mp4"); got != "ana+" {
		t.Errorf("the video's Info %s", got)
	}
	files, err := mg.dao.SearchMedia("", nil, []string{"ana"}, "", false, nil, nil)
	if err != nil || len(files) != 1 || files[0].Hash != orig {
		t.Errorf("Ana's photos and videos: %v, %v", files, err)
	}
	edited, err := mg.EditVideo(ses, &pb.EditVideo{Path: "/phone/VID_1.mp4", Hash: orig, StartSecs: 1, EndSecs: 3}, "en")
	if err != nil {
		t.Fatal(err)
	}
	if got := byHandOf(t, db, edited.Hash); got != "ana" {
		t.Errorf("the cut's people added by hand: %q", got)
	}
	if got := infoOf(t, mg, "/phone/VID_1.mp4"); got != "ana+" {
		t.Errorf("the cut's Info %s", got)
	}
}
