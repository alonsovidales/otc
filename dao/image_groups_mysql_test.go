// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"strings"
	"testing"
)

// Remove from collection, against a real MySQL/MariaDB (OTC_TEST_MYSQL_DSN,
// as out_of_images_mysql_test.go): the membership goes, never a picture.
func TestRemovePathsFromImageGroupMySQL(t *testing.T) {
	db, d := outOfImagesTestDB(t)
	insertMediaRows(t, db, "files",
		mediaRow{"A", "image/jpeg", "/Phone/a.jpg", month(1)},
		mediaRow{"A", "image/jpeg", "/Backup/a.jpg", month(1)},
		mediaRow{"B", "image/jpeg", "/Phone/b.jpg", month(1)},
		mediaRow{"C", "image/jpeg", "/Phone/c.jpg", month(1)},
	)
	for _, q := range []string{
		"insert into `image_groups` values ('g1', 'Trip', now()), ('g2', 'Beach', now())",
		"insert into `image_group_files` values ('g1', 'A', now()), ('g1', 'B', now()), ('g1', 'C', now()), ('g2', 'A', now())",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	members := func() string {
		t.Helper()
		rows, err := db.Query("select concat(`group_id`, ':', `hash`) from `image_group_files` order by 1")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var got []string
		for rows.Next() {
			var m string
			if err := rows.Scan(&m); err != nil {
				t.Fatal(err)
			}
			got = append(got, m)
		}
		return strings.Join(got, ",")
	}

	// One copy of A is enough to take the photo out (issue #201: a member
	// is a photo); a path that isn't a member, or doesn't exist, is ignored.
	if err := d.RemovePathsFromImageGroup("g1", []string{"/Backup/a.jpg", "/Phone/c.jpg", "/Nowhere/x.jpg"}); err != nil {
		t.Fatal(err)
	}
	if got, want := members(), "g1:B,g2:A"; got != want {
		t.Errorf("members %s, want %s (the other collection keeps A)", got, want)
	}
	var files int
	if err := db.QueryRow("select count(*) from `files`").Scan(&files); err != nil || files != 4 {
		t.Errorf("files %d, %v: the pictures must stay", files, err)
	}

	// Again, and with nothing: no error, nothing changes.
	for _, paths := range [][]string{{"/Phone/a.jpg"}, nil} {
		if err := d.RemovePathsFromImageGroup("g1", paths); err != nil {
			t.Fatal(err)
		}
	}
	if got := members(); got != "g1:B,g2:A" {
		t.Errorf("members %s after removing again", got)
	}
}

// Favourites, against a real MySQL/MariaDB: one row however often it is
// ensured, listed first, and a photo is a favourite through any of its
// copies.
func TestFavouritesMySQL(t *testing.T) {
	db, d := outOfImagesTestDB(t)
	insertMediaRows(t, db, "files",
		mediaRow{"A", "image/jpeg", "/Phone/a.jpg", month(1)},
		mediaRow{"A", "image/jpeg", "/Backup/a.jpg", month(1)},
		mediaRow{"B", "image/jpeg", "/Phone/b.jpg", month(1)},
	)
	if _, err := db.Exec("insert into `image_groups` values ('g1', 'Trip', now() + interval 1 day)"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := d.EnsureFavourites(); err != nil {
			t.Fatal(err)
		}
	}
	groups, err := d.ListImageGroups(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || !groups[0].IsFavourites() || groups[1].ID != "g1" {
		t.Fatalf("groups %+v, want Favourites first and then g1, newer", groups)
	}

	if err := d.AddPathsToImageGroup(FavouritesGroupID, []string{"/Phone/a.jpg"}); err != nil {
		t.Fatal(err)
	}
	got, err := d.FavouritePaths([]string{"/Backup/a.jpg", "/Phone/b.jpg", "/Nowhere/x.jpg"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "/Backup/a.jpg" {
		t.Errorf("favourite paths %v, want the other copy of A only", got)
	}
	if got, err := d.FavouritePaths(nil); err != nil || got != nil {
		t.Errorf("no paths: %v, %v", got, err)
	}
}
