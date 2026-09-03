package imagestagger

import (
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
	// tagThresholds holds RAM++'s own per-tag calibrated cutoffs (issue
	// #33), index-aligned with tagNames. nil when NewRAMTagger was given no
	// thresholds file, in which case Tags falls back to the flat
	// RAMOptions.Threshold for every tag, as before.
	tagThresholds []float32
}

// Initialize ONNX Runtime once in main().
//   defer ort.DestroyEnvironment()

// NewRAMTagger creates a tagger for a RAM ONNX and a tag list file.
// modelPath:      path to *.onnx (e.g., models/ram/ram_swin_large_14m.onnx)
// tagListPath:    path to tag list (tag_list.txt / labels.csv / etc.)
// thresholdsPath: optional path to a per-tag threshold file (one float per
// line, aligned by index to tagListPath) — issue #33. Pass "" to use the
// flat RAMOptions.Threshold for every tag instead, as before.
func NewRAMTagger(modelPath, tagListPath, thresholdsPath string, opt RAMOptions) (*RAMTagger, error) {
	if opt.ImageSize == 0 {
		opt.ImageSize = 384
	}

	ort.SetSharedLibraryPath("/opt/onnxruntime/lib/libonnxruntime.so")
	if err := ort.InitializeEnvironment(); err != nil {
		return nil, fmt.Errorf("InitializeEnvironment: %w", err)
	}

	// read tag names
	tags, err := readTagList(tagListPath)
	if err != nil {
		return nil, err
	}
	if len(tags) == 0 {
		return nil, errors.New("no tags found in list")
	}

	thresholds, err := readThresholds(thresholdsPath)
	if err != nil {
		return nil, err
	}
	if thresholds != nil && len(thresholds) != len(tags) {
		return nil, fmt.Errorf("thresholds file has %d entries, tag list has %d", len(thresholds), len(tags))
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
		sess:          sess,
		inName:        inName,
		outName:       outName,
		imgSize:       opt.ImageSize,
		mean:          [3]float32{0.485, 0.456, 0.406},
		std:           [3]float32{0.229, 0.224, 0.225},
		tagNames:      tags,
		tagThresholds: thresholds,
	}, nil
}

func (r *RAMTagger) Close() { _ = r.sess.Destroy() }

// Tags runs inference and returns tag strings (sorted by score desc).
func (r *RAMTagger) Tags(ctx context.Context, img image.Image, opt RAMOptions) ([]RAMTag, error) {
	if opt.ImageSize == 0 {
		opt.ImageSize = r.imgSize
	}
	if opt.Threshold == 0 {
		opt.Threshold = 0.40
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

	return scoresToTags(prob, r.tagNames, r.tagThresholds, opt.Threshold, opt.TopK), nil
}

// scoresToTags converts raw per-tag model output into a sorted list of tags
// that clear threshold, capped at topK entries. Some RAM exports produce
// logits rather than probabilities, so scores outside [0,1] are passed
// through a sigmoid first. prob is mutated in place when that happens.
//
// perTag, when non-nil, overrides flatThreshold with RAM++'s own tuned
// per-tag cutoff for that index (issue #33) — the model's authors found
// some tags need a higher bar than others to avoid false positives, so a
// single flat cutoff for all 4585 tags is strictly less accurate than
// using their calibration.
func scoresToTags(prob []float32, tagNames []string, perTag []float32, flatThreshold float32, topK int) []RAMTag {
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
		if i >= len(tagNames) {
			break
		}
		threshold := flatThreshold
		if perTag != nil && i < len(perTag) {
			threshold = perTag[i]
		}
		if p >= threshold {
			pairs = append(pairs, RAMTag{Name: tagNames[i], Score: p})
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Score > pairs[j].Score })
	if topK > 0 && len(pairs) > topK {
		pairs = pairs[:topK]
	}

	return pairs
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

// readThresholds reads one float per line (RAM++'s own per-tag calibrated
// cutoffs, issue #33), index-aligned to the tag list. Returns (nil, nil)
// for an empty path — the caller falls back to a flat threshold in that
// case, preserving old behavior for callers that don't have this file.
func readThresholds(path string) ([]float32, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	out := make([]float32, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		v, err := strconv.ParseFloat(l, 32)
		if err != nil {
			return nil, fmt.Errorf("bad threshold line %q: %w", l, err)
		}
		out = append(out, float32(v))
	}
	return out, nil
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
