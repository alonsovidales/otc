// SPDX-License-Identifier: AGPL-3.0-or-later

// Package imagesembedder turns photos and search text into vectors in one
// space, so a photo can be found by describing it ("kids playing in the
// snow", "un perro en la playa"): Smart search (beta). The model is
// Google's SigLIP 2 (base, patch 16, 256 px; Apache-2.0), exported to
// ONNX as two towers run through ONNX Runtime like images_tagger:
//
//   - the image tower, 8-bit weights in blocks of 32 (MatMulNBits,
//     accuracy level 4): ~114 MB, cosine >= 0.9997 against the fp32
//     model. Plain dynamic int8 quantized the ViT's activations too and
//     fell to 0.90.
//   - the text tower, dynamic int8 (its 256k-token embedding table
//     included): ~283 MB, cosine >= 0.998 against fp32. It is needed only
//     to search, so it loads apart from the image tower.
//
// Both answer L2-normalised vectors, so the dot product of a photo's and a
// query's is their cosine. SigLIP 2 is multilingual and was trained on
// lowercased text padded to 64 tokens with the Gemma tokenizer (the
// SentencePiece model shipped with it): Tokenizer does the same, id for id
// with the reference tokenizer in the seven languages OTC speaks.
package imagesembedder

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"strings"
	"sync"

	imagestagger "github.com/alonsovidales/otc/images_tagger"
	sentencepiece "github.com/eliben/go-sentencepiece"
	ort "github.com/yalue/onnxruntime_go"
	"golang.org/x/image/draw"
)

const (
	// ImageSize is the image tower's input: ImageSize x ImageSize RGB.
	ImageSize = 256
	// TextLen is the text tower's input: always TextLen token ids, the
	// text's, its end-of-sequence id, then padding.
	TextLen = 64

	imageInput = "pixel_values"
	textInput  = "input_ids"
	output     = "embeds"

	eosID = 1
	padID = 0
)

// LoadOptions is how a tower runs in ONNX Runtime. The zero value is ONNX
// Runtime's defaults.
type LoadOptions struct {
	// NoArena turns off ONNX Runtime's CPU memory arena and memory
	// pattern, for a device short of memory (as images_tagger's).
	NoArena bool
	// Threads caps the threads one run uses (0: ONNX Runtime's default,
	// one per core), so a background backfill can leave cores to the rest.
	Threads int
}

func (lo LoadOptions) sessionOptions() (*ort.SessionOptions, error) {
	if !lo.NoArena && lo.Threads == 0 {
		return nil, nil
	}
	so, err := ort.NewSessionOptions()
	if err != nil {
		return nil, err
	}
	if lo.NoArena {
		if err := so.SetCpuMemArena(false); err != nil {
			so.Destroy()
			return nil, err
		}
		if err := so.SetMemPattern(false); err != nil {
			so.Destroy()
			return nil, err
		}
	}
	if lo.Threads > 0 {
		if err := so.SetIntraOpNumThreads(lo.Threads); err != nil {
			so.Destroy()
			return nil, err
		}
	}
	return so, nil
}

// tower is one loaded ONNX model with one input and one [batch, dim]
// output.
type tower struct {
	mu   sync.Mutex // a session runs one call at a time here
	sess *ort.DynamicAdvancedSession
	dim  int
}

func loadTower(modelPath, input string, lo LoadOptions) (*tower, error) {
	// ONNX Runtime's environment, once per process for every model, from
	// images_tagger's library path.
	if err := imagestagger.InitRuntime(); err != nil {
		return nil, err
	}
	ins, outs, err := ort.GetInputOutputInfo(modelPath)
	if err != nil {
		return nil, err
	}
	if len(ins) != 1 || ins[0].Name != input || len(outs) != 1 || outs[0].Name != output {
		return nil, fmt.Errorf("%s: want one input %q and one output %q, have %v -> %v", modelPath, input, output, ins, outs)
	}
	dims := outs[0].Dimensions
	if len(dims) != 2 || dims[1] <= 0 {
		return nil, fmt.Errorf("%s: output shape %v, want [batch, dim]", modelPath, dims)
	}
	so, err := lo.sessionOptions()
	if err != nil {
		return nil, fmt.Errorf("session options: %w", err)
	}
	if so != nil {
		defer so.Destroy()
	}
	sess, err := ort.NewDynamicAdvancedSession(modelPath, []string{input}, []string{output}, so)
	if err != nil {
		return nil, err
	}
	return &tower{sess: sess, dim: int(dims[1])}, nil
}

// run is the tower's normalised vector for x, which it destroys.
func (t *tower) run(ctx context.Context, x ort.Value) ([]float32, error) {
	defer x.Destroy()
	y, err := ort.NewEmptyTensor[float32](ort.NewShape(1, int64(t.dim)))
	if err != nil {
		return nil, err
	}
	defer y.Destroy()
	t.mu.Lock()
	defer t.mu.Unlock()
	// The run itself is native and can't be interrupted, but a call whose
	// time is up while it waited doesn't start.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if t.sess == nil {
		return nil, errors.New("the model is closed")
	}
	if err := t.sess.Run([]ort.Value{x}, []ort.Value{y}); err != nil {
		return nil, err
	}
	v := append([]float32(nil), y.GetData()...)
	if !Normalize(v) {
		return nil, errors.New("the model answered a zero vector")
	}
	return v, nil
}

func (t *tower) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sess != nil {
		_ = t.sess.Destroy()
		t.sess = nil
	}
}

// ImageEncoder is the image tower.
type ImageEncoder struct{ t *tower }

// NewImageEncoder loads the image tower. It can be loaded again after
// Close (ONNX Runtime's environment is created once per process).
func NewImageEncoder(modelPath string, lo LoadOptions) (*ImageEncoder, error) {
	t, err := loadTower(modelPath, imageInput, lo)
	if err != nil {
		return nil, err
	}
	return &ImageEncoder{t: t}, nil
}

// Dim is the length of the vectors it answers.
func (e *ImageEncoder) Dim() int { return e.t.dim }

// Close frees the model; calls after it fail.
func (e *ImageEncoder) Close() { e.t.close() }

// EmbedImage is img's normalised vector. img should be upright (EXIF
// orientation applied), as the thumbnails are.
func (e *ImageEncoder) EmbedImage(ctx context.Context, img image.Image) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return e.EmbedResized(ctx, Resize(img))
}

// EmbedResized is EmbedImage of an image already through Resize, which is
// all the model sees of a photo (a supervised instance can send just that,
// as it does for the tagger).
func (e *ImageEncoder) EmbedResized(ctx context.Context, img *image.RGBA) ([]float32, error) {
	if img.Rect != image.Rect(0, 0, ImageSize, ImageSize) {
		return nil, fmt.Errorf("the image is %v, not the model's %dx%d", img.Rect, ImageSize, ImageSize)
	}
	x, err := ort.NewTensor(ort.NewShape(1, 3, ImageSize, ImageSize), pixels(img))
	if err != nil {
		return nil, err
	}
	return e.t.run(ctx, x)
}

// Resize is the model's view of src: squeezed to ImageSize x ImageSize,
// with no crop, by a bilinear filter that averages when it shrinks - as
// SigLIP's own preprocessing (PIL's bilinear resize) does.
func Resize(src image.Image) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, ImageSize, ImageSize))
	draw.BiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
	return dst
}

// pixels is the image tower's input for a Resize'd image: planes of R, G
// and B, each value scaled from [0, 255] to [-1, 1].
func pixels(img *image.RGBA) []float32 {
	const n = ImageSize * ImageSize
	out := make([]float32, 3*n)
	for y := 0; y < ImageSize; y++ {
		row := img.Pix[y*img.Stride : y*img.Stride+ImageSize*4]
		for x := 0; x < ImageSize; x++ {
			i := y*ImageSize + x
			px := row[x*4 : x*4+3]
			out[i] = float32(px[0])/127.5 - 1
			out[n+i] = float32(px[1])/127.5 - 1
			out[2*n+i] = float32(px[2])/127.5 - 1
		}
	}
	return out
}

// Tokenizer is SigLIP 2's text preprocessing: the Gemma SentencePiece
// model over the lowercased text.
type Tokenizer struct{ sp *sentencepiece.Processor }

// NewTokenizer loads the SentencePiece model (tokenizer.model).
func NewTokenizer(path string) (*Tokenizer, error) {
	sp, err := sentencepiece.NewProcessorFromPath(path)
	if err != nil {
		return nil, err
	}
	return &Tokenizer{sp: sp}, nil
}

// IDs is the text tower's input for text: its tokens (lowercased, at most
// TextLen-1 of them), the end-of-sequence id, then padding to TextLen.
func (t *Tokenizer) IDs(text string) []int64 {
	toks := t.sp.Encode(strings.ToLower(text))
	ids := make([]int, len(toks))
	for i, tk := range toks {
		ids[i] = tk.ID
	}
	return padIDs(ids)
}

func padIDs(ids []int) []int64 {
	out := make([]int64, TextLen) // padID is 0
	n := min(len(ids), TextLen-1)
	for i := 0; i < n; i++ {
		out[i] = int64(ids[i])
	}
	out[n] = eosID
	return out
}

// TextEncoder is the text tower with its tokenizer.
type TextEncoder struct {
	t   *tower
	tok *Tokenizer
}

// NewTextEncoder loads the text tower and the tokenizer.
func NewTextEncoder(modelPath, tokenizerPath string, lo LoadOptions) (*TextEncoder, error) {
	tok, err := NewTokenizer(tokenizerPath)
	if err != nil {
		return nil, err
	}
	t, err := loadTower(modelPath, textInput, lo)
	if err != nil {
		return nil, err
	}
	return &TextEncoder{t: t, tok: tok}, nil
}

// Dim is the length of the vectors it answers.
func (e *TextEncoder) Dim() int { return e.t.dim }

// Close frees the model; calls after it fail.
func (e *TextEncoder) Close() { e.t.close() }

// EmbedText is text's normalised vector, comparable by Dot with the image
// tower's.
func (e *TextEncoder) EmbedText(ctx context.Context, text string) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	x, err := ort.NewTensor(ort.NewShape(1, TextLen), e.tok.IDs(text))
	if err != nil {
		return nil, err
	}
	return e.t.run(ctx, x)
}

// Normalize scales v to length 1 in place; false (v untouched) for a zero
// or non-finite vector.
func Normalize(v []float32) bool {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	if s == 0 || math.IsInf(s, 0) || math.IsNaN(s) {
		return false
	}
	inv := float32(1 / math.Sqrt(s))
	for i := range v {
		v[i] *= inv
	}
	return true
}

// Dot is the dot product of two vectors of the same length: their cosine
// when both are normalised.
func Dot(a, b []float32) float32 {
	if len(a) != len(b) {
		panic("imagesembedder.Dot: lengths differ")
	}
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}
