// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"errors"
	"strings"
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

// The faces' looks, against a real MySQL/MariaDB: BestFaces ranks a
// person's measured faces by their own looks and their photo's score, and
// FacesToMeasure lists the faces of someone not measured yet, in id order.
func TestBestFacesMySQL(t *testing.T) {
	db, d := outOfImagesTestDB(t)
	for _, q := range []string{
		"alter table `faces` add column `quality` float default null",
		"create temporary table `photo_quality` (`hash` varchar(64) not null, `aesthetic` float null, `sharpness` float not null, `dark` float not null, `bright` float not null, `brightness` float not null, `width` int not null, `height` int not null, `camera` tinyint(1) null, `scored` datetime not null, primary key (`hash`)) engine=InnoDB",
		"create temporary table `settings` (`face_quality_done` tinyint(1) not null default 0) engine=InnoDB",
		"insert into `settings` values (0)",
		"insert into `faces` values" +
			" ('f1', 'A', 'p1', 0, 0, 10, 10, 'e1', 't1', now(), 0.70)," +
			" ('f2', 'B', 'p1', 0, 0, 10, 10, 'e2', 't2', now(), 0.70)," +
			" ('f3', 'C', 'p1', 0, 0, 10, 10, 'e3', 't3', now(), 0.90)," +
			" ('f4', 'D', 'p1', 0, 0, 10, 10, 'e4', 't4', now(), null)," +
			" ('f5', 'E', 'p2', 0, 0, 10, 10, 'e5', 't5', now(), 0.99)," +
			" ('f6', 'F', '', 0, 0, 10, 10, 'e6', 't6', now(), null)," +
			" ('f7', 'G', 'p2', 0, 0, 12, 14, 'e7', 't7', now(), null)",
		// B's photo is a beauty, A's isn't: same face looks, B first.
		"insert into `photo_quality` (`hash`, `aesthetic`, `sharpness`, `dark`, `bright`, `brightness`, `width`, `height`, `scored`) values" +
			" ('A', 3, 0, 0, 0, 0, 1, 1, now()), ('B', 7, 0, 0, 0, 0, 1, 1, now())",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	got, err := d.BestFaces("p1", 10)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range got {
		ids = append(ids, c.ID)
	}
	// f3: 0.8*0.9 + 0.2*0.5 = 0.82; f2: 0.56 + 0.2 = 0.76; f1: 0.56 + 0 = 0.56;
	// f4 not measured.
	if strings.Join(ids, ",") != "f3,f2,f1" || string(got[0].Embedding) != "e3" || got[0].Quality < 0.89 {
		t.Errorf("BestFaces = %v (%+v)", ids, got)
	}
	if got, _ := d.BestFaces("p1", 1); len(got) != 1 || got[0].ID != "f3" {
		t.Errorf("limit 1: %+v", got)
	}

	todo, err := d.FacesToMeasure("", 10)
	if err != nil || len(todo) != 2 || todo[0].ID != "f4" || todo[1].ID != "f7" || todo[1].W != 12 || todo[1].H != 14 || string(todo[1].Thumbnail) != "t7" {
		t.Fatalf("FacesToMeasure = %+v, %v (f6 is no one's)", todo, err)
	}
	if n, err := d.CountFacesToMeasure(); err != nil || n != 2 {
		t.Errorf("CountFacesToMeasure = %d, %v", n, err)
	}
	if after, _ := d.FacesToMeasure("f4", 10); len(after) != 1 || after[0].ID != "f7" {
		t.Errorf("after f4: %+v", after)
	}
	if err := d.SetFaceQuality("f4", 0.5); err != nil {
		t.Fatal(err)
	}
	if n, _ := d.CountFacesToMeasure(); n != 1 {
		t.Errorf("%d left after measuring f4", n)
	}

	if done, err := d.FaceQualityDone(); err != nil || done {
		t.Errorf("done %v, %v before the pass", done, err)
	}
	if err := d.SetFaceQualityDone(); err != nil {
		t.Fatal(err)
	}
	if done, _ := d.FaceQualityDone(); !done {
		t.Error("not done after SetFaceQualityDone")
	}
}
