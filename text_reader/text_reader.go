// SPDX-License-Identifier: AGPL-3.0-or-later

// Package textreader reads the text in photos (Text in photos): signs,
// receipts, documents, screenshots. The models are PaddleOCR's PP-OCRv6
// small detector and small recogniser (Baidu's PaddlePaddle, June 2026;
// Apache-2.0; their official ONNX files), run through ONNX Runtime like
// images_tagger. The recogniser reads about 50 languages - OTC's seven,
// Chinese and Japanese among them - with one alphabet of 18,708
// characters.
//
//   - The detector (DB, 9.9 MB) sees the photo scaled so its short side is
//     736 px (never up; its sides multiples of 32) and answers each pixel's
//     probability of being text. A line is the rotated rectangle around a
//     region over 0.2 (OpenCV's contours, through gocv) whose mean
//     probability is at least 0.4, grown on every side by
//     area*1.4/perimeter (DB's unclip).
//   - The recogniser (21 MB) reads each line cut out of the full-size
//     photo, perspective-corrected and 48 px high, six at a time, with
//     greedy CTC.
//
// These are PaddleOCR's own parameters at Immich's scale: the short side
// at 736 rather than PaddleOCR's long side at 960, which loses a small sign
// in a 12 MP photo. A line is kept when the recogniser is sure of it (its
// characters' mean probability at least 0.8) and it has two characters or
// more: one alone is usually a stray glyph read in a texture. The pixels
// the models see are the Python benchmark's to the byte (Pillow's resize
// is ported, see resample.go; the cut-outs are OpenCV's).
package textreader

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	imagestagger "github.com/alonsovidales/otc/images_tagger"
	ort "github.com/yalue/onnxruntime_go"
)

// Model names the models whose text Read answers. It is stored with each
// photo's text: a photo whose text another model read is read again.
const Model = "ppocrv6-small"

const (
	detShortSide  = 736  // the detector's input: the short side at this...
	detMaxSide    = 4000 // ...unless the long side would pass this
	detMultiple   = 32   // the detector's sides are multiples of this
	detThreshold  = 0.2  // a pixel is text over this probability
	boxThreshold  = 0.4  // a line's mean probability is at least this
	unclipRatio   = 1.4  // DB's unclip: a line grows by area*this/perimeter
	maxCandidates = 3000 // regions looked at, at most
	minSide       = 3    // a region's rectangle at least this thick...
	minGrownSide  = 5    // ...and this once grown, in the detector's pixels
	sameRow       = 10   // lines whose tops are this close are one row, read left to right

	recHeight   = 48   // the recogniser's input height
	recBatch    = 6    // lines read at once, sorted by width/height
	recMinWidth = 32   // a batch's width, at least...
	recMaxWidth = 3200 // ...and at most
	recStride   = 8    // the recogniser answers a step per this many pixels of width
	maxRunWidth = 1920 // a run's widths add up to at most this (memory), see recognize
	minScore    = 0.8  // a line's characters' mean probability is at least this
	minChars    = 2    // and it has this many characters
)

// Files are the models' files: the two .onnx and the recogniser's
// inference.yml, whose character_dict is its alphabet.
type Files struct {
	Detector, Recognizer, Dictionary string
}

// LoadOptions is how the models run in ONNX Runtime. The zero value is
// ONNX Runtime's defaults.
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

// Line is a line of text in a photo.
type Line struct {
	Text string
	// Score is the mean probability of its characters.
	Score float32
	// Quad is its corners in the photo's pixels (from its bounds' top-left
	// corner) - top-left, top-right, bottom-right, bottom-left - as x,y
	// pairs.
	Quad [8]float32
}

// Reader is the two models. It is safe for concurrent use: each model runs
// one call at a time.
type Reader struct {
	det, rec *model
	dict     []string // index 0 is CTC's blank
}

// Load loads the models. They can be loaded again after Close (ONNX
// Runtime's environment is created once per process).
func Load(f Files, lo LoadOptions) (*Reader, error) {
	dict, err := readDictionary(f.Dictionary)
	if err != nil {
		return nil, err
	}
	if err := imagestagger.InitRuntime(); err != nil {
		return nil, err
	}
	so, err := lo.sessionOptions()
	if err != nil {
		return nil, fmt.Errorf("session options: %w", err) // i18n-ignore: logged only
	}
	if so != nil {
		defer so.Destroy()
	}
	det, err := loadModel(f.Detector, so, func(ins, outs []ort.InputOutputInfo) error {
		if len(ins) != 1 || ins[0].Name != "x" || len(outs) != 1 || len(outs[0].Dimensions) != 4 {
			return fmt.Errorf("want one input \"x\" and one [n, 1, h, w] output, have %v -> %v", ins, outs) // i18n-ignore: logged only
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	rec, err := loadModel(f.Recognizer, so, func(ins, outs []ort.InputOutputInfo) error {
		if len(ins) != 1 || ins[0].Name != "x" || len(outs) != 1 || len(outs[0].Dimensions) != 3 {
			return fmt.Errorf("want one input \"x\" and one [n, steps, classes] output, have %v -> %v", ins, outs) // i18n-ignore: logged only
		}
		if c := outs[0].Dimensions[2]; c != int64(len(dict)) {
			return fmt.Errorf("%d classes, but the dictionary has %d", c, len(dict)) // i18n-ignore: logged only
		}
		return nil
	})
	if err != nil {
		det.close()
		return nil, err
	}
	return &Reader{det: det, rec: rec, dict: dict}, nil
}

// Close frees the models; calls after it fail.
func (r *Reader) Close() {
	r.det.close()
	r.rec.close()
}

// Read is the text in img, line by line, top to bottom and left to right
// (none: nil). img is the full-size decoded photo, upright (EXIF
// orientation applied): the detector sees it at 736 px, but each line is
// cut out of it at full size. ctx is checked before each model run. Beyond
// the photo and ONNX Runtime's own, a read allocates ~20 bytes a pixel of
// the detector's input (~15 MB for a 12 MP photo), each line at 48 px and
// at most ~19 MB for the recogniser's runs.
func (r *Reader) Read(ctx context.Context, img image.Image) ([]Line, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b := img.Bounds()
	if b.Dx() < 1 || b.Dy() < 1 {
		return nil, errors.New("the image is empty") // i18n-ignore: logged only
	}
	quads, err := r.detect(ctx, img)
	if err != nil || len(quads) == 0 {
		return nil, err
	}
	return r.recognize(ctx, img, quads)
}

// detect is the lines' quads in img, in reading order.
func (r *Reader) detect(ctx context.Context, img image.Image) ([][8]float32, error) {
	b := img.Bounds()
	w, h := detSize(b.Dx(), b.Dy())
	x, err := ort.NewTensor(ort.NewShape(1, 3, int64(h), int64(w)), detPixels(resizeRGB(img, b, w, h, bilinear)))
	if err != nil {
		return nil, err
	}
	defer x.Destroy()
	y, err := ort.NewEmptyTensor[float32](ort.NewShape(1, 1, int64(h), int64(w)))
	if err != nil {
		return nil, err
	}
	defer y.Destroy()
	if err := r.det.run(ctx, []ort.Value{x}, []ort.Value{y}); err != nil {
		return nil, err
	}
	quads, err := dbBoxes(y.GetData(), w, h, b.Dx(), b.Dy())
	if err != nil {
		return nil, err
	}
	sortQuads(quads)
	return quads, nil
}

// recognize reads the lines at quads (in reading order) in img: the ones
// kept, in that order.
func (r *Reader) recognize(ctx context.Context, img image.Image, quads [][8]float32) ([]Line, error) {
	lines := make([]line, 0, len(quads))
	for i, q := range quads {
		l, ok, err := cutLine(img, q)
		if err != nil {
			return nil, err
		}
		if ok {
			l.i = i
			lines = append(lines, l)
		}
	}
	// PaddleOCR's batches: by width/height, so a batch's lines are padded
	// to about their own width.
	sort.SliceStable(lines, func(a, b int) bool { return lines[a].ratio() < lines[b].ratio() })
	read := make([]*Line, len(quads))
	var buf runBuffers
	for s := 0; s < len(lines); s += recBatch {
		batch := lines[s:min(s+recBatch, len(lines))]
		width := batchWidth(batch[len(batch)-1])
		// A batch's lines are padded to its width; it runs in parts when
		// that width is large, which changes nothing in each line's input,
		// only the memory a run takes: its output is ~9 KB per pixel of
		// width (18 MB for maxRunWidth), and ONNX Runtime's own grows with
		// it. On the text samples, runs of 1920 px peaked 120 MB lower than
		// runs of 3840 and 260 MB lower than 7680, in the same time.
		per := max(1, maxRunWidth/width)
		for a := 0; a < len(batch); a += per {
			part := batch[a:min(a+per, len(batch))]
			if err := r.read(ctx, part, width, read, &buf); err != nil {
				return nil, err
			}
		}
	}
	out := make([]Line, 0, len(lines))
	for _, l := range read {
		if l != nil {
			out = append(out, *l)
		}
	}
	return out, nil
}

// runBuffers are a Read's recogniser input and output, used again run
// after run: the output is ~9 KB per pixel of the run's width.
type runBuffers struct{ in, out []float32 }

// get is the buffers for an input of nIn values, all zero (the padding),
// and an output of nOut.
func (b *runBuffers) get(nIn, nOut int) (in, out []float32) {
	if cap(b.in) < nIn {
		b.in = make([]float32, nIn)
	}
	if cap(b.out) < nOut {
		b.out = make([]float32, nOut)
	}
	in = b.in[:nIn]
	clear(in)
	return in, b.out[:nOut]
}

// read runs the recogniser on lines, padded to width, and puts what it
// keeps of each line at its index in into.
func (r *Reader) read(ctx context.Context, lines []line, width int, into []*Line, buf *runBuffers) error {
	n := len(lines)
	plane := recHeight * width
	steps, classes := width/recStride, len(r.dict)
	pix, probs := buf.get(n*3*plane, n*steps*classes)
	for j, l := range lines {
		for c := 0; c < 3; c++ {
			// B, G, R planes from R, G, B bytes
			dst := pix[(j*3+c)*plane : (j*3+c+1)*plane]
			for y := 0; y < recHeight; y++ {
				row := l.pix[y*l.rw*3 : (y+1)*l.rw*3]
				for x := 0; x < l.rw; x++ {
					dst[y*width+x] = float32(row[3*x+2-c])/127.5 - 1
				}
			}
		}
	}
	x, err := ort.NewTensor(ort.NewShape(int64(n), 3, recHeight, int64(width)), pix)
	if err != nil {
		return err
	}
	defer x.Destroy()
	y, err := ort.NewTensor(ort.NewShape(int64(n), int64(steps), int64(classes)), probs)
	if err != nil {
		return err
	}
	defer y.Destroy()
	if err := r.rec.run(ctx, []ort.Value{x}, []ort.Value{y}); err != nil {
		return err
	}
	for j, l := range lines {
		text, score := decode(probs[j*steps*classes:(j+1)*steps*classes], steps, classes, r.dict)
		text = strings.TrimSpace(text)
		if float64(score) >= minScore && utf8.RuneCountInString(text) >= minChars {
			into[l.i] = &Line{Text: text, Score: score, Quad: l.quad}
		}
	}
	return nil
}

// detSize is the detector's input size for a w x h photo: the short side
// at detShortSide, never scaled up, the long side at most detMaxSide, each
// rounded to a multiple of detMultiple (half to even, as Python's round).
func detSize(w, h int) (dw, dh int) {
	r := math.Min(detShortSide/float64(min(w, h)), 1)
	if long := float64(max(w, h)); long*r > detMaxSide {
		r = detMaxSide / long
	}
	side := func(v int) int {
		return max(detMultiple, int(math.RoundToEven(float64(v)*r/detMultiple))*detMultiple)
	}
	return side(w), side(h)
}

// detMean and detStd are ImageNet's, applied - as the benchmark did - to
// the B, G and R planes in that order.
var (
	detMean = [3]float64{0.485, 0.456, 0.406}
	detStd  = [3]float64{0.229, 0.224, 0.225}
)

// detPixels is the detector's input for its resized R,G,B bytes: planes of
// B, G and R, each value (v/255 - mean)/std, computed as numpy did.
func detPixels(rgb []byte) []float32 {
	var lut [3][256]float32
	for c := range lut {
		for v := range lut[c] {
			f := float32(v) / 255
			lut[c][v] = float32((float64(f) - detMean[c]) / detStd[c])
		}
	}
	n := len(rgb) / 3
	out := make([]float32, 3*n)
	for i := 0; i < n; i++ {
		out[i] = lut[0][rgb[3*i+2]]
		out[n+i] = lut[1][rgb[3*i+1]]
		out[2*n+i] = lut[2][rgb[3*i]]
	}
	return out
}

// model is one loaded ONNX model.
type model struct {
	mu   sync.Mutex // a session runs one call at a time here
	sess *ort.DynamicAdvancedSession
}

// loadModel loads path once check accepts its inputs and outputs.
func loadModel(path string, so *ort.SessionOptions, check func(ins, outs []ort.InputOutputInfo) error) (*model, error) {
	ins, outs, err := ort.GetInputOutputInfoWithOptions(path, so)
	if err != nil {
		return nil, err
	}
	if err := check(ins, outs); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err) // i18n-ignore: logged only
	}
	in := make([]string, len(ins))
	for i, v := range ins {
		in[i] = v.Name
	}
	out := make([]string, len(outs))
	for i, v := range outs {
		out[i] = v.Name
	}
	sess, err := ort.NewDynamicAdvancedSession(path, in, out, so)
	if err != nil {
		return nil, err
	}
	return &model{sess: sess}, nil
}

// run runs the model, one call at a time. The run itself is native and
// can't be interrupted, but a call whose time is up while it waited
// doesn't start.
func (m *model) run(ctx context.Context, in, out []ort.Value) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.sess == nil {
		return errors.New("the model is closed") // i18n-ignore: logged only
	}
	return m.sess.Run(in, out)
}

func (m *model) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sess != nil {
		_ = m.sess.Destroy()
		m.sess = nil
	}
}
