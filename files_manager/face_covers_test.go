// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	facerecognition "github.com/alonsovidales/otc/face_recognition"
)

// The face chosen for a person is the best-looking of theirs that is
// clearly them: a sharper stranger misfiled under them is passed over, and
// with nothing close enough it is their most typical face.
func TestChooseCoverPrefersTheBestLookingFaceThatIsThem(t *testing.T) {
	ses := faceTestSession(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mg := NewWithDAO(dao.NewWithDB(db))
	refs := refsOf(map[string][][]float32{
		"alice": {{1, 0, 0}, {0.98, 0.2, 0}, {0.97, 0, 0.24}},
	})
	enc := func(e []float32) []byte { return ses.Encrypt(facerecognition.EncodeEmbedding(e)) }
	best := func(rows *sqlmock.Rows) {
		mock.ExpectQuery("select `f`.`id`, `f`.`embedding`, `f`.`quality`, .* as `score` from `faces` as `f` left join `photo_quality`").
			WithArgs("alice", cCoverCandidates).WillReturnRows(rows)
	}
	cover := func(id string) {
		mock.ExpectExec("update `people` set `cover_face_id` = if").WithArgs(id, sqlmock.AnyArg(), "alice").
			WillReturnResult(sqlmock.NewResult(0, 1))
	}

	// The stranger ranks first and looks best; the next is clearly her.
	best(sqlmock.NewRows([]string{"id", "embedding", "quality", "score"}).
		AddRow("stranger", enc([]float32{0, 1, 0}), 0.99, 0.95).
		AddRow("alice-sharp", enc([]float32{0.99, 0.1, 0.1}), 0.9, 0.9))
	cover("alice-sharp")
	mg.chooseCoverLocked(ses, "alice", refs["alice"])
	if p := refs["alice"]; !p.coverKnown || p.coverQuality != 0.9 {
		t.Errorf("chosen %v at %v, want known at 0.9", p.coverKnown, p.coverQuality)
	}

	// No candidate is her: the medoid, which looks like nothing known yet.
	medoid, _ := facerecognition.MedoidAndCohesion(refs["alice"].identified())
	best(sqlmock.NewRows([]string{"id", "embedding", "quality", "score"}).
		AddRow("stranger", enc([]float32{0, 1, 0}), 0.99, 0.95))
	cover(medoid)
	mg.chooseCoverLocked(ses, "alice", refs["alice"])
	if p := refs["alice"]; !p.coverKnown || p.coverQuality != 0 {
		t.Errorf("medoid kept as %v at %v", p.coverKnown, p.coverQuality)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A new face chooses the person's face again when it changed their
// references, looks better than the one chosen, or none was chosen yet.
func TestCoverMayChange(t *testing.T) {
	p := &personFaceRefs{}
	if !coverMayChange(p, false, 0.1) {
		t.Error("nothing chosen yet: must choose")
	}
	p.coverKnown, p.coverQuality = true, 0.6
	for _, c := range []struct {
		kept    bool
		quality float64
		want    bool
	}{{false, 0.5, false}, {false, 0.7, true}, {true, 0.1, true}} {
		if got := coverMayChange(p, c.kept, c.quality); got != c.want {
			t.Errorf("kept %v, quality %v: %v, want %v", c.kept, c.quality, got, c.want)
		}
	}
	if coverMayChange(nil, true, 1) {
		t.Error("no references: nothing to choose from")
	}
}

// faceQuality reads the recognizer's crop, a small JPEG; one it can't read
// is 0.
func TestFaceQualityReadsTheCrop(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 112, 112))
	for y := 0; y < 112; y++ {
		for x := 0; x < 112; x++ {
			xs := min(x, 111-x)
			v := uint8(128 - 40)
			if (xs+y)%2 == 0 {
				v = 128 + 40
			}
			img.SetGray(x, y, color.Gray{Y: v})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	if q := faceQuality(b.Bytes(), 300, 320); q < 0.6 {
		t.Errorf("a sharp, large, frontal crop scored %v", q)
	}
	if q := faceQuality([]byte("not a jpeg"), 300, 300); q != 0 {
		t.Errorf("unreadable crop scored %v", q)
	}
	if small, large := faceQuality(b.Bytes(), 40, 40), faceQuality(b.Bytes(), 300, 300); small >= large {
		t.Errorf("a small face (%v) must score below a large one (%v)", small, large)
	}
}
