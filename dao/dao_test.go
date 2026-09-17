// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// SearchMedia composes whichever filters the Photo Gallery's search bar
// actually applied (issue #52 follow-up: person filters live there now,
// not a separate People screen) - these pin its query-building logic
// (which joins get added, args land in the right order, and multiple
// people are AND'd via a HAVING count, not OR'd) without needing a real
// database.
func TestSearchMediaNoFiltersImagesOnly(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("from `files` as `f` where `f`\\.`mime` like 'image%' order by `f`\\.`created` desc").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}))

	d := NewWithDB(db)
	if _, err := d.SearchMedia("", nil, nil, true, nil); err != nil {
		t.Fatalf("SearchMedia: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

func TestSearchMediaTagsOnlyOrdersByScore(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("join `file_tags` as `tg` on `tg`\\.`hash` = `f`\\.`hash` and `tg`\\.`tag` in \\(\\?,\\?\\).*group by `f`\\.`hash`.*order by `score` desc").
		WithArgs("dogs", "beach").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size", "score"}))

	d := NewWithDB(db)
	if _, err := d.SearchMedia("", []string{"dogs", "beach"}, nil, true, nil); err != nil {
		t.Fatalf("SearchMedia: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

// Two selected people must AND, not OR - a photo of just one of them
// doesn't qualify.
func TestSearchMediaMultiplePeopleRequiresAllOfThem(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("join `faces` as `fc` on `fc`\\.`hash` = `f`\\.`hash` and `fc`\\.`person_id` in \\(\\?,\\?\\).*group by `f`\\.`hash`.*having count\\(distinct `fc`\\.`person_id`\\) = 2").
		WithArgs("alice-id", "bob-id").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}))

	d := NewWithDB(db)
	if _, err := d.SearchMedia("", nil, []string{"alice-id", "bob-id"}, true, nil); err != nil {
		t.Fatalf("SearchMedia: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

// "person X with dogs": tags and a person filter combine on one request,
// args bound in the order their placeholders actually appear in the
// assembled query (tags' join first, then the person join).
func TestSearchMediaCombinesTagsAndPerson(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(
		"join `file_tags` as `tg` on `tg`\\.`hash` = `f`\\.`hash` and `tg`\\.`tag` in \\(\\?\\) "+
			"join `faces` as `fc` on `fc`\\.`hash` = `f`\\.`hash` and `fc`\\.`person_id` in \\(\\?\\).*"+
			"having count\\(distinct `fc`\\.`person_id`\\) = 1 order by `score` desc").
		WithArgs("dogs", "alice-id").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size", "score"}))

	d := NewWithDB(db)
	if _, err := d.SearchMedia("", []string{"dogs"}, []string{"alice-id"}, true, nil); err != nil {
		t.Fatalf("SearchMedia: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

// imagesOnly must be ignored once any filter is applied - matches the old
// SearchByTags/SearchByPerson behavior (neither ever filtered by mime),
// only the plain "browse everything" case respects it.
func TestSearchMediaImagesOnlyIgnoredWhenFiltersApplied(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("join `faces`").
		WithArgs("alice-id").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}))

	d := NewWithDB(db)
	if _, err := d.SearchMedia("", nil, []string{"alice-id"}, true, nil); err != nil {
		t.Fatalf("SearchMedia: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran (imagesOnly should be ignored, no mime filter expected in the query): %v", err)
	}
}

// Issue #77: the date scrubber's "jump to date" adds a created<=? cutoff
// as the last WHERE part regardless of which other filters are active, so
// its arg must land last too.
func TestSearchMediaBeforeCutoffAddsWhereClause(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	before := time.Date(2022, 6, 30, 23, 59, 59, 0, time.UTC)
	mock.ExpectQuery("where `f`\\.`mime` like 'image%' and `f`\\.`created` <= \\? order by `f`\\.`created` desc").
		WithArgs(before).
		WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}))

	d := NewWithDB(db)
	if _, err := d.SearchMedia("", nil, nil, true, &before); err != nil {
		t.Fatalf("SearchMedia: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

// SearchMediaDateBuckets backs the date scrubber's tick marks/placeholder
// counts (issue #77) - pins that it buckets by month via a subquery
// (settling which files match first) rather than grouping the raw joined
// rows directly, which would double-count/miscount whenever a join can
// produce more than one row per file (tags, multi-person AND matches).
func TestSearchMediaDateBucketsNoFilters(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("select date_format\\(`month_src`\\.`created`, '%Y-%m'\\) as `bucket`, count\\(\\*\\) from \\(" +
		"select `f`\\.`hash`, `f`\\.`created` from `files` as `f` where `f`\\.`mime` like 'image%'" +
		"\\) as `month_src` group by `bucket` order by `bucket` desc").
		WillReturnRows(sqlmock.NewRows([]string{"bucket", "count"}).
			AddRow("2022-06", 3).
			AddRow("2022-05", 1))

	d := NewWithDB(db)
	buckets, err := d.SearchMediaDateBuckets(nil, nil, true)
	if err != nil {
		t.Fatalf("SearchMediaDateBuckets: %v", err)
	}
	if len(buckets) != 2 || buckets[0].Month != "2022-06" || buckets[0].Count != 3 {
		t.Fatalf("unexpected buckets: %+v", buckets)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

// Two selected people must still AND within a single file even once
// bucketed by month - the subquery's own group-by-hash/having (identical
// to SearchMedia's) has to run before the outer month grouping, or a month
// with two files each matching only one of two requested people would
// wrongly count as a match.
func TestSearchMediaDateBucketsMultiplePeopleRequiresAllOfThem(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("select `f`\\.`hash`, `f`\\.`created` from `files` as `f` "+
		"join `faces` as `fc` on `fc`\\.`hash` = `f`\\.`hash` and `fc`\\.`person_id` in \\(\\?,\\?\\) "+
		"group by `f`\\.`hash` having count\\(distinct `fc`\\.`person_id`\\) = 2"+
		"\\) as `month_src` group by `bucket` order by `bucket` desc").
		WithArgs("alice-id", "bob-id").
		WillReturnRows(sqlmock.NewRows([]string{"bucket", "count"}))

	d := NewWithDB(db)
	if _, err := d.SearchMediaDateBuckets(nil, []string{"alice-id", "bob-id"}, true); err != nil {
		t.Fatalf("SearchMediaDateBuckets: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

// ListRawFaces/UpdateFaceEncryption back files_manager.MigrateLegacyFace
// Encryption's one-off re-encryption sweep (issue #52 follow-up) - see
// that function's doc comment for why it exists.
func TestListRawFaces(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("select .* from `faces`").
		WillReturnRows(sqlmock.NewRows([]string{"id", "embedding", "thumbnail"}).
			AddRow("face-1", []byte("emb-1"), []byte("thumb-1")).
			AddRow("face-2", []byte("emb-2"), []byte(nil)))

	d := NewWithDB(db)
	faces, err := d.ListRawFaces()
	if err != nil {
		t.Fatalf("ListRawFaces: %v", err)
	}
	if len(faces) != 2 {
		t.Fatalf("ListRawFaces returned %d rows, want 2", len(faces))
	}
	if faces[0].ID != "face-1" || string(faces[0].Embedding) != "emb-1" || string(faces[0].Thumbnail) != "thumb-1" {
		t.Errorf("faces[0] = %+v, want id=face-1 embedding=emb-1 thumbnail=thumb-1", faces[0])
	}
	if faces[1].ID != "face-2" || len(faces[1].Thumbnail) != 0 {
		t.Errorf("faces[1] = %+v, want id=face-2 with no thumbnail", faces[1])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

func TestUpdateFaceEncryption(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec("update `faces` set `embedding` = \\?, `thumbnail` = \\? where `id` = \\?").
		WithArgs([]byte("new-emb"), []byte("new-thumb"), "face-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	d := NewWithDB(db)
	if err := d.UpdateFaceEncryption("face-1", []byte("new-emb"), []byte("new-thumb")); err != nil {
		t.Fatalf("UpdateFaceEncryption: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

// Issue #75: most-photographed person first, not most-recently-created.
func TestListPeopleOrdersByFaceCountDesc(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// 0.363 mirrors face_recognition.SamePersonThreshold - not imported
	// here (or anywhere else in dao) since face_recognition pulls in
	// CGO/OpenCV, which this package is deliberately free of.
	const cohesionThreshold = 0.363
	mock.ExpectQuery("select .* from `people`.*order by \\(`p`\\.`cohesion`.*`face_count` desc, `p`\\.`created` desc").
		WithArgs(cohesionThreshold).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "face_count", "cover_face_id"}).
			AddRow("alice-id", "Alice", 5, nil).
			AddRow("bob-id", "Bob", 1, nil))
	// No cover_face_id yet for either - ListPeople falls back to each
	// person's oldest face (see TestListPeoplePrefersMedoidCoverFace for
	// the case where one's already been computed).
	mock.ExpectQuery("select `thumbnail` from `faces` where `person_id` = \\?").WithArgs("alice-id").
		WillReturnRows(sqlmock.NewRows([]string{"thumbnail"}).AddRow([]byte("thumb-a")))
	mock.ExpectQuery("select `thumbnail` from `faces` where `person_id` = \\?").WithArgs("bob-id").
		WillReturnRows(sqlmock.NewRows([]string{"thumbnail"}).AddRow([]byte("thumb-b")))

	d := NewWithDB(db)
	people, err := d.ListPeople(cohesionThreshold)
	if err != nil {
		t.Fatalf("ListPeople: %v", err)
	}
	if len(people) != 2 || people[0].Id != "alice-id" || people[1].Id != "bob-id" {
		t.Fatalf("ListPeople() = %+v, want alice-id (5 faces) before bob-id (1 face)", people)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

// Once a person has a computed medoid (face_recognition.MedoidFaceID, set
// via SetPersonCoverFace), ListPeople must look their cover thumbnail up
// by that specific face id, not fall back to "oldest face" - the whole
// point of the medoid pick is to show a better thumbnail than that
// arbitrary default.
func TestListPeoplePrefersMedoidCoverFace(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("select .* from `people`.*order by \\(`p`\\.`cohesion`.*`face_count` desc, `p`\\.`created` desc").
		WithArgs(0.363).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "face_count", "cover_face_id"}).
			AddRow("alice-id", "Alice", 5, "face-42"))
	mock.ExpectQuery("select `thumbnail` from `faces` where `id` = \\?").WithArgs("face-42").
		WillReturnRows(sqlmock.NewRows([]string{"thumbnail"}).AddRow([]byte("thumb-medoid")))

	d := NewWithDB(db)
	people, err := d.ListPeople(0.363)
	if err != nil {
		t.Fatalf("ListPeople: %v", err)
	}
	if len(people) != 1 || string(people[0].CoverThumbnail) != "thumb-medoid" {
		t.Fatalf("ListPeople() = %+v, want the medoid face's thumbnail", people)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran (should look up by face id, not fall back to oldest-face): %v", err)
	}
}

func TestSetPersonCoverFace(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec("update `people` set `cover_face_id` = \\?, `cohesion` = \\? where `id` = \\?").
		WithArgs("face-1", float32(0.82), "alice-id").
		WillReturnResult(sqlmock.NewResult(0, 1))

	d := NewWithDB(db)
	if err := d.SetPersonCoverFace("alice-id", "face-1", 0.82); err != nil {
		t.Fatalf("SetPersonCoverFace: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

func TestListFaceEmbeddingsIncludesID(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("select `id`, `person_id`, `embedding` from `faces`").
		WillReturnRows(sqlmock.NewRows([]string{"id", "person_id", "embedding"}).
			AddRow("face-1", "alice-id", []byte("emb-1")))

	d := NewWithDB(db)
	faces, err := d.ListFaceEmbeddings()
	if err != nil {
		t.Fatalf("ListFaceEmbeddings: %v", err)
	}
	if len(faces) != 1 || faces[0].ID != "face-1" || faces[0].PersonID != "alice-id" {
		t.Fatalf("ListFaceEmbeddings() = %+v, want one row with id=face-1 person=alice-id", faces)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

// Issue #74: merging folds every source person's faces into the target and
// removes the (now-empty) source person rows, all in one transaction.
func TestMergePeopleReassignsFacesAndDeletesSources(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectExec("update `faces` set `person_id` = \\? where `person_id` in \\(\\?,\\?\\)").
		WithArgs("target-id", "src-1", "src-2").
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec("delete from `people` where `id` in \\(\\?,\\?\\)").
		WithArgs("src-1", "src-2").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	d := NewWithDB(db)
	if err := d.MergePeople("target-id", []string{"src-1", "src-2"}); err != nil {
		t.Fatalf("MergePeople: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

// A target id accidentally included in its own source list is dropped
// rather than merged into itself - see MergePeople's own doc comment.
func TestMergePeopleFiltersTargetOutOfSources(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectExec("update `faces` set `person_id` = \\? where `person_id` in \\(\\?\\)").
		WithArgs("target-id", "src-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("delete from `people` where `id` in \\(\\?\\)").
		WithArgs("src-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	d := NewWithDB(db)
	if err := d.MergePeople("target-id", []string{"src-1", "target-id"}); err != nil {
		t.Fatalf("MergePeople: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

// Merging with only the target itself in sourceIDs is a no-op, not an
// error and not an empty transaction against the database.
func TestMergePeopleNoOpWhenOnlySourceIsTarget(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	d := NewWithDB(db)
	if err := d.MergePeople("target-id", []string{"target-id"}); err != nil {
		t.Fatalf("MergePeople: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected database calls for a no-op merge: %v", err)
	}
}

func TestStatusToPb(t *testing.T) {
	d := &Dao{}

	cases := map[string]pb.FriendShipStatus{
		"pending":  pb.FriendShipStatus_Pending,
		"accepted": pb.FriendShipStatus_Accepted,
		"blocked":  pb.FriendShipStatus_Blocked,
		"unknown":  pb.FriendShipStatus_Pending, // zero value fallback
	}

	for in, want := range cases {
		if got := d.statusToPb(in); got != want {
			t.Errorf("statusToPb(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestPbToStatus(t *testing.T) {
	d := &Dao{}

	cases := map[pb.FriendShipStatus]string{
		pb.FriendShipStatus_Pending:  "pending",
		pb.FriendShipStatus_Accepted: "accepted",
		pb.FriendShipStatus_Blocked:  "blocked",
	}

	for in, want := range cases {
		if got := d.pbToStatus(in); got != want {
			t.Errorf("pbToStatus(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestStatusToPbAndPbToStatusRoundTrip(t *testing.T) {
	d := &Dao{}

	for _, s := range []string{"pending", "accepted", "blocked"} {
		if got := d.pbToStatus(d.statusToPb(s)); got != s {
			t.Errorf("round trip for %q produced %q", s, got)
		}
	}
}

// Batch-deleting several files at once now fires their DelFile calls
// concurrently (see websocket.handleConnection on the device side), so two
// deletes sharing a hash can run their ref-count check at the same time —
// without a lock on the count query, both could see "still >1 other
// reference", both skip cleaning up file_tags, and both delete their own
// files row, leaving file_tags referencing a hash no files row has left:
// exactly the "Error 1451 ... foreign key constraint fails" a real batch
// delete hit. sqlmock can't exercise real row locking (that's MySQL's job,
// not something a scripted mock does), but it does confirm the count query
// text itself asks for it, and that both ref-count branches still commit
// the right statements either way.
func TestDelFileByPathLocksRefCountQuery(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	d := NewWithDB(db)

	const hash = "abc123"

	t.Run("last reference cleans up file_tags too", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectQuery("select `hash` from `files` where `path` = \\?").
			WithArgs("/a.jpg").
			WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow(hash))
		mock.ExpectQuery("select count\\(\\*\\) from `files` where `hash` = \\? for update").
			WithArgs(hash).
			WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(1))
		mock.ExpectExec("delete from `file_tags` where `hash` = \\?").
			WithArgs(hash).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("delete from `files` where `path` = \\?").
			WithArgs("/a.jpg").
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()

		if err := d.DelFileByPath("/a.jpg"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations (a fix that drops the FOR UPDATE lock, or skips deleting file_tags, would show up here): %v", err)
		}
	})

	t.Run("another reference leaves file_tags alone", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectQuery("select `hash` from `files` where `path` = \\?").
			WithArgs("/b.jpg").
			WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow(hash))
		mock.ExpectQuery("select count\\(\\*\\) from `files` where `hash` = \\? for update").
			WithArgs(hash).
			WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(2))
		mock.ExpectExec("delete from `files` where `path` = \\?").
			WithArgs("/b.jpg").
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()

		if err := d.DelFileByPath("/b.jpg"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations (a fix that deletes file_tags even though another path still references the hash would show up here): %v", err)
		}
	})
}

// Issue #73: full-library reprocess bookkeeping.

func TestGetReprocessState(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("select `status`, `total`, `processed`, `last_hash` from `reprocess_state` where `id` = 1").
		WillReturnRows(sqlmock.NewRows([]string{"status", "total", "processed", "last_hash"}).
			AddRow("running", 100, 42, "abc123"))

	d := NewWithDB(db)
	state, err := d.GetReprocessState()
	if err != nil {
		t.Fatalf("GetReprocessState: %v", err)
	}
	want := ReprocessState{Status: "running", Total: 100, Processed: 42, LastHash: "abc123"}
	if state != want {
		t.Errorf("GetReprocessState() = %+v, want %+v", state, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

func TestStartReprocessResetsProgress(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec("update `reprocess_state` set `status` = 'running', `total` = \\?, `processed` = 0, `last_hash` = '', `started` = now\\(\\), `updated` = now\\(\\) where `id` = 1").
		WithArgs(250).
		WillReturnResult(sqlmock.NewResult(0, 1))

	d := NewWithDB(db)
	if err := d.StartReprocess(250); err != nil {
		t.Fatalf("StartReprocess: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

func TestResumeReprocessLeavesProgressAlone(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// No `total`/`processed`/`last_hash` in this statement at all - a
	// resume must not reset the checkpoint StartReprocess owns.
	mock.ExpectExec("update `reprocess_state` set `status` = 'running', `updated` = now\\(\\) where `id` = 1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	d := NewWithDB(db)
	if err := d.ResumeReprocess(); err != nil {
		t.Fatalf("ResumeReprocess: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

func TestUpdateReprocessProgress(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec("update `reprocess_state` set `processed` = \\?, `last_hash` = \\?, `updated` = now\\(\\) where `id` = 1").
		WithArgs(7, "deadbeef").
		WillReturnResult(sqlmock.NewResult(0, 1))

	d := NewWithDB(db)
	if err := d.UpdateReprocessProgress(7, "deadbeef"); err != nil {
		t.Fatalf("UpdateReprocessProgress: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

func TestFinishReprocess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec("update `reprocess_state` set `status` = \\?, `updated` = now\\(\\) where `id` = 1").
		WithArgs("completed").
		WillReturnResult(sqlmock.NewResult(0, 1))

	d := NewWithDB(db)
	if err := d.FinishReprocess("completed"); err != nil {
		t.Fatalf("FinishReprocess: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

func TestWipeTagsAndFacesDeletesAllThreeTablesInOneTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectExec("delete from `file_tags`").WillReturnResult(sqlmock.NewResult(0, 10))
	mock.ExpectExec("delete from `faces`").WillReturnResult(sqlmock.NewResult(0, 5))
	mock.ExpectExec("delete from `people`").WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectCommit()

	d := NewWithDB(db)
	if err := d.WipeTagsAndFaces(); err != nil {
		t.Fatalf("WipeTagsAndFaces: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran (order matters here: file_tags, then faces, then people): %v", err)
	}
}

func TestCountMediaFilesCountsDistinctHashes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("select count\\(distinct `hash`\\) from `files` where `mime` like 'image%' or `mime` like 'video%'").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(42))

	d := NewWithDB(db)
	count, err := d.CountMediaFiles()
	if err != nil {
		t.Fatalf("CountMediaFiles: %v", err)
	}
	if count != 42 {
		t.Errorf("CountMediaFiles() = %d, want 42", count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

func TestListMediaForReprocessGroupsByHash(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// Two rows sharing "hash-1" (a deduped file uploaded under two paths)
	// must collapse into a single result - see this method's own doc
	// comment on why walking one row per path would make hash-ordered
	// pagination unsafe.
	mock.ExpectQuery("select .* from `files`.*group by `hash` order by `hash` asc limit \\?").
		WithArgs("hash-0", 20).
		WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).
			AddRow("hash-1", "image/jpeg", time.Now(), time.Now(), "/a.jpg", 100))

	d := NewWithDB(db)
	files, err := d.ListMediaForReprocess("hash-0", 20)
	if err != nil {
		t.Fatalf("ListMediaForReprocess: %v", err)
	}
	if len(files) != 1 || files[0].Hash != "hash-1" {
		t.Fatalf("ListMediaForReprocess() = %+v, want exactly one file with hash-1", files)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

func TestMarkStaleReprocessStopped(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec("update `reprocess_state` set `status` = 'stopped', `updated` = now\\(\\) where `id` = 1 and `status` = 'running'").
		WillReturnResult(sqlmock.NewResult(0, 1))

	d := NewWithDB(db)
	if err := d.MarkStaleReprocessStopped(); err != nil {
		t.Fatalf("MarkStaleReprocessStopped: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran (must only touch status='running' rows): %v", err)
	}
}

// Issue #78: notifications.

func TestNotificationTypeToStrAndBack(t *testing.T) {
	d := &Dao{}

	cases := map[pb.NotificationType]string{
		pb.NotificationType_NotificationLikePublication: "LikePublication",
		pb.NotificationType_NotificationLikeComment:     "LikeComment",
		pb.NotificationType_NotificationNewComment:      "NewComment",
		pb.NotificationType_NotificationFriendRequest:   "FriendRequest",
		pb.NotificationType_NotificationFriendAccepted:  "FriendAccepted",
	}

	for in, want := range cases {
		if got := d.notificationTypeToStr(in); got != want {
			t.Errorf("notificationTypeToStr(%v) = %q, want %q", in, got, want)
		}
		if got := d.strToNotificationType(want); got != in {
			t.Errorf("strToNotificationType(%q) = %v, want %v", want, got, in)
		}
	}
}

func TestNewNotification(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec("insert into `notifications` \\(`uuid`, `dt`, `type`, `actor_name`, `actor_domain`, `pub_uuid`, `comment_uuid`\\) values \\(\\?, now\\(\\), \\?, \\?, \\?, \\?, \\?\\)").
		WillReturnResult(sqlmock.NewResult(0, 1))

	d := NewWithDB(db)
	if err := d.NewNotification(pb.NotificationType_NotificationLikePublication, "Alice", "alice.off-the.cloud", "pub-1", ""); err != nil {
		t.Fatalf("NewNotification: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

func TestListNotificationsOrdersByDateDesc(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Now()
	mock.ExpectQuery("select n\\.uuid, n\\.dt, n\\.type, n\\.actor_name, n\\.actor_domain, n\\.pub_uuid, n\\.comment_uuid, n\\.acknowledged, f\\.image, pf\\.hash " +
		"from `notifications` n " +
		"left join `social_friendship` f on f\\.domain = n\\.actor_domain " +
		"left join `social_publications_files` pf on pf\\.uuid = n\\.pub_uuid and pf\\.pos = 0 " +
		"order by n\\.dt desc limit \\?").
		WithArgs(50).
		WillReturnRows(sqlmock.NewRows([]string{"uuid", "dt", "type", "actor_name", "actor_domain", "pub_uuid", "comment_uuid", "acknowledged", "image", "hash"}).
			AddRow("n1", now, "LikePublication", "Alice", "alice.off-the.cloud", "pub-1", nil, false, []byte("avatar-bytes"), "thumb-hash-1"))

	d := NewWithDB(db)
	notifications, thumbHashes, err := d.ListNotifications(50)
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	if len(notifications) != 1 || notifications[0].Uuid != "n1" || notifications[0].PubUuid != "pub-1" || notifications[0].CommentUuid != "" {
		t.Fatalf("unexpected notifications: %+v", notifications)
	}
	if notifications[0].Type != pb.NotificationType_NotificationLikePublication {
		t.Errorf("unexpected type: %v", notifications[0].Type)
	}
	if string(notifications[0].ActorImage) != "avatar-bytes" {
		t.Errorf("unexpected actor image: %q", notifications[0].ActorImage)
	}
	if thumbHashes["n1"] != "thumb-hash-1" {
		t.Errorf("unexpected thumb hash: %+v", thumbHashes)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

func TestUnacknowledgedNotificationCount(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("select count\\(\\*\\) from `notifications` where `acknowledged` = 0").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))

	d := NewWithDB(db)
	count, err := d.UnacknowledgedNotificationCount()
	if err != nil {
		t.Fatalf("UnacknowledgedNotificationCount: %v", err)
	}
	if count != 3 {
		t.Fatalf("unexpected count: %d", count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

func TestMarkAllNotificationsAcknowledged(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec("update `notifications` set `acknowledged` = 1 where `acknowledged` = 0").
		WillReturnResult(sqlmock.NewResult(0, 3))

	d := NewWithDB(db)
	if err := d.MarkAllNotificationsAcknowledged(); err != nil {
		t.Fatalf("MarkAllNotificationsAcknowledged: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

func TestGetSocialPublicationByUUIDOwnPost(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Now()
	mock.ExpectQuery("select `friend_domain`, `uuid`, `dt`, `text`, `own_publication`, `likes` from `social_publications` where `uuid` = \\?").
		WithArgs("pub-1").
		WillReturnRows(sqlmock.NewRows([]string{"friend_domain", "uuid", "dt", "text", "own_publication", "likes"}).
			AddRow("", "pub-1", now, "hello", true, 2))
	mock.ExpectQuery("select `hash`, `mime`, `created`, `modified`, `size` from `social_publications_files` where `uuid` = \\? order by `pos`").
		WithArgs("pub-1").
		WillReturnRows(sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "size"}))
	mock.ExpectQuery("select 1 from `social_publication_likes` where `pub_uuid` = \\? and `friend_domain` = \\? limit 1").
		WithArgs("pub-1", "me.off-the.cloud").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}))

	d := NewWithDB(db)
	pub, err := d.GetSocialPublicationByUUID("pub-1", "Owner", "bio", nil, "me.off-the.cloud")
	if err != nil {
		t.Fatalf("GetSocialPublicationByUUID: %v", err)
	}
	if pub.Uuid != "pub-1" || !pub.Own || pub.Publisher.Name != "Owner" {
		t.Fatalf("unexpected publication: %+v", pub)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

// Issue #82: multiple OTC "users" on one device - dao.go's own single-DB
// functions (the cross-database provisioning/drop/storage-usage ones in
// provisioning.go are integration-shaped, real CREATE DATABASE calls -
// exercised manually against a real device instead, not here).

func TestCreateUserAndListUsers(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec("insert into `users`").
		WithArgs("u1", "alice", 8081, "otc_u1", "dbpass", "/mnt/storage/user_u1", "alice.off-the.cloud", "bsecret", "stoken", true).
		WillReturnResult(sqlmock.NewResult(1, 1))

	d := NewWithDB(db)
	if err := d.CreateUser(User{
		Uuid: "u1", Username: "alice", Port: 8081, DbName: "otc_u1", DbPass: "dbpass",
		StoragePath: "/mnt/storage/user_u1", Subdomain: "alice.off-the.cloud",
		BridgeSecret: "bsecret", SupervisorToken: "stoken", Active: true,
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	now := time.Now()
	mock.ExpectQuery("select `uuid`, `username`, `port`, `subdomain`, `active`, `created` from `users` order by `created` asc").
		WillReturnRows(sqlmock.NewRows([]string{"uuid", "username", "port", "subdomain", "active", "created"}).
			AddRow("u1", "alice", 8081, "alice.off-the.cloud", true, now))

	users, err := d.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 1 || users[0].Username != "alice" || users[0].Port != 8081 || users[0].Active != true {
		t.Fatalf("unexpected users: %+v", users)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

func TestGetUserInternalIncludesSecrets(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("select `uuid`, `username`, `port`, `db_name`, `db_pass`, `storage_path`, `subdomain`, `bridge_secret`, `supervisor_token`, `active` from `users` where `uuid` = \\?").
		WithArgs("u1").
		WillReturnRows(sqlmock.NewRows([]string{"uuid", "username", "port", "db_name", "db_pass", "storage_path", "subdomain", "bridge_secret", "supervisor_token", "active"}).
			AddRow("u1", "alice", 8081, "otc_u1", "dbpass", "/mnt/storage/user_u1", "alice.off-the.cloud", "bsecret", "stoken", true))

	d := NewWithDB(db)
	u, err := d.GetUserInternal("u1")
	if err != nil {
		t.Fatalf("GetUserInternal: %v", err)
	}
	if u.DbPass != "dbpass" || u.SupervisorToken != "stoken" || u.DbName != "otc_u1" {
		t.Fatalf("unexpected internal user: %+v", u)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

func TestListActiveUsersInternalFiltersInactive(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("select `uuid`, `username`, `port`, `db_name`, `db_pass`, `storage_path`, `subdomain`, `bridge_secret`, `supervisor_token`, `active` from `users` where `active` = 1").
		WillReturnRows(sqlmock.NewRows([]string{"uuid", "username", "port", "db_name", "db_pass", "storage_path", "subdomain", "bridge_secret", "supervisor_token", "active"}).
			AddRow("u1", "alice", 8081, "otc_u1", "dbpass", "/mnt/storage/user_u1", "alice.off-the.cloud", "bsecret", "stoken", true))

	d := NewWithDB(db)
	users, err := d.ListActiveUsersInternal()
	if err != nil {
		t.Fatalf("ListActiveUsersInternal: %v", err)
	}
	if len(users) != 1 || users[0].Username != "alice" {
		t.Fatalf("unexpected users: %+v", users)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

func TestDeactivateAndDeleteUserRow(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec("update `users` set `active` = 0 where `uuid` = \\?").
		WithArgs("u1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("delete from `users` where `uuid` = \\?").
		WithArgs("u1").WillReturnResult(sqlmock.NewResult(0, 1))

	d := NewWithDB(db)
	if err := d.DeactivateUser("u1"); err != nil {
		t.Fatalf("DeactivateUser: %v", err)
	}
	if err := d.DeleteUserRow("u1"); err != nil {
		t.Fatalf("DeleteUserRow: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

func TestIsUsernameTakenAndIsPortInUse(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("select count\\(\\*\\) from `users` where `username` = \\?").
		WithArgs("alice").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("select count\\(\\*\\) from `users` where `port` = \\?").
		WithArgs(8081).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	d := NewWithDB(db)
	taken, err := d.IsUsernameTaken("alice")
	if err != nil || !taken {
		t.Fatalf("IsUsernameTaken: taken=%v err=%v", taken, err)
	}
	inUse, err := d.IsPortInUse(8081)
	if err != nil || inUse {
		t.Fatalf("IsPortInUse: inUse=%v err=%v", inUse, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

func TestNextFreePortDefaultsToBaseWhenNoUsers(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("select max\\(`port`\\) from `users`").
		WillReturnRows(sqlmock.NewRows([]string{"max"}).AddRow(nil))

	d := NewWithDB(db)
	port, err := d.NextFreePort(8081)
	if err != nil {
		t.Fatalf("NextFreePort: %v", err)
	}
	if port != 8081 {
		t.Fatalf("expected default base port 8081, got %d", port)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

func TestNextFreePortIncrementsPastHighestExisting(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("select max\\(`port`\\) from `users`").
		WillReturnRows(sqlmock.NewRows([]string{"max"}).AddRow(8083))

	d := NewWithDB(db)
	port, err := d.NextFreePort(8081)
	if err != nil {
		t.Fatalf("NextFreePort: %v", err)
	}
	if port != 8084 {
		t.Fatalf("expected 8084 (one past the highest existing), got %d", port)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}

// Issue #92: ends a friend's notification "catch-up" suppression once
// their pre-existing backlog has been fully replayed - see social.go's
// updateFriendEvents.
func TestMarkNotificationsStarted(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec("update `social_friendship` set `notifications_started` = 1 where `domain` = \\?").
		WithArgs("alice.off-the.cloud").
		WillReturnResult(sqlmock.NewResult(0, 1))

	d := NewWithDB(db)
	if err := d.MarkNotificationsStarted("alice.off-the.cloud"); err != nil {
		t.Fatalf("MarkNotificationsStarted: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran: %v", err)
	}
}
