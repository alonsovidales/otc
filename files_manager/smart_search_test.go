// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"context"
	"errors"
	"image"
	"math"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
)

func normalised(v []float32) []float32 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	inv := float32(1 / math.Sqrt(s))
	for i := range v {
		v[i] *= inv
	}
	return v
}

func randomVec(r *rand.Rand, n int) []float32 {
	v := make([]float32, n)
	for i := range v {
		v[i] = float32(r.NormFloat64())
	}
	return normalised(v)
}

// A vector kept as one byte a number keeps its cosines: the demo library's
// search precision was the same as with float32.
func TestPackVecRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	v := randomVec(r, 768)
	packed := packVec(v)
	if len(packed) != 773 || packed[0] != cEmbedFormat {
		t.Fatalf("packed %d bytes, format %d", len(packed), packed[0])
	}
	scale, q, err := unpackVec(packed)
	if err != nil || len(q) != 768 {
		t.Fatalf("unpack: %v %d", err, len(q))
	}
	var dot float64
	for i := range v {
		dot += float64(v[i]) * float64(q[i]) * float64(scale)
	}
	if dot < 0.999 {
		t.Errorf("cosine with itself after packing %.5f", dot)
	}
	for _, bad := range [][]byte{nil, {cEmbedFormat}, {2, 0, 0, 0x80, 0x3f, 1}, {cEmbedFormat, 0, 0, 0, 0, 1}} {
		if _, _, err := unpackVec(bad); err == nil {
			t.Errorf("%v was read as a vector", bad)
		}
	}
	// A zero vector packs without dividing by zero.
	if _, _, err := unpackVec(packVec(make([]float32, 4))); err != nil {
		t.Errorf("a zero vector: %v", err)
	}
}

func TestRankScores(t *testing.T) {
	got := rankScores([]scoredPhoto{{0, 0.04}, {1, 0.09}, {2, 0.06}, {3, 0.10}, {4, 0.07}, {5, 0.07}})
	// The best is 0.10: kept from max(0.05, 0.07) up, best first, ties in
	// the order they came (newest first).
	want := []int{3, 1, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("kept %v", got)
	}
	for i, w := range want {
		if got[i].idx != w {
			t.Fatalf("order %v, want %v", got, want)
		}
	}
	// The floor: nothing close enough, nothing kept.
	if got := rankScores([]scoredPhoto{{0, 0.03}, {1, 0.04}}); len(got) != 0 {
		t.Errorf("below the floor: %v", got)
	}
	if rankScores(nil) != nil {
		t.Error("no candidates")
	}
}

func TestSmartText(t *testing.T) {
	if got := smartText("  kids in the snow \t"); got != "kids in the snow" {
		t.Errorf("trimmed %q", got)
	}
	if smartText("   ") != "" {
		t.Error("spaces are no text")
	}
	long := strings.Repeat("ñ", cMaxSmartText) // two bytes each
	got := smartText(long)
	if len(got) > cMaxSmartText || !utf8.ValidString(got) {
		t.Errorf("cut to %d bytes, valid %v", len(got), utf8.ValidString(got))
	}
}

// Memories' bursts by Smart search's vectors: the same photo whenever it
// was taken, a burst's shots within a minute, and the vectors win over the
// difference hashes when both photos have one.
func TestBurstOfBySmartSearch(t *testing.T) {
	at := func(s int) time.Time { return time.Date(2024, 6, 1, 12, 0, s, 0, time.UTC) }
	photos := []memPhoto{{hash: "a", local: at(0)}, {hash: "b", local: at(0).Add(3 * time.Hour)}, {hash: "c", local: at(40)}, {hash: "d", local: at(100)}, {hash: "e", local: at(90)}}
	cos := map[[2]string]float32{{"b", "a"}: 0.96, {"c", "a"}: 0.93, {"d", "a"}: 0.93, {"e", "a"}: 0.5}
	similar := func(x, y string) (float32, bool) {
		c, ok := cos[[2]string{x, y}]
		return c, ok
	}
	same := func(string) (uint64, bool) { return 42, true } // every hash alike
	for i, want := range map[int]bool{1: true, 2: true, 3: false, 4: false} {
		if got := burstOf(photos, []int{0}, i, same, similar); got != want {
			t.Errorf("%s: burst %v, want %v", photos[i].hash, got, want)
		}
	}
	// Without Smart search the difference hashes decide, within the window.
	if !burstOf(photos, []int{0}, 4, same, nil) {
		t.Error("without vectors, alike hashes 90 s apart are a burst")
	}
	// A photo without a vector falls back to the hashes too.
	photos = append(photos, memPhoto{hash: "f", local: at(30)})
	if !burstOf(photos, []int{0}, 5, same, similar) {
		t.Error("no vector: the hashes decide")
	}
}

type fakeImageTower struct {
	mu     sync.Mutex
	closed bool
	calls  int
}

func (f *fakeImageTower) EmbedResized(context.Context, *image.RGBA) ([]float32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return []float32{1, 0}, nil
}
func (f *fakeImageTower) Close() { f.closed = true }

type fakeTextTower struct {
	vec    []float32
	closed bool
}

func (f *fakeTextTower) EmbedText(context.Context, string) ([]float32, error) { return f.vec, nil }
func (f *fakeTextTower) Close()                                               { f.closed = true }

// The towers load when first needed, one load each, and go once unused for
// idle - never while a call is under way.
func TestEmbedModelsLoadsOnUseAndReleasesWhenIdle(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	img, txt := &fakeImageTower{}, &fakeTextTower{vec: []float32{0, 1}}
	loads := 0
	e := &embedModels{
		run: &sync.Mutex{}, idle: cEmbedIdle, now: func() time.Time { return now },
		loadImage: func() (imageTower, error) { loads++; return img, nil },
		loadText:  func() (textTower, error) { loads++; return txt, nil },
	}
	if r := e.releaseIdle(); len(r) != 0 {
		t.Fatalf("released %v before anything loaded", r)
	}
	for i := 0; i < 3; i++ {
		if _, err := e.EmbedResized(context.Background(), image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.EmbedText(context.Background(), "snow"); err != nil {
		t.Fatal(err)
	}
	if loads != 2 || img.calls != 3 {
		t.Fatalf("%d loads, %d calls", loads, img.calls)
	}
	now = now.Add(cEmbedIdle - time.Second)
	if r := e.releaseIdle(); len(r) != 0 {
		t.Errorf("released %v before idle", r)
	}
	now = now.Add(2 * time.Second)
	if r := e.releaseIdle(); len(r) != 2 || !img.closed || !txt.closed {
		t.Errorf("released %v", r)
	}
	// A failed load is an error, nothing kept.
	e.loadImage = func() (imageTower, error) { return nil, errors.New("no file") }
	if _, err := e.EmbedResized(context.Background(), image.NewRGBA(image.Rect(0, 0, 1, 1))); err == nil {
		t.Error("a failed load answered")
	}
}

// fakeEmbedder is Smart search's model in the Manager's tests.
type fakeEmbedder struct{ text []float32 }

func (f *fakeEmbedder) EmbedResized(context.Context, *image.RGBA) ([]float32, error) {
	return []float32{1, 0, 0}, nil
}
func (f *fakeEmbedder) EmbedText(context.Context, string) ([]float32, error) { return f.text, nil }

// The switch: on needs the model; off deletes every vector in one
// transaction and drops the decrypted ones, and no write lands after.
func TestSmartSearchSwitch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mg := &Manager{dao: dao.NewWithDB(db)}
	mock.ExpectQuery("select `smart_search_enabled` from `settings`").WillReturnRows(sqlmock.NewRows([]string{"on"}).AddRow(false))
	if mg.SmartSearchEnabled() {
		t.Fatal("on by default")
	}
	if err := mg.SetSmartSearchEnabled(nil, true); !errors.Is(err, ErrSmartSearchUnavailable) {
		t.Fatalf("on without the model: %v", err)
	}
	mg.embedder = &fakeEmbedder{}
	mock.ExpectExec("update `settings` set `smart_search_enabled` = 1").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := mg.SetSmartSearchEnabled(nil, true); err != nil || !mg.SmartSearchEnabled() || !mg.SmartSearchReading() {
		t.Fatalf("on: %v %v", err, mg.SmartSearchEnabled())
	}
	turn := mg.smart.turn.Load()
	// Something decrypted, to be dropped.
	mg.smart.index.mu.Lock()
	mg.smart.index.loaded, mg.smart.index.pos = true, map[string]int{}
	mg.smart.index.addLocked("h", 1, []int8{1, 2, 3})
	mg.smart.index.mu.Unlock()

	mock.ExpectBegin()
	mock.ExpectExec("update `settings` set `smart_search_enabled` = 0").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("delete from `photo_embeddings`").WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectCommit()
	if err := mg.SetSmartSearchEnabled(nil, false); err != nil || mg.SmartSearchEnabled() || mg.SmartSearchReading() {
		t.Fatalf("off: %v", err)
	}
	if mg.smart.index.loaded {
		t.Error("the decrypted vectors stayed")
	}
	ran, err := mg.smartWrite(turn, func() error { t.Error("a write ran after off"); return nil })
	if ran || err != nil {
		t.Errorf("write after off: %v %v", ran, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
	// A text search while off.
	if _, _, err := mg.ImageSearchText("snow", nil, nil, "", nil, "", false, nil, "", nil, nil, 0, 0, false, false); !errors.Is(err, ErrSmartSearchOff) {
		t.Errorf("a text search while off: %v", err)
	}
}

// A text search ranks the photos the other filters kept by their vectors,
// decrypted once with the user's key, best first and cut; a photo without
// one isn't among them.
func TestRankByTextRanksWithinTheFilters(t *testing.T) {
	ses := faceTestSession(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mg := &Manager{dao: dao.NewWithDB(db), embedder: &fakeEmbedder{text: []float32{1, 0, 0}}}
	mock.ExpectQuery("select `smart_search_enabled` from `settings`").WillReturnRows(sqlmock.NewRows([]string{"on"}).AddRow(true))
	vec := func(x, y, z float32) []byte { return ses.Encrypt(packVec(normalised([]float32{x, y, z}))) }
	mock.ExpectQuery("select `hash`, `vec` from `photo_embeddings` where `model` = ?").WithArgs(cEmbedModel).
		WillReturnRows(sqlmock.NewRows([]string{"hash", "vec"}).
			AddRow("dog", vec(1, 0.05, 0)).
			AddRow("beach", vec(0.08, 1, 0)).
			AddRow("dog2", vec(0.9, 0.3, 0)).
			AddRow("elsewhere", vec(1, 0, 0)). // not among the filtered photos
			AddRow("broken", []byte("not sealed")))
	files := []*pb.File{{Hash: "beach"}, {Hash: "dog2"}, {Hash: "nodesc"}, {Hash: "dog"}}
	got, err := mg.rankByText(ses, "a dog", files)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Hash != "dog" || got[1].Hash != "dog2" {
		t.Fatalf("ranked %v", hashesOf(got))
	}
	// The second search uses what is in memory: no query.
	if got, err := mg.rankByText(ses, "a dog", files); err != nil || len(got) != 2 {
		t.Fatalf("again: %v %v", hashesOf(got), err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
	// Memories' similarity from the same vectors.
	similar := mg.smartSimilarity(ses)
	if c, ok := similar("dog", "dog2"); !ok || c < 0.9 {
		t.Errorf("dog/dog2 %v %v", c, ok)
	}
	if _, ok := similar("dog", "nodesc"); ok {
		t.Error("a photo without a vector has a similarity")
	}
	// Deleted content leaves the index.
	mg.smart.index.remove("dog")
	if got, _ := mg.rankByText(ses, "a dog", files); len(got) != 1 || got[0].Hash != "dog2" {
		t.Errorf("after a delete: %v", hashesOf(got))
	}
}

func hashesOf(files []*pb.File) []string {
	var out []string
	for _, f := range files {
		out = append(out, f.Hash)
	}
	return out
}
