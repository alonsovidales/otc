// SPDX-License-Identifier: AGPL-3.0-or-later

// Package facerecognition finds and identifies human faces in photos
// (issue #52) using OpenCV's YuNet (detection) and SFace (recognition)
// models via gocv - see this package's own tests and
// files_manager.processFaces' doc comment for the fuller design rationale
// (why gocv rather than a hand-rolled ONNX decode, why cosine similarity
// over raw embeddings rather than OpenCV's own Match call, why nothing here
// ever runs retroactively).
//
// Both models are official, actively-maintained OpenCV Zoo models
// (https://github.com/opencv/opencv_zoo), MIT/Apache-2.0 licensed, and
// designed as a matched pair - YuNet's detection output feeds directly into
// SFace's AlignCrop with no reimplementation of its anchor decoding or
// 5-point landmark alignment math needed on this side.
package facerecognition

import (
	"encoding/binary"
	"fmt"
	"image"
	"math"
	"os"
	"sync"

	"github.com/alonsovidales/otc/log"
	"gocv.io/x/gocv"
)

const (
	// cScoreThreshold/cNMSThreshold/cTopK are YuNet's own detector
	// parameters - 0.6 score threshold matches the model's own opencv_zoo
	// demo default (a real but non-borderline face), 5000 is a generous
	// per-photo cap that in practice a personal photo library never gets
	// close to.
	cScoreThreshold = 0.6
	cNMSThreshold   = 0.3
	cTopK           = 5000

	// SamePersonThreshold is OpenCV's own published cosine-similarity
	// cutoff for "this is the same person" with the SFace model
	// (https://docs.opencv.org/4.x/da/d09/classcv_1_1FaceRecognizerSF.html) -
	// not something this package tuned itself. Embeddings are compared
	// with CosineSimilarity below, never OpenCV's own Match/MatchWithParams
	// (which needs a live gocv.Mat on both sides) so a newly detected
	// face's embedding can be compared against every previously stored one
	// (plain []float32 from the database) without reconstructing a Mat for
	// each. Exported so dao.ListPeople's caller can pass it through as the
	// cohesion cutoff (see MedoidAndCohesion) without dao itself - a
	// low-level, CGO/OpenCV-free package by design - importing this one.
	SamePersonThreshold = 0.363

	// cMaxDetectionDim caps the longest side (in pixels) of whatever's
	// actually fed to the detector. A modern phone photo is routinely
	// 12MP+ (4032x3024 or bigger) - YuNet's anchor grid was designed and
	// benchmarked around much more modest resolutions (its own opencv_zoo
	// demo runs on a webcam frame or a handful-of-megapixels sample image,
	// never a raw phone photo), and running it against the full native
	// resolution is a well-known way to get exactly the failure mode this
	// constant exists to prevent: spurious high-confidence "detections" on
	// background texture (a patterned curtain, a dog's fur) while the
	// actual, perfectly clear human face in the same photo scores below
	// threshold or gets lost to NMS. 1600px is comfortably above the size
	// SFace's own aligned crop needs (112x112) and keeps the detector
	// inside the regime it was actually tuned for. Detection runs against
	// the resized copy, but embedFace still crops from the *original*
	// resolution (see the scale plumbing in DetectFaces) so image quality
	// of the stored face thumbnail/embedding never suffers for it.
	cMaxDetectionDim = 1600

	// cMinFaceSizeFraction is the smallest a detected face's bounding box
	// (width or height) may be, *as a fraction of the source image's own
	// width/height*, before its embedding is discarded rather than
	// clustered. Reproduced live: a crowd photo (a marathon, shot at
	// several thousand pixels wide) has dozens of real, correctly-
	// detected but tiny (20-50px) background faces - SFace's aligned crop
	// is 112x112, so a 20px source face is mostly upscale noise by the
	// time it's embedded, and that embedding carries so little
	// discriminating information that it ends up above the same-person
	// cosine threshold against nearly anything, silently merging
	// strangers (and, once, a dog) into whoever's cluster it happens to
	// compare against first.
	//
	// A fraction of the image's own dimensions, not a fixed pixel count:
	// a fixed cutoff is resolution-dependent in exactly the wrong
	// direction - it correctly rejects tiny crowd faces in a 6000px-wide
	// photo, but would just as readily reject a *real, primary* subject's
	// face in an 800px-wide compressed/screenshotted photo, where even a
	// legitimate subject's face is only 60-80px. 4% keeps the same
	// relative bar regardless of the source photo's resolution - the
	// primary-subject faces behind this fix occupied 5-15% of their
	// photo's width, the wrongly-clustered background ones under 1%.
	cMinFaceSizeFraction = 0.04

	// cMinFaceSharpness is a complementary filter to the size one above:
	// a face can be large enough to pass that check and still be a soft,
	// out-of-focus background element (depth-of-field separation from
	// the actual subject), which size alone can't distinguish. Measured
	// as the variance of the Laplacian of the aligned, grayscale crop - a
	// standard, cheap focus measure (sharp edges produce a
	// high-variance response; a smooth/blurry image produces almost
	// none). Calibrated empirically (see face_recognition_test.go): a
	// heavily-blurred 112x112 crop scored ~4, a mildly-soft one ~7900 -
	// 20 sits just above the "genuinely blurry" floor with a wide margin
	// before anything resembling real facial detail, erring toward
	// rejecting only clear-cut cases rather than risking real subjects.
	cMinFaceSharpness = 20
)

// FaceDetection is one detected face in a photo: its bounding box (pixels,
// in the original image), the aligned face's embedding (for clustering -
// see CosineSimilarity), and a small JPEG thumbnail of the aligned crop
// itself (issue #52's "list of recognised people" needs something to show
// per face without re-decoding/re-cropping the original photo every time).
type FaceDetection struct {
	X, Y, W, H int
	Score      float32
	Embedding  []float32
	Thumbnail  []byte
}

// Recognizer wraps one loaded YuNet detector + SFace recognizer pair.
// gocv's own docs don't promise a FaceDetectorYN/FaceRecognizerSF instance
// is safe for concurrent use, so every call is serialized through mu -
// detection only ever runs from files_manager's already-serialized
// per-upload background goroutine in practice, but this makes that safe to
// rely on rather than assumed.
type Recognizer struct {
	mu         sync.Mutex
	detector   gocv.FaceDetectorYN
	recognizer gocv.FaceRecognizerSF
}

// NewRecognizer loads both models from disk. Returns an error (not
// log.Fatal, unlike images_tagger.NewRAMTagger) since issue #52's feature
// is opt-in and off by default - a device that never enables it, or whose
// operator hasn't downloaded these two (small: ~230KB + ~10MB) models yet,
// must still start up and serve everything else normally.
func NewRecognizer(detectorModelPath, recognizerModelPath string) (*Recognizer, error) {
	if _, err := os.Stat(detectorModelPath); err != nil {
		return nil, fmt.Errorf("face detector model: %w", err)
	}
	if _, err := os.Stat(recognizerModelPath); err != nil {
		return nil, fmt.Errorf("face recognizer model: %w", err)
	}

	detector := gocv.NewFaceDetectorYNWithParams(
		detectorModelPath, "", image.Pt(320, 320),
		cScoreThreshold, cNMSThreshold, cTopK, 0, 0,
	)
	recognizer := gocv.NewFaceRecognizerSF(recognizerModelPath, "")

	return &Recognizer{detector: detector, recognizer: recognizer}, nil
}

func (r *Recognizer) Close() {
	r.detector.Close()
	r.recognizer.Close()
}

// DetectFaces finds every face in img and returns each one's bounding box,
// embedding, and thumbnail. A face that detects but fails to embed (a
// corrupt crop, an unexpected model output) is logged and skipped rather
// than failing the whole photo - same "don't let one bad thing take down
// the rest" reasoning as GetPublicationFiles' missing-thumbnail handling
// elsewhere in this codebase.
func (r *Recognizer) DetectFaces(img image.Image) ([]FaceDetection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Despite its name, gocv.ImageToMatRGB's own output is already genuine
	// BGR channel order (it packs each pixel's bytes as B,G,R before
	// building the Mat - see its source) - the same convention YuNet/SFace
	// and IMEncode below all expect, confirmed empirically: round-tripping
	// a known pure-red pixel through this exact call sequence and back out
	// through IMEncode+jpeg.Decode returns pure red untouched. An earlier
	// version of this function ran an *additional* CvtColor(ColorRGBToBGR)
	// on top of this already-BGR Mat - a second channel swap that un-does
	// the first, leaving genuinely RGB-ordered data flowing into both
	// detection and AlignCrop/IMEncode. That explains two real, reported
	// symptoms at once: face thumbnails coming out visibly blue-tinted
	// (skin tone's red channel landing in the blue channel once IMEncode's
	// BGR assumption was violated), and detection itself being degraded
	// (a CNN trained on BGR seeing channel-swapped input). Do not
	// reintroduce that conversion.
	bgr, err := gocv.ImageToMatRGB(img)
	if err != nil {
		return nil, fmt.Errorf("converting image for face detection: %w", err)
	}
	defer bgr.Close()

	// See cMaxDetectionDim's doc comment: detect against a capped-size
	// copy, but keep cropping/embedding from the original bgr so a face
	// thumbnail/embedding is never worse than the source photo allows.
	scale, detW, detH := detectionScale(bgr.Cols(), bgr.Rows(), cMaxDetectionDim)
	detectMat := bgr
	if scale != 1 {
		resized := gocv.NewMat()
		defer resized.Close()
		gocv.Resize(bgr, &resized, image.Pt(detW, detH), 0, 0, gocv.InterpolationArea)
		detectMat = resized
	}

	r.detector.SetInputSize(image.Pt(detectMat.Cols(), detectMat.Rows()))

	facesMat := gocv.NewMat()
	defer facesMat.Close()
	if rv := r.detector.Detect(detectMat, &facesMat); rv != 1 {
		return nil, fmt.Errorf("face detection failed (return code %d)", rv)
	}

	detections := make([]FaceDetection, 0, facesMat.Rows())
	for i := 0; i < facesMat.Rows(); i++ {
		row := facesMat.Row(i)
		if scale != 1 {
			// Map the bbox + 5 landmarks (everything but the trailing
			// confidence score) back to the original image's coordinate
			// space before AlignCrop below reads them against the
			// full-resolution bgr, not the downscaled copy just used for
			// detection.
			for c := 0; c < 14; c++ {
				row.SetFloatAt(0, c, row.GetFloatAt(0, c)/float32(scale))
			}
		}

		// See cMinFaceSizeFraction's doc comment: a real, legitimately-
		// detected but too-small-relative-to-its-photo face (background/
		// crowd) is skipped here, before it's ever embedded or clustered.
		if !isFaceLargeEnough(row.GetFloatAt(0, 2), row.GetFloatAt(0, 3), float32(bgr.Cols()), float32(bgr.Rows()), cMinFaceSizeFraction) {
			row.Close()
			continue
		}

		det, err := r.embedFace(bgr, row)
		row.Close()
		if err != nil {
			log.Error("error embedding detected face", i, ":", err)
			continue
		}
		detections = append(detections, det)
	}
	return detections, nil
}

// isFaceLargeEnough is the pure predicate behind cMinFaceSizeFraction's
// filter - split out, same reasoning as detectionScale, so the cutoff
// itself is unit-testable without a real Mat/detection row. w/h are the
// face's own bounding box dimensions; imgW/imgH the source photo's -
// checked as independent fractions (width against width, height against
// height) rather than mixing axes, so this stays correct for both
// portrait and landscape photos.
func isFaceLargeEnough(w, h, imgW, imgH, minFraction float32) bool {
	if imgW <= 0 || imgH <= 0 {
		return false
	}
	return w/imgW >= minFraction && h/imgH >= minFraction
}

// laplacianVariance is a standard, cheap focus measure: the variance of
// the Laplacian (second-derivative/edge response) of a grayscale image.
// Sharp edges (real facial detail - eyes, hairline, texture) produce a
// high-variance response; a smooth, out-of-focus image produces almost
// none. See cMinFaceSharpness's doc comment for the calibration data
// behind the threshold this feeds.
func laplacianVariance(bgr gocv.Mat) float64 {
	gray := gocv.NewMat()
	defer gray.Close()
	gocv.CvtColor(bgr, &gray, gocv.ColorBGRToGray)

	lap := gocv.NewMat()
	defer lap.Close()
	gocv.Laplacian(gray, &lap, gocv.MatTypeCV64F, 1, 1, 0, gocv.BorderDefault)

	mean := gocv.NewMat()
	defer mean.Close()
	stddev := gocv.NewMat()
	defer stddev.Close()
	gocv.MeanStdDev(lap, &mean, &stddev)

	sd := stddev.GetDoubleAt(0, 0)
	return sd * sd
}

// isSharpEnough is the pure predicate behind cMinFaceSharpness's filter -
// split out for the same testability reasons as isFaceLargeEnough/
// detectionScale.
func isSharpEnough(variance, minVariance float64) bool {
	return variance >= minVariance
}

// detectionScale returns the downscale factor (1 if no resize is needed)
// and target dimensions to bring an image's longest side down to maxDim -
// pure arithmetic, deliberately split out of DetectFaces so it's testable
// without a real Mat/model (see this package's own tests).
func detectionScale(width, height, maxDim int) (scale float64, targetW, targetH int) {
	longest := width
	if height > longest {
		longest = height
	}
	if longest <= maxDim || longest == 0 {
		return 1, width, height
	}
	scale = float64(maxDim) / float64(longest)
	targetW = int(float64(width) * scale)
	targetH = int(float64(height) * scale)
	if targetW < 1 {
		targetW = 1
	}
	if targetH < 1 {
		targetH = 1
	}
	return scale, targetW, targetH
}

// embedFace aligns+embeds one YuNet detection row - row is handed straight
// to AlignCrop exactly as YuNet produced it (bbox + 5-point landmarks +
// score, 15 floats), never reconstructed by hand: it's already in exactly
// the format AlignCrop expects, since the two models are designed as a
// matched pair.
func (r *Recognizer) embedFace(bgr, row gocv.Mat) (FaceDetection, error) {
	raw, err := row.DataPtrFloat32()
	if err != nil {
		return FaceDetection{}, fmt.Errorf("reading detection row: %w", err)
	}
	if len(raw) < 15 {
		return FaceDetection{}, fmt.Errorf("unexpected detection row length %d, want >= 15", len(raw))
	}

	aligned := gocv.NewMat()
	defer aligned.Close()
	r.recognizer.AlignCrop(bgr, row, &aligned)

	// See cMinFaceSharpness's doc comment: a face large enough to pass
	// the size filter can still be a soft, out-of-focus background
	// element (depth-of-field separation from the actual subject) - size
	// alone can't catch that, and clustering a blurry embedding is just
	// as much a source of bad matches as a too-small one. Checked before
	// the (more expensive) embedding inference below, so a rejected crop
	// doesn't pay for it.
	if sharpness := laplacianVariance(aligned); !isSharpEnough(sharpness, cMinFaceSharpness) {
		return FaceDetection{}, fmt.Errorf("face too blurry (sharpness %.1f < %.1f)", sharpness, float64(cMinFaceSharpness))
	}

	feature := gocv.NewMat()
	defer feature.Close()
	r.recognizer.Feature(aligned, &feature)
	featData, err := feature.DataPtrFloat32()
	if err != nil {
		return FaceDetection{}, fmt.Errorf("reading face embedding: %w", err)
	}
	// featData/thumbBuf reference gocv-owned memory that's freed as soon as
	// feature/thumbBuf are Closed (deferred/explicit below) - copy both out
	// before returning.
	embedding := append([]float32(nil), featData...)

	thumbBuf, err := gocv.IMEncode(gocv.JPEGFileExt, aligned)
	if err != nil {
		return FaceDetection{}, fmt.Errorf("encoding face thumbnail: %w", err)
	}
	defer thumbBuf.Close()
	thumbnail := append([]byte(nil), thumbBuf.GetBytes()...)

	return FaceDetection{
		X: int(raw[0]), Y: int(raw[1]), W: int(raw[2]), H: int(raw[3]),
		Score:     raw[14],
		Embedding: embedding,
		Thumbnail: thumbnail,
	}, nil
}

// CosineSimilarity is plain Go, deliberately independent of gocv/OpenCV
// (unlike FaceRecognizerSF.Match, which needs a live Mat on both sides) -
// see the package doc comment for why: it's what lets a newly detected
// face's embedding be compared against every previously stored embedding
// (plain []float32 rows from the database) without a Mat round-trip per
// comparison. Returns 0 for mismatched-length or empty vectors rather than
// panicking or dividing by zero - callers treat that the same as "not a
// match" (see SamePersonThreshold).
func CosineSimilarity(a, b []float32) float32 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	sim := dot / (math.Sqrt(normA) * math.Sqrt(normB))
	return float32(sim)
}

// IsSamePerson reports whether two embeddings clear OpenCV's own published
// cosine-similarity cutoff for the SFace model.
func IsSamePerson(a, b []float32) bool {
	return IsSamePersonScore(CosineSimilarity(a, b))
}

// IsSamePersonScore is IsSamePerson's threshold check on its own, for a
// caller (files_manager.matchOrNewPerson) that already has a similarity
// score in hand - e.g. the best of several comparisons - and would
// otherwise have to re-derive it from the two embeddings again.
func IsSamePersonScore(score float32) bool {
	return score >= SamePersonThreshold
}

// IdentifiedEmbedding pairs one face's id with its embedding - MedoidFaceID's
// only input, kept independent of dao's own row type so this package has
// no dependency on dao.
type IdentifiedEmbedding struct {
	ID        string
	Embedding []float32
}

// MedoidFaceID returns the id of whichever face in faces has the highest
// average cosine similarity to every other face in the set - the most
// "typical" example of this person's face among everything already
// stored, used as their cover thumbnail (issue #52 follow-up: the
// previous pick, simply the oldest detected face, could just as easily be
// a bad angle or an awkward expression). This is the medoid of the
// cluster, not its centroid - the centroid (a literal average of every
// embedding) isn't a face anyone actually has a photo of; the medoid is
// the real member that best represents it, recomputed by
// files_manager.processFaces every time a new face is added to a person
// so the pick can only improve as more photos come in.
func MedoidFaceID(faces []IdentifiedEmbedding) string {
	id, _ := MedoidAndCohesion(faces)
	return id
}

// ClusterCohesion is MedoidFaceID's companion number: the medoid's own
// average similarity to every other face in the set. See
// MedoidAndCohesion's doc comment for what this signals and why - a
// convenience for a caller that only wants the score, e.g. deciding
// whether a person is legitimate rather than which face represents them.
func ClusterCohesion(faces []IdentifiedEmbedding) float32 {
	_, cohesion := MedoidAndCohesion(faces)
	return cohesion
}

// MedoidAndCohesion computes the medoid and its cohesion score together,
// in one O(n^2) pass - split out so a caller wanting both (files_manager.
// updatePersonCoverFace) doesn't pay for the pairwise comparisons twice.
// O(n^2) is fine at personal-library scale for one person's own face
// count (see ListFaceEmbeddings' own doc comment on the same assumption),
// especially now that cMinFaceSizeFraction/cMinFaceSharpness keep a
// person's cluster from ballooning with misclustered background faces in
// the first place.
//
// Cohesion matters because matchOrNewPerson clusters by nearest-neighbor:
// a new face joins a person if it's close to *any one* of their existing
// faces, not close to the person as a whole. That's a real, well-known
// failure mode of this kind of clustering ("chaining"): if face A matches
// B, and face C separately matches B (not A), C joins the same person as
// A even though A and C were never compared to each other and might not
// be alike at all. A person built up partly through chains like that can
// end up with faces that, taken as a group, aren't mutually similar -
// reproduced live as a person someone had renamed "Nope" (a mix of
// unrelated garbage, one frame literally a dog) accumulating faces across
// many photos and out-ranking real people in the "most-photographed"
// list (issue #75) purely because chaining inflated its count, not
// because it was a real, recurring subject. Cohesion (the medoid's own
// average similarity to the rest of the cluster) is low exactly when this
// has happened - a real person's medoid (their most "typical" photo) is
// close to everything else in their cluster by construction; a chained-
// together non-person's isn't. See dao.ListPeople for how this sinks a
// low-cohesion "person" to the bottom of the list rather than trusting
// raw face count alone.
func MedoidAndCohesion(faces []IdentifiedEmbedding) (id string, cohesion float32) {
	if len(faces) == 0 {
		return "", 0
	}
	if len(faces) == 1 {
		// Nothing to disagree with yet - one face can't be incohesive.
		return faces[0].ID, 1
	}

	bestID := faces[0].ID
	bestAvg := float32(math.Inf(-1))
	for i := range faces {
		var total float32
		for j := range faces {
			if i == j {
				continue
			}
			total += CosineSimilarity(faces[i].Embedding, faces[j].Embedding)
		}
		avg := total / float32(len(faces)-1)
		if avg > bestAvg {
			bestAvg = avg
			bestID = faces[i].ID
		}
	}
	return bestID, bestAvg
}

// EncodeEmbedding/DecodeEmbedding turn a raw feature vector into the flat
// byte blob dao.AddFace/ListFaceEmbeddings store and read back - little-
// endian float32s, back to back, self-describing by length (4 bytes each)
// so nothing needs to know the model's embedding dimension up front. Kept
// as plain functions independent of any particular Recognizer instance:
// encoding/decoding an embedding has nothing to do with which model
// produced it.
func EncodeEmbedding(embedding []float32) []byte {
	out := make([]byte, len(embedding)*4)
	for i, v := range embedding {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(v))
	}
	return out
}

func DecodeEmbedding(data []byte) []float32 {
	out := make([]float32, len(data)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return out
}
