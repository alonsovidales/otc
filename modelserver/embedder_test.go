// SPDX-License-Identifier: AGPL-3.0-or-later

package modelserver

import (
	"context"
	"encoding/gob"
	"errors"
	"image"
	"image/color"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	imagesembedder "github.com/alonsovidales/otc/images_embedder"
	imagestagger "github.com/alonsovidales/otc/images_tagger"
)

type fakeEmbedder struct {
	img  *image.RGBA
	text string
}

func (f *fakeEmbedder) EmbedResized(_ context.Context, img *image.RGBA) ([]float32, error) {
	f.img = img
	return []float32{0.6, 0.8}, nil
}

func (f *fakeEmbedder) EmbedText(_ context.Context, text string) ([]float32, error) {
	f.text = text
	return []float32{1, 0}, nil
}

// Smart search: a child's photos and searches are described by the
// primary's model, from the 256x256 picture the child resized itself.
func TestClientEmbedsWithThePrimarysModel(t *testing.T) {
	path := socketPath(t)
	fe := &fakeEmbedder{}
	go ServeModels(path, Models{Tagger: func() Tagger { return &fakeTagger{} }, Embedder: func() Embedder { return fe }}, 2)
	waitForSocket(path)
	c := &Client{Path: path}
	if has, err := c.HasEmbedder(); err != nil || !has {
		t.Fatalf("HasEmbedder = %v %v", has, err)
	}
	img := imagesembedder.Resize(image.NewRGBA(image.Rect(0, 0, 1000, 750)))
	img.SetRGBA(9, 4, color.RGBA{R: 7, G: 8, B: 9, A: 255})
	vec, err := c.EmbedResized(context.Background(), img)
	if err != nil || len(vec) != 2 || vec[1] != 0.8 {
		t.Fatalf("image vector %v %v", vec, err)
	}
	if fe.img == nil || fe.img.Rect.Dx() != imagesembedder.ImageSize || fe.img.RGBAAt(9, 4) != (color.RGBA{R: 7, G: 8, B: 9, A: 255}) {
		t.Error("the model didn't get the child's picture")
	}
	if vec, err := c.EmbedText(context.Background(), "un perro en la playa"); err != nil || len(vec) != 2 || vec[0] != 1 {
		t.Fatalf("text vector %v %v", vec, err)
	}
	if fe.text != "un perro en la playa" {
		t.Errorf("the model got %q", fe.text)
	}
	// Anything but the model's square, or a text past the socket's limit,
	// is refused before the model.
	if _, err := c.EmbedResized(context.Background(), image.NewRGBA(image.Rect(0, 0, 100, 100))); err == nil {
		t.Error("a 100x100 picture was described")
	}
	if _, err := c.EmbedText(context.Background(), strings.Repeat("a", cMaxEmbedText+1)); err == nil {
		t.Error("an over-long text was described")
	}
}

// A primary without the model, and one older than it, answer ErrNoEmbedder:
// the child can't turn Smart search on.
func TestNoEmbedderReachesTheChildAsItself(t *testing.T) {
	path := socketPath(t)
	go ServeModels(path, Models{Tagger: func() Tagger { return &fakeTagger{} }}, 0)
	waitForSocket(path)
	c := &Client{Path: path}
	if has, err := c.HasEmbedder(); err != nil || has {
		t.Errorf("HasEmbedder = %v %v, want false", has, err)
	}
	img := imagesembedder.Resize(image.NewRGBA(image.Rect(0, 0, 10, 10)))
	if _, err := c.EmbedResized(context.Background(), img); !errors.Is(err, ErrNoEmbedder) {
		t.Errorf("no model, image: %v", err)
	}
	if _, err := c.EmbedText(context.Background(), "snow"); !errors.Is(err, ErrNoEmbedder) {
		t.Errorf("no model, text: %v", err)
	}
	old := &Client{Path: oldPrimary(t, make(chan request, 4))}
	if has, err := old.HasEmbedder(); err != nil || has {
		t.Errorf("an older primary: HasEmbedder = %v %v", has, err)
	}
	if _, err := old.EmbedText(context.Background(), "snow"); !errors.Is(err, ErrNoEmbedder) {
		t.Errorf("an older primary: %v", err)
	}
}

// fetchingPrimary is a primary downloading Smart search's model: the
// children's asks, and the model once it is there.
type fetchingPrimary struct {
	mu    sync.Mutex
	asked []string // "who want"
	model atomic.Pointer[fakeEmbedder]
	state EmbedderState
}

func (p *fetchingPrimary) models() Models {
	return Models{
		Tagger: func() Tagger { return &fakeTagger{} },
		Embedder: func() Embedder {
			if e := p.model.Load(); e != nil {
				return e
			}
			return nil
		},
		EmbedderFetch: func(who string, want bool) EmbedderState {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.asked = append(p.asked, who+" "+map[bool]string{true: "on", false: "off"}[want])
			return p.state
		},
		EmbedderState: func() EmbedderState {
			p.mu.Lock()
			defer p.mu.Unlock()
			return p.state
		},
	}
}

// Since release 152 a child asks the primary for the model ("embed-fetch")
// and learns from "info" how far its download is and when it is there.
func TestAChildAsksForTheModelAndFollowsItsDownload(t *testing.T) {
	path := socketPath(t)
	p := &fetchingPrimary{state: EmbedderState{CanFetch: true, Downloading: true, Percent: 42, Problem: "no_space", Need: 300 << 20, Left: 10 << 20}}
	go ServeModels(path, p.models(), 1)
	waitForSocket(path)
	c := &Client{Path: path}
	has, st, err := c.FetchEmbedder("4b0e", true)
	if err != nil || has || st != p.state {
		t.Fatalf("FetchEmbedder = %v %+v %v", has, st, err)
	}
	if has, st, err := c.EmbedderStatus(); err != nil || has || st.Percent != 42 || !st.CanFetch {
		t.Fatalf("EmbedderStatus = %v %+v %v", has, st, err)
	}
	if _, err := c.EmbedText(context.Background(), "snow"); !errors.Is(err, ErrNoEmbedder) {
		t.Errorf("a search while downloading: %v", err)
	}
	// There now.
	p.model.Store(&fakeEmbedder{})
	p.mu.Lock()
	p.state = EmbedderState{CanFetch: true}
	p.mu.Unlock()
	if has, _, err := c.EmbedderStatus(); err != nil || !has {
		t.Fatalf("once there: %v %v", has, err)
	}
	if vec, err := c.EmbedText(context.Background(), "snow"); err != nil || len(vec) != 2 {
		t.Fatalf("once there: %v %v", vec, err)
	}
	if has, _, err := c.FetchEmbedder("4b0e", false); err != nil || !has {
		t.Fatalf("off: %v %v", has, err)
	}
	p.mu.Lock()
	asked := strings.Join(p.asked, ",")
	p.mu.Unlock()
	if asked != "4b0e on,4b0e off" {
		t.Errorf("the primary was asked %q", asked)
	}
	if _, _, err := c.FetchEmbedder(strings.Repeat("x", cMaxWho+1), true); err == nil {
		t.Error("an over-long name was taken")
	}
}

// "embed-fetch" never waits behind the image requests, as "info".
func TestAskingForTheModelNeverWaits(t *testing.T) {
	path := socketPath(t)
	faces := &slowFaces{release: make(chan struct{})}
	p := &fetchingPrimary{state: EmbedderState{CanFetch: true, Downloading: true}}
	m := p.models()
	m.Faces = faces
	go ServeModels(path, m, 1)
	waitForSocket(path)
	c := &Client{Path: path}
	go c.DetectFaces(image.NewRGBA(image.Rect(0, 0, 8, 8)))
	time.Sleep(100 * time.Millisecond)
	done := make(chan error, 1)
	go func() {
		_, _, err := c.FetchEmbedder("4b0e", true)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("embed-fetch waited behind an image request")
	}
	faces.release <- struct{}{}
}

// A primary that can't download the model - one older than the download on
// demand, or without anywhere to put it - says so as ErrNoEmbedder, and
// its "info" says CanFetch false: the child can't turn Smart search on.
func TestAPrimaryThatCantFetchTheModel(t *testing.T) {
	old := &Client{Path: oldPrimary(t, make(chan request, 4))}
	if _, _, err := old.FetchEmbedder("4b0e", true); !errors.Is(err, ErrNoEmbedder) {
		t.Errorf("an older primary: %v", err)
	}
	if has, st, err := old.EmbedderStatus(); err != nil || has || st.CanFetch {
		t.Errorf("an older primary's info: %v %+v %v", has, st, err)
	}
	path := socketPath(t)
	go ServeModels(path, Models{Tagger: func() Tagger { return &fakeTagger{} }}, 0)
	waitForSocket(path)
	c := &Client{Path: path}
	if _, _, err := c.FetchEmbedder("4b0e", true); !errors.Is(err, ErrNoEmbedder) {
		t.Errorf("a primary without the download: %v", err)
	}
}

// oldRequest and oldResponse are the socket's messages as a child from
// release 151 knows them, before the download on demand.
type oldRequest struct {
	Op     string
	Text   string
	W, H   int
	Stride int
	Pix    []byte
	Opt    imagestagger.RAMOptions
	N      int64
}

type oldResponse struct {
	Tags         []imagestagger.RAMTag
	HasFaces     bool
	Err          string
	Proto        int
	Aesthetic    float32
	HasAesthetic bool
	Vec          []float32
	HasEmbedder  bool
}

func oldCall(t *testing.T, path string, req oldRequest) oldResponse {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := gob.NewEncoder(conn).Encode(&req); err != nil {
		t.Fatal(err)
	}
	var resp oldResponse
	if err := gob.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

// A child from release 151 (an update in progress) still reads the new
// primary's answers: the download's state is a field it doesn't know.
func TestAnOlderChildReadsTheNewPrimary(t *testing.T) {
	path := socketPath(t)
	p := &fetchingPrimary{state: EmbedderState{CanFetch: true, Downloading: true, Percent: 7, Problem: "unreachable"}}
	go ServeModels(path, p.models(), 1)
	waitForSocket(path)
	if resp := oldCall(t, path, oldRequest{Op: "info"}); resp.Err != "" || resp.HasEmbedder || resp.Proto != cProto {
		t.Errorf("info while downloading: %+v", resp)
	}
	p.model.Store(&fakeEmbedder{})
	if resp := oldCall(t, path, oldRequest{Op: "info"}); !resp.HasEmbedder {
		t.Errorf("info once there: %+v", resp)
	}
	if resp := oldCall(t, path, oldRequest{Op: "embed-text", Text: "snow"}); resp.Err != "" || len(resp.Vec) != 2 {
		t.Errorf("a search: %+v", resp)
	}
}
