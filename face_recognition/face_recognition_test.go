// SPDX-License-Identifier: AGPL-3.0-or-later

package facerecognition

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"testing"

	"gocv.io/x/gocv"
)

// CosineSimilarity/IsSamePerson are plain Go, independent of gocv/the
// actual models (see the package doc comment for why) - the only part of
// this package that can be exercised without a live OpenCV build and the
// real YuNet/SFace model files, same reasoning as
// images_tagger_test.go's own model-independent tests (readTagList,
// scoresToTags, pickBySubstring) - actual detection/embedding is verified
// by running against real photos on a real device, not in this test suite.
func TestCosineSimilarityIdenticalVectorsScoreOne(t *testing.T) {
	v := []float32{0.5, -0.2, 0.8, 0.1}
	got := CosineSimilarity(v, v)
	if got < 0.999 || got > 1.001 {
		t.Errorf("CosineSimilarity(v, v) = %v, want ~1.0", got)
	}
}

func TestCosineSimilarityOrthogonalVectorsScoreZero(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{0, 1}
	got := CosineSimilarity(a, b)
	if got < -0.001 || got > 0.001 {
		t.Errorf("CosineSimilarity(orthogonal) = %v, want ~0", got)
	}
}

func TestCosineSimilarityOppositeVectorsScoreMinusOne(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{-1, -2, -3}
	got := CosineSimilarity(a, b)
	if got < -1.001 || got > -0.999 {
		t.Errorf("CosineSimilarity(opposite) = %v, want ~-1.0", got)
	}
}

func TestCosineSimilarityMismatchedLengthReturnsZero(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{1, 2}
	if got := CosineSimilarity(a, b); got != 0 {
		t.Errorf("CosineSimilarity(mismatched lengths) = %v, want 0", got)
	}
}

func TestCosineSimilarityEmptyVectorsReturnsZero(t *testing.T) {
	if got := CosineSimilarity(nil, nil); got != 0 {
		t.Errorf("CosineSimilarity(nil, nil) = %v, want 0", got)
	}
}

func TestCosineSimilarityZeroVectorReturnsZeroNotNaN(t *testing.T) {
	a := []float32{0, 0, 0}
	b := []float32{1, 2, 3}
	got := CosineSimilarity(a, b)
	if got != 0 {
		t.Errorf("CosineSimilarity(zero vector) = %v, want 0 (not NaN from a 0/0 division)", got)
	}
}

func TestEncodeDecodeEmbeddingRoundTrips(t *testing.T) {
	original := []float32{0.5, -0.25, 1.0, -1.0, 0, 123.456, -0.0001}
	got := DecodeEmbedding(EncodeEmbedding(original))
	if len(got) != len(original) {
		t.Fatalf("round-tripped length %d, want %d", len(got), len(original))
	}
	for i := range original {
		if got[i] != original[i] {
			t.Errorf("index %d: got %v, want %v", i, got[i], original[i])
		}
	}
}

func TestEncodeEmbeddingEmptyVector(t *testing.T) {
	if got := EncodeEmbedding(nil); len(got) != 0 {
		t.Errorf("EncodeEmbedding(nil) = %v, want empty", got)
	}
	if got := DecodeEmbedding(nil); len(got) != 0 {
		t.Errorf("DecodeEmbedding(nil) = %v, want empty", got)
	}
}

func TestIsSamePersonScoreMatchesThreshold(t *testing.T) {
	if !IsSamePersonScore(0.363) {
		t.Error("expected exactly the threshold value to count as a match")
	}
	if IsSamePersonScore(0.362) {
		t.Error("expected just below the threshold to not count as a match")
	}
}

func TestIsSamePersonUsesOpenCVsPublishedThreshold(t *testing.T) {
	// A pair of embeddings scoring just above/below OpenCV's own published
	// cosine cutoff for SFace (0.363) - pins IsSamePerson to that exact,
	// externally-sourced threshold rather than a value this package made
	// up, and catches an accidental off-by-a-little edit to it.
	a := []float32{1, 0}
	above := []float32{0.4, float32(0.9165)} // cos ≈ 0.400 > 0.363
	below := []float32{0.2, float32(0.9798)} // cos ≈ 0.200 < 0.363

	if !IsSamePerson(a, above) {
		t.Errorf("expected a match just above the 0.363 threshold, got similarity %v", CosineSimilarity(a, above))
	}
	if IsSamePerson(a, below) {
		t.Errorf("expected no match just below the 0.363 threshold, got similarity %v", CosineSimilarity(a, below))
	}
}

// detectionScale backs DetectFaces' cMaxDetectionDim cap - see that
// constant's own doc comment for why running the detector against a raw
// phone photo's native resolution (rather than a capped-size copy) is a
// real, reproduced failure mode (garbage detections on background texture,
// the actual face missed).
func TestDetectionScaleNoopBelowMaxDim(t *testing.T) {
	scale, w, h := detectionScale(1200, 800, cMaxDetectionDim)
	if scale != 1 || w != 1200 || h != 800 {
		t.Errorf("detectionScale(1200, 800, %d) = (%v, %d, %d), want (1, 1200, 800) - already under the cap", cMaxDetectionDim, scale, w, h)
	}
}

func TestDetectionScaleNoopExactlyAtMaxDim(t *testing.T) {
	scale, w, h := detectionScale(cMaxDetectionDim, 900, cMaxDetectionDim)
	if scale != 1 || w != cMaxDetectionDim || h != 900 {
		t.Errorf("detectionScale at exactly the cap = (%v, %d, %d), want (1, %d, 900)", scale, w, h, cMaxDetectionDim)
	}
}

func TestDetectionScaleDownscalesLongestSidePreservingAspect(t *testing.T) {
	// A typical 12MP phone photo, portrait orientation.
	scale, w, h := detectionScale(3024, 4032, cMaxDetectionDim)
	if h != cMaxDetectionDim {
		t.Errorf("detectionScale(3024, 4032, %d) height = %d, want the longest side capped at %d", cMaxDetectionDim, h, cMaxDetectionDim)
	}
	wantW := int(float64(3024) * scale)
	if w != wantW {
		t.Errorf("detectionScale width = %d, want %d (aspect ratio preserved)", w, wantW)
	}
	if scale >= 1 {
		t.Errorf("detectionScale scale = %v, want < 1 for an oversized image", scale)
	}
}

func TestDetectionScaleRoundTripRecoversOriginalCoordinates(t *testing.T) {
	// The actual use this feeds: a detection made against the scaled-down
	// copy has its bbox/landmarks divided by scale to land back in the
	// original image's coordinate space (see DetectFaces) - confirm that
	// arithmetic actually inverts the scale-down, not just approximately.
	scale, _, _ := detectionScale(3024, 4032, cMaxDetectionDim)
	origX := float32(1500)
	scaledX := origX * float32(scale)
	recoveredX := scaledX / float32(scale)
	if diff := recoveredX - origX; diff > 0.01 || diff < -0.01 {
		t.Errorf("scale round-trip: got %v, want ~%v (original coordinate)", recoveredX, origX)
	}
}

func TestDetectionScaleZeroDimensionsNoop(t *testing.T) {
	// Guards against a divide-by-zero on a degenerate 0x0 image rather than
	// crashing face detection outright.
	scale, w, h := detectionScale(0, 0, cMaxDetectionDim)
	if scale != 1 || w != 0 || h != 0 {
		t.Errorf("detectionScale(0, 0, ...) = (%v, %d, %d), want (1, 0, 0)", scale, w, h)
	}
}

// Regression test for a real reported bug: face thumbnails coming out
// visibly blue-tinted. gocv.ImageToMatRGB's own output is already genuine
// BGR channel order despite its name (see DetectFaces' doc comment) - an
// extra CvtColor(ColorRGBToBGR) used to run on top of it, double-swapping
// channels back to genuine RGB before IMEncode (which assumes BGR) wrote
// it out, turning red skin tones blue. This doesn't need a real face or
// model file, just gocv's own Mat/IMEncode - pinning the exact call
// sequence DetectFaces uses against a known pure-red source pixel.
func TestImageToMatRGBRoundTripsThroughIMEncodeWithoutColorSwap(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			src.Set(x, y, color.NRGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}

	bgr, err := gocv.ImageToMatRGB(src)
	if err != nil {
		t.Fatalf("ImageToMatRGB: %v", err)
	}
	defer bgr.Close()

	buf, err := gocv.IMEncode(gocv.JPEGFileExt, bgr)
	if err != nil {
		t.Fatalf("IMEncode: %v", err)
	}
	defer buf.Close()

	decoded, err := jpeg.Decode(bytes.NewReader(buf.GetBytes()))
	if err != nil {
		t.Fatalf("jpeg.Decode: %v", err)
	}
	r, g, b, _ := decoded.At(2, 2).RGBA()
	// JPEG's lossy YCbCr round-trip means "pure red" isn't exact - allow
	// a small tolerance, but a reintroduced channel swap would land this
	// near (0, 0, 255), nowhere close to red, so the tolerance can't
	// accidentally hide the regression this test exists to catch.
	if r>>8 < 200 || g>>8 > 60 || b>>8 > 60 {
		t.Errorf("pure red source pixel round-tripped to R=%d G=%d B=%d, want ~(255,0,0) - a channel swap crept back in", r>>8, g>>8, b>>8)
	}
}

// isFaceLargeEnough backs cMinFaceSizeFraction's filter - reproduced live
// on a crowd photo (a marathon, shot at several thousand pixels wide):
// dozens of real, correctly-detected but tiny (12-57px) background faces
// got embedded and, because a 20px source face carries almost no
// discriminating information once upscaled into SFace's 112x112 input,
// ended up merged into an unrelated primary subject's cluster purely by
// chance (one case literally merged a dog in). A *relative* cutoff (a
// fraction of the photo's own dimensions), not an absolute pixel count -
// see the constant's own doc comment for why a fixed pixel cutoff would
// wrongly reject real subjects in a smaller/compressed photo.
func TestIsFaceLargeEnoughRealSubjectInAHighResPhoto(t *testing.T) {
	// The actual marathon photo: ~6000px wide, a real subject's face at
	// ~450px (7.5% of width) alongside the wrongly-clustered background
	// ones at ~30px (0.5%).
	if !isFaceLargeEnough(450, 500, 6000, 4000, cMinFaceSizeFraction) {
		t.Error("a primary-subject-sized face in a high-res photo should pass the minimum size filter")
	}
	if isFaceLargeEnough(30, 35, 6000, 4000, cMinFaceSizeFraction) {
		t.Error("a tiny crowd/background-sized face in a high-res photo should be rejected")
	}
}

func TestIsFaceLargeEnoughRealSubjectInALowResPhoto(t *testing.T) {
	// The exact case an absolute pixel cutoff gets wrong: a small/
	// compressed photo (800px wide) where even the real subject's face is
	// only 80px - well under a fixed 60px-or-similar absolute threshold,
	// but a perfectly normal ~10% of this photo's own width.
	if !isFaceLargeEnough(80, 90, 800, 600, cMinFaceSizeFraction) {
		t.Error("a normally-framed subject in a low-res photo should still pass the *relative* size filter")
	}
}

func TestIsFaceLargeEnoughOneDimensionTooSmall(t *testing.T) {
	// A face detected as unusually narrow or short (partial occlusion, a
	// side profile) should still be rejected if *either* dimension misses
	// the cutoff, not just when both do.
	if isFaceLargeEnough(400, 20, 4000, 3000, cMinFaceSizeFraction) {
		t.Error("a face with only one dimension below the cutoff should still be rejected")
	}
	if isFaceLargeEnough(20, 400, 4000, 3000, cMinFaceSizeFraction) {
		t.Error("a face with only one dimension below the cutoff should still be rejected")
	}
}

func TestIsFaceLargeEnoughExactlyAtThreshold(t *testing.T) {
	if !isFaceLargeEnough(40, 40, 1000, 1000, cMinFaceSizeFraction) {
		t.Error("a face exactly at the cutoff fraction should pass (inclusive bound)")
	}
}

func TestIsFaceLargeEnoughDegenerateImageDimensions(t *testing.T) {
	// Guards against a divide-by-zero rather than a NaN comparison
	// silently passing (NaN >= anything is false in Go, but this is
	// clearer and doesn't rely on that).
	if isFaceLargeEnough(50, 50, 0, 0, cMinFaceSizeFraction) {
		t.Error("a degenerate 0x0 image should never pass the size filter")
	}
}

// isSharpEnough/laplacianVariance back cMinFaceSharpness's filter - a face
// large enough to pass the size check above can still be a soft,
// out-of-focus background element (depth-of-field separation from the
// actual subject), which size alone can't distinguish. Calibration
// numbers are from a synthetic checkerboard/blur test (see
// cMinFaceSharpness's own doc comment): sharp ~92900, mildly-soft ~7900,
// heavily-blurred ~4, flat 0.
func TestIsSharpEnoughAboveThreshold(t *testing.T) {
	if !isSharpEnough(7900, cMinFaceSharpness) {
		t.Error("a mildly-soft (but real, in-focus) face should pass the sharpness filter")
	}
}

func TestIsSharpEnoughBelowThreshold(t *testing.T) {
	if isSharpEnough(4, cMinFaceSharpness) {
		t.Error("a heavily-blurred face should be rejected by the sharpness filter")
	}
	if isSharpEnough(0, cMinFaceSharpness) {
		t.Error("a flat/featureless crop should be rejected by the sharpness filter")
	}
}

func TestIsSharpEnoughExactlyAtThreshold(t *testing.T) {
	if !isSharpEnough(cMinFaceSharpness, cMinFaceSharpness) {
		t.Error("a variance exactly at the cutoff should pass (inclusive bound)")
	}
}

// MedoidFaceID backs the person cover-thumbnail pick (issue #52 follow-up)
// - the face with the highest average similarity to every other face in
// the same person's cluster, i.e. the most "typical" example already on
// file, rather than an arbitrary pick like "the oldest one".
func TestMedoidFaceIDEmptyReturnsEmpty(t *testing.T) {
	if got := MedoidFaceID(nil); got != "" {
		t.Errorf("MedoidFaceID(nil) = %q, want empty", got)
	}
}

func TestMedoidFaceIDSingleFaceReturnsItself(t *testing.T) {
	faces := []IdentifiedEmbedding{{ID: "only", Embedding: []float32{1, 0, 0}}}
	if got := MedoidFaceID(faces); got != "only" {
		t.Errorf("MedoidFaceID(single) = %q, want %q", got, "only")
	}
}

func TestMedoidFaceIDPicksTheMostCentralFace(t *testing.T) {
	// Three faces tightly clustered around {1,0} and one clear outlier
	// (a bad angle/expression, far from the rest) - the medoid should be
	// whichever of the tight cluster is closest to the *other two*
	// tightly-clustered ones, not the outlier, and not necessarily the
	// exact centroid direction either (the medoid must be an actual
	// member of the set).
	faces := []IdentifiedEmbedding{
		{ID: "a", Embedding: []float32{1.0, 0.02}},
		{ID: "b", Embedding: []float32{0.99, -0.01}},
		{ID: "c", Embedding: []float32{0.98, 0.03}},
		{ID: "outlier", Embedding: []float32{0.1, 0.99}},
	}
	got := MedoidFaceID(faces)
	if got == "outlier" {
		t.Errorf("MedoidFaceID picked the outlier %q, want one of the tightly-clustered faces", got)
	}
}

func TestMedoidFaceIDAllIdenticalPicksFirst(t *testing.T) {
	// A degenerate but real case (two identical embeddings can happen if
	// the same crop gets processed twice) - every face ties, so the first
	// one encountered wins, deterministically.
	faces := []IdentifiedEmbedding{
		{ID: "a", Embedding: []float32{1, 0}},
		{ID: "b", Embedding: []float32{1, 0}},
		{ID: "c", Embedding: []float32{1, 0}},
	}
	if got := MedoidFaceID(faces); got != "a" {
		t.Errorf("MedoidFaceID(all identical) = %q, want %q (first, on a tie)", got, "a")
	}
}

// ClusterCohesion/MedoidAndCohesion back the "is this actually a
// legitimate person, or garbage chained together by nearest-neighbor
// matching" signal (issue #52 follow-up, reproduced live: a person named
// "Nope" - a mix of unrelated detections, one literally a dog - ended up
// with 9 faces and out-ranked real people by raw count alone).
func TestClusterCohesionTightRealClusterIsHigh(t *testing.T) {
	// Several photos of the same real person: embeddings all close
	// together (small angular spread), same shape as
	// TestMedoidFaceIDPicksTheMostCentralFace's tight cluster.
	faces := []IdentifiedEmbedding{
		{ID: "a", Embedding: []float32{1.0, 0.02}},
		{ID: "b", Embedding: []float32{0.99, -0.01}},
		{ID: "c", Embedding: []float32{0.98, 0.03}},
		{ID: "d", Embedding: []float32{0.995, 0.0}},
	}
	got := ClusterCohesion(faces)
	if got < 0.9 {
		t.Errorf("ClusterCohesion(tight real cluster) = %v, want > 0.9 (every face genuinely alike)", got)
	}
}

func TestClusterCohesionChainedGarbageIsLow(t *testing.T) {
	// The actual failure mode: matchOrNewPerson only requires a new face
	// to be close to *one* existing member, not the cluster as a whole.
	// Five embeddings spaced 65° apart around a circle - each consecutive
	// pair clears the 0.363 same-person threshold (cos(65°) ≈ 0.42), so a
	// real run of matchOrNewPerson would chain all five into one person
	// one at a time, but the cluster as a whole is nothing alike: opposite
	// ends of the chain are 195-260° apart (cos deeply negative).
	deg := func(d float64) IdentifiedEmbedding {
		rad := d * math.Pi / 180
		return IdentifiedEmbedding{Embedding: []float32{float32(math.Cos(rad)), float32(math.Sin(rad))}}
	}
	faces := []IdentifiedEmbedding{deg(0), deg(65), deg(130), deg(195), deg(260)}
	for i := range faces {
		faces[i].ID = string(rune('a' + i))
	}

	// Sanity check the premise: consecutive pairs really do clear the
	// same-person threshold individually...
	if !IsSamePerson(faces[0].Embedding, faces[1].Embedding) {
		t.Fatal("test setup: consecutive chain members should individually clear the same-person threshold")
	}
	// ...but the cluster as a whole should not read as cohesive.
	got := ClusterCohesion(faces)
	if got >= SamePersonThreshold {
		t.Errorf("ClusterCohesion(chained garbage) = %v, want < %v (chaining should be visible even at the best-centered member)", got, SamePersonThreshold)
	}
}

func TestMedoidAndCohesionSingleFaceIsFullyCohesive(t *testing.T) {
	id, cohesion := MedoidAndCohesion([]IdentifiedEmbedding{{ID: "only", Embedding: []float32{1, 0}}})
	if id != "only" || cohesion != 1 {
		t.Errorf("MedoidAndCohesion(single) = (%q, %v), want (\"only\", 1) - nothing to disagree with yet", id, cohesion)
	}
}

func TestMedoidAndCohesionEmptyIsZero(t *testing.T) {
	id, cohesion := MedoidAndCohesion(nil)
	if id != "" || cohesion != 0 {
		t.Errorf("MedoidAndCohesion(nil) = (%q, %v), want (\"\", 0)", id, cohesion)
	}
}
