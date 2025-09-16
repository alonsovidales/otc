package imagestagger

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	ort "github.com/yalue/onnxruntime_go"
	"golang.org/x/image/draw"
)

type RAMOptions struct {
	// Preprocessing
	ImageSize int // 384 or 512 (check your model card). Default 384.
	// Thresholding
	Threshold float32 // 0..1 cutoff for tag probability. Typical 0.30..0.50
	TopK      int     // optional cap on number of tags returned (0 = no cap)
}

func DefaultRAMOptions() RAMOptions {
	return RAMOptions{
		ImageSize: 384,
		Threshold: 0.60,
		TopK:      20,
	}
}

type RAMTag struct {
	Name  string
	Score float32
}

type RAMTagger struct {
	sess     *ort.DynamicAdvancedSession
	inName   string
	outName  string
	imgSize  int
	mean     [3]float32
	std      [3]float32
	tagNames []string
}

// Initialize ONNX Runtime once in main().
//   defer ort.DestroyEnvironment()

// NewRAMTagger creates a tagger for a RAM ONNX and a tag list file.
// modelPath:   path to *.onnx (e.g., models/ram/ram_swin_large_14m.onnx)
// tagListPath: path to tag list (tag_list.txt / labels.csv / etc.)
func NewRAMTagger(modelPath, tagListPath string, opt RAMOptions) (*RAMTagger, error) {
	if opt.ImageSize == 0 {
		opt.ImageSize = 384
	}

	ort.SetSharedLibraryPath("/opt/onnxruntime/lib/libonnxruntime.so")
	if err := ort.InitializeEnvironment(); err != nil {
		return nil, errors.New(fmt.Sprintf("InitializeEnvironment: %w", err))
	}

	// read tag names
	tags, err := readTagList(tagListPath)
	if err != nil {
		return nil, err
	}
	if len(tags) == 0 {
		return nil, errors.New("no tags found in list")
	}

	// detect IO names (don't hardcode)
	inName, outName, err := firstTensorIO(modelPath, "input", "logits")
	if err != nil {
		return nil, err
	}

	// create session
	sess, err := ort.NewDynamicAdvancedSession(modelPath, []string{inName}, []string{outName}, nil)
	if err != nil {
		return nil, err
	}

	return &RAMTagger{
		sess:     sess,
		inName:   inName,
		outName:  outName,
		imgSize:  opt.ImageSize,
		mean:     [3]float32{0.485, 0.456, 0.406},
		std:      [3]float32{0.229, 0.224, 0.225},
		tagNames: tags,
	}, nil
}

func (r *RAMTagger) Close() { _ = r.sess.Destroy() }

// Tags runs inference and returns tag strings (sorted by score desc).
func (r *RAMTagger) Tags(ctx context.Context, imgBytes []byte, opt RAMOptions) ([]RAMTag, error) {
	if opt.ImageSize == 0 {
		opt.ImageSize = r.imgSize
	}
	if opt.Threshold == 0 {
		opt.Threshold = 0.40
	}

	// decode & preprocess
	img, _, err := image.Decode(bytes.NewReader(imgBytes))
	if err != nil {
		return nil, err
	}
	input := r.preprocess(img, opt.ImageSize)

	// tensor [1,3,H,W]
	x, err := ort.NewTensor[float32](ort.NewShape(1, 3, int64(opt.ImageSize), int64(opt.ImageSize)), input)
	if err != nil {
		return nil, err
	}
	defer x.Destroy()

	// output tensor [1, num_tags]
	// We don't know num_tags from code; allocate from tag list length.
	y, err := ort.NewEmptyTensor[float32](ort.NewShape(1, int64(len(r.tagNames))))
	if err != nil {
		return nil, err
	}
	defer y.Destroy()

	// run
	if err := r.sess.Run([]ort.Value{x}, []ort.Value{y}); err != nil {
		return nil, err
	}

	// read scores
	prob := y.GetData()
	// Some RAM exports output logits; if values are outside [0,1], apply sigmoid.
	isLogits := false
	for i := 0; i < len(prob) && i < 10; i++ {
		if prob[i] < 0 || prob[i] > 1 {
			isLogits = true
			break
		}
	}
	if isLogits {
		for i := range prob {
			prob[i] = 1.0 / (1.0 + float32(math.Exp(float64(-prob[i]))))
		}
	}

	// threshold → collect
	pairs := make([]RAMTag, 0, len(prob))
	for i, p := range prob {
		if i >= len(r.tagNames) {
			break
		}
		if p >= opt.Threshold {
			pairs = append(pairs, RAMTag{Name: r.tagNames[i], Score: p})
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Score > pairs[j].Score })
	if opt.TopK > 0 && len(pairs) > opt.TopK {
		pairs = pairs[:opt.TopK]
	}

	return pairs, nil
}

// ---------- helpers ----------

func (r *RAMTagger) preprocess(src image.Image, size int) []float32 {
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	// RAM typically uses a resize+center-crop to a square. For simplicity we letterbox-scale.
	// If your model card specifies center-crop, swap to that; RAM is fairly tolerant.
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)

	out := make([]float32, 3*size*size)
	i := 0
	for c := 0; c < 3; c++ {
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				px := dst.RGBAAt(x, y)
				var v uint8
				if c == 0 {
					v = px.R
				} else if c == 1 {
					v = px.G
				} else {
					v = px.B
				}
				f := (float32(v)/255.0 - r.mean[c]) / r.std[c]
				out[i] = f
				i++
			}
		}
	}
	return out
}

// readTagList supports plain text (one tag per line) or CSV-like (tag in first column).
func readTagList(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(b), "\n")
	var tags []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if strings.Contains(l, ",") {
			parts := strings.Split(l, ",")
			t := strings.TrimSpace(parts[0])
			if t != "" {
				tags = append(tags, t)
			}
		} else {
			// strip optional "index: tag" or "index tag"
			if idx := strings.IndexAny(l, ": \t"); idx > 0 {
				left := strings.TrimSpace(l[:idx])
				if _, err := strconv.Atoi(left); err == nil {
					l = strings.TrimSpace(l[idx+1:])
				}
			}
			tags = append(tags, l)
		}
	}
	return tags, nil
}

// Resolve first input containing wantIn and first output containing wantOut (fallback to [0])
func firstTensorIO(onnxPath, wantIn, wantOut string) (inName, outName string, err error) {
	ins, outs, err := ort.GetInputOutputInfo(onnxPath)
	if err != nil {
		return "", "", err
	}
	inName = pickBySubstring(ins, wantIn)
	if wantOut == "" {
		outName = outs[0].Name
	} else {
		outName = pickBySubstring(outs, wantOut)
	}
	if inName == "" || outName == "" {
		return "", "", errors.New("could not resolve model IO names: " + filepath.Base(onnxPath))
	}
	return
}

func pickBySubstring(a []ort.InputOutputInfo, sub string) string {
	s := strings.ToLower(sub)
	for _, x := range a {
		if strings.Contains(strings.ToLower(x.Name), s) {
			return x.Name
		}
	}
	if len(a) > 0 {
		return a[0].Name
	}
	return ""
}
