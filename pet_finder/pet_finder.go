// SPDX-License-Identifier: AGPL-3.0-or-later

// Package petfinder finds the dogs and cats in photos and says which are
// the same animal (Pets), as face_recognition does for people. Two models
// run through ONNX Runtime like images_tagger:
//
//   - D-FINE small (trained on COCO; Apache-2.0; ~42 MB, Hugging Face's
//     onnx-community export) finds them: it sees the photo squeezed to
//     640x640 and answers 300 boxes, each with a probability for each of
//     COCO's 80 classes. A box is a pet when its cat or its dog
//     probability is at least 0.5; boxes of one animal (overlapping by
//     half or more) keep the likeliest, and a box whose shorter side is
//     under 32 px or 5% of the photo's is too small to tell pets apart.
//     At most 10 a photo. On the demo library it found the dog in 12 of
//     13 photos with none wrong, where D-FINE nano found 4.
//   - DINOv2 small (Meta; Apache-2.0) with 8-bit weights (~24 MB)
//     describes each: the box, 10% wider and taller on every side,
//     squeezed to 224x224, answers 384 numbers (its class token,
//     L2-normalised), so two pets' dot product is how alike they are. On
//     real dogs' photos a photo's closest other was the same dog 94% of
//     the time, against 77% with Smart search's SigLIP 2; full precision
//     gave the same at twice the time. Two dogs of one breed and colour
//     are the hard case. The caller groups them, a dog only with dogs.
//
// Each pet has a square 160 px JPEG thumbnail for People. The pixels the
// models see are the Python benchmark's to the byte (Pillow's resize is
// ported, see resample.go).
package petfinder

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"sync"

	imagestagger "github.com/alonsovidales/otc/images_tagger"
	ort "github.com/yalue/onnxruntime_go"
)

// Model names the models whose pets Find answers. It is stored with each
// photo's pets: a photo another model looked at is looked at again.
const Model = "dfine-s+dinov2-s-int8"

const (
	detSize      = 640 // D-FINE's input: detSize x detSize
	personClass  = 0   // COCO's person in D-FINE's 80 classes
	catClass     = 15  // its cat
	dogClass     = 16  // and its dog
	minScore     = 0.5 // a box is a pet at this probability
	sameAnimal   = 0.5 // boxes overlapping this much (IoU) are one animal
	minSideShare = 0.05
	minSidePx    = 32 // a pet's box's shorter side is at least this, and minSideShare of the photo's
	maxPets      = 10

	embSize = 224 // DINOv2's input: embSize x embSize
	embPad  = 0.1 // the box grown by this much of its width and height on each side

	ThumbnailSize    = 160 // a pet's thumbnail is ThumbnailSize x ThumbnailSize
	thumbnailQuality = 90
)

// Kind is what a pet is. The device stores it as faces.kind, where 0 is a
// person's face.
type Kind int8

const (
	Dog Kind = 1
	Cat Kind = 2
)

// Pet is a dog or a cat in a photo.
type Pet struct {
	Kind  Kind
	Score float32 // the detector's probability
	// X, Y, W, H is its box in the photo's pixels (from its bounds'
	// top-left corner), within the photo.
	X, Y, W, H int
	// Embedding is its DINOv2 vector: 384 floats, L2-normalised, so the
	// dot product of two is their cosine.
	Embedding []float32
	// Thumbnail is a square JPEG of it for People: centred on the box, as
	// wide as its longer side.
	Thumbnail []byte
}

// Found is what Find saw in a photo: its pets, and where the people in it
// are (the detector's person boxes at minScore, in the photo's pixels from
// its bounds' top-left corner) - for the device to tell a pet's face from
// a person's, as the face model takes some animals' faces for people's.
type Found struct {
	Pets   []Pet
	People []image.Rectangle
}

// Files are the models' files.
type Files struct {
	Detector, Embedder string
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

// Finder is the two models. It is safe for concurrent use: each model runs
// one call at a time.
type Finder struct {
	det, emb         *model
	queries, classes int // the detector's boxes and classes
	dim              int // the embedding's length
}

// Load loads the models. They can be loaded again after Close (ONNX
// Runtime's environment is created once per process).
func Load(f Files, lo LoadOptions) (*Finder, error) {
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
	fd := &Finder{}
	fd.det, err = loadModel(f.Detector, []string{"pixel_values"}, []string{"logits", "pred_boxes"}, so, func(outs []ort.InputOutputInfo) error {
		l, b := outs[0].Dimensions, outs[1].Dimensions
		if len(l) != 3 || len(b) != 3 || l[1] <= 0 || l[1] != b[1] || l[2] <= dogClass || b[2] != 4 {
			return fmt.Errorf("outputs %v and %v, want [n, queries, classes] and [n, queries, 4]", l, b) // i18n-ignore: logged only
		}
		fd.queries, fd.classes = int(l[1]), int(l[2])
		return nil
	})
	if err != nil {
		return nil, err
	}
	fd.emb, err = loadModel(f.Embedder, []string{"pixel_values"}, []string{"last_hidden_state"}, so, func(outs []ort.InputOutputInfo) error {
		d := outs[0].Dimensions
		if len(d) != 3 || d[2] <= 0 {
			return fmt.Errorf("output %v, want [n, tokens, dim]", d) // i18n-ignore: logged only
		}
		fd.dim = int(d[2])
		return nil
	})
	if err != nil {
		fd.det.close()
		return nil, err
	}
	return fd, nil
}

// Close frees the models; calls after it fail.
func (f *Finder) Close() {
	f.det.close()
	f.emb.close()
}

// Find is the dogs and cats in img, the likeliest first (none: nil). img
// is the full-size decoded photo, upright (EXIF orientation applied): the
// detector sees it at 640x640, each pet is described and shown from it at
// full size. ctx is checked before each model run.
func (f *Finder) Find(ctx context.Context, img image.Image) ([]Pet, error) {
	found, err := f.FindAll(ctx, img)
	return found.Pets, err
}

// FindAll is Find with the people the detector saw in img too - only when
// it found a pet (none: no people either).
func (f *Finder) FindAll(ctx context.Context, img image.Image) (Found, error) {
	if err := ctx.Err(); err != nil {
		return Found{}, err
	}
	b := img.Bounds()
	if b.Dx() < 1 || b.Dy() < 1 {
		return Found{}, errors.New("the image is empty") // i18n-ignore: logged only
	}
	found, people, err := f.detect(ctx, img)
	if err != nil || len(found) == 0 {
		return Found{}, err
	}
	pets := make([]Pet, 0, len(found))
	for _, c := range found {
		x0, y0 := int(math.Round(c.box[0])), int(math.Round(c.box[1]))
		p := Pet{Kind: c.kind, Score: c.score, X: x0, Y: y0,
			W: int(math.Round(c.box[2])) - x0, H: int(math.Round(c.box[3])) - y0}
		if p.Embedding, err = f.embed(ctx, img, cropBox(c.box, b.Dx(), b.Dy())); err != nil {
			return Found{}, err
		}
		if p.Thumbnail, err = thumbnail(img, thumbSquare(p.X, p.Y, p.W, p.H, b.Dx(), b.Dy())); err != nil {
			return Found{}, err
		}
		pets = append(pets, p)
	}
	return Found{Pets: pets, People: people}, nil
}

// detect runs D-FINE on img: its pets, the likeliest first, and the
// people it saw.
func (f *Finder) detect(ctx context.Context, img image.Image) ([]candidate, []image.Rectangle, error) {
	b := img.Bounds()
	x, err := ort.NewTensor(ort.NewShape(1, 3, detSize, detSize), detPixels(resizeRGB(img, b, detSize, detSize, bilinear)))
	if err != nil {
		return nil, nil, err
	}
	defer x.Destroy()
	logits, err := ort.NewEmptyTensor[float32](ort.NewShape(1, int64(f.queries), int64(f.classes)))
	if err != nil {
		return nil, nil, err
	}
	defer logits.Destroy()
	boxes, err := ort.NewEmptyTensor[float32](ort.NewShape(1, int64(f.queries), 4))
	if err != nil {
		return nil, nil, err
	}
	defer boxes.Destroy()
	if err := f.det.run(ctx, []ort.Value{x}, []ort.Value{logits, boxes}); err != nil {
		return nil, nil, err
	}
	pets := choose(candidates(logits.GetData(), boxes.GetData(), f.classes, b.Dx(), b.Dy()), b.Dx(), b.Dy())
	return pets, peopleBoxes(logits.GetData(), boxes.GetData(), f.classes, b.Dx(), b.Dy()), nil
}

// embed is the DINOv2 vector of img's pixels in r (relative to its
// bounds).
func (f *Finder) embed(ctx context.Context, img image.Image, r image.Rectangle) ([]float32, error) {
	if r.Empty() {
		return nil, errors.New("the pet's crop is empty") // i18n-ignore: logged only
	}
	r = r.Add(img.Bounds().Min)
	x, err := ort.NewTensor(ort.NewShape(1, 3, embSize, embSize), embPixels(resizeRGB(img, r, embSize, embSize, bicubic)))
	if err != nil {
		return nil, err
	}
	defer x.Destroy()
	out := []ort.Value{nil} // ONNX Runtime allocates [1, tokens, dim]
	if err := f.emb.run(ctx, []ort.Value{x}, out); err != nil {
		return nil, err
	}
	defer out[0].Destroy()
	y, ok := out[0].(*ort.Tensor[float32])
	if !ok {
		return nil, errors.New("the embedding isn't a float tensor") // i18n-ignore: logged only
	}
	if s := y.GetShape(); len(s) != 3 || s[0] != 1 || s[1] < 1 || s[2] != int64(f.dim) {
		return nil, fmt.Errorf("the embedding's shape is %v", s) // i18n-ignore: logged only
	}
	v := append([]float32(nil), y.GetData()[:f.dim]...) // the class token
	if !normalize(v) {
		return nil, errors.New("the model answered a zero vector") // i18n-ignore: logged only
	}
	return v, nil
}

// detPixels is D-FINE's input for its resized R,G,B bytes: planes of R, G
// and B, each value v/255.
func detPixels(rgb []byte) []float32 {
	n := len(rgb) / 3
	out := make([]float32, 3*n)
	for i := 0; i < n; i++ {
		out[i] = float32(rgb[3*i]) / 255
		out[n+i] = float32(rgb[3*i+1]) / 255
		out[2*n+i] = float32(rgb[3*i+2]) / 255
	}
	return out
}

// embMean and embStd are ImageNet's.
var (
	embMean = [3]float32{0.485, 0.456, 0.406}
	embStd  = [3]float32{0.229, 0.224, 0.225}
)

// embPixels is DINOv2's input for its resized R,G,B bytes: planes of R, G
// and B, each value (v/255 - mean)/std in float32, as numpy did.
func embPixels(rgb []byte) []float32 {
	var lut [3][256]float32
	for c := range lut {
		for v := range lut[c] {
			lut[c][v] = (float32(v)/255 - embMean[c]) / embStd[c]
		}
	}
	n := len(rgb) / 3
	out := make([]float32, 3*n)
	for i := 0; i < n; i++ {
		out[i] = lut[0][rgb[3*i]]
		out[n+i] = lut[1][rgb[3*i+1]]
		out[2*n+i] = lut[2][rgb[3*i+2]]
	}
	return out
}

// normalize scales v to length 1 in place; false (v untouched) for a zero
// or non-finite vector.
func normalize(v []float32) bool {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	if s == 0 || math.IsInf(s, 0) || math.IsNaN(s) {
		return false
	}
	inv := 1 / math.Sqrt(s)
	for i := range v {
		v[i] = float32(float64(v[i]) * inv)
	}
	return true
}

// model is one loaded ONNX model.
type model struct {
	mu   sync.Mutex // a session runs one call at a time here
	sess *ort.DynamicAdvancedSession
}

// loadModel loads path, whose inputs must be ins and whose outputs must
// include outs (in that order to check), once check accepts those outputs.
func loadModel(path string, ins, outs []string, so *ort.SessionOptions, check func(outs []ort.InputOutputInfo) error) (*model, error) {
	in, out, err := ort.GetInputOutputInfoWithOptions(path, so)
	if err != nil {
		return nil, err
	}
	if len(in) != len(ins) {
		return nil, fmt.Errorf("%s: inputs %v, want %v", path, in, ins) // i18n-ignore: logged only
	}
	for i, name := range ins {
		if in[i].Name != name {
			return nil, fmt.Errorf("%s: inputs %v, want %v", path, in, ins) // i18n-ignore: logged only
		}
	}
	picked := make([]ort.InputOutputInfo, len(outs))
	for i, name := range outs {
		found := false
		for _, o := range out {
			if o.Name == name {
				picked[i], found = o, true
			}
		}
		if !found {
			return nil, fmt.Errorf("%s: outputs %v, want %v", path, out, outs) // i18n-ignore: logged only
		}
	}
	if err := check(picked); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err) // i18n-ignore: logged only
	}
	sess, err := ort.NewDynamicAdvancedSession(path, ins, outs, so)
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
