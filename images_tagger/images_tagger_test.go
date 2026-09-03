package imagestagger

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"

	ort "github.com/yalue/onnxruntime_go"
)

func TestScoresToTagsThresholdAndSort(t *testing.T) {
	tagNames := []string{"cat", "dog", "car", "tree"}
	prob := []float32{0.9, 0.2, 0.95, 0.5}

	got := scoresToTags(prob, tagNames, nil, 0.5, 0)

	want := []RAMTag{{Name: "car", Score: 0.95}, {Name: "cat", Score: 0.9}, {Name: "tree", Score: 0.5}}
	if len(got) != len(want) {
		t.Fatalf("got %d tags, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Name != want[i].Name || got[i].Score != want[i].Score {
			t.Errorf("tag %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestScoresToTagsTopKCap(t *testing.T) {
	tagNames := []string{"a", "b", "c", "d"}
	prob := []float32{0.9, 0.8, 0.7, 0.6}

	got := scoresToTags(prob, tagNames, nil, 0.0, 2)

	if len(got) != 2 {
		t.Fatalf("expected TopK to cap the result at 2 tags, got %d: %+v", len(got), got)
	}
	if got[0].Name != "a" || got[1].Name != "b" {
		t.Errorf("expected the two highest-scoring tags (a, b), got %+v", got)
	}
}

func TestScoresToTagsAppliesSigmoidForLogits(t *testing.T) {
	tagNames := []string{"a", "b"}
	// Values outside [0,1] should be treated as logits and passed through a
	// sigmoid: sigmoid(0) == 0.5.
	prob := []float32{0, -10}

	got := scoresToTags(prob, tagNames, nil, 0.4, 0)

	if len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("expected only tag 'a' (sigmoid(0)=0.5 >= 0.4) to pass, got %+v", got)
	}
	if got[0].Score < 0.49 || got[0].Score > 0.51 {
		t.Errorf("expected sigmoid(0) ~= 0.5, got %v", got[0].Score)
	}
}

func TestScoresToTagsNoSigmoidWhenAlreadyProbabilities(t *testing.T) {
	tagNames := []string{"a", "b"}
	prob := []float32{0.1, 0.9}

	got := scoresToTags(prob, tagNames, nil, 0.5, 0)

	if len(got) != 1 || got[0].Name != "b" || got[0].Score != 0.9 {
		t.Errorf("values already in [0,1] should not be transformed, got %+v", got)
	}
}

func TestScoresToTagsMoreScoresThanTagNames(t *testing.T) {
	// The model output can be longer than the tag list in pathological
	// cases; extra scores must be ignored rather than panicking.
	tagNames := []string{"only-one"}
	prob := []float32{0.9, 0.9, 0.9}

	got := scoresToTags(prob, tagNames, nil, 0.5, 0)

	if len(got) != 1 || got[0].Name != "only-one" {
		t.Errorf("expected exactly one tag, got %+v", got)
	}
}

func TestScoresToTagsEmpty(t *testing.T) {
	if got := scoresToTags(nil, nil, nil, 0.5, 0); len(got) != 0 {
		t.Errorf("expected no tags for empty input, got %+v", got)
	}
}

// Issue #33: per-tag thresholds should override the flat one wherever
// they're provided, so a tag with a deliberately higher/lower calibrated
// cutoff behaves accordingly instead of everything sharing one bar.
func TestScoresToTagsPerTagThresholdOverridesFlat(t *testing.T) {
	tagNames := []string{"strict", "lenient", "unset"}
	prob := []float32{0.7, 0.55, 0.6}
	perTag := []float32{0.8, 0.5, 0} // "unset" has no real override (index OOB below)
	perTag = perTag[:2]              // only "strict" and "lenient" have entries

	got := scoresToTags(prob, tagNames, perTag, 0.65, 0)

	names := map[string]float32{}
	for _, t := range got {
		names[t.Name] = t.Score
	}
	if _, ok := names["strict"]; ok {
		t.Errorf("expected 'strict' (0.7) to be rejected by its own 0.8 threshold, got %+v", got)
	}
	if _, ok := names["lenient"]; !ok {
		t.Errorf("expected 'lenient' (0.55) to pass its own 0.5 threshold, got %+v", got)
	}
	if _, ok := names["unset"]; ok {
		t.Errorf("expected 'unset' (0.6) to fall back to the flat 0.65 threshold and be rejected, got %+v", got)
	}
}

func TestReadTagListPlainLines(t *testing.T) {
	path := writeTagFile(t, "cat\ndog\n\n# a comment\ntree\n")

	tags, err := readTagList(path)
	if err != nil {
		t.Fatalf("readTagList: %v", err)
	}

	want := []string{"cat", "dog", "tree"}
	assertStringSlice(t, tags, want)
}

func TestReadTagListCSVStyle(t *testing.T) {
	path := writeTagFile(t, "cat, a small domesticated feline\ndog, a loyal companion\n")

	tags, err := readTagList(path)
	if err != nil {
		t.Fatalf("readTagList: %v", err)
	}

	want := []string{"cat", "dog"}
	assertStringSlice(t, tags, want)
}

func TestReadTagListIndexPrefixedLines(t *testing.T) {
	path := writeTagFile(t, "0: cat\n1: dog\n")

	tags, err := readTagList(path)
	if err != nil {
		t.Fatalf("readTagList: %v", err)
	}

	want := []string{"cat", "dog"}
	assertStringSlice(t, tags, want)
}

func TestReadTagListMissingFile(t *testing.T) {
	if _, err := readTagList(filepath.Join(t.TempDir(), "does-not-exist.txt")); err == nil {
		t.Error("expected an error reading a nonexistent tag list file")
	}
}

func TestPickBySubstringMatch(t *testing.T) {
	infos := []ort.InputOutputInfo{{Name: "pixel_values"}, {Name: "logits"}}

	if got := pickBySubstring(infos, "logit"); got != "logits" {
		t.Errorf("expected 'logits', got %q", got)
	}
	if got := pickBySubstring(infos, "PIXEL"); got != "pixel_values" {
		t.Errorf("expected case-insensitive match 'pixel_values', got %q", got)
	}
}

func TestPickBySubstringFallsBackToFirst(t *testing.T) {
	infos := []ort.InputOutputInfo{{Name: "input_0"}, {Name: "input_1"}}

	if got := pickBySubstring(infos, "nonexistent"); got != "input_0" {
		t.Errorf("expected fallback to the first entry 'input_0', got %q", got)
	}
}

func TestPickBySubstringEmpty(t *testing.T) {
	if got := pickBySubstring(nil, "anything"); got != "" {
		t.Errorf("expected empty string for no candidates, got %q", got)
	}
}

func TestPreprocessOutputShape(t *testing.T) {
	r := &RAMTagger{mean: [3]float32{0.5, 0.5, 0.5}, std: [3]float32{0.5, 0.5, 0.5}}
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: 128, G: 128, B: 128, A: 255})
		}
	}

	size := 4
	out := r.preprocess(img, size)

	if len(out) != 3*size*size {
		t.Fatalf("expected %d values, got %d", 3*size*size, len(out))
	}

	// A mid-gray (128) pixel normalized with mean=std=0.5 should land close
	// to (128/255 - 0.5) / 0.5 ~= 0.004.
	for i, v := range out {
		if v < -0.05 || v > 0.05 {
			t.Fatalf("value %d = %v is outside the expected range for a uniform mid-gray image", i, v)
		}
	}
}

// --- helpers ---

func writeTagFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tags.txt")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing fixture tag file: %v", err)
	}
	return path
}

func assertStringSlice(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
