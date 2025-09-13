package filesmanager

// files_manager/mobileclip.go

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"

	"github.com/alonsovidales/otc/log"
	"github.com/sugarme/tokenizer"
	"github.com/sugarme/tokenizer/model/bpe"
	"github.com/sugarme/tokenizer/normalizer"
	"github.com/sugarme/tokenizer/pretokenizer"
	onnx "github.com/yalue/onnxruntime_go"
	ort "github.com/yalue/onnxruntime_go"
	"golang.org/x/image/draw"
)

// CLIP / MobileCLIP normalization
var (
	clipMean = [3]float32{0.48145466, 0.4578275, 0.40821073}
	clipStd  = [3]float32{0.26862954, 0.26130258, 0.27577711}
)

// Encoders wires sessions, tensors, and tokenizer.
type Encoders struct {
	visionSess *onnx.AdvancedSession
	visionIn   *onnx.Tensor[float32] // [1,3,H,W]
	visionOut  *onnx.Tensor[float32] // [1,D]

	textSess *onnx.AdvancedSession
	textIDs  *onnx.Tensor[int64]   // [1,MaxSeqLen]
	textMask *onnx.Tensor[int64]   // [1,MaxSeqLen]
	textOut  *onnx.Tensor[float32] // [1,D]

	tok       *tokenizer.Tokenizer
	InputSize int // e.g., 224
	MaxSeqLen int // e.g., 77
	Dim       int // e.g., 512
}

func dumpIO(modelPath string) error {
	in, out, err := ort.GetInputOutputInfo(modelPath)
	if err != nil {
		return err
	}
	fmt.Println("== I/O for", modelPath)
	for _, x := range in {
		log.Debug(fmt.Sprintf("IN  %-20s kind=%s elem=%s shape=%v\n",
			x.Name, x.OrtValueType.String(), x.DataType.String(), x.Dimensions))
	}
	for _, x := range out {
		log.Debug(fmt.Sprintf("OUT %-20s kind=%s elem=%s shape=%v\n",
			x.Name, x.OrtValueType.String(), x.DataType.String(), x.Dimensions))
	}
	return nil
}

// NewEncoders sets everything up. No tokenizer padding/truncation helpers used;
// we’ll manually pad/truncate in RunText to match MaxSeqLen.
func NewEncoders(
	visionONNX string,
	textONNX string,
	vocabPath string,
	mergesPath string,
	inputSize int, // e.g., 224
	maxSeqLen int, // e.g., 77
	embDim int, // e.g., 512
) (*Encoders, error) {
	//onnx.SetSharedLibraryPath("/usr/local/lib/libonnxruntime.so")
	onnx.SetSharedLibraryPath("/opt/onnxruntime/lib/libonnxruntime.so")
	if err := onnx.InitializeEnvironment(); err != nil {
		return nil, fmt.Errorf("InitializeEnvironment: %w", err)
	}

	dumpIO(visionONNX)
	dumpIO(textONNX)

	// ---- Vision model ----
	visIn, visOut, err := onnx.GetInputOutputInfo(visionONNX)
	if err != nil {
		return nil, fmt.Errorf("vision GetInputOutputInfo: %w", err)
	}
	if len(visIn) == 0 || len(visOut) == 0 {
		return nil, errors.New("vision model missing inputs/outputs")
	}
	visionIn, err := onnx.NewEmptyTensor[float32](onnx.NewShape(1, 3, int64(inputSize), int64(inputSize)))
	if err != nil {
		return nil, err
	}
	visionOut, err := onnx.NewEmptyTensor[float32](onnx.NewShape(1, int64(embDim)))
	if err != nil {
		visionIn.Destroy()
		return nil, err
	}

	visionSess, err := onnx.NewAdvancedSession(
		visionONNX,
		[]string{visIn[0].Name},
		[]string{visOut[0].Name},
		[]onnx.Value{visionIn},
		[]onnx.Value{visionOut},
		nil,
	)
	if err != nil {
		visionIn.Destroy()
		visionOut.Destroy()
		return nil, fmt.Errorf("vision session: %w", err)
	}

	// ---- Text model ----
	txtIn, txtOut, err := onnx.GetInputOutputInfo(textONNX)
	if err != nil {
		visionSess.Destroy()
		visionIn.Destroy()
		visionOut.Destroy()
		return nil, fmt.Errorf("text GetInputOutputInfo: %w", err)
	}
	/*if len(txtIn) < 2 || len(txtOut) == 0 {
		visionSess.Destroy()
		visionIn.Destroy()
		visionOut.Destroy()
		return nil, errors.New("text model missing inputs (need ids+mask) or outputs")
	}*/

	textIDs, _ := onnx.NewEmptyTensor[int64](onnx.NewShape(1, int64(maxSeqLen)))
	textMask, _ := onnx.NewEmptyTensor[int64](onnx.NewShape(1, int64(maxSeqLen)))
	textOut, _ := onnx.NewEmptyTensor[float32](onnx.NewShape(1, int64(embDim)))

	inputNames := []string{txtIn[0].Name}
	inputVals := []onnx.Value{textIDs}
	hasMask := false
	for _, io := range txtIn {
		if io.Name == "attention_mask" {
			hasMask = true
			break
		}
	}

	if hasMask {
		inputNames = append(inputNames, "attention_mask")
		inputVals = append(inputVals, textMask)
	}
	textSess, err := onnx.NewAdvancedSession(
		textONNX,
		inputNames,
		[]string{txtOut[0].Name},
		inputVals,
		[]onnx.Value{textOut},
		nil,
	)
	if err != nil {
		textIDs.Destroy()
		textMask.Destroy()
		textOut.Destroy()
		visionSess.Destroy()
		visionIn.Destroy()
		visionOut.Destroy()
		return nil, fmt.Errorf("text session: %w", err)
	}

	log.Error("Tokenizer JSON not loaded")
	bpeModel, err := bpe.NewBpeFromFiles(vocabPath, mergesPath)
	if err != nil {
		return nil, err
	}
	// Set unk if your v0.2.2 has SetUnk()
	if s, ok := interface{}(bpeModel).(interface{ SetUnk(string) }); ok {
		s.SetUnk("<|endoftext|>")
	}

	tok := tokenizer.NewTokenizer(bpeModel)
	tok.WithPreTokenizer(pretokenizer.NewByteLevel())
	tok.WithDecoder(pretokenizer.NewByteLevel())
	tok.WithNormalizer(normalizer.NewNFKC())
	tok.AddSpecialTokens([]tokenizer.AddedToken{
		{Content: "<|startoftext|>"},
		{Content: "<|endoftext|>"},
	})

	if tok != nil {
		log.Debug("Toenizer loaded", tok)
	}

	return &Encoders{
		visionSess: visionSess,
		visionIn:   visionIn,
		visionOut:  visionOut,
		textSess:   textSess,
		textIDs:    textIDs,
		textMask:   textMask,
		textOut:    textOut,
		tok:        tok,
		InputSize:  inputSize,
		MaxSeqLen:  maxSeqLen,
		Dim:        embDim,
	}, nil
}

// Close releases all native resources.
func (e *Encoders) Close() {
	if e.textSess != nil {
		e.textSess.Destroy()
	}
	if e.visionSess != nil {
		e.visionSess.Destroy()
	}
	if e.textOut != nil {
		e.textOut.Destroy()
	}
	if e.textMask != nil {
		e.textMask.Destroy()
	}
	if e.textIDs != nil {
		e.textIDs.Destroy()
	}
	if e.visionOut != nil {
		e.visionOut.Destroy()
	}
	if e.visionIn != nil {
		e.visionIn.Destroy()
	}
	onnx.DestroyEnvironment()
}

// RunImage: JPEG/PNG bytes → L2-normalized embedding ([]float32)
func (e *Encoders) RunImage(imgBytes []byte) ([]float32, error) {
	if e.visionSess == nil || e.visionIn == nil || e.visionOut == nil {
		return nil, errors.New("vision encoder not initialized")
	}
	img, _, err := image.Decode(bytes.NewReader(imgBytes))
	if err != nil {
		return nil, err
	}

	// Resize + center-crop to InputSize
	log.Debug("Croping to:", e.InputSize)
	img = centerCropResize(img, e.InputSize, e.InputSize)

	// HWC → NCHW float32 + CLIP normalization
	chw := hwcToCHW(img, clipMean, clipStd)
	inData := e.visionIn.GetData()
	if len(inData) != len(chw) {
		return nil, fmt.Errorf("vision input size mismatch: got %d want %d", len(inData), len(chw))
	}
	copy(inData, chw)

	if err := e.visionSess.Run(); err != nil {
		return nil, err
	}

	vec := make([]float32, e.Dim)
	copy(vec, e.visionOut.GetData())
	l2Normalize(vec)
	return vec, nil
}

// RunText: query → L2-normalized embedding ([]float32)
// Manually pad/truncate to MaxSeqLen, then fill input_ids and attention_mask.
func (e *Encoders) RunText(query string) ([]float32, error) {
	if e.textSess == nil || e.textIDs == nil || e.textMask == nil || e.textOut == nil || e.tok == nil {
		return nil, errors.New("text encoder/tokenizer not initialized")
	}

	log.Debug("Encoding:", query)
	enc, err := e.tok.EncodeSingle(query)
	if err != nil {
		return nil, err
	}

	log.Debug("Tokens:", enc.Tokens)
	log.Debug("Offset:", enc.Offsets)

	ids := enc.Ids
	attn := enc.AttentionMask

	// Manual pad/truncate to MaxSeqLen
	if len(ids) > e.MaxSeqLen {
		ids = ids[:e.MaxSeqLen]
		attn = attn[:e.MaxSeqLen]
	}
	if len(ids) < e.MaxSeqLen {
		pad := e.MaxSeqLen - len(ids)
		// pad with 0 (common pad id in CLIP tokenizers)
		for i := 0; i < pad; i++ {
			ids = append(ids, 0)
			attn = append(attn, 0)
		}
	}

	idData := e.textIDs.GetData()
	mskData := e.textMask.GetData()
	for i := 0; i < e.MaxSeqLen; i++ {
		idData[i] = int64(ids[i])
		mskData[i] = int64(attn[i])
	}

	if err := e.textSess.Run(); err != nil {
		return nil, err
	}

	vec := make([]float32, e.Dim)
	copy(vec, e.textOut.GetData())
	l2Normalize(vec)
	return vec, nil
}

// ---------- helpers ----------

func centerCropResize(src image.Image, tw, th int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()

	var sw, sh int
	if w < h {
		sw = tw
		sh = int(math.Round(float64(h) * float64(tw) / float64(w)))
	} else {
		sh = th
		sw = int(math.Round(float64(w) * float64(th) / float64(h)))
	}

	tmp := image.NewRGBA(image.Rect(0, 0, sw, sh))
	draw.CatmullRom.Scale(tmp, tmp.Bounds(), src, b, draw.Over, nil)

	x0 := (sw - tw) / 2
	y0 := (sh - th) / 2
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.Copy(dst, image.Pt(0, 0), tmp, image.Rect(x0, y0, x0+tw, y0+th), draw.Over, nil)
	return dst
}

func hwcToCHW(img image.Image, mean, std [3]float32) []float32 {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := make([]float32, 3*w*h)
	iR, iG, iB := 0, w*h, 2*w*h

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			fr := float32(r) / 65535.0
			fg := float32(g) / 65535.0
			fb := float32(b) / 65535.0
			out[iR] = (fr - mean[0]) / std[0]
			out[iG] = (fg - mean[1]) / std[1]
			out[iB] = (fb - mean[2]) / std[2]
			iR++
			iG++
			iB++
		}
	}
	return out
}

func l2Normalize(x []float32) {
	var s float64
	for _, v := range x {
		s += float64(v * v)
	}
	if s == 0 {
		return
	}
	inv := float32(1.0 / math.Sqrt(s))
	for i := range x {
		x[i] *= inv
	}
}
