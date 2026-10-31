// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"errors"
	"testing"
)

// The face chosen for a person, against a real MySQL/MariaDB
// (OTC_TEST_MYSQL_DSN, as out_of_images_mysql_test.go): a face from one of
// their photos or a picture of the owner's own shows in People and stays as
// the medoid moves; the chosen face gone, the medoid is back.
func TestPersonCoverMySQL(t *testing.T) {
	db, d := outOfImagesTestDB(t)
	insertMediaRows(t, db, "files",
		mediaRow{"A", "image/jpeg", "/Phone/a.jpg", month(1)},
		mediaRow{"B", "image/jpeg", "/Phone/b.jpg", month(1)},
	)
	for _, q := range []string{
		"insert into `people` (`id`, `name`, `created`) values ('p1', 'Ana', now())",
		"insert into `faces` values" +
			" ('f1', 'A', 'p1', 0, 0, 10, 10, '', 'thumb-a', now())," +
			" ('f2', 'B', 'p1', 0, 0, 40, 40, '', 'thumb-b-big', now())," +
			" ('f3', 'B', 'p1', 0, 0, 20, 20, '', 'thumb-b-small', now())," +
			" ('g1', 'B', 'p2', 0, 0, 90, 90, '', 'thumb-someone', now())",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	cover := func() string {
		t.Helper()
		people, err := d.ListPeople(0.3)
		if err != nil || len(people) != 1 {
			t.Fatalf("ListPeople: %v, %v", people, err)
		}
		return string(people[0].CoverThumbnail)
	}
	if err := d.SetPersonCoverFace("p1", "f1", 0.9); err != nil {
		t.Fatal(err)
	}
	if got := cover(); got != "thumb-a" {
		t.Fatalf("medoid cover %q", got)
	}

	// Their face in B: the biggest of theirs, never someone else's.
	face, err := d.PersonFaceIn("p1", "B")
	if err != nil || face != "f2" {
		t.Fatalf("PersonFaceIn = %q, %v", face, err)
	}
	if face, err := d.PersonFaceIn("p1", "Z"); err != nil || face != "" {
		t.Fatalf("no face of theirs: %q, %v", face, err)
	}
	if err := d.SetPersonCover("p1", "f2", nil); err != nil {
		t.Fatal(err)
	}
	// A new medoid doesn't replace the choice; the cohesion still follows.
	if err := d.SetPersonCoverFace("p1", "f1", 0.5); err != nil {
		t.Fatal(err)
	}
	var cohesion float64
	if err := db.QueryRow("select `cohesion` from `people` where `id` = 'p1'").Scan(&cohesion); err != nil || cohesion != 0.5 {
		t.Errorf("cohesion %v, %v", cohesion, err)
	}
	if got := cover(); got != "thumb-b-big" {
		t.Errorf("chosen face %q", got)
	}
	if people, _ := d.ListPeople(0.3); len(people) != 1 || !people[0].CoverChosen {
		t.Errorf("not listed as chosen: %+v", people)
	}

	// A picture of the owner's own shows instead, and survives its face
	// going; a face chosen afterwards takes over from it.
	if err := d.SetPersonCover("p1", "", []byte("picture")); err != nil {
		t.Fatal(err)
	}
	if got := cover(); got != "picture" {
		t.Errorf("picture %q", got)
	}
	if err := d.SetPersonCover("p1", "f1", nil); err != nil {
		t.Fatal(err)
	}
	if got := cover(); got != "thumb-a" {
		t.Errorf("face after the picture %q", got)
	}

	// The chosen face taken off them: the device's choice again.
	if _, err := d.RemoveFromPerson("p1", []string{"A"}, ""); err != nil {
		t.Fatal(err)
	}
	var chosen int
	if err := db.QueryRow("select `cover_chosen` from `people` where `id` = 'p1'").Scan(&chosen); err != nil || chosen != 0 {
		t.Errorf("cover_chosen %d, %v after its face left", chosen, err)
	}
	if err := d.SetPersonCoverFace("p1", "f2", 0.8); err != nil {
		t.Fatal(err)
	}
	if got := cover(); got != "thumb-b-big" {
		t.Errorf("medoid again %q", got)
	}

	// Neither: back to the medoid; no such person: ErrNoSuchPerson.
	if err := d.SetPersonCover("p1", "", []byte("picture")); err != nil {
		t.Fatal(err)
	}
	if err := d.SetPersonCover("p1", "", nil); err != nil {
		t.Fatal(err)
	}
	if got := cover(); got != "thumb-b-big" {
		t.Errorf("reset %q", got)
	}
	if err := d.SetPersonCover("gone", "", nil); !errors.Is(err, ErrNoSuchPerson) {
		t.Errorf("no such person: %v", err)
	}
}
