// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"errors"
	"fmt"
	"sort"
	"testing"
)

// "Not this person" and "Someone else…" against a real MySQL/MariaDB
// (OTC_TEST_MYSQL_DSN, temporary tables as outOfImagesTestDB's): what the
// faces taken off a person become for People, search, face matching and
// the content's own lifetime.
func TestRemoveFromPersonMySQL(t *testing.T) {
	db, d := outOfImagesTestDB(t)
	insertMediaRows(t, db, "files",
		mediaRow{"A", "image/jpeg", "/a.jpg", month(1)},
		mediaRow{"B", "image/jpeg", "/b.jpg", month(2)},
		mediaRow{"C", "image/jpeg", "/c.jpg", month(3)},
		mediaRow{"D", "image/jpeg", "/d.jpg", month(4)},
	)
	for _, q := range []string{
		// p1: unnamed, in A (its cover), B and C. p2: Ana, in D (its
		// cover). p3: unnamed, in C too.
		"insert into `people` (`id`, `name`, `created`, `cover_face_id`) values ('p1', '', now(), 'f1'), ('p2', 'Ana', now(), 'f4'), ('p3', '', now(), null)",
		"insert into `faces` values ('f1', 'A', 'p1', 0, 0, 1, 1, '', '', now()), ('f2', 'B', 'p1', 0, 0, 1, 1, '', '', now()), ('f3', 'C', 'p1', 0, 0, 1, 1, '', '', now()), ('f4', 'D', 'p2', 0, 0, 1, 1, '', '', now()), ('f5', 'C', 'p3', 5, 5, 1, 1, '', '', now())",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	people := func() string {
		list, err := d.ListPeople(0.5)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, p := range list {
			out = append(out, fmt.Sprintf("%s:%d", p.Id, p.FaceCount))
		}
		sort.Strings(out)
		return fmt.Sprint(out)
	}
	covers := func() map[string]string {
		rows, err := db.Query("select `id`, coalesce(`cover_face_id`, '') from `people`")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		m := map[string]string{}
		for rows.Next() {
			var id, cover string
			if err := rows.Scan(&id, &cover); err != nil {
				t.Fatal(err)
			}
			m[id] = cover
		}
		return m
	}
	photosOf := func(ids ...string) string {
		files, err := d.SearchMedia("", nil, ids, "", true, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		var paths []string
		for _, f := range files {
			paths = append(paths, f.Path)
		}
		return pathsOf(paths)
	}
	matchable := func() string {
		faces, err := d.ListFaceEmbeddings()
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, f := range faces {
			ids = append(ids, f.ID)
		}
		return pathsOf(ids)
	}

	// Not this person: A (whose face is p1's cover) and a photo p1 isn't
	// in leave p1. Its cover is cleared; it keeps B and C.
	if moved, err := d.RemoveFromPerson("p1", []string{"A", "D"}, ""); err != nil || moved != 1 {
		t.Fatalf("not this person: %d, %v", moved, err)
	}
	if got := people(); got != "[p1:2 p2:1 p3:1]" {
		t.Errorf("people %s", got)
	}
	if got := covers()["p1"]; got != "" {
		t.Errorf("p1's cover %q, want none", got)
	}
	if got := photosOf("p1"); got != "/b.jpg,/c.jpg" {
		t.Errorf("p1's photos %s", got)
	}
	// The face is no one's: not matched against, not searched by, but the
	// photo still has its faces found, so it isn't analysed again.
	if got := matchable(); got != "f2,f3,f4,f5" {
		t.Errorf("faces matched against %s", got)
	}
	if got := photosOf(""); got != "" {
		t.Errorf("the photos of no one: %s", got)
	}
	if has, err := d.HashHasFaces("A"); err != nil || !has {
		t.Errorf("HashHasFaces(A) = %v, %v; want true", has, err)
	}

	// Someone else: C goes from p1 to Ana; p3's own face in C stays p3's.
	if moved, err := d.RemoveFromPerson("p1", []string{"C"}, "p2"); err != nil || moved != 1 {
		t.Fatalf("someone else: %d, %v", moved, err)
	}
	if got := photosOf("p2"); got != "/c.jpg,/d.jpg" {
		t.Errorf("Ana's photos %s", got)
	}
	if got := photosOf("p2", "p3"); got != "/c.jpg" {
		t.Errorf("Ana and p3's photos %s", got)
	}
	if got := covers()["p2"]; got != "f4" {
		t.Errorf("Ana's cover %q, want f4 kept", got)
	}

	// To someone who is gone: nothing moves.
	if _, err := d.RemoveFromPerson("p1", []string{"B"}, "nobody"); !errors.Is(err, ErrNoSuchPerson) {
		t.Errorf("to a person who is gone: %v", err)
	}
	if got := photosOf("p1"); got != "/b.jpg" {
		t.Errorf("p1's photos after a refused move %s", got)
	}

	// p1, unnamed, loses its last face to Ana and goes; Ana, named, loses
	// every face and stays, with no cover.
	if moved, err := d.RemoveFromPerson("p1", []string{"B"}, "p2"); err != nil || moved != 1 {
		t.Fatalf("p1's last face: %d, %v", moved, err)
	}
	if moved, err := d.RemoveFromPerson("p2", []string{"B", "C", "D"}, ""); err != nil || moved != 3 {
		t.Fatalf("all of Ana's: %d, %v", moved, err)
	}
	if got := people(); got != "[p2:0 p3:1]" {
		t.Errorf("people %s", got)
	}
	if got := covers()["p2"]; got != "" {
		t.Errorf("Ana's cover %q, want none", got)
	}

	// No one is never deleted nor merged into someone.
	if err := d.DeletePerson(""); err != nil {
		t.Fatal(err)
	}
	if err := d.MergePeople("p3", []string{""}); err != nil {
		t.Fatal(err)
	}
	var noOne int
	if err := db.QueryRow("select count(*) from `faces` where `person_id` = ''").Scan(&noOne); err != nil || noOne != 4 {
		t.Errorf("%d faces of no one, want 4 (%v)", noOne, err)
	}

	// They go with their content, as every face does.
	if _, err := d.DelFacesByHash("A"); err != nil {
		t.Fatal(err)
	}
	if has, err := d.HashHasFaces("A"); err != nil || has {
		t.Errorf("HashHasFaces(A) after its content went = %v, %v", has, err)
	}
	if got := people(); got != "[p2:0 p3:1]" {
		t.Errorf("people after A went %s", got)
	}
}
