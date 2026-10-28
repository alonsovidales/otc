// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"errors"
	"fmt"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	facerecognition "github.com/alonsovidales/otc/face_recognition"
	"github.com/alonsovidales/otc/session"
)

func storedFace(ses *session.Session, id, person string, emb []float32) []any {
	return []any{id, person, ses.Encrypt(facerecognition.EncodeEmbedding(emb))}
}

func faceRows(rows ...[]any) *sqlmock.Rows {
	r := sqlmock.NewRows([]string{"id", "person_id", "embedding"})
	for _, row := range rows {
		r.AddRow(row[0], row[1], row[2])
	}
	return r
}

func refIDsOf(fr faceRefs, person string) string {
	p := fr[person]
	if p == nil {
		return "none"
	}
	var ids []string
	for _, r := range p.refs {
		ids = append(ids, r.id)
	}
	return fmt.Sprint(ids)
}

// "Someone else…": the moved face leaves alice's references and joins
// bob's; both are built again from their own faces only (no read of
// every face), and both covers are worked out again over them.
func TestRemoveFromPersonRebuildsBothPeople(t *testing.T) {
	ses := faceTestSession(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mg := &Manager{dao: dao.NewWithDB(db), faceRefs: refsOf(map[string][][]float32{
		"alice": {{1, 0}, {0.9, 0.1}, {0.1, 0.9}}, // alice-3 is really bob
		"bob":   {{0, 1}},
		"carol": {{0.7, 0.7}},
	}), faceRefsStale: map[string]bool{"alice": true}}

	mock.ExpectBegin()
	mock.ExpectQuery("select 1 from `people` where `id` = \\? for update").WithArgs("bob").
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery("select `id` from `faces` where `person_id` = \\? and `hash` in").WithArgs("alice", "h3").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("alice-3"))
	mock.ExpectExec("update `faces` set `person_id` = \\?").WithArgs("bob", "alice-3").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("update `people` set `cover_face_id` = null").WithArgs("alice-3").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("delete from `people` where `id` = \\?").WithArgs("alice").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	mock.ExpectQuery("select `id`, `person_id`, `embedding` from `faces` where `person_id` = \\?").WithArgs("alice").
		WillReturnRows(faceRows(storedFace(ses, "alice-1", "alice", []float32{1, 0}), storedFace(ses, "alice-2", "alice", []float32{0.9, 0.1})))
	mock.ExpectExec("update `people` set `cover_face_id` = \\?, `cohesion` = \\? where `id` = \\?").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "alice").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("select `id`, `person_id`, `embedding` from `faces` where `person_id` = \\?").WithArgs("bob").
		WillReturnRows(faceRows(storedFace(ses, "bob-1", "bob", []float32{0, 1}), storedFace(ses, "alice-3", "bob", []float32{0.1, 0.9})))
	mock.ExpectExec("update `people` set `cover_face_id` = \\?, `cohesion` = \\? where `id` = \\?").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "bob").WillReturnResult(sqlmock.NewResult(0, 1))

	if err := mg.RemoveFromPerson(ses, "alice", []string{"h3"}, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
	if got := refIDsOf(mg.faceRefs, "alice"); got != "[alice-1 alice-2]" {
		t.Errorf("alice's references %s", got)
	}
	if got := refIDsOf(mg.faceRefs, "bob"); got != "[bob-1 alice-3]" {
		t.Errorf("bob's references %s", got)
	}
	if got := refIDsOf(mg.faceRefs, "carol"); got != "[carol-1]" {
		t.Errorf("carol's references %s, want them untouched", got)
	}
	if mg.faceRefsStale["alice"] {
		t.Error("alice, just built again, is still stale")
	}
	if got := matchOrNewPerson([]float32{0.12, 0.88}, mg.faceRefs); got != "bob" {
		t.Errorf("a face like the moved one matches %q, want bob", got)
	}
}

// "Not this person": the face goes to no one - never a reference of
// anyone, so a new face just like it doesn't come back to the person - and
// the person, left with no face, leaves the matching set (and, unnamed,
// the database). Nothing is read for no one.
func TestRemoveFromPersonDismissesTheFaces(t *testing.T) {
	ses := faceTestSession(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mg := &Manager{dao: dao.NewWithDB(db), searchTokens: newSearchTokenCache(100), faceRefs: refsOf(map[string][][]float32{
		"stranger": {{1, 0}},
		"bob":      {{0, 1}},
	})}

	mock.ExpectBegin()
	mock.ExpectQuery("select `id` from `faces` where `person_id` = \\? and `hash` in").WithArgs("stranger", "h1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("stranger-1"))
	mock.ExpectExec("update `faces` set `person_id` = \\?").WithArgs("", "stranger-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("update `people` set `cover_face_id` = null").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("delete from `people` where `id` = \\?").WithArgs("stranger").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery("select `id`, `person_id`, `embedding` from `faces` where `person_id` = \\?").WithArgs("stranger").
		WillReturnRows(faceRows())

	if err := mg.RemoveFromPerson(ses, "stranger", []string{"h1"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
	if _, ok := mg.faceRefs["stranger"]; ok {
		t.Error("a person with no face left is still matched against")
	}
	if _, ok := mg.faceRefs[""]; ok {
		t.Error("no one has references")
	}
	if got := matchOrNewPerson([]float32{1, 0}, mg.faceRefs); got != "" {
		t.Errorf("a face like the dismissed one matches %q, want a new person", got)
	}
	// The apps drop the photos from the person's grid and lower their
	// have, as after a delete: no page is served again across it.
	if _, deletes := mg.searchTokens.begin(); deletes != 1 {
		t.Errorf("deletes noted %d, want 1", deletes)
	}
}

// A refused move (the person picked is gone) changes nothing and keeps
// the matching set; any other failure may or may not have changed the
// faces, so the set is built again from what is stored.
func TestRemoveFromPersonFailures(t *testing.T) {
	ses := faceTestSession(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	refs := refsOf(map[string][][]float32{"alice": {{1, 0}}})
	mg := &Manager{dao: dao.NewWithDB(db), faceRefs: refs}

	mock.ExpectBegin()
	mock.ExpectQuery("select 1 from `people` where `id` = \\? for update").WithArgs("gone").
		WillReturnRows(sqlmock.NewRows([]string{"1"}))
	mock.ExpectRollback()
	if err := mg.RemoveFromPerson(ses, "alice", []string{"h"}, "gone"); !errors.Is(err, dao.ErrNoSuchPerson) {
		t.Errorf("to a person who is gone: %v", err)
	}
	if mg.faceRefs == nil {
		t.Error("a refused move dropped the matching set")
	}

	mock.ExpectBegin()
	mock.ExpectQuery("select `id` from `faces`").WillReturnError(errors.New("database gone"))
	mock.ExpectRollback()
	if err := mg.RemoveFromPerson(ses, "alice", []string{"h"}, ""); err == nil {
		t.Error("a failed move answered ok")
	}
	if mg.faceRefs != nil {
		t.Error("the matching set was kept after a failed move")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// Faces of no one (person_id "") are never references: not when the set
// is built from every face (should one be listed), nor offered one by
// one, nor matched through a set that somehow held them - "" from
// matchOrNewPerson means a new person, never "no one".
func TestNoOneIsNeverAReference(t *testing.T) {
	ses := faceTestSession(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("select `id`, `person_id`, `embedding` from `faces` where `person_id` <> ''").
		WillReturnRows(faceRows(storedFace(ses, "f1", "", []float32{1, 0}), storedFace(ses, "f2", "bob", []float32{0, 1})))
	mg := &Manager{dao: dao.NewWithDB(db)}
	mg.faceRefsMu.Lock()
	refs, err := mg.loadFaceRefsLocked(ses)
	mg.faceRefsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := refs[""]; ok || len(refs) != 1 {
		t.Errorf("references %v, want bob's only", refs)
	}

	fr := faceRefs{}
	if fr.add("", "f1", []float32{1, 0}) || len(fr) != 0 {
		t.Error("a face of no one was kept as a reference")
	}

	held := faceRefs{"": &personFaceRefs{refs: []faceRef{{id: "f1", emb: []float32{1, 0}}}}}
	held.add("bob", "f2", []float32{0.8, 0.6})
	if got := matchOrNewPerson([]float32{1, 0}, held); got != "bob" {
		t.Errorf("matchOrNewPerson = %q, want bob, the closest person", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
