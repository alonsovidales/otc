// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
)

// People added by hand against a real MySQL/MariaDB (OTC_TEST_MYSQL_DSN,
// temporary tables as outOfImagesTestDB's, with people_by_hand and the
// faces' quality): what a row makes of its content, and how it follows
// the content and its person.

func byHandTestDB(tb testing.TB) (*sql.DB, *Dao) {
	tb.Helper()
	db, d := outOfImagesTestDB(tb)
	for _, q := range []string{
		"create temporary table `people_by_hand` (`hash` varchar(64) not null, `person_id` varchar(36) not null, `created` datetime not null, primary key (`person_id`, `hash`), key (`hash`)) engine=InnoDB",
		"alter table `faces` add column `quality` float default null, add column `kind` tinyint not null default 0",
	} {
		if _, err := db.Exec(q); err != nil {
			tb.Fatal(err)
		}
	}
	return db, d
}

func execAll(tb testing.TB, db *sql.DB, queries ...string) {
	tb.Helper()
	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			tb.Fatalf("%s: %v", q, err)
		}
	}
}

// byHandRows is people_by_hand as "person:hash" pairs, sorted.
func byHandRows(tb testing.TB, db *sql.DB) string {
	tb.Helper()
	rows, err := db.Query("select `person_id`, `hash` from `people_by_hand`")
	if err != nil {
		tb.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p, h string
		if err := rows.Scan(&p, &h); err != nil {
			tb.Fatal(err)
		}
		out = append(out, p+":"+h)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

// peopleCounts is ListPeople's "id:count" for everyone, sorted.
func peopleCounts(tb testing.TB, d *Dao) string {
	tb.Helper()
	list, err := d.ListPeople(0.5)
	if err != nil {
		tb.Fatal(err)
	}
	var out []string
	for _, p := range list {
		out = append(out, fmt.Sprintf("%s:%d", p.Id, p.FaceCount))
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

func filePaths(files []*pb.File) string {
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	return pathsOf(paths)
}

// The by-hand library: A, B photos, V a video, T a text, K kept out of
// Images; Ana (named) has a face in B, p2 (unnamed) one in A.
func byHandLibrary(tb testing.TB) (*sql.DB, *Dao) {
	tb.Helper()
	db, d := byHandTestDB(tb)
	insertMediaRows(tb, db, "files",
		mediaRow{"A", "image/jpeg", "/a.jpg", month(1)},
		mediaRow{"B", "image/jpeg", "/b.jpg", month(2)},
		mediaRow{"V", "video/mp4", "/v.mp4", month(3)},
		mediaRow{"T", "text/plain", "/t.txt", month(4)},
		mediaRow{"K", "image/jpeg", "/Private/k.jpg", month(5)},
	)
	execAll(tb, db,
		"insert into `people` (`id`, `name`, `created`, `cover_face_id`) values ('ana', 'Ana', now(), 'fb'), ('p2', '', now(), null)",
		"insert into `faces` (`id`, `hash`, `person_id`, `bbox_x`, `bbox_y`, `bbox_w`, `bbox_h`, `embedding`, `thumbnail`, `created`) values"+
			" ('fb', 'B', 'ana', 0, 0, 10, 10, '', 'ana-crop', now()), ('fa', 'A', 'p2', 0, 0, 10, 10, '', 'p2-crop', now())",
		"insert into `skipped_analysis` values ('K', now())",
	)
	return db, d
}

// AddToPerson adds a person to the photos and videos a file holds - once,
// not where a face of theirs is, nor to a text or content kept out of
// Images - and says when none of the hashes was a photo or video at all.
func TestAddToPersonMySQL(t *testing.T) {
	db, d := byHandLibrary(t)

	added, held, err := d.AddToPerson("ana", []string{"A", "B", "V", "T", "K", "Z", "A"})
	if err != nil || added != 2 || !held {
		t.Fatalf("AddToPerson = %d, %v, %v; want 2 added", added, held, err)
	}
	if got := byHandRows(t, db); got != "ana:A ana:V" {
		t.Errorf("rows %s", got)
	}
	// Again: everyone there already, held.
	if added, held, err := d.AddToPerson("ana", []string{"A", "V"}); err != nil || added != 0 || !held {
		t.Errorf("again: %d, %v, %v", added, held, err)
	}
	// None of them a photo or video on the device.
	if added, held, err := d.AddToPerson("ana", []string{"Z", "T"}); err != nil || added != 0 || held {
		t.Errorf("nothing held: %d, %v, %v", added, held, err)
	}
	if _, _, err := d.AddToPerson("gone", []string{"A"}); !errors.Is(err, ErrNoSuchPerson) {
		t.Errorf("a person who is gone: %v", err)
	}
	if got := byHandRows(t, db); got != "ana:A ana:V" {
		t.Errorf("rows after the refusals %s", got)
	}

	// Without the table (a database release 179's script skipped):
	// ErrSchemaBehind, nothing changed.
	execAll(t, db, "drop temporary table `people_by_hand`")
	if _, _, err := d.AddToPerson("ana", []string{"A"}); !errors.Is(err, ErrSchemaBehind) {
		t.Errorf("without the table: %v", err)
	}
}

// A person added by hand is in the content as a face makes them: their
// photos in every Images read (a search, the date buckets, the browse by
// keyset, the lists read by index, the map's), People's count, the
// memories, Info.
func TestPeopleByHandAreListedMySQL(t *testing.T) {
	db, d := byHandLibrary(t)
	if _, _, err := d.AddToPerson("ana", []string{"A", "V"}); err != nil {
		t.Fatal(err)
	}
	execAll(t, db, "insert into `file_tags` values ('A', 'beach', 0.9), ('B', 'beach', 0.5)")

	search := func(ids ...string) string {
		files, err := d.SearchMedia("", nil, ids, "", true, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return filePaths(files)
	}
	if got := search("ana"); got != "/a.jpg,/b.jpg,/v.mp4" {
		t.Errorf("Ana's photos %s", got)
	}
	// AND: Ana by hand and p2 by a face, both in A.
	if got := search("ana", "p2"); got != "/a.jpg" {
		t.Errorf("Ana and p2 %s", got)
	}
	if got := search(""); got != "" {
		t.Errorf("no one's %s", got)
	}
	buckets, err := d.SearchMediaDateBuckets(nil, []string{"ana"}, "", true, nil)
	if err != nil || len(buckets) != 3 {
		t.Errorf("Ana's months %+v, %v", buckets, err)
	}
	var hashes []string
	if err := d.EachMatchingMediaHash(nil, []string{"ana"}, "", true, nil, func(h string) { hashes = append(hashes, h) }); err != nil || pathsOf(hashes) != "A,B,V" {
		t.Errorf("the map's %v, %v", hashes, err)
	}

	b := &MediaBrowse{MediaFilters: MediaFilters{PersonIDs: []string{"ana"}}}
	if got := filePaths(browseAll(t, d, b, 1)); got != "/a.jpg,/b.jpg,/v.mp4" {
		t.Errorf("browsed %s", got)
	}
	if n, err := d.EstimateMedia(&b.MediaFilters); err != nil || n != 3 {
		t.Errorf("estimate %d, %v", n, err)
	}
	list, ok, err := d.DatedMedia(b, 100)
	if err != nil || !ok || len(list) != 3 {
		t.Fatalf("DatedMedia %+v, %v, %v", list, ok, err)
	}
	files, _, err := d.MediaListChunk(b, list, 0, 10, ListDated)
	if err != nil || filePaths(files) != "/a.jpg,/b.jpg,/v.mp4" {
		t.Errorf("the dated list's photos %v, %v", filePaths(files), err)
	}
	ranked := &MediaFilters{Tags: []string{"beach"}, PersonIDs: []string{"ana"}}
	rank, err := d.RankMedia(ranked)
	if err != nil || len(rank) != 2 || rank[0].Hash != "A" {
		t.Errorf("ranked %+v, %v", rank, err)
	}

	// People: Ana's face in B and the photo and video she was added to.
	if got := peopleCounts(t, d); got != "ana:3 p2:1" {
		t.Errorf("people %s", got)
	}
	// The memories: once each.
	people, err := d.FacesOfHashes([]string{"A", "B", "V", "T"})
	if err != nil {
		t.Fatal(err)
	}
	var who []string
	for h, faces := range people {
		for _, f := range faces {
			who = append(who, h+":"+f.PersonID+":"+f.Name)
		}
	}
	if got := pathsOf(who); got != "A:ana:Ana,A:p2:,B:ana:Ana,V:ana:Ana" {
		t.Errorf("the memories' people %s", got)
	}
	// Info: in the order added, with the face People shows for them.
	time.Sleep(1100 * time.Millisecond) // the next second: created orders them
	if _, _, err := d.AddToPerson("p2", []string{"V"}); err != nil {
		t.Fatal(err)
	}
	in, err := d.PeopleByHandIn("V")
	if err != nil || len(in) != 2 {
		t.Fatalf("PeopleByHandIn %+v, %v", in, err)
	}
	if p := in[0]; p.PersonID != "ana" || p.Name != "Ana" || string(p.Thumbnail) != "ana-crop" {
		t.Errorf("first added %+v", p)
	}
	if p := in[1]; p.PersonID != "p2" || p.Name != "" || string(p.Thumbnail) != "p2-crop" {
		t.Errorf("then %+v", p)
	}
	// The faces found stay the faces found.
	if faces, err := d.FacesInPhoto("A"); err != nil || len(faces) != 1 || faces[0].PersonID != "p2" {
		t.Errorf("A's faces %+v, %v", faces, err)
	}
	if has, err := d.HashHasFaces("V"); err != nil || has {
		t.Errorf("HashHasFaces(V) = %v, %v: people added by hand aren't faces found", has, err)
	}
	if faces, err := d.ListFaceEmbeddings(); err != nil || len(faces) != 2 {
		t.Errorf("faces matched against %+v, %v", faces, err)
	}
}

// "Not this person" takes a person added by hand off the content, and
// "Someone else…" moves them - once a content: dropped where the other
// person is already, by hand or by a face. An unnamed person added by hand
// somewhere stays when their last face goes.
func TestRemoveFromPersonByHandMySQL(t *testing.T) {
	db, d := byHandLibrary(t)
	execAll(t, db, "insert into `people` (`id`, `name`, `created`) values ('p3', '', now())")
	for _, add := range []struct {
		person string
		hashes []string
	}{{"ana", []string{"A", "V"}}, {"p3", []string{"A", "B", "V"}}} {
		if _, _, err := d.AddToPerson(add.person, add.hashes); err != nil {
			t.Fatal(err)
		}
	}
	if got := byHandRows(t, db); got != "ana:A ana:V p3:A p3:B p3:V" {
		t.Fatalf("rows %s", got)
	}

	// Not this person, of a row: no face moves, the row goes.
	if faces, byHand, err := d.RemoveFromPerson("ana", []string{"A"}, ""); err != nil || faces != 0 || byHand != 1 {
		t.Fatalf("not this person: %d, %d, %v", faces, byHand, err)
	}
	if got := byHandRows(t, db); got != "ana:V p3:A p3:B p3:V" {
		t.Errorf("rows %s", got)
	}

	// Someone else: p3's A goes to Ana; B, where Ana has a face, and V,
	// where she is by hand already, are dropped. p3, unnamed with nothing
	// left, goes.
	if faces, byHand, err := d.RemoveFromPerson("p3", []string{"A", "B", "V"}, "ana"); err != nil || faces != 0 || byHand != 3 {
		t.Fatalf("to Ana: %d, %d, %v", faces, byHand, err)
	}
	if got := byHandRows(t, db); got != "ana:A ana:V" {
		t.Errorf("rows %s", got)
	}
	if got := peopleCounts(t, d); got != "ana:3 p2:1" {
		t.Errorf("people %s", got)
	}

	// p2 (unnamed) added by hand to V, then its only face taken off it: it
	// stays, for its row.
	if _, _, err := d.AddToPerson("p2", []string{"V"}); err != nil {
		t.Fatal(err)
	}
	if faces, byHand, err := d.RemoveFromPerson("p2", []string{"A"}, ""); err != nil || faces != 1 || byHand != 0 {
		t.Fatalf("p2's face: %d, %d, %v", faces, byHand, err)
	}
	if got := peopleCounts(t, d); got != "ana:3 p2:1" {
		t.Errorf("p2 with a row only: %s", got)
	}
	// Moved to Ana where she is by hand: her row stays, p2's goes, and p2
	// with nothing left goes too.
	if faces, byHand, err := d.RemoveFromPerson("p2", []string{"V"}, "ana"); err != nil || faces != 0 || byHand != 1 {
		t.Fatalf("p2's row to Ana: %d, %d, %v", faces, byHand, err)
	}
	if got := peopleCounts(t, d); got != "ana:3" {
		t.Errorf("people %s", got)
	}

	// A face moved to someone takes the place of their row there.
	execAll(t, db, "insert into `people` (`id`, `name`, `created`) values ('p4', 'Bo', now())",
		"insert into `faces` (`id`, `hash`, `person_id`, `bbox_x`, `bbox_y`, `bbox_w`, `bbox_h`, `embedding`, `thumbnail`, `created`) values ('fv', 'V', 'p4', 0, 0, 1, 1, '', '', now())")
	if faces, byHand, err := d.RemoveFromPerson("p4", []string{"V"}, "ana"); err != nil || faces != 1 || byHand != 0 {
		t.Fatalf("Bo's face to Ana: %d, %d, %v", faces, byHand, err)
	}
	if got := byHandRows(t, db); got != "ana:A" {
		t.Errorf("rows %s, want Ana's in V gone for her face", got)
	}
	if got := peopleCounts(t, d); got != "ana:3 p4:0" {
		t.Errorf("people %s", got)
	}
}

// A merge moves the rows to the target, once a content and never where
// the target has a face; a delete takes them; a fresh Reprocess wipes
// them all.
func TestMergeAndDeleteByHandMySQL(t *testing.T) {
	db, d := byHandLibrary(t)
	execAll(t, db, "insert into `people` (`id`, `name`, `created`) values ('p3', '', now()), ('p4', '', now())")
	for _, add := range []struct {
		person string
		hashes []string
	}{{"ana", []string{"V"}}, {"p3", []string{"B", "V"}}, {"p4", []string{"A", "V"}}} {
		if _, _, err := d.AddToPerson(add.person, add.hashes); err != nil {
			t.Fatal(err)
		}
	}
	// p3's B: Ana's face is there; V: Ana is there by hand; p4's V the
	// same, A: new for her.
	if err := d.MergePeople("ana", []string{"p3", "p4"}); err != nil {
		t.Fatal(err)
	}
	if got := byHandRows(t, db); got != "ana:A ana:V" {
		t.Errorf("rows %s", got)
	}
	// p2's face merged into Ana where she is by hand: the face takes it.
	if err := d.MergePeople("ana", []string{"p2"}); err != nil {
		t.Fatal(err)
	}
	if got := byHandRows(t, db); got != "ana:V" {
		t.Errorf("rows after p2's face %s", got)
	}
	if got := peopleCounts(t, d); got != "ana:3" {
		t.Errorf("people %s", got)
	}

	if err := d.DeletePerson("ana"); err != nil {
		t.Fatal(err)
	}
	if got := byHandRows(t, db); got != "" {
		t.Errorf("rows after the delete %s", got)
	}

	execAll(t, db, "insert into `people` (`id`, `name`, `created`) values ('p5', '', now())")
	if _, _, err := d.AddToPerson("p5", []string{"A"}); err != nil {
		t.Fatal(err)
	}
	if err := d.WipeTagsAndFaces(); err != nil {
		t.Fatal(err)
	}
	if got := byHandRows(t, db); got != "" {
		t.Errorf("rows after the wipe %s", got)
	}
}

// The rows go with their content: its last file deleted (DelFacesByHash,
// the orphan sweep), or kept out of Images (DelFacesByHashes); an unnamed
// person added by hand nowhere else goes with them, a named one stays.
func TestPeopleByHandGoWithTheContentMySQL(t *testing.T) {
	db, d := byHandLibrary(t)
	execAll(t, db, "insert into `people` (`id`, `name`, `created`) values ('p3', '', now())")
	for _, add := range []struct {
		person string
		hashes []string
	}{{"ana", []string{"A", "V"}}, {"p3", []string{"V"}}, {"p2", []string{"B"}}} {
		if _, _, err := d.AddToPerson(add.person, add.hashes); err != nil {
			t.Fatal(err)
		}
	}

	// A's file goes: Ana's row and p2's face there go; p2 (unnamed) stays
	// for its row in B.
	execAll(t, db, "delete from `files` where `hash` = 'A'")
	if gone, err := d.DelFacesByHash("A"); err != nil || len(gone) != 1 || gone[0].ID != "fa" {
		t.Fatalf("DelFacesByHash(A) = %+v, %v", gone, err)
	}
	if got := byHandRows(t, db); got != "ana:V p2:B p3:V" {
		t.Errorf("rows %s", got)
	}
	if got := peopleCounts(t, d); got != "ana:2 p2:1 p3:1" {
		t.Errorf("people %s", got)
	}

	// V's file goes before its rows did (a delete from before release
	// 179's binary, say): the orphan sweep finds it; with rows alone, the
	// unnamed p3 goes, Ana stays.
	execAll(t, db, "delete from `files` where `hash` = 'V'")
	orphans, err := d.OrphanFaceHashes()
	if err != nil || pathsOf(orphans) != "V" {
		t.Fatalf("orphans %v, %v", orphans, err)
	}
	if gone, err := d.DelFacesByHash("V"); err != nil || len(gone) != 0 {
		t.Fatalf("DelFacesByHash(V) = %+v, %v", gone, err)
	}
	if got := byHandRows(t, db); got != "p2:B" {
		t.Errorf("rows %s", got)
	}
	if got := peopleCounts(t, d); got != "ana:1 p2:1" {
		t.Errorf("people %s", got)
	}

	// B kept out of Images: its faces and rows go; p2 with nothing goes.
	gone, err := d.DelFacesByHashes([]string{"B", "T"})
	if err != nil || len(gone) != 1 || gone[0].ID != "fb" {
		t.Fatalf("DelFacesByHashes = %+v, %v", gone, err)
	}
	if got := byHandRows(t, db); got != "" {
		t.Errorf("rows %s", got)
	}
	if got := peopleCounts(t, d); got != "ana:0" {
		t.Errorf("people %s", got)
	}
}

// A face of the person stored in the content takes the place of their row
// there (AddFace, CopyFaces); an edit keeps the people added by hand to
// what it was made from (CopyPeopleByHand), but where a face of theirs is.
func TestFacesTakeThePlaceOfRowsMySQL(t *testing.T) {
	db, d := byHandLibrary(t)
	if _, _, err := d.AddToPerson("ana", []string{"A", "V"}); err != nil {
		t.Fatal(err)
	}
	if err := d.AddFace("fa2", "A", "ana", 5, 5, 10, 10, []byte{}, []byte("crop"), 0.5); err != nil {
		t.Fatal(err)
	}
	if got := byHandRows(t, db); got != "ana:V" {
		t.Errorf("rows %s", got)
	}

	// An edit E of A, which p2 was added to by hand: the faces copied to
	// it (p2's, Ana's) take the place of p2's row there. V's people added
	// by hand go to its cut W.
	insertMediaRows(t, db, "files", mediaRow{"E", "image/jpeg", "/e.jpg", month(6)}, mediaRow{"W", "video/mp4", "/w.mp4", month(6)})
	if _, _, err := d.AddToPerson("p2", []string{"E"}); err != nil {
		t.Fatal(err)
	}
	keep := func(b FaceBox) (FaceBox, bool) { return b, true }
	n := 0
	if copied, err := d.CopyFaces("A", "E", keep, func() string { n++; return fmt.Sprint("e", n) }); err != nil || copied != 2 {
		t.Fatalf("CopyFaces = %d, %v", copied, err)
	}
	if got := byHandRows(t, db); got != "ana:V" {
		t.Errorf("rows %s: p2's row in E is still there with their face", got)
	}
	if copied, err := d.CopyPeopleByHand("V", "W"); err != nil || copied != 1 {
		t.Fatalf("CopyPeopleByHand = %d, %v", copied, err)
	}
	if copied, err := d.CopyPeopleByHand("V", "W"); err != nil || copied != 0 {
		t.Errorf("again: %d, %v", copied, err)
	}
	// Not where a face of theirs is.
	if copied, err := d.CopyPeopleByHand("V", "A"); err != nil || copied != 0 {
		t.Errorf("onto Ana's face: %d, %v", copied, err)
	}
	if got := byHandRows(t, db); got != "ana:V ana:W" {
		t.Errorf("rows %s", got)
	}
}

// On a database without people_by_hand everything reads as before and
// nothing fails: no one is added by hand there.
func TestNoByHandTableMySQL(t *testing.T) {
	db, d := outOfImagesTestDB(t)
	insertMediaRows(t, db, "files", mediaRow{"A", "image/jpeg", "/a.jpg", month(1)}, mediaRow{"B", "image/jpeg", "/b.jpg", month(2)})
	execAll(t, db,
		"insert into `people` (`id`, `name`, `created`) values ('ana', 'Ana', now()), ('p2', '', now())",
		"insert into `faces` values ('fa', 'A', 'ana', 0, 0, 1, 1, '', 'crop', now()), ('fb', 'B', 'p2', 0, 0, 1, 1, '', '', now())",
	)
	files, err := d.SearchMedia("", nil, []string{"ana"}, "", true, nil, nil)
	if err != nil || filePaths(files) != "/a.jpg" {
		t.Errorf("Ana's photos %v, %v", filePaths(files), err)
	}
	b := &MediaBrowse{MediaFilters: MediaFilters{PersonIDs: []string{"ana"}}}
	if got := filePaths(browseAll(t, d, b, 1)); got != "/a.jpg" {
		t.Errorf("browsed %s", got)
	}
	if list, ok, err := d.DatedMedia(b, 100); err != nil || !ok || len(list) != 1 {
		t.Errorf("DatedMedia %+v, %v, %v", list, ok, err)
	}
	if got := peopleCounts(t, d); got != "ana:1 p2:1" {
		t.Errorf("people %s", got)
	}
	if people, err := d.FacesOfHashes([]string{"A"}); err != nil || len(people["A"]) != 1 {
		t.Errorf("the memories' people %+v, %v", people, err)
	}
	if in, err := d.PeopleByHandIn("A"); err != nil || len(in) != 0 {
		t.Errorf("PeopleByHandIn %+v, %v", in, err)
	}
	if _, err := d.CopyPeopleByHand("A", "B"); err != nil {
		t.Errorf("CopyPeopleByHand: %v", err)
	}
	if faces, byHand, err := d.RemoveFromPerson("ana", []string{"A"}, "p2"); err != nil || faces != 1 || byHand != 0 {
		t.Errorf("RemoveFromPerson %d, %d, %v", faces, byHand, err)
	}
	if err := d.MergePeople("ana", []string{"p2"}); err != nil {
		t.Errorf("MergePeople: %v", err)
	}
	if gone, err := d.DelFacesByHash("A"); err != nil || len(gone) != 1 {
		t.Errorf("DelFacesByHash %+v, %v", gone, err)
	}
	if orphans, err := d.OrphanFaceHashes(); err != nil || len(orphans) != 0 {
		t.Errorf("orphans %v, %v", orphans, err)
	}
	if err := d.DeletePerson("ana"); err != nil {
		t.Errorf("DeletePerson: %v", err)
	}
	if err := d.WipeTagsAndFaces(); err != nil {
		t.Errorf("WipeTagsAndFaces: %v", err)
	}
}
