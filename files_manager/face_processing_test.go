// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"fmt"
	"math"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	facerecognition "github.com/alonsovidales/otc/face_recognition"
	"github.com/alonsovidales/otc/session"
)

// refsOf builds a reference set from person -> embeddings, ids
// "<person>-<n>" - a stand-in for what loadFaceRefsLocked builds from the
// database.
func refsOf(people map[string][][]float32) faceRefs {
	fr := faceRefs{}
	for person, embs := range people {
		for i, e := range embs {
			fr.add(person, fmt.Sprintf("%s-%d", person, i+1), e)
		}
	}
	return fr
}

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
	refs := refsOf(map[string][][]float32{
		// Orthogonal (cosine 0) - not a match.
		"stranger": {{0, 1}},
		// Near-identical - clears the 0.363 threshold easily.
		"alice": {{0.99, 0.05}},
	})

	got := matchOrNewPerson(newFace, refs)
	if got != "alice" {
		t.Errorf("matchOrNewPerson() = %q, want %q", got, "alice")
	}
}

func TestMatchOrNewPersonNothingClearsThresholdReturnsEmpty(t *testing.T) {
	newFace := []float32{1, 0}
	refs := refsOf(map[string][][]float32{
		"stranger-1": {{0, 1}},
		"stranger-2": {{-1, 0}},
	})

	got := matchOrNewPerson(newFace, refs)
	if got != "" {
		t.Errorf("matchOrNewPerson() = %q, want empty (nothing close enough)", got)
	}
}

// Two faces both close enough to match, but one closer than the other -
// the nearest one wins, not just "the first one that clears the bar".
func TestMatchOrNewPersonPicksTheClosestNotTheFirst(t *testing.T) {
	newFace := []float32{1, 0, 0}
	refs := refsOf(map[string][][]float32{
		"bob":   {{0.9, 0.1, 0}},
		"alice": {{0.99, 0.01, 0}},
	})

	got := matchOrNewPerson(newFace, refs)
	if got != "alice" {
		t.Errorf("matchOrNewPerson() = %q, want the closer match %q", got, "alice")
	}
}

// cTestDim is big enough for every synthetic face below to get its own
// orthogonal "variation" axis.
const cTestDim = 64

// unit returns the unit vector along base plus 0.8 x each of axes - a
// synthetic face that shares base with every other face built on it
// (cosine ~0.61 between two with one distinct axis each, comfortably
// above the 0.363 same-person threshold) while still differing from them.
func unit(base int, axes map[int]float32) []float32 {
	v := make([]float32, cTestDim)
	v[base] = 1
	for a, w := range axes {
		v[a] += w
	}
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	for i := range v {
		v[i] /= float32(math.Sqrt(n))
	}
	return v
}

// fullPerson is a person with exactly cMaxFaceRefsPerPerson references
// "r1".."r20", each base axis 0 plus its own axis - except r5, which is a
// near-duplicate of r4 (same axis, a tiny extra component), making that
// pair the most redundant in the set.
func fullPerson() *personFaceRefs {
	p := &personFaceRefs{}
	for i := 1; i <= cMaxFaceRefsPerPerson; i++ {
		axes := map[int]float32{i: 0.8}
		if i == 5 {
			axes = map[int]float32{4: 0.8, 40: 0.05}
		}
		if !p.add(fmt.Sprintf("r%d", i), unit(0, axes)) {
			panic("fullPerson: a face below the cap must always be kept")
		}
	}
	return p
}

func refIDs(p *personFaceRefs) map[string]bool {
	ids := map[string]bool{}
	for _, r := range p.refs {
		ids[r.id] = true
	}
	return ids
}

// Issue #173: at the cap, a new (non-outlier) face is kept and the most
// redundant reference - the one with the highest similarity to another
// reference - is dropped, so the set stays at 20 and stays diverse. r4 and
// r5 are near-duplicates; the earlier one, r4, goes.
func TestFaceRefsAtCapDropsMostRedundant(t *testing.T) {
	p := fullPerson()

	if !p.add("new", unit(0, map[int]float32{21: 0.8})) {
		t.Fatal("add() of a typical face at the cap = false, want it kept")
	}
	if len(p.refs) != cMaxFaceRefsPerPerson {
		t.Fatalf("len(refs) = %d, want %d", len(p.refs), cMaxFaceRefsPerPerson)
	}
	ids := refIDs(p)
	if ids["r4"] {
		t.Error("r4 (near-duplicate of r5) is still a reference, want it dropped as the most redundant")
	}
	for _, want := range []string{"new", "r5", "r1", "r20"} {
		if !ids[want] {
			t.Errorf("reference %q was dropped, want it kept", want)
		}
	}
	assertSimMatrixConsistent(t, p)
}

// A new face that's a near-copy of an existing reference adds nothing: one
// of the two twins is dropped, never a distinct reference.
func TestFaceRefsAtCapDropsTheDuplicateNotAVariant(t *testing.T) {
	p := fullPerson()
	// Remove the built-in r4/r5 pair's redundancy first so the new twin
	// is the only near-duplicate pair.
	p.remove(4) // r5
	p.add("r5b", unit(0, map[int]float32{25: 0.8}))

	p.add("twin7", unit(0, map[int]float32{7: 0.8, 41: 0.01}))

	ids := refIDs(p)
	if ids["r7"] && ids["twin7"] {
		t.Error("both r7 and its near-copy are references, want one dropped")
	}
	for i := 1; i <= cMaxFaceRefsPerPerson; i++ {
		id := fmt.Sprintf("r%d", i)
		if i == 5 || i == 7 {
			continue
		}
		if !ids[id] {
			t.Errorf("distinct reference %q was dropped, want only a twin dropped", id)
		}
	}
	assertSimMatrixConsistent(t, p)
}

// Issue #173: at the cap, an outlier (average similarity to the references
// below the same-person threshold) is not added and nothing is dropped.
func TestFaceRefsAtCapOutlierNotAdded(t *testing.T) {
	p := fullPerson()
	before := refIDs(p)

	if p.add("outlier", unit(50, nil)) { // orthogonal to every reference
		t.Fatal("add() of an outlier at the cap = true, want it rejected")
	}
	after := refIDs(p)
	if len(after) != len(before) || after["outlier"] {
		t.Errorf("references changed after rejecting an outlier: %v", after)
	}
	for id := range before {
		if !after[id] {
			t.Errorf("reference %q dropped by a rejected outlier", id)
		}
	}
}

// Below the cap every face is kept - the outlier rule only guards a full
// set, so a new person's first faces all count.
func TestFaceRefsBelowCapKeepsEverything(t *testing.T) {
	p := &personFaceRefs{}
	p.add("a", unit(0, nil))
	if !p.add("b", unit(50, nil)) {
		t.Error("add() below the cap = false, want every face kept")
	}
	if len(p.refs) != 2 {
		t.Errorf("len(refs) = %d, want 2", len(p.refs))
	}
}

func assertSimMatrixConsistent(t *testing.T, p *personFaceRefs) {
	t.Helper()
	if len(p.sim) != len(p.refs) {
		t.Fatalf("len(sim) = %d, len(refs) = %d", len(p.sim), len(p.refs))
	}
	for i := range p.refs {
		if len(p.sim[i]) != len(p.refs) {
			t.Fatalf("len(sim[%d]) = %d, want %d", i, len(p.sim[i]), len(p.refs))
		}
		for j := range p.refs {
			if i == j {
				continue
			}
			want := facerecognition.CosineSimilarity(p.refs[i].emb, p.refs[j].emb)
			if d := p.sim[i][j] - want; d > 1e-6 || d < -1e-6 {
				t.Errorf("sim[%d][%d] = %v, want %v", i, j, p.sim[i][j], want)
			}
		}
	}
}

// Matching only looks at references (issue #173): a face that was stored
// but rejected as an outlier from a full person's references doesn't pull
// a later look-alike of it into that person.
func TestMatchOrNewPersonUsesReferencesOnly(t *testing.T) {
	refs := faceRefs{"alice": fullPerson()}
	outlier := unit(50, nil)
	if refs.add("alice", "alice-outlier", outlier) {
		t.Fatal("outlier was added to a full reference set")
	}

	if got := matchOrNewPerson(unit(50, map[int]float32{51: 0.1}), refs); got != "" {
		t.Errorf("matchOrNewPerson() = %q, want empty: the only similar face isn't a reference", got)
	}
	if got := matchOrNewPerson(unit(0, map[int]float32{30: 0.8}), refs); got != "alice" {
		t.Errorf("matchOrNewPerson() = %q, want alice via her references", got)
	}
}

// updatePersonCoverFace recomputes a person's medoid cover face (issue
// #52 follow-up: previously just "the oldest face") over their references
// (issue #173) and writes it for that person only.
func TestUpdatePersonCoverFaceWritesTargetPerson(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	refs := refsOf(map[string][][]float32{
		"alice": {{1, 0}, {0.99, 0.01}},
		"bob":   {{0, 1}},
	})

	mock.ExpectExec("update `people` set `cover_face_id` = \\?, `cohesion` = \\? where `id` = \\?").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "alice").
		WillReturnResult(sqlmock.NewResult(0, 1))

	updatePersonCoverFace(dao.NewWithDB(db), "alice", refs["alice"])

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("updatePersonCoverFace didn't update the right person (or updated the wrong one/none): %v", err)
	}
}

func TestUpdatePersonCoverFaceNoRefsIsNoOp(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// No mock.ExpectExec at all - a person with no references must not
	// write a garbage cover_face_id.
	updatePersonCoverFace(dao.NewWithDB(db), "someone-else", nil)
	updatePersonCoverFace(dao.NewWithDB(db), "someone-else", &personFaceRefs{})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("updatePersonCoverFace made an unexpected database call: %v", err)
	}
}

// Issue #173: the stored embeddings are read and decrypted once, then
// served from memory until InvalidateFaceRefs, which makes the next call
// read the database again.
func TestLoadFaceRefsCachesUntilInvalidated(t *testing.T) {
	sesDB, sesMock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer sesDB.Close()
	sesMock.ExpectQuery("select count\\(\\*\\) from `vault`").WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(0))
	sesMock.ExpectExec("insert into `vault`").WillReturnResult(sqlmock.NewResult(1, 1))
	ses, err := session.New("owner-uuid", "test-password", true, dao.NewWithDB(sesDB))
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	rows := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"id", "person_id", "embedding"}).
			AddRow("f1", "alice", ses.Encrypt(facerecognition.EncodeEmbedding([]float32{1, 0}))).
			AddRow("f2", "bob", ses.Encrypt(facerecognition.EncodeEmbedding([]float32{0, 1})))
	}
	mock.ExpectQuery("select `id`, `person_id`, `embedding` from `faces`").WillReturnRows(rows())

	mg := &Manager{dao: dao.NewWithDB(db)}
	mg.faceRefsMu.Lock()
	refs, err := mg.loadFaceRefsLocked(ses)
	if err == nil {
		// Second call must be served from the cache - sqlmock fails an
		// unexpected second query.
		_, err = mg.loadFaceRefsLocked(ses)
	}
	mg.faceRefsMu.Unlock()
	if err != nil {
		t.Fatalf("loadFaceRefsLocked: %v", err)
	}
	if got := matchOrNewPerson([]float32{0.9, 0.1}, refs); got != "alice" {
		t.Errorf("matchOrNewPerson against decrypted refs = %q, want alice", got)
	}

	mg.InvalidateFaceRefs()
	mock.ExpectQuery("select `id`, `person_id`, `embedding` from `faces`").WillReturnRows(rows())
	mg.faceRefsMu.Lock()
	_, err = mg.loadFaceRefsLocked(ses)
	mg.faceRefsMu.Unlock()
	if err != nil {
		t.Fatalf("loadFaceRefsLocked after invalidate: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expected exactly one reload after InvalidateFaceRefs: %v", err)
	}
}
