// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import "testing"

// A photo's faces against a real MySQL/MariaDB (OTC_TEST_MYSQL_DSN, as
// out_of_images_mysql_test.go): left to right, with their people's names
// ("" while unnamed), crops and boxes; a face taken off everyone and
// another photo's faces aren't listed.
func TestFacesInPhotoMySQL(t *testing.T) {
	db, d := outOfImagesTestDB(t)
	for _, q := range []string{
		"insert into `people` (`id`, `name`, `created`) values ('ana', 'Ana', now()), ('p2', '', now())",
		"insert into `faces` values" +
			" ('f1', 'A', 'p2', 300, 10, 40, 40, '', 'crop-right', now())," +
			" ('f2', 'A', 'ana', 20, 15, 50, 60, '', 'crop-left', now())," +
			" ('f3', 'A', '', 150, 10, 30, 30, '', 'crop-removed', now())," +
			" ('f4', 'B', 'ana', 0, 0, 10, 10, '', 'crop-other', now())",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	faces, err := d.FacesInPhoto("A")
	if err != nil {
		t.Fatal(err)
	}
	if len(faces) != 2 {
		t.Fatalf("faces: %+v", faces)
	}
	if f := faces[0]; f.PersonID != "ana" || f.Name != "Ana" || string(f.Thumbnail) != "crop-left" || f.X != 20 || f.Y != 15 || f.W != 50 || f.H != 60 {
		t.Errorf("left face: %+v", f)
	}
	if f := faces[1]; f.PersonID != "p2" || f.Name != "" || string(f.Thumbnail) != "crop-right" {
		t.Errorf("right face: %+v", f)
	}
	if faces, err := d.FacesInPhoto("none"); err != nil || len(faces) != 0 {
		t.Errorf("a photo without faces: %+v, %v", faces, err)
	}
}
