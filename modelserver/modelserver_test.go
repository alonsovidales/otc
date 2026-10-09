// SPDX-License-Identifier: AGPL-3.0-or-later

package modelserver

import (
	"bytes"
	"context"
	"encoding/gob"
	"image"
	"image/color"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	facerecognition "github.com/alonsovidales/otc/face_recognition"
	imagestagger "github.com/alonsovidales/otc/images_tagger"
)

type fakeTagger struct {
	gotW, gotH int
	gotPix     color.RGBA
	// resized: what TagsResized got - the primary's model input.
	resized *image.RGBA
}

func (f *fakeTagger) Tags(_ context.Context, img image.Image, opt imagestagger.RAMOptions) ([]imagestagger.RAMTag, error) {
	f.gotW, f.gotH = img.Bounds().Dx(), img.Bounds().Dy()
	f.gotPix = img.At(3, 2).(color.RGBA)
	return []imagestagger.RAMTag{{Name: "dog", Score: 0.9}}, nil
}

func (f *fakeTagger) TagsResized(_ context.Context, img *image.RGBA, opt imagestagger.RAMOptions) ([]imagestagger.RAMTag, error) {
	f.resized = img
	return []imagestagger.RAMTag{{Name: "dog", Score: 0.9}}, nil
}

type fakeFaces struct {
	gotW, gotH int
	got        *image.RGBA
}

func (f *fakeFaces) DetectFaces(img image.Image) ([]facerecognition.FaceDetection, error) {
	f.gotW, f.gotH = img.Bounds().Dx(), img.Bounds().Dy()
	f.got, _ = img.(*image.RGBA)
	return []facerecognition.FaceDetection{{X: 1, Y: 2, W: 30, H: 40, Score: 0.8, Embedding: []float32{0.1, 0.2}, Thumbnail: []byte{9}}}, nil
}

func socketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "otcms") // short: Unix socket paths are length-limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "m.sock")
}

func waitForSocket(path string) {
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func serveForTest(t *testing.T, faces FaceDetector) (*Client, *fakeTagger) {
	t.Helper()
	path := socketPath(t)
	tg := &fakeTagger{}
	go Serve(path, func() Tagger { return tg }, faces)
	waitForSocket(path)
	return &Client{Path: path}, tg
}

// Issue #167: a child's results are what the primary's models give for
// its image. Faces get it whole - full resolution, same pixels; tags get
// the model's 384x384 input, scaled by the child exactly as the primary
// would have scaled it.
func TestClientUsesTheSharedModels(t *testing.T) {
	ff := &fakeFaces{}
	c, tg := serveForTest(t, ff)
	img := image.NewNRGBA(image.Rect(10, 10, 4042, 3034)) // not RGBA, not at 0,0
	img.Set(13, 12, color.NRGBA{R: 200, G: 100, B: 50, A: 255})
	img.Set(2000, 1500, color.NRGBA{R: 1, G: 2, B: 3, A: 255})

	tags, err := c.Tags(context.Background(), img, imagestagger.RAMOptions{ImageSize: 384})
	if err != nil || len(tags) != 1 || tags[0].Name != "dog" {
		t.Fatalf("tags %v %v", tags, err)
	}
	if tg.resized == nil || tg.resized.Rect != image.Rect(0, 0, 384, 384) {
		t.Fatalf("the model got %v, want the 384x384 input", tg.resized)
	}
	if want := imagestagger.Resize(img, 384); !bytes.Equal(tg.resized.Pix, want.Pix) {
		t.Error("the model's input isn't what scaling the photo here gives")
	}

	faces, err := c.DetectFaces(img)
	if err != nil || len(faces) != 1 || faces[0].W != 30 || len(faces[0].Embedding) != 2 || faces[0].Thumbnail[0] != 9 {
		t.Fatalf("faces %+v %v", faces, err)
	}
	if ff.gotW != 4032 || ff.gotH != 3024 {
		t.Errorf("faces got %dx%d, want the full 4032x3024", ff.gotW, ff.gotH)
	}
	if got := ff.got.RGBAAt(3, 2); got != (color.RGBA{R: 200, G: 100, B: 50, A: 255}) {
		t.Errorf("pixel changed on the way: %v", got)
	}
	if got := ff.got.RGBAAt(1990, 1490); got != (color.RGBA{R: 1, G: 2, B: 3, A: 255}) {
		t.Errorf("pixel changed on the way: %v", got)
	}
	if has, err := c.HasFaces(); err != nil || !has {
		t.Errorf("HasFaces = %v %v", has, err)
	}
	if p := c.protocol(); p != cProto {
		t.Errorf("the child speaks socket version %d with a current primary, want %d", p, cProto)
	}
}

// An RGBA cut out of a larger one keeps its parent's stride: it travels
// repacked, its own pixels only.
func TestClientSendsASubImageAsItsOwnPixels(t *testing.T) {
	ff := &fakeFaces{}
	c, _ := serveForTest(t, ff)
	parent := image.NewRGBA(image.Rect(0, 0, 40, 30))
	parent.SetRGBA(5, 6, color.RGBA{R: 9, G: 8, B: 7, A: 255})
	sub := parent.SubImage(image.Rect(0, 0, 20, 10)) // at 0,0, stride 160 not 80
	if _, err := c.DetectFaces(sub); err != nil {
		t.Fatal(err)
	}
	if ff.gotW != 20 || ff.gotH != 10 || ff.got.RGBAAt(5, 6) != (color.RGBA{R: 9, G: 8, B: 7, A: 255}) {
		t.Errorf("faces got %dx%d, pixel %v", ff.gotW, ff.gotH, ff.got.RGBAAt(5, 6))
	}
}

func TestNoFaceModelsOnThePrimary(t *testing.T) {
	c, _ := serveForTest(t, nil)
	if has, err := c.HasFaces(); err != nil || has {
		t.Errorf("HasFaces = %v %v, want false", has, err)
	}
	if _, err := c.DetectFaces(image.NewRGBA(image.Rect(0, 0, 2, 2))); err == nil {
		t.Error("faces answered without face models")
	}
}

// rawCall sends a header and then body as they are, and reads the answer.
func rawCall(t *testing.T, path string, req request, body []byte) (response, error) {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := gob.NewEncoder(conn).Encode(&req); err != nil {
		t.Fatal(err)
	}
	conn.Write(body)
	conn.(*net.UnixConn).CloseWrite()
	var resp response
	err = gob.NewDecoder(conn).Decode(&resp)
	return resp, err
}

// A header announcing more pixels than follow, or pixels that don't
// match its size, is refused - promptly, not at the call's deadline.
func TestMalformedImagesAreRefused(t *testing.T) {
	ff := &fakeFaces{}
	c, _ := serveForTest(t, ff)
	start := time.Now()
	resp, err := rawCall(t, c.Path, request{Op: "faces", W: 10, H: 10, N: 400}, make([]byte, 100))
	if err == nil && resp.Err == "" {
		t.Error("a short image was taken")
	}
	if time.Since(start) > 5*time.Second {
		t.Error("a short image held the connection")
	}
	for _, req := range []request{
		{Op: "faces", W: 10, H: 10, N: 399},
		{Op: "faces", W: 0, H: 10, N: 4},
		{Op: "faces", W: 1 << 40, H: 1 << 40, N: 4},
		{Op: "tags-resized", W: 10, H: 10, N: 400, Opt: imagestagger.RAMOptions{ImageSize: 384}},
	} {
		if resp, err := rawCall(t, c.Path, req, make([]byte, req.N)); err != nil || resp.Err == "" {
			t.Errorf("%+v was taken (%v)", req, err)
		}
	}
	if ff.got != nil {
		t.Error("a malformed image reached the model")
	}
}

// A child of an older release (during an update) sends its pixels in the
// message: still served.
func TestAnOlderChildIsStillServed(t *testing.T) {
	ff := &fakeFaces{}
	c, tg := serveForTest(t, ff)
	img := image.NewRGBA(image.Rect(0, 0, 6, 4))
	img.SetRGBA(3, 2, color.RGBA{R: 7, A: 255})
	for _, op := range []string{"faces", "tags"} {
		resp, err := rawCall(t, c.Path, request{Op: op, W: 6, H: 4, Stride: img.Stride, Pix: img.Pix}, nil)
		if err != nil || resp.Err != "" {
			t.Fatalf("%s: %v %s", op, err, resp.Err)
		}
	}
	if ff.got.RGBAAt(3, 2) != (color.RGBA{R: 7, A: 255}) || tg.gotPix != (color.RGBA{R: 7, A: 255}) {
		t.Error("an older child's pixels changed on the way")
	}
}

// oldPrimary answers as a primary of an older release: no socket
// version, pixels only in the message, no "tags-resized".
func oldPrimary(t *testing.T, got chan<- request) string {
	t.Helper()
	path := socketPath(t)
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			var req request
			gob.NewDecoder(conn).Decode(&req)
			var resp response
			switch req.Op {
			case "info":
				resp.HasFaces = true
			case "tags", "faces":
				if _, err := toImage(req); err != nil {
					resp.Err = err.Error()
				}
				got <- req
			default:
				resp.Err = "unknown request " + req.Op
			}
			gob.NewEncoder(conn).Encode(&resp)
			conn.Close()
		}
	}()
	return path
}

// A child from a new binary while the primary is still the older one (an
// update in progress) speaks the older socket: whole images, in the
// message.
func TestANewChildSpeaksAnOlderPrimarysSocket(t *testing.T) {
	got := make(chan request, 2)
	c := &Client{Path: oldPrimary(t, got)}
	img := image.NewNRGBA(image.Rect(0, 0, 50, 40))
	if _, err := c.Tags(context.Background(), img, imagestagger.DefaultRAMOptions()); err != nil {
		t.Fatal(err)
	}
	if req := <-got; req.Op != "tags" || req.W != 50 || req.H != 40 || len(req.Pix) != 50*40*4 {
		t.Errorf("tags sent as %s %dx%d with %d bytes", req.Op, req.W, req.H, len(req.Pix))
	}
	if _, err := c.DetectFaces(img); err != nil {
		t.Fatal(err)
	}
	if req := <-got; req.Op != "faces" || len(req.Pix) != 50*40*4 {
		t.Errorf("faces sent as %s with %d bytes", req.Op, len(req.Pix))
	}
}

// slowFaces is a detector that takes a while and counts calls at once.
type slowFaces struct {
	mu        sync.Mutex
	now, peak int
	release   chan struct{}
}

func (f *slowFaces) DetectFaces(img image.Image) ([]facerecognition.FaceDetection, error) {
	f.mu.Lock()
	f.now++
	f.peak = max(f.peak, f.now)
	f.mu.Unlock()
	<-f.release
	f.mu.Lock()
	f.now--
	f.mu.Unlock()
	return nil, nil
}

// ServeLimited(1) - a low-memory primary - answers one image request at a
// time; "info" never waits for its turn.
func TestServeLimitedOneAtATime(t *testing.T) {
	path := socketPath(t)
	faces := &slowFaces{release: make(chan struct{})}
	go ServeLimited(path, func() Tagger { return &fakeTagger{} }, faces, 1)
	waitForSocket(path)
	c := &Client{Path: path}
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.DetectFaces(img); err != nil {
				t.Error(err)
			}
		}()
	}
	time.Sleep(100 * time.Millisecond) // all three sent
	done := make(chan struct{})
	go func() {
		if _, err := c.HasFaces(); err != nil {
			t.Error(err)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("info waited behind the image requests")
	}
	for i := 0; i < 3; i++ {
		faces.release <- struct{}{}
	}
	wg.Wait()
	if faces.peak != 1 {
		t.Errorf("%d requests at once, want 1", faces.peak)
	}
}
