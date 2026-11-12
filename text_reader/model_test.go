// SPDX-License-Identifier: AGPL-3.0-or-later

package textreader

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	imagestagger "github.com/alonsovidales/otc/images_tagger"
)

// The model tests need the models, which are not in the repository:
// OTC_TEST_VISION_MODELS names a folder holding ppocrv6-small-det.onnx,
// ppocrv6-small-rec.onnx and ppocrv6-small-rec.yml (pet_finder's too), and
// OTC_ORT_LIB the ONNX Runtime library when it isn't at
// images_tagger.RuntimeLibrary. Without them those tests skip.
func modelFiles(t *testing.T) Files {
	t.Helper()
	dir := os.Getenv("OTC_TEST_VISION_MODELS")
	if dir == "" {
		t.Skip("OTC_TEST_VISION_MODELS is not set")
	}
	f := Files{
		Detector:   filepath.Join(dir, "ppocrv6-small-det.onnx"),
		Recognizer: filepath.Join(dir, "ppocrv6-small-rec.onnx"),
		Dictionary: filepath.Join(dir, "ppocrv6-small-rec.yml"),
	}
	for _, p := range []string{f.Detector, f.Recognizer, f.Dictionary} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("no %s: %v", p, err)
		}
	}
	if lib := os.Getenv("OTC_ORT_LIB"); lib != "" {
		imagestagger.RuntimeLibrary = lib
	}
	if _, err := os.Stat(imagestagger.RuntimeLibrary); err != nil {
		t.Skipf("no ONNX Runtime at %s", imagestagger.RuntimeLibrary)
	}
	return f
}

func loadReader(t *testing.T) *Reader {
	t.Helper()
	r, err := Load(modelFiles(t), LoadOptions{NoArena: true, Threads: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return r
}

func decodeFile(t *testing.T, name string) image.Image {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// reference is testdata/reference.json: the Python benchmark's lines for
// the test photos (Pillow's decode, pyclipper's unclip), every line it
// read, kept or not.
type reference struct {
	Images map[string]struct {
		Size  [2]int
		Lines []struct {
			Text  string
			Score float32
			Kept  bool
			Quad  [8]float32
		}
	}
}

// quadDistance is the mean distance between two quads' corners.
func quadDistance(a, b [8]float32) float64 {
	var d float64
	for i := 0; i < 8; i += 2 {
		d += math.Hypot(float64(a[i]-b[i]), float64(a[i+1]-b[i+1]))
	}
	return d / 4
}

// Read finds the reference's lines and reads at least 90% of them the
// same: Go decodes a JPEG a little differently from Pillow, and the unclip
// is pyclipper's to a pixel (from the same pixels, the lines and their
// scores are the reference's but for that).
func TestReadMatchesReference(t *testing.T) {
	r := loadReader(t)
	b, err := os.ReadFile("testdata/reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var ref reference
	if err := json.Unmarshal(b, &ref); err != nil {
		t.Fatal(err)
	}
	for name, want := range ref.Images {
		img := decodeFile(t, name)
		if s := img.Bounds().Size(); s.X != want.Size[0] || s.Y != want.Size[1] {
			t.Fatalf("%s: %v, the reference's %v", name, s, want.Size)
		}
		got, err := r.Read(context.Background(), img)
		if err != nil {
			t.Fatal(err)
		}
		kept, same, last := 0, 0, -1
		paired := map[int]bool{}
		for _, w := range want.Lines {
			if !w.Kept {
				continue
			}
			kept++
			best, at := math.Inf(1), -1
			for i, g := range got {
				if d := quadDistance(g.Quad, w.Quad); d < best {
					best, at = d, i
				}
			}
			if at < 0 || best > 4 {
				t.Errorf("%s: %q at %v not found (nearest %.1f px)", name, w.Text, w.Quad, best)
				continue
			}
			paired[at] = true
			if at < last {
				t.Errorf("%s: %q read before the line the reference reads before it", name, got[at].Text)
			}
			last = at
			if got[at].Text == strings.TrimSpace(w.Text) {
				same++
				if math.Abs(float64(got[at].Score-w.Score)) > 0.05 {
					t.Errorf("%s: %q score %.3f, the reference's %.3f", name, w.Text, got[at].Score, w.Score)
				}
			} else {
				t.Logf("%s: read %q, the reference %q", name, got[at].Text, w.Text)
			}
		}
		if extra := len(got) - len(paired); extra*10 > kept {
			t.Errorf("%s: %d lines the reference hasn't, of %d", name, extra, len(got))
		}
		if same*10 < kept*9 {
			t.Errorf("%s: %d of %d lines read as the reference", name, same, kept)
		}
		t.Logf("%s: %d of %d lines as the reference, %d read", name, same, kept, len(got))
	}
}

// The models run one call at a time; callers at once get what one alone
// gets.
func TestReadConcurrent(t *testing.T) {
	r := loadReader(t)
	img := decodeFile(t, "boardgame.jpg")
	want, err := r.Read(context.Background(), img)
	if err != nil || len(want) == 0 {
		t.Fatal(want, err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := r.Read(context.Background(), img)
			if err == nil && !reflect.DeepEqual(got, want) {
				err = errors.New("a concurrent read differs")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadCancelledAndClosed(t *testing.T) {
	f := modelFiles(t)
	r, err := Load(f, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	img := decodeFile(t, "boardgame.jpg")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Read(ctx, img); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	r.Close()
	if _, err := r.Read(context.Background(), img); err == nil {
		t.Fatal("read after Close")
	}
	r.Close() // twice is fine

	// Loaded again after Close.
	r, err = Load(f, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := r.Read(context.Background(), img); err != nil {
		t.Fatal(err)
	}
}

// The wrong file is refused at Load.
func TestLoadChecksTheModels(t *testing.T) {
	f := modelFiles(t)
	swapped := f
	swapped.Detector, swapped.Recognizer = f.Recognizer, f.Detector
	if r, err := Load(swapped, LoadOptions{}); err == nil {
		r.Close()
		t.Fatal("loaded the recogniser as the detector")
	}
	short := filepath.Join(t.TempDir(), "short.yml")
	if err := os.WriteFile(short, []byte("PostProcess:\n  character_dict:\n  - a\n  - b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := f
	other.Dictionary = short
	if r, err := Load(other, LoadOptions{}); err == nil {
		r.Close()
		t.Fatal("loaded a dictionary of the wrong size")
	}
}
