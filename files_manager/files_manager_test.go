// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"archive/zip"
	"bytes"
	"image"
	"image/color"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/exifinfo"
	imagestagger "github.com/alonsovidales/otc/images_tagger"
)

func TestCosineSimilarityIdenticalVectors(t *testing.T) {
	mg := &Manager{}
	a := []float32{1, 2, 3}

	got := mg.cosineSimilarity(a, a)
	if math.Abs(float64(got-1)) > 1e-6 {
		t.Errorf("expected cosine similarity of a vector with itself to be ~1, got %v", got)
	}
}

func TestCosineSimilarityOrthogonalVectors(t *testing.T) {
	mg := &Manager{}
	a := []float32{1, 0}
	b := []float32{0, 1}

	got := mg.cosineSimilarity(a, b)
	if math.Abs(float64(got)) > 1e-6 {
		t.Errorf("expected cosine similarity of orthogonal vectors to be ~0, got %v", got)
	}
}

func TestCosineSimilarityOppositeVectors(t *testing.T) {
	mg := &Manager{}
	a := []float32{1, 2, 3}
	b := []float32{-1, -2, -3}

	got := mg.cosineSimilarity(a, b)
	if math.Abs(float64(got+1)) > 1e-6 {
		t.Errorf("expected cosine similarity of opposite vectors to be ~-1, got %v", got)
	}
}

func TestGetCipherRoundTrip(t *testing.T) {
	cp := getCipher("some-secret")

	nonce := make([]byte, cp.NonceSize())
	plain := []byte("round trip me")
	enc := cp.Seal(nonce, nonce, plain, nil)

	dec, err := cp.Open(nil, enc[:cp.NonceSize()], enc[cp.NonceSize():], nil)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	if !bytes.Equal(dec, plain) {
		t.Errorf("round trip mismatch: got %q, want %q", dec, plain)
	}
}

func TestGetCipherIsDeterministicPerSecret(t *testing.T) {
	a := getCipher("same-secret")
	b := getCipher("same-secret")

	nonce := make([]byte, a.NonceSize())
	plain := []byte("data")
	encA := a.Seal(nonce, nonce, plain, nil)

	// A cipher derived from the same secret must be able to decrypt data
	// sealed by another cipher derived from that same secret.
	dec, err := b.Open(nil, encA[:b.NonceSize()], encA[b.NonceSize():], nil)
	if err != nil {
		t.Fatalf("expected ciphers derived from the same secret to interoperate: %v", err)
	}
	if !bytes.Equal(dec, plain) {
		t.Errorf("round trip mismatch: got %q, want %q", dec, plain)
	}
}

func TestGetCipherDifferentSecretsDoNotInteroperate(t *testing.T) {
	a := getCipher("secret-one")
	b := getCipher("secret-two")

	nonce := make([]byte, a.NonceSize())
	enc := a.Seal(nonce, nonce, []byte("data"), nil)

	if _, err := b.Open(nil, enc[:b.NonceSize()], enc[b.NonceSize():], nil); err == nil {
		t.Error("expected a cipher derived from a different secret to fail to decrypt")
	}
}

func TestIsSharedLinkExpired(t *testing.T) {
	now := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	ttl := 24 * time.Hour

	cases := []struct {
		name    string
		created time.Time
		want    bool
	}{
		{"well within ttl", now.Add(-time.Hour), false},
		{"just under ttl", now.Add(-ttl + time.Minute), false},
		{"exactly at ttl boundary", now.Add(-ttl), false},
		{"just past ttl", now.Add(-ttl - time.Minute), true},
		{"long expired", now.Add(-30 * 24 * time.Hour), true},
		{"created in the future", now.Add(time.Hour), false},
	}

	for _, c := range cases {
		if got := isSharedLinkExpired(c.created, now, ttl); got != c.want {
			t.Errorf("%s: isSharedLinkExpired() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCollectExpiredTokensRemovesOnlyExpiredEntries(t *testing.T) {
	mg := &Manager{
		searchTokens:   new(sync.Map),
		tokensToExpire: new(sync.Map),
	}

	now := time.Now()
	mg.searchTokens.Store("fresh", "fresh-value")
	mg.tokensToExpire.Store("fresh", now)

	mg.searchTokens.Store("stale", "stale-value")
	mg.tokensToExpire.Store("stale", now.Add(-cToeknsTTL-time.Minute))

	mg.collectExpiredTokens()

	if _, ok := mg.searchTokens.Load("fresh"); !ok {
		t.Error("expected the fresh token to survive collection")
	}
	if _, ok := mg.tokensToExpire.Load("fresh"); !ok {
		t.Error("expected the fresh token's expiry entry to survive collection")
	}
	if _, ok := mg.searchTokens.Load("stale"); ok {
		t.Error("expected the stale token to be removed")
	}
	if _, ok := mg.tokensToExpire.Load("stale"); ok {
		t.Error("expected the stale token's expiry entry to be removed")
	}
}

func TestSharedLinkTTLFromHours(t *testing.T) {
	cases := []struct {
		hours int64
		want  time.Duration
	}{
		{0, cDefaultSharedLinkTTL},  // unset key parses to 0 -> default
		{-5, cDefaultSharedLinkTTL}, // defensive: never a negative TTL
		{1, time.Hour},
		{168, 168 * time.Hour},
	}

	for _, c := range cases {
		if got := sharedLinkTTLFromHours(c.hours); got != c.want {
			t.Errorf("sharedLinkTTLFromHours(%d) = %v, want %v", c.hours, got, c.want)
		}
	}
}

// Issue #66: goheif.Decode returns HEIC pixels un-rotated, so
// applyOrientation is what actually makes a portrait phone photo come out
// right-side-up after conversion to JPEG. Cases below use a labelled 2x2
// image (A top-left, B top-right, C bottom-left, D bottom-right) and check
// against the layout each EXIF Orientation value is defined to produce.
func TestApplyOrientation(t *testing.T) {
	a := color.NRGBA{R: 1, A: 255}
	b := color.NRGBA{R: 2, A: 255}
	c := color.NRGBA{R: 3, A: 255}
	d := color.NRGBA{R: 4, A: 255}

	newSrc := func() *image.NRGBA {
		img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
		img.SetNRGBA(0, 0, a)
		img.SetNRGBA(1, 0, b)
		img.SetNRGBA(0, 1, c)
		img.SetNRGBA(1, 1, d)
		return img
	}

	cases := []struct {
		name        string
		orientation int
		want        [2][2]color.NRGBA // want[y][x]
	}{
		{"unspecified is a no-op", 0, [2][2]color.NRGBA{{a, b}, {c, d}}},
		{"normal is a no-op", 1, [2][2]color.NRGBA{{a, b}, {c, d}}},
		{"out of range is a no-op", 9, [2][2]color.NRGBA{{a, b}, {c, d}}},
		{"mirror horizontal", 2, [2][2]color.NRGBA{{b, a}, {d, c}}},
		{"rotate 180", 3, [2][2]color.NRGBA{{d, c}, {b, a}}},
		{"mirror vertical", 4, [2][2]color.NRGBA{{c, d}, {a, b}}},
		{"transpose", 5, [2][2]color.NRGBA{{a, c}, {b, d}}},
		{"rotate 90 CW", 6, [2][2]color.NRGBA{{c, a}, {d, b}}},
		{"transverse", 7, [2][2]color.NRGBA{{d, b}, {c, a}}},
		{"rotate 270 CW", 8, [2][2]color.NRGBA{{b, d}, {a, c}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := applyOrientation(newSrc(), tc.orientation)
			bounds := got.Bounds()
			if bounds.Dx() != 2 || bounds.Dy() != 2 {
				t.Fatalf("unexpected bounds for a 2x2 source: %v", bounds)
			}
			for y := 0; y < 2; y++ {
				for x := 0; x < 2; x++ {
					r, g, bl, al := got.At(x, y).RGBA()
					wr, wg, wbl, wal := tc.want[y][x].RGBA()
					if r != wr || g != wg || bl != wbl || al != wal {
						t.Errorf("pixel (%d,%d) = %v, want %v", x, y, got.At(x, y), tc.want[y][x])
					}
				}
			}
		})
	}
}

// testdata/rotate.heic is github.com/jdeng/goheif's own metadata-only test
// fixture (heif/testdata/rotate.heic, MIT licensed) — a real HEIC container
// with a genuine "irot" rotation property but its pixel data trimmed down
// to keep the fixture small, so it parses fine for heifTransform but can't
// be run through a full goheif.Decode. It's what actually caught issue #66
// not being fixed by the first attempt: a real photo like this reads as
// EXIF Orientation 1 ("normal") despite being visibly rotated, because the
// rotation lives in this irot property instead — exactly the gap
// heifTransform exists to close.
func TestHeifTransformReadsRealIrotProperty(t *testing.T) {
	data, err := os.ReadFile("testdata/rotate.heic")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	rotations, hasMirror, _ := heifTransform(data)
	if rotations != 3 {
		t.Errorf("expected 3 rotations (matches goheif's own heif_test.go), got %d", rotations)
	}
	if hasMirror {
		t.Error("expected no mirror property on this fixture")
	}
}

func TestHeifTransformReturnsZeroValueOnUnparseableData(t *testing.T) {
	rotations, hasMirror, mirrorAxis := heifTransform([]byte("not a heic file"))
	if rotations != 0 || hasMirror || mirrorAxis != 0 {
		t.Errorf("expected the zero value for unparseable data, got rotations=%d hasMirror=%v mirrorAxis=%d", rotations, hasMirror, mirrorAxis)
	}
}

// applyHeicOrientation itself needs a real goheif-decodable image to
// exercise end-to-end (which testdata/rotate.heic, trimmed to metadata
// only, can't provide) — these instead pin its two branches directly
// against known heifTransform outputs, using the same labelled-2x2-image
// approach as TestApplyOrientation.
func TestApplyHeicOrientation(t *testing.T) {
	a := color.NRGBA{R: 1, A: 255}
	b := color.NRGBA{R: 2, A: 255}
	c := color.NRGBA{R: 3, A: 255}
	d := color.NRGBA{R: 4, A: 255}
	newSrc := func() *image.NRGBA {
		img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
		img.SetNRGBA(0, 0, a)
		img.SetNRGBA(1, 0, b)
		img.SetNRGBA(0, 1, c)
		img.SetNRGBA(1, 1, d)
		return img
	}
	pixelAt := func(img image.Image, x, y int) color.NRGBA {
		r, g, bl, al := img.At(x, y).RGBA()
		return color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(bl >> 8), A: uint8(al >> 8)}
	}

	// No real HEIC container behind this data, so heifTransform reads
	// rotations=0/hasMirror=false and this must fall back to the EXIF
	// orientation the caller already read — orientation 6 (rotate 90 CW)
	// matches TestApplyOrientation's own case for the same layout.
	got := applyHeicOrientation([]byte("not a heic file"), newSrc(), 6)
	want := [2][2]color.NRGBA{{c, a}, {d, b}}
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			if p := pixelAt(got, x, y); p != want[y][x] {
				t.Errorf("fallback-to-EXIF: pixel (%d,%d) = %v, want %v", x, y, p, want[y][x])
			}
		}
	}
}

// testdata/orientation6.jpg is a synthetic 16x16 JPEG (4 solid-color
// quadrants: red top-left, green top-right, blue bottom-left, yellow
// bottom-right) carrying a real EXIF Orientation=6 tag, built the same way
// a plain (non-HEIC) photo straight off a phone camera actually looks. This
// is the exact case that turned out to be issue #66's real reproduction:
// the reported photo was never HEIC at all, so the fix aimed at HEIC files
// alone (reading heifTransform/EXIF inside heicToJpeg) never ran for it —
// the rotation was only ever lost in UploadFile's thumbnail path, which
// re-encodes any image, HEIC-sourced or not, into a brand new JPEG with no
// EXIF segment at all. This test exercises that exact sequence: decode,
// read EXIF, applyOrientation - the same three steps now inlined in
// UploadFile's goroutine for the !isHeic case - and checks the quadrants
// land where orientation 6 (rotate 90 CW) says they should, matching
// TestApplyOrientation's own "rotate_90_CW" case for the same layout.
func TestUploadFileOrientationSequenceOnPlainJPEG(t *testing.T) {
	data, err := os.ReadFile("testdata/orientation6.jpg")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decoding fixture: %v", err)
	}
	info, err := exifinfo.FromJPEG(data)
	if err != nil {
		t.Fatalf("reading EXIF: %v", err)
	}
	if info.Orientation != 6 {
		t.Fatalf("expected fixture to carry EXIF Orientation 6, got %d", info.Orientation)
	}

	got := applyOrientation(img, info.Orientation)

	bounds := got.Bounds()
	if bounds.Dx() != 16 || bounds.Dy() != 16 {
		t.Fatalf("expected a 16x16 result (square source, so no dimension swap to check separately), got %v", bounds)
	}
	quadrant := func(x, y int) color.Color { return got.At(x, y) }
	closeEnough := func(c color.Color, r, g, b uint32) bool {
		cr, cg, cb, _ := c.RGBA()
		const tolerance = 0x1000 // JPEG is lossy even at quality 100
		diff := func(a, b uint32) uint32 {
			if a > b {
				return a - b
			}
			return b - a
		}
		return diff(cr, r) < tolerance && diff(cg, g) < tolerance && diff(cb, b) < tolerance
	}

	// Rotate 90 CW: source top-left(red) -> dest top-right,
	// top-right(green) -> bottom-right, bottom-left(blue) -> top-left,
	// bottom-right(yellow) -> bottom-left (same mapping TestApplyOrientation
	// verifies for orientation 6 on a labelled 2x2).
	cases := []struct {
		name    string
		x, y    int
		r, g, b uint32
	}{
		{"top-left is now blue (was bottom-left)", 4, 4, 0, 0, 0xffff},
		{"top-right is now red (was top-left)", 12, 4, 0xffff, 0, 0},
		{"bottom-left is now yellow (was bottom-right)", 4, 12, 0xffff, 0xffff, 0},
		{"bottom-right is now green (was top-right)", 12, 12, 0, 0xffff, 0},
	}
	for _, c := range cases {
		if got := quadrant(c.x, c.y); !closeEnough(got, c.r, c.g, c.b) {
			t.Errorf("%s: pixel (%d,%d) = %v, want ~(%d,%d,%d)", c.name, c.x, c.y, got, c.r, c.g, c.b)
		}
	}
}

func TestApplyOrientationSwapsDimensionsWhenRotated(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 3, 2)) // 3 wide, 2 tall

	got := applyOrientation(src, 6) // rotate 90 CW
	if b := got.Bounds(); b.Dx() != 2 || b.Dy() != 3 {
		t.Errorf("rotate 90 on a 3x2 image should produce 2x3, got %v", b)
	}

	got = applyOrientation(src, 3) // rotate 180, no dimension swap
	if b := got.Bounds(); b.Dx() != 3 || b.Dy() != 2 {
		t.Errorf("rotate 180 on a 3x2 image should stay 3x2, got %v", b)
	}
}

// DelFile used to re-fetch by the exact path it had just deleted to decide
// whether another path still shares the same hash (dao.GetFileByPath scans
// with `where path = ?`) — but that query can only ever miss right after a
// delete, and a miss doesn't come back as nil (see dao.GetFileByPath's own
// implementation): it's a non-nil *pb.File either way, only the error
// distinguishes found from not-found. So `file != nil` was always true,
// and DelFile always returned early, never actually removing the
// underlying blob/thumbnail from disk — which is exactly why "delete this
// test photo and re-upload it" didn't regenerate its thumbnail with
// corrected rotation: the stale blob was never gone for the re-upload to
// find missing. This exercises the fixed check (by hash, not the
// now-deleted path) for the "still referenced" branch — the other branch
// needs a live [otc] storage-path to actually call os.Remove against,
// which isn't safe to exercise here (cfg.GetStr fatals the whole test
// binary via os.Exit if [otc] was never loaded, and loading real config
// isn't something a unit test should do process-wide).
func TestDelFileKeepsUnderlyingBlobWhenHashStillReferencedByAnotherPath(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	const hash = "abc123"
	fileRow := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"}).
			AddRow(hash, "image/jpeg", time.Now(), time.Now(), "/photos/a.jpg", 1234)
	}

	mock.ExpectQuery("select .* from `files` where `path` = \\?").
		WithArgs("/photos/a.jpg").
		WillReturnRows(fileRow())

	mock.ExpectBegin()
	mock.ExpectQuery("select `hash` from `files` where `path` = \\?").
		WithArgs("/photos/a.jpg").
		WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow(hash))
	mock.ExpectQuery("select count\\(\\*\\) from `files` where `hash` = \\?").
		WithArgs(hash).
		WillReturnRows(sqlmock.NewRows([]string{"count(*)"}).AddRow(2)) // >1: another path exists too
	mock.ExpectExec("delete from `files` where `path` = \\?").
		WithArgs("/photos/a.jpg").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	// The check DelFile actually needs: does *another* path still
	// reference this hash. A found row (no error) means yes.
	mock.ExpectQuery("select .* from `files` where `hash` = \\?").
		WithArgs(hash).
		WillReturnRows(fileRow())

	mg := &Manager{dao: dao.NewWithDB(db)}
	if err := mg.DelFile(nil, "/photos/a.jpg"); err != nil {
		t.Fatalf("DelFile returned an unexpected error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("not all expected queries ran (a fix that skips the GetFileByHash check, or reaches for cfg/os.Remove instead, would show up here): %v", err)
	}
}

// This is the actual bug behind a real "Could not publish: ... no such
// file or directory" report: UploadFile's thumbnail step used to only
// write a thumbnail file when resizing was actually needed (source wider
// than the configured max), leaving nothing on disk at all for an image
// that was already narrow enough — and NewPublication always expects one
// to be there. A portrait photo's corrected (post-rotation) width can
// easily end up under that threshold even when its original, unrotated
// width wasn't, which is exactly what made this easy to hit once
// orientation correction started actually rotating images.
func TestThumbnailSourceAlwaysReturnsSomethingToEncode(t *testing.T) {
	t.Run("narrower than max is returned unchanged, not dropped", func(t *testing.T) {
		src := image.NewRGBA(image.Rect(0, 0, 400, 800)) // portrait, e.g. post-rotation
		got := thumbnailSource(src, 1000)
		if got != image.Image(src) {
			t.Error("expected the exact same image back when no resize is needed, not a copy or nil")
		}
	})

	t.Run("exactly at max is returned unchanged", func(t *testing.T) {
		src := image.NewRGBA(image.Rect(0, 0, 1000, 500))
		got := thumbnailSource(src, 1000)
		if got != image.Image(src) {
			t.Error("expected the exact same image back at the boundary width")
		}
	})

	t.Run("wider than max is resized down to it", func(t *testing.T) {
		src := image.NewRGBA(image.Rect(0, 0, 4000, 2000))
		got := thumbnailSource(src, 1000)
		b := got.Bounds()
		if b.Dx() != 1000 {
			t.Errorf("expected width resized to 1000, got %d", b.Dx())
		}
		if b.Dy() != 500 {
			t.Errorf("expected height scaled proportionally to 500, got %d", b.Dy())
		}
	})
}

// A shared/downloaded zip used to name every entry "."+file.Path - each
// selected file's full path from the storage root - which reproduced the
// entire directory tree down to that file inside the archive instead of
// holding just the files actually selected. commonDirPrefix is what lets
// GetSharedLink strip that down to each file's location relative to what's
// actually shared between the selection.
func TestCommonDirPrefix(t *testing.T) {
	cases := []struct {
		name  string
		paths []string
		want  string
	}{
		{"single file", []string{"/a/b/c.jpg"}, "/a/b/"},
		{"several files, same directory - the reported case", []string{"/a/b/c.jpg", "/a/b/d.jpg", "/a/b/e.jpg"}, "/a/b/"},
		{"several files, different subdirectories", []string{"/a/b/c.jpg", "/a/e/f.jpg"}, "/a/"},
		{"root-level files", []string{"/c.jpg", "/d.jpg"}, "/"},
		{"no common directory at all", []string{"/a/c.jpg", "/z/d.jpg"}, "/"},
		{"nested common ancestor several levels deep", []string{"/a/b/c/d.jpg", "/a/b/c/e/f.jpg"}, "/a/b/c/"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := commonDirPrefix(c.paths); got != c.want {
				t.Errorf("commonDirPrefix(%v) = %q, want %q", c.paths, got, c.want)
			}
		})
	}
}

func TestGetSharedLinkZipsOnlySelectedFilesFlatWhenSameDirectory(t *testing.T) {
	buf := zipBytesForPaths(t, []string{"/a/b/c.jpg", "/a/b/d.jpg"})
	names := zipEntryNamesOf(t, buf)
	want := map[string]bool{"c.jpg": true, "d.jpg": true}
	if len(names) != len(want) {
		t.Fatalf("expected %d entries, got %v", len(want), names)
	}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected zip entry %q - should be a bare filename, not the full path (issue: used to include the whole directory tree)", n)
		}
	}
}

// zipBytesForPaths exercises the exact entry-naming logic GetSharedLink
// uses, without needing a live session/dao/GetFile round trip - the zip
// writing itself is standard library code not worth re-testing here.
func zipBytesForPaths(t *testing.T, paths []string) []byte {
	t.Helper()
	prefix := commonDirPrefix(paths)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range paths {
		w, err := zw.Create(strings.TrimPrefix(p, prefix))
		if err != nil {
			t.Fatalf("zip.Create: %v", err)
		}
		if _, err := w.Write([]byte("content")); err != nil {
			t.Fatalf("writing zip entry: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing zip writer: %v", err)
	}
	return buf.Bytes()
}

func zipEntryNamesOf(t *testing.T, data []byte) []string {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	names := make([]string, len(r.File))
	for i, f := range r.File {
		names[i] = f.Name
	}
	return names
}

// Issue #105 follow-up: the tagging model loads in the background so the
// device can register with the bridge immediately instead of being
// unreachable for the ~15s it takes (see Init). The gate is what keeps
// that safe - an upload arriving before the model is ready must wait for
// it, never read a half-initialized manager.
func TestWaitForTaggerBlocksUntilTheModelIsReady(t *testing.T) {
	mg := &Manager{taggerReady: make(chan struct{})}

	done := make(chan *imagestagger.RAMTagger, 1)
	go func() { done <- mg.waitForTagger() }()

	select {
	case <-done:
		t.Fatal("waitForTagger returned before the model was ready - an upload could dereference a nil tagger")
	case <-time.After(50 * time.Millisecond):
		// Still waiting, as it must be.
	}

	// Whatever Init's goroutine publishes is what callers get.
	sentinel := new(imagestagger.RAMTagger)
	mg.tagger = sentinel
	close(mg.taggerReady)

	select {
	case got := <-done:
		if got != sentinel {
			t.Error("waitForTagger returned something other than the loaded tagger")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitForTagger never returned after the model became ready")
	}
}
