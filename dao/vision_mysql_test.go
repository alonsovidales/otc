// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"bytes"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// Text in photos and Pets, against a real MySQL/MariaDB (OTC_TEST_MYSQL_DSN,
// as search_files_mysql_test.go): temporary tables shaped as db.sql's.

func visionTestDB(tb testing.TB) *Dao {
	tb.Helper()
	db, d := outOfImagesTestDB(tb)
	for _, q := range []string{
		"alter table `faces` add column `quality` float default null, add column `kind` tinyint not null default 0",
		"create temporary table `photo_text` (`hash` varchar(64) not null, `model` varchar(64) not null, `text` mediumblob default null, `made` datetime not null, primary key (`hash`)) engine=InnoDB",
		"create temporary table `pet_scans` (`hash` varchar(64) not null, `model` varchar(64) not null, `made` datetime not null, primary key (`hash`)) engine=InnoDB",
		"create temporary table `photo_embeddings` (`hash` varchar(64) not null, `model` varchar(64) not null, `vec` varbinary(2048) not null, `made` datetime not null, primary key (`hash`)) engine=InnoDB",
		"create temporary table `settings` (`domain` varchar(64) not null default '', `text_in_photos_enabled` tinyint(1) not null default 1, `pets_enabled` tinyint(1) not null default 1) engine=InnoDB",
		"insert into `settings` (`domain`) values ('t')",
	} {
		if _, err := db.Exec(q); err != nil {
			tb.Fatal(err)
		}
	}
	insertMediaRows(tb, db, "files",
		mediaRow{"h1", "image/jpeg", "/Photos/a.jpg", month(1)},
		mediaRow{"h1", "image/jpeg", "/Backup/a.jpg", month(2)}, // a second copy
		mediaRow{"h2", "image/heic", "/Photos/b.heic", month(3)},
		mediaRow{"h3", "video/mp4", "/Photos/c.mp4", month(4)},  // not a photo
		mediaRow{"h4", "image/png", "/Kept/d.png", month(5)},    // only kept out
		mediaRow{"h5", "image/jpeg", "/Photos/e.jpg", month(6)}, // read by another model
	)
	return d
}

func lookAtHashes(t *testing.T, d *Dao, after string, text, pets bool) []string {
	t.Helper()
	work, err := d.PhotosToLookAt(after, 10, text, "tm", pets, "pm", "vm", []string{"/Kept/"})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, w := range work {
		out = append(out, w.File.Hash)
	}
	return out
}

func TestPhotoTextAndPetScansMySQL(t *testing.T) {
	d := visionTestDB(t)
	if on, err := d.TextInPhotosEnabled(); err != nil || !on {
		t.Fatalf("Text in photos on by default: %v %v", on, err)
	}
	if on, err := d.PetsEnabled(); err != nil || !on {
		t.Fatalf("Pets on by default: %v %v", on, err)
	}
	// What is left: photos only, once each, outside the kept-out folders.
	if got := strings.Join(lookAtHashes(t, d, "", true, true), " "); got != "h1 h2 h5" {
		t.Errorf("to look at: %s", got)
	}
	if n, err := d.CountPhotosToRead("tm", []string{"/Kept/"}); err != nil || n != 3 {
		t.Errorf("CountPhotosToRead = %d %v", n, err)
	}

	sealed := bytes.Repeat([]byte{3}, 40)
	if err := d.SetPhotoText("h1", "tm", sealed); err != nil {
		t.Fatal(err)
	}
	if err := d.SetPhotoText("h2", "tm", nil); err != nil { // read, no text
		t.Fatal(err)
	}
	if err := d.SetPhotoText("h5", "old", sealed); err != nil {
		t.Fatal(err)
	}
	if err := d.SetPetScanned("h1", "pm"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetPhotoEmbedding("h2", "vm", []byte{9, 9}); err != nil {
		t.Fatal(err)
	}
	// h1 is done; h2 still needs its pets (with its vector), h5 its text
	// by this model and its pets.
	work, err := d.PhotosToLookAt("", 10, true, "tm", true, "pm", "vm", []string{"/Kept/"})
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 2 || work[0].File.Hash != "h2" || !work[0].HasText || work[0].Scanned || !bytes.Equal(work[0].Vec, []byte{9, 9}) ||
		work[1].File.Hash != "h5" || work[1].HasText || work[1].Scanned || work[1].Vec != nil {
		t.Errorf("work: %+v", work)
	}
	// With Pets off only the text is waited for; after a hash, the next.
	if got := strings.Join(lookAtHashes(t, d, "", true, false), " "); got != "h5" {
		t.Errorf("text alone: %s", got)
	}
	if got := strings.Join(lookAtHashes(t, d, "h2", false, true), " "); got != "h5" {
		t.Errorf("pets after h2: %s", got)
	}
	if n, err := d.CountPhotosToScanForPets("pm", []string{"/Kept/"}); err != nil || n != 2 {
		t.Errorf("CountPhotosToScanForPets = %d %v", n, err)
	}

	got, read, err := d.PhotoText("h1", "tm")
	if err != nil || !read || !bytes.Equal(got, sealed) {
		t.Errorf("PhotoText(h1) = %v %v %v", got, read, err)
	}
	if got, read, err := d.PhotoText("h2", "tm"); err != nil || !read || got != nil {
		t.Errorf("PhotoText(h2) = %v %v %v, want read without text", got, read, err)
	}
	if _, read, _ := d.PhotoText("h5", "tm"); read {
		t.Error("another model's text read as this one's")
	}
	var each []string
	if err := d.EachPhotoText("tm", func(h string, _ []byte) error { each = append(each, h); return nil }); err != nil || strings.Join(each, " ") != "h1" {
		t.Errorf("EachPhotoText = %v %v", each, err)
	}
	if scanned, err := d.PetScanned("h1", "pm"); err != nil || !scanned {
		t.Errorf("PetScanned(h1) = %v %v", scanned, err)
	}
	if err := d.DelPetScans("h1", "zz"); err != nil {
		t.Fatal(err)
	}
	if scanned, _ := d.PetScanned("h1", "pm"); scanned {
		t.Error("a scan forgotten is still there")
	}
	if err := d.DelPhotoTexts("h1"); err != nil {
		t.Fatal(err)
	}
	if _, read, _ := d.PhotoText("h1", "tm"); read {
		t.Error("a text forgotten is still there")
	}

	// Off deletes every text; Pets off deletes nothing.
	if err := d.SetTextInPhotosEnabled(false); err != nil {
		t.Fatal(err)
	}
	if _, read, _ := d.PhotoText("h2", "tm"); read {
		t.Error("Text in photos off kept a text")
	}
	if on, _ := d.TextInPhotosEnabled(); on {
		t.Error("still on")
	}
	if err := d.SetPetsEnabled(false); err != nil {
		t.Fatal(err)
	}
	if on, _ := d.PetsEnabled(); on {
		t.Error("Pets still on")
	}
}

// A pet's face is a face of its kind: matched apart from people's, never
// saying a photo's faces were found.
func TestPetFacesMySQL(t *testing.T) {
	d := visionTestDB(t)
	if err := d.CreatePerson("biscuit"); err != nil {
		t.Fatal(err)
	}
	if err := d.CreatePerson("alice"); err != nil {
		t.Fatal(err)
	}
	if err := d.AddPetFace("pf1", "h1", "biscuit", FaceKindDog, 1, 2, 30, 40, []byte("pe"), []byte("pt"), 0.7); err != nil {
		t.Fatal(err)
	}
	if has, err := d.HashHasFaces("h1"); err != nil || has {
		t.Errorf("a pet said the photo's faces were found: %v %v", has, err)
	}
	if err := d.AddFace("ff1", "h1", "alice", 5, 5, 10, 10, []byte("fe"), []byte("ft"), 0.5); err != nil {
		t.Fatal(err)
	}
	if has, err := d.HashHasFaces("h1"); err != nil || !has {
		t.Errorf("a person's face didn't: %v %v", has, err)
	}
	people, err := d.ListFaceEmbeddings()
	if err != nil || len(people) != 1 || people[0].ID != "ff1" {
		t.Errorf("ListFaceEmbeddings = %+v %v", people, err)
	}
	pets, err := d.ListPetEmbeddings()
	if err != nil || len(pets) != 1 || pets[0].ID != "pf1" || pets[0].Kind != FaceKindDog || string(pets[0].Embedding) != "pe" {
		t.Errorf("ListPetEmbeddings = %+v %v", pets, err)
	}
	if mine, err := d.ListPersonPetEmbeddings("biscuit"); err != nil || len(mine) != 1 {
		t.Errorf("ListPersonPetEmbeddings = %+v %v", mine, err)
	}
	if theirs, err := d.ListPersonFaceEmbeddings("biscuit"); err != nil || len(theirs) != 0 {
		t.Errorf("a pet's faces among a person's: %+v %v", theirs, err)
	}
	// The faces Pets weighs: a person's (not a pet's), whoever's - no
	// one's too - and whether they have a name.
	if err := d.AddFace("ff2", "h1", "", 50, 5, 10, 10, []byte("fe"), []byte("ft"), 0.5); err != nil {
		t.Fatal(err)
	}
	faces, err := d.PersonFacesIn("h1")
	if err != nil || len(faces) != 2 || faces[0].ID != "ff1" || faces[0].PersonID != "alice" || faces[0].Named || faces[0].Box != (FaceBox{5, 5, 10, 10}) ||
		faces[1].ID != "ff2" || faces[1].PersonID != "" || faces[1].Named {
		t.Errorf("PersonFacesIn = %+v %v", faces, err)
	}
	if err := d.RenamePerson("alice", "Alice"); err != nil {
		t.Fatal(err)
	}
	if faces, err := d.PersonFacesIn("h1"); err != nil || len(faces) != 2 || !faces[0].Named || faces[1].Named {
		t.Errorf("once Alice is named: %+v %v", faces, err)
	}
}

// Pets grouped (files_manager/pet_groups.go): every pet face of someone
// with its photo and whether its pet has a name; a pet's faces given to
// another - never a person's face, nor one taken off - its cover gone with
// them, and a pet left with none deleted. People counts each photo once.
func TestPetGroupingMySQL(t *testing.T) {
	d := visionTestDB(t)
	for _, id := range []string{"biscuit", "rex", "alice"} {
		if err := d.CreatePerson(id); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []struct {
		id, hash, person string
	}{{"pf1", "h1", "biscuit"}, {"pf2", "h2", "biscuit"}, {"pf3", "h1", "biscuit"}, {"pf4", "h4", "rex"}, {"pf5", "h2", ""}} {
		if err := d.AddPetFace(f.id, f.hash, f.person, FaceKindDog, 1, 2, 30, 40, []byte("e"+f.id), []byte("pt"), 0.7); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.AddFace("ff1", "h1", "alice", 5, 5, 10, 10, []byte("fe"), []byte("ft"), 0.5); err != nil {
		t.Fatal(err)
	}
	if err := d.RenamePerson("rex", "Rex"); err != nil {
		t.Fatal(err)
	}
	if named, err := d.PeopleNamed([]string{"biscuit", "rex", "gone"}); err != nil || fmt.Sprint(named) != "map[biscuit:false rex:true]" {
		t.Errorf("PeopleNamed = %v %v, want biscuit unnamed, Rex named, no one gone", named, err)
	}
	faces, err := d.ListPetFaces()
	got := []string{}
	for _, f := range faces {
		got = append(got, fmt.Sprintf("%s:%s:%s:%d:%v:%s", f.ID, f.PersonID, f.Hash, f.Kind, f.Named, f.Embedding))
	}
	if want := "[pf1:biscuit:h1:1:false:epf1 pf2:biscuit:h2:1:false:epf2 pf3:biscuit:h1:1:false:epf3 pf4:rex:h4:1:true:epf4]"; err != nil || fmt.Sprint(got) != want {
		t.Errorf("ListPetFaces = %v %v, want %s", got, err, want)
	}
	counts := func() map[string]int32 {
		t.Helper()
		people, err := d.ListPeople(0)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int32{}
		for _, p := range people {
			out[p.Id] = p.FaceCount
		}
		return out
	}
	// Biscuit is twice in h1: one photo.
	if c := counts(); c["biscuit"] != 2 || c["rex"] != 1 {
		t.Errorf("counts %v, want biscuit 2 (h1, h2) and rex 1", c)
	}
	if err := d.SetPersonCoverFace("biscuit", "pf3", 0.5); err != nil {
		t.Fatal(err)
	}
	// pf3 is Rex; Alice's face ff1 and no one's pf5 never move.
	if err := d.GivePetFaces([]string{"pf3", "ff1", "pf5"}, "rex"); err != nil {
		t.Fatal(err)
	}
	if c := counts(); c["biscuit"] != 2 || c["rex"] != 2 || c["alice"] != 1 {
		t.Errorf("counts %v, want biscuit 2, rex 2 (h1, h4), alice 1", c)
	}
	var cover sql.NullString
	if err := d.db.QueryRow("select `cover_face_id` from `people` where `id` = 'biscuit'").Scan(&cover); err != nil || cover.Valid {
		t.Errorf("Biscuit's cover is still the face he gave: %v %v", cover, err)
	}
	var owner string
	if err := d.db.QueryRow("select `person_id` from `faces` where `id` = 'pf5'").Scan(&owner); err != nil || owner != "" {
		t.Errorf("a face taken off went to %q: %v", owner, err)
	}
	// Biscuit's last faces: he is gone.
	if err := d.GivePetFaces([]string{"pf1", "pf2"}, "rex"); err != nil {
		t.Fatal(err)
	}
	if c := counts(); c["rex"] != 3 {
		t.Errorf("counts %v, want rex 3 (h1, h2, h4)", c)
	} else if _, ok := c["biscuit"]; ok {
		t.Errorf("Biscuit stayed with no face: %v", c)
	}
	if named, err := d.PeopleNamed([]string{"biscuit"}); err != nil || len(named) != 0 {
		t.Errorf("PeopleNamed = %v %v, want Biscuit gone", named, err)
	}
}
