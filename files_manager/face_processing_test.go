// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	facerecognition "github.com/alonsovidales/otc/face_recognition"
)

// matchOrNewPerson is processFaces' actual clustering decision (issue #52),
// pinned directly here since driving processFaces itself needs a real (or
// faked) database and the real face detection models - same reasoning as
// TestThumbnailPollBudgetCoversMeasuredWorstCase in social_test.go.
func TestMatchOrNewPersonNoExistingFacesReturnsEmpty(t *testing.T) {
	got := matchOrNewPerson([]float32{1, 0, 0}, nil)
	if got != "" {
		t.Errorf("matchOrNewPerson with no existing faces = %q, want empty (new person)", got)
	}
}

func TestMatchOrNewPersonMatchesClosestAboveThreshold(t *testing.T) {
	newFace := []float32{1, 0}
	existing := []dao.FaceEmbedding{
		// Orthogonal (cosine 0) - not a match.
		{PersonID: "stranger", Embedding: facerecognition.EncodeEmbedding([]float32{0, 1})},
		// Near-identical - clears the 0.363 threshold easily.
		{PersonID: "alice", Embedding: facerecognition.EncodeEmbedding([]float32{0.99, 0.05})},
	}

	got := matchOrNewPerson(newFace, existing)
	if got != "alice" {
		t.Errorf("matchOrNewPerson() = %q, want %q", got, "alice")
	}
}

func TestMatchOrNewPersonNothingClearsThresholdReturnsEmpty(t *testing.T) {
	newFace := []float32{1, 0}
	existing := []dao.FaceEmbedding{
		{PersonID: "stranger-1", Embedding: facerecognition.EncodeEmbedding([]float32{0, 1})},
		{PersonID: "stranger-2", Embedding: facerecognition.EncodeEmbedding([]float32{-1, 0})},
	}

	got := matchOrNewPerson(newFace, existing)
	if got != "" {
		t.Errorf("matchOrNewPerson() = %q, want empty (nothing close enough)", got)
	}
}

// Two faces both close enough to match, but one closer than the other -
// the nearest one wins, not just "the first one that clears the bar".
func TestMatchOrNewPersonPicksTheClosestNotTheFirst(t *testing.T) {
	newFace := []float32{1, 0, 0}
	existing := []dao.FaceEmbedding{
		{PersonID: "bob", Embedding: facerecognition.EncodeEmbedding([]float32{0.9, 0.1, 0})},
		{PersonID: "alice", Embedding: facerecognition.EncodeEmbedding([]float32{0.99, 0.01, 0})},
	}

	got := matchOrNewPerson(newFace, existing)
	if got != "alice" {
		t.Errorf("matchOrNewPerson() = %q, want the closer match %q", got, "alice")
	}
}

// updatePersonCoverFace recomputes a person's medoid cover face (issue
// #52 follow-up: previously just "the oldest face") every time a face is
// added to them - these pin that it filters `existing` down to just the
// target person before handing it to MedoidFaceID, and writes whichever
// id comes back.
func TestUpdatePersonCoverFaceFiltersToTargetPersonOnly(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// Two faces for "alice" tightly clustered, one for "bob" thrown in to
	// prove cross-person contamination doesn't happen - if bob's face
	// leaked into the medoid computation for alice, or alice's id got
	// used to update bob, this mock's expectation would go unmet.
	existing := []dao.FaceEmbedding{
		{ID: "alice-1", PersonID: "alice", Embedding: facerecognition.EncodeEmbedding([]float32{1, 0})},
		{ID: "alice-2", PersonID: "alice", Embedding: facerecognition.EncodeEmbedding([]float32{0.99, 0.01})},
		{ID: "bob-1", PersonID: "bob", Embedding: facerecognition.EncodeEmbedding([]float32{0, 1})},
	}

	mock.ExpectExec("update `people` set `cover_face_id` = \\?, `cohesion` = \\? where `id` = \\?").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "alice").
		WillReturnResult(sqlmock.NewResult(0, 1))

	updatePersonCoverFace(dao.NewWithDB(db), "alice", existing)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("updatePersonCoverFace didn't update the right person (or updated the wrong one/none): %v", err)
	}
}

func TestUpdatePersonCoverFaceNoFacesForPersonIsNoOp(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	existing := []dao.FaceEmbedding{
		{ID: "bob-1", PersonID: "bob", Embedding: facerecognition.EncodeEmbedding([]float32{0, 1})},
	}

	// No mock.ExpectExec at all - a personID with nothing in `existing`
	// (shouldn't normally happen, but the function must not panic or
	// write a garbage cover_face_id in that case) must not touch the DB.
	updatePersonCoverFace(dao.NewWithDB(db), "someone-else", existing)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("updatePersonCoverFace made an unexpected database call: %v", err)
	}
}
