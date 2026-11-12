// SPDX-License-Identifier: AGPL-3.0-or-later

package petfinder

import (
	"image"
	"math"
	"sort"
)

// candidate is a box the detector thinks is a pet.
type candidate struct {
	kind  Kind
	score float32
	box   [4]float64 // x0, y0, x1, y1 in the photo's pixels, within it
}

// candidates is every box of D-FINE's answer (queries x classes logits,
// queries x 4 boxes: centre x, centre y, width, height, as fractions of the
// photo) whose cat or dog probability is at least minScore, for a w x h
// photo, in the detector's order (a box can be both).
func candidates(logits, boxes []float32, classes, w, h int) []candidate {
	var out []candidate
	for q := 0; q < len(boxes)/4; q++ {
		for _, k := range [...]struct {
			class int
			kind  Kind
		}{{catClass, Cat}, {dogClass, Dog}} {
			s := sigmoid(logits[q*classes+k.class])
			if s < minScore {
				continue
			}
			cx, cy, bw, bh := boxes[4*q], boxes[4*q+1], boxes[4*q+2], boxes[4*q+3]
			fw, fh := float32(w), float32(h)
			// As numpy computed them, in float32.
			x0, y0 := float64((cx-bw/2)*fw), float64((cy-bh/2)*fh)
			x1, y1 := float64((cx+bw/2)*fw), float64((cy+bh/2)*fh)
			out = append(out, candidate{kind: k.kind, score: s, box: [4]float64{
				math.Max(0, x0), math.Max(0, y0), math.Min(float64(w), x1), math.Min(float64(h), y1)}})
		}
	}
	return out
}

// peopleBoxes is every box of D-FINE's answer whose person probability is
// at least minScore, in a w x h photo's pixels, within it, in the
// detector's order (a person may be there twice: only whether a point is in
// one counts).
func peopleBoxes(logits, boxes []float32, classes, w, h int) []image.Rectangle {
	var out []image.Rectangle
	for q := 0; q < len(boxes)/4; q++ {
		if sigmoid(logits[q*classes+personClass]) < minScore {
			continue
		}
		cx, cy, bw, bh := boxes[4*q], boxes[4*q+1], boxes[4*q+2], boxes[4*q+3]
		fw, fh := float64(w), float64(h)
		r := image.Rect(int(math.Floor(float64(cx-bw/2)*fw)), int(math.Floor(float64(cy-bh/2)*fh)),
			int(math.Ceil(float64(cx+bw/2)*fw)), int(math.Ceil(float64(cy+bh/2)*fh))).Intersect(image.Rect(0, 0, w, h))
		if !r.Empty() {
			out = append(out, r)
		}
	}
	return out
}

func sigmoid(x float32) float32 {
	return float32(1 / (1 + math.Exp(-float64(x))))
}

// choose is the pets among candidates in a w x h photo: the likeliest
// first; of boxes overlapping by sameAnimal or more (whatever their kind:
// D-FINE's queries repeat an animal, sometimes as the other kind) the
// likeliest; none too small (minSidePx, minSideShare of the photo's
// shorter side); at most maxPets.
func choose(c []candidate, w, h int) []candidate {
	sort.SliceStable(c, func(i, j int) bool { return c[i].score > c[j].score })
	var kept []candidate
next:
	for _, a := range c {
		for _, k := range kept {
			if iou(a.box, k.box) >= sameAnimal {
				continue next
			}
		}
		kept = append(kept, a)
	}
	least := math.Max(minSidePx, minSideShare*float64(min(w, h)))
	out := kept[:0]
	for _, a := range kept {
		if math.Min(a.box[2]-a.box[0], a.box[3]-a.box[1]) >= least {
			out = append(out, a)
		}
	}
	return out[:min(len(out), maxPets)]
}

// iou is two boxes' intersection over their union.
func iou(a, b [4]float64) float64 {
	ix := math.Max(0, math.Min(a[2], b[2])-math.Max(a[0], b[0]))
	iy := math.Max(0, math.Min(a[3], b[3])-math.Max(a[1], b[1]))
	i := ix * iy
	u := (a[2]-a[0])*(a[3]-a[1]) + (b[2]-b[0])*(b[3]-b[1]) - i
	if u <= 0 {
		return 0
	}
	return i / u
}

// cropBox is what DINOv2 sees of a pet in a w x h photo: its box grown by
// embPad of its width and height on every side, within the photo, its
// corners rounded half to even as Pillow's crop rounds them.
func cropBox(b [4]float64, w, h int) image.Rectangle {
	pw, ph := embPad*(b[2]-b[0]), embPad*(b[3]-b[1])
	return image.Rect(
		int(math.RoundToEven(math.Max(0, b[0]-pw))), int(math.RoundToEven(math.Max(0, b[1]-ph))),
		int(math.RoundToEven(math.Min(float64(w), b[2]+pw))), int(math.RoundToEven(math.Min(float64(h), b[3]+ph))))
}
