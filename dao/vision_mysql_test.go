// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"bytes"
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
	// The faces Pets may take off: a person's (not a pet's), of someone
	// with no name.
	if faces, err := d.UnnamedFacesIn("h1"); err != nil || len(faces) != 1 || faces[0].ID != "ff1" || faces[0].PersonID != "alice" || faces[0].Box != (FaceBox{5, 5, 10, 10}) {
		t.Errorf("UnnamedFacesIn = %+v %v", faces, err)
	}
	if err := d.RenamePerson("alice", "Alice"); err != nil {
		t.Fatal(err)
	}
	if faces, err := d.UnnamedFacesIn("h1"); err != nil || len(faces) != 0 {
		t.Errorf("a named person's faces offered: %+v %v", faces, err)
	}
}
