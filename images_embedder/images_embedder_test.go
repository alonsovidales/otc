// SPDX-License-Identifier: AGPL-3.0-or-later

package imagesembedder

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"testing"

	imagestagger "github.com/alonsovidales/otc/images_tagger"
)

// The model tests need the exported towers and the tokenizer, which are
// not in the repository: OTC_EMBEDDER_DIR names a folder holding
// image.onnx, text.onnx and tokenizer.model, and OTC_ORT_LIB the ONNX
// Runtime library when it isn't at SharedLibraryPath (macOS:
// libonnxruntime.dylib from Microsoft's onnxruntime-osx-arm64 release).
// Without them those tests skip.
func modelFile(t *testing.T, name string) string {
	t.Helper()
	dir := os.Getenv("OTC_EMBEDDER_DIR")
	if dir == "" {
		t.Skip("OTC_EMBEDDER_DIR is not set")
	}
	p := filepath.Join(dir, name)
	if _, err := os.Stat(p); err != nil {
		t.Skipf("no %s: %v", name, err)
	}
	return p
}

func useRuntime(t *testing.T) {
	t.Helper()
	if lib := os.Getenv("OTC_ORT_LIB"); lib != "" {
		imagestagger.RuntimeLibrary = lib
	}
	if _, err := os.Stat(imagestagger.RuntimeLibrary); err != nil {
		t.Skipf("no ONNX Runtime at %s", imagestagger.RuntimeLibrary)
	}
}

func TestPadIDs(t *testing.T) {
	got := padIDs([]int{5, 6, 7})
	if len(got) != TextLen {
		t.Fatalf("length %d, want %d", len(got), TextLen)
	}
	if got[0] != 5 || got[1] != 6 || got[2] != 7 || got[3] != eosID {
		t.Fatalf("head %v, want the ids then <eos>", got[:5])
	}
	for i := 4; i < TextLen; i++ {
		if got[i] != padID {
			t.Fatalf("id %d = %d, want padding", i, got[i])
		}
	}

	// Empty text is <eos> alone.
	if got := padIDs(nil); got[0] != eosID || got[1] != padID {
		t.Fatalf("empty text: %v", got[:3])
	}

	// A long text keeps TextLen-1 tokens and still ends with <eos>, as the
	// reference tokenizer's truncation does.
	long := make([]int, 100)
	for i := range long {
		long[i] = 1000 + i
	}
	got = padIDs(long)
	if got[TextLen-2] != int64(1000+TextLen-2) || got[TextLen-1] != eosID {
		t.Fatalf("long text tail %v", got[TextLen-3:])
	}
}

func TestPixelsLayoutAndRange(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, ImageSize, ImageSize))
	for y := 0; y < ImageSize; y++ {
		for x := 0; x < ImageSize; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 255, G: 0, B: 51, A: 255})
		}
	}
	img.SetRGBA(3, 2, color.RGBA{R: 0, G: 255, B: 255, A: 255})
	px := pixels(img)
	const n = ImageSize * ImageSize
	if len(px) != 3*n {
		t.Fatalf("length %d", len(px))
	}
	near := func(a, b float32) bool { return math.Abs(float64(a-b)) < 1e-6 }
	// Planes of R, then G, then B, each scaled to [-1, 1].
	if !near(px[0], 1) || !near(px[n], -1) || !near(px[2*n], 51/127.5-1) {
		t.Fatalf("first pixel %v %v %v", px[0], px[n], px[2*n])
	}
	i := 2*ImageSize + 3
	if !near(px[i], -1) || !near(px[n+i], 1) || !near(px[2*n+i], 1) {
		t.Fatalf("pixel (3,2) %v %v %v", px[i], px[n+i], px[2*n+i])
	}
}

func TestResizeSqueezesWithoutCrop(t *testing.T) {
	// A wide picture, left half black and right half white, keeps its two
	// halves side by side: squeezed, not cropped to the middle.
	src := image.NewRGBA(image.Rect(0, 0, 1000, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 1000; x++ {
			v := uint8(0)
			if x >= 500 {
				v = 255
			}
			src.SetRGBA(x, y, color.RGBA{v, v, v, 255})
		}
	}
	dst := Resize(src)
	if dst.Rect != image.Rect(0, 0, ImageSize, ImageSize) {
		t.Fatalf("size %v", dst.Rect)
	}
	if l, r := dst.RGBAAt(10, 128).R, dst.RGBAAt(ImageSize-10, 128).R; l != 0 || r != 255 {
		t.Fatalf("left %d right %d, want 0 and 255", l, r)
	}
}

func TestEmbedResizedRefusesAnotherSize(t *testing.T) {
	var e ImageEncoder
	if _, err := e.EmbedResized(context.Background(), image.NewRGBA(image.Rect(0, 0, 384, 384))); err == nil {
		t.Fatal("a 384x384 image was taken")
	}
}

func TestNormalizeAndDot(t *testing.T) {
	v := []float32{3, 4}
	if !Normalize(v) || math.Abs(float64(v[0])-0.6) > 1e-6 || math.Abs(float64(v[1])-0.8) > 1e-6 {
		t.Fatalf("normalised %v", v)
	}
	if d := Dot(v, v); math.Abs(float64(d)-1) > 1e-6 {
		t.Fatalf("self dot %v", d)
	}
	zero := []float32{0, 0}
	if Normalize(zero) {
		t.Fatal("a zero vector was normalised")
	}
	nan := []float32{float32(math.NaN()), 1}
	if Normalize(nan) {
		t.Fatal("a NaN vector was normalised")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("Dot of different lengths didn't panic")
		}
	}()
	Dot([]float32{1}, []float32{1, 2})
}

func TestDefaultLoadOptionsPassNoSessionOptions(t *testing.T) {
	so, err := LoadOptions{}.sessionOptions()
	if err != nil || so != nil {
		t.Fatalf("zero options gave %v, %v", so, err)
	}
}

// The ids match transformers' SigLIP 2 tokenizer (GemmaTokenizer, text
// lowercased, padded to 64) for every case in testdata/tokens.json:
// English, Spanish, French, German, Italian, Portuguese and Dutch,
// accents, emoji, runs of spaces and tabs, empty and over-long text.
func TestTokenizerMatchesReference(t *testing.T) {
	tok, err := NewTokenizer(modelFile(t, "tokenizer.model"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("testdata/tokens.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Text string `json:"text"`
		IDs  []int  `json:"ids"`
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		got := tok.IDs(c.Text)
		want := padIDs(c.IDs[:len(c.IDs)-1]) // the reference ends with <eos>
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%q: id %d = %d, want %d (got %v)", c.Text, i, got[i], want[i], got[:len(c.IDs)+1])
				break
			}
		}
	}
}

func solid(c color.RGBA) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 320, 240))
	for y := 0; y < 240; y++ {
		for x := 0; x < 320; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// A sanity check of the two towers together: a plain red picture is
// nearest "a red square", in English and in Spanish, and a query is nearer
// its translation than an unrelated one.
func TestTowersAgree(t *testing.T) {
	imgPath, txtPath, tokPath := modelFile(t, "image.onnx"), modelFile(t, "text.onnx"), modelFile(t, "tokenizer.model")
	useRuntime(t)
	ie, err := NewImageEncoder(imgPath, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer ie.Close()
	te, err := NewTextEncoder(txtPath, tokPath, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer te.Close()
	if ie.Dim() != te.Dim() {
		t.Fatalf("image dim %d, text dim %d", ie.Dim(), te.Dim())
	}
	ctx := context.Background()
	red, err := ie.EmbedImage(ctx, solid(color.RGBA{220, 20, 20, 255}))
	if err != nil {
		t.Fatal(err)
	}
	embed := func(s string) []float32 {
		v, err := te.EmbedText(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(float64(Dot(v, v))-1) > 1e-4 {
			t.Fatalf("%q isn't normalised", s)
		}
		return v
	}
	for _, q := range [][3]string{
		{"a red square", "a blue square", "a green square"},
		{"un cuadrado rojo", "un cuadrado azul", "un cuadrado verde"},
	} {
		r, b, g := Dot(red, embed(q[0])), Dot(red, embed(q[1])), Dot(red, embed(q[2]))
		if r <= b || r <= g {
			t.Errorf("red picture: %q %.3f, %q %.3f, %q %.3f", q[0], r, q[1], b, q[2], g)
		}
	}
	dog, perro, car := embed("a dog on the beach"), embed("un perro en la playa"), embed("a red car in the city")
	if Dot(dog, perro) <= Dot(dog, car) {
		t.Errorf("dog/perro %.3f, dog/car %.3f", Dot(dog, perro), Dot(dog, car))
	}
	ie.Close()
	if _, err := ie.EmbedImage(ctx, solid(color.RGBA{1, 2, 3, 255})); err == nil {
		t.Error("a closed encoder answered")
	}
}
