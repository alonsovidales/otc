// SPDX-License-Identifier: AGPL-3.0-or-later

// Package imagesquality scores how a photo looks, for On this day
// (Memories): which photos of a day are worth showing again. NIMA, Google's
// Neural Image Assessment, gives the aesthetic score - its MobileNet
// version trained on the AVA dataset's ratings (idealo's
// image-quality-assessment, Apache-2.0), exported to ONNX - and plain Go
// measures the rest: sharpness, exposure and a difference hash to tell
// shots of a burst apart (measures.go).
//
// The model sees a photo as 224x224 pixels, resized nearest-neighbour from
// the photo's big thumbnail (Resize): nearest-neighbour is how it was
// trained (Keras' load_img), and an antialiased resize scored every photo a
// point lower and ranked them differently (Spearman 0.77 against 0.99 for a
// 1000 px thumbnail resized nearest-neighbour, on 114 photos).
package imagesquality

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"

	imagestagger "github.com/alonsovidales/otc/images_tagger"
	ort "github.com/yalue/onnxruntime_go"
)

// InputSize is the side of the square the model takes.
const InputSize = 224

// cBins is how many score bins the model answers: the probability of each
// rating from 1 to 10.
const cBins = 10

// NIMA is the loaded aesthetic model.
type NIMA struct {
	sess    *ort.DynamicAdvancedSession
	inName  string
	outName string
}

// NewNIMA loads the model at modelPath. noArena turns off ONNX Runtime's
// memory arena (a device short of memory, as the tagging model's
// LoadOptions.NoArena). A run takes one thread: the model is small, and it
// runs beside the tagging model and the people the device serves.
func NewNIMA(modelPath string, noArena bool) (*NIMA, error) {
	if err := imagestagger.InitRuntime(); err != nil {
		return nil, err
	}
	ins, outs, err := ort.GetInputOutputInfo(modelPath)
	if err != nil {
		return nil, err
	}
	if len(ins) != 1 || len(outs) != 1 {
		return nil, fmt.Errorf("the quality model has %d inputs and %d outputs, not 1 and 1", len(ins), len(outs)) // i18n-ignore: logged only
	}
	so, err := ort.NewSessionOptions()
	if err != nil {
		return nil, err
	}
	defer so.Destroy()
	if err := so.SetIntraOpNumThreads(1); err != nil {
		return nil, err
	}
	if err := so.SetInterOpNumThreads(1); err != nil {
		return nil, err
	}
	if noArena {
		if err := so.SetCpuMemArena(false); err != nil {
			return nil, err
		}
		if err := so.SetMemPattern(false); err != nil {
			return nil, err
		}
	}
	sess, err := ort.NewDynamicAdvancedSession(modelPath, []string{ins[0].Name}, []string{outs[0].Name}, so)
	if err != nil {
		return nil, err
	}
	return &NIMA{sess: sess, inName: ins[0].Name, outName: outs[0].Name}, nil
}

// Close lets the model go.
func (n *NIMA) Close() { _ = n.sess.Destroy() }

// AestheticResized is the aesthetic score, from 1 to 10, of a photo already
// through Resize. A call whose time is up doesn't start (the run itself
// can't be interrupted).
func (n *NIMA) AestheticResized(ctx context.Context, img *image.RGBA) (float32, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if img.Rect != image.Rect(0, 0, InputSize, InputSize) {
		return 0, fmt.Errorf("the image is %v, not the model's %dx%d", img.Rect, InputSize, InputSize) // i18n-ignore: logged only
	}
	x, err := ort.NewTensor[float32](ort.NewShape(1, InputSize, InputSize, 3), Input(img))
	if err != nil {
		return 0, err
	}
	defer x.Destroy()
	y, err := ort.NewEmptyTensor[float32](ort.NewShape(1, cBins))
	if err != nil {
		return 0, err
	}
	defer y.Destroy()
	if err := n.sess.Run([]ort.Value{x}, []ort.Value{y}); err != nil {
		return 0, err
	}
	return MeanScore(y.GetData())
}

// MeanScore is the rating the model's distribution expects: the sum of
// each rating times its probability, the probabilities normalized.
func MeanScore(p []float32) (float32, error) {
	if len(p) != cBins {
		return 0, fmt.Errorf("the model answered %d values, not %d", len(p), cBins) // i18n-ignore: logged only
	}
	var sum, weighted float64
	for i, v := range p {
		if v < 0 || v != v {
			return 0, errors.New("the model answered a value that isn't a probability") // i18n-ignore: logged only
		}
		sum += float64(v)
		weighted += float64(i+1) * float64(v)
	}
	if sum <= 0 {
		return 0, errors.New("the model answered no probability") // i18n-ignore: logged only
	}
	return float32(weighted / sum), nil
}

// Resize is the model's view of src: InputSize x InputSize, every pixel the
// source pixel under its centre (nearest-neighbour, PIL's rounding), the
// whole picture squeezed to a square as the model was trained.
func Resize(src image.Image) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, InputSize, InputSize))
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return dst
	}
	xs := make([]int, InputSize)
	for x := range xs {
		xs[x] = b.Min.X + min(w-1, int((float64(x)+0.5)*float64(w)/InputSize))
	}
	for y := 0; y < InputSize; y++ {
		sy := b.Min.Y + min(h-1, int((float64(y)+0.5)*float64(h)/InputSize))
		row := dst.Pix[y*dst.Stride:]
		for x, sx := range xs {
			r, g, bl := rgbAt(src, sx, sy)
			row[4*x], row[4*x+1], row[4*x+2], row[4*x+3] = r, g, bl, 0xff
		}
	}
	return dst
}

// rgbAt is src's pixel at x, y as 8-bit RGB, reading the formats a JPEG and
// a PNG decode to without an interface call per pixel.
func rgbAt(src image.Image, x, y int) (r, g, b uint8) {
	switch s := src.(type) {
	case *image.YCbCr:
		yi, ci := s.YOffset(x, y), s.COffset(x, y)
		return color.YCbCrToRGB(s.Y[yi], s.Cb[ci], s.Cr[ci])
	case *image.RGBA:
		i := s.PixOffset(x, y)
		return s.Pix[i], s.Pix[i+1], s.Pix[i+2]
	case *image.NRGBA:
		i := s.PixOffset(x, y)
		return s.Pix[i], s.Pix[i+1], s.Pix[i+2]
	case *image.Gray:
		v := s.Pix[s.PixOffset(x, y)]
		return v, v, v
	}
	c := color.RGBAModel.Convert(src.At(x, y)).(color.RGBA)
	return c.R, c.G, c.B
}

// Input is the model's input tensor for a Resize'd image: height, width,
// then the three channels, each value scaled to -1..1 (MobileNet's
// preprocessing).
func Input(img *image.RGBA) []float32 {
	out := make([]float32, 0, 3*InputSize*InputSize)
	for y := 0; y < InputSize; y++ {
		row := img.Pix[y*img.Stride:]
		for x := 0; x < InputSize; x++ {
			for c := 0; c < 3; c++ {
				out = append(out, float32(row[4*x+c])/127.5-1)
			}
		}
	}
	return out
}
