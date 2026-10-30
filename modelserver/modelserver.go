// SPDX-License-Identifier: AGPL-3.0-or-later

// Package modelserver shares the machine-learning models between the
// instances on one device (issue #167). Each supervised user instance (see
// package supervisor) used to load its own RAM++ tagger - ~870 MB of native
// memory, outside the Go memory limit - and its own face models; with two
// or three users that alone could take the whole Pi. The primary instance
// loads them once and serves them over a Unix socket; the children send it
// their images and get back tags and faces, computed exactly as they would
// have been locally. For faces the full-resolution image travels (face
// embeddings are cropped from it); for tags only the 384x384 image the
// tagging model takes, scaled by the child (imagestagger.Resize); for On
// this day's photo quality only the 224x224 one NIMA takes
// (imagesquality.Resize).
//
// A request is a gob header; its pixels, when there are any, follow it as
// raw bytes (header.N), so neither side builds a gob message the size of
// the photo - decoding one took about five times the image in memory on
// the primary. That is version 1 of the socket (response.Proto), which a
// child uses only once the primary has said it speaks it ("info"): a child
// started from a new binary while an older primary still runs (an update
// in progress) keeps to version 0, the pixels inside the gob message,
// which every primary still accepts.
package modelserver

import (
	"bufio"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	facerecognition "github.com/alonsovidales/otc/face_recognition"
	imagesembedder "github.com/alonsovidales/otc/images_embedder"
	imagesquality "github.com/alonsovidales/otc/images_quality"
	imagestagger "github.com/alonsovidales/otc/images_tagger"
	"github.com/alonsovidales/otc/log"
)

// EnvSocket is set by the supervisor on a child it spawns: the primary's
// model socket. Its presence is what makes a child use the shared models.
const EnvSocket = "OTC_MODELS_SOCKET"

// cCallTimeout bounds one inference, queueing included: the primary runs
// every instance's requests, and a Reprocess on one can keep it busy.
const cCallTimeout = 10 * time.Minute

// ErrNoTagger is the answer to tagging while the primary has no model
// loaded (files_manager's tagger_load.go: its load failed and is tried
// again): the photo is processed without tags, and the owner was told
// once. A child gets it back through the socket as itself.
var ErrNoTagger = errors.New("the image tagging model is not loaded") // i18n-ignore: matched by errors.Is and across the socket, logged only

// Tagger is what files_manager tags with: the local RAM++ model, or Client.
type Tagger interface {
	Tags(ctx context.Context, img image.Image, opt imagestagger.RAMOptions) ([]imagestagger.RAMTag, error)
}

// FaceDetector is what files_manager finds faces with: the local
// Recognizer, or Client.
type FaceDetector interface {
	DetectFaces(img image.Image) ([]facerecognition.FaceDetection, error)
}

// AestheticScorer is what files_manager scores photos with for On this day:
// the local NIMA model (images_quality), or Client. img is already through
// imagesquality.Resize (an InputSize square), all the model takes of a
// photo.
type AestheticScorer interface {
	AestheticResized(ctx context.Context, img *image.RGBA) (float32, error)
}

// ErrNoAesthetic is the answer to scoring while the primary has no quality
// model (its file missing: photos are scored by the measures alone), and
// what a child gets from a primary older than the model.
var ErrNoAesthetic = errors.New("the photo quality model is not loaded") // i18n-ignore: matched by errors.Is and across the socket, logged only

// Embedder is Smart search's model (images_embedder, SigLIP 2): a photo
// already through imagesembedder.Resize, or a search's text, as a
// normalised vector. files_manager's, or Client.
type Embedder interface {
	EmbedResized(ctx context.Context, img *image.RGBA) ([]float32, error)
	EmbedText(ctx context.Context, text string) ([]float32, error)
}

// ErrNoEmbedder is the answer to embedding while the primary has no Smart
// search model (its files missing), and what a child gets from a primary
// older than it.
var ErrNoEmbedder = errors.New("the smart search model is not available") // i18n-ignore: matched by errors.Is and across the socket, logged only

// cMaxEmbedText is the longest search text the socket carries (bytes): the
// model reads 63 tokens at most.
const cMaxEmbedText = 4096

// Models is what the primary serves: the tagger (asked per request, so it
// can wait for a model still loading), the face models, the quality model
// and Smart search's (nil: none on this device).
type Models struct {
	Tagger    func() Tagger
	Faces     FaceDetector
	Aesthetic AestheticScorer
	Embedder  Embedder
}

// cProto is the socket version this side speaks (see the package doc).
const cProto = 1

// cMaxPixelBytes bounds the raw pixels one request may announce: gob's own
// cap on a message (1 GiB << 1 on 64-bit), so the largest image accepted
// is what it always was.
const cMaxPixelBytes = 1 << 31

type request struct {
	Op     string // "tags", "tags-resized" (version 1), "faces", "aesthetic", "embed-image", "embed-text", "info"
	Text   string // "embed-text": the search's text
	W, H   int
	Stride int
	Pix    []byte // version 0: the pixels, in the message
	Opt    imagestagger.RAMOptions
	// N (version 1): this many bytes of pixels follow the message, W*H
	// RGBA pixels with no padding, instead of Pix.
	N int64
}

type response struct {
	Tags  []imagestagger.RAMTag
	Faces []facerecognition.FaceDetection
	// HasFaces: the primary has face models (info).
	HasFaces bool
	Err      string
	// Proto: the socket version the primary speaks (info); 0 from one
	// older than versions.
	Proto int
	// Aesthetic is the quality model's score ("aesthetic"); HasAesthetic
	// (info): the primary has the model. Unset by a primary older than it.
	Aesthetic    float32
	HasAesthetic bool
	// Vec is Smart search's vector ("embed-image", "embed-text");
	// HasEmbedder (info): the primary has the model. Unset by a primary
	// older than it.
	Vec         []float32
	HasEmbedder bool
}

// resizedTagger is the primary's tagger taking an image already scaled to
// the model's size (imagestagger.RAMTagger).
type resizedTagger interface {
	TagsResized(ctx context.Context, img *image.RGBA, opt imagestagger.RAMOptions) ([]imagestagger.RAMTag, error)
}

// SocketPath is where the primary serves the models: the child's EnvSocket
// when it has one, else models.sock in the working directory.
func SocketPath() string {
	if p := os.Getenv(EnvSocket); p != "" {
		return p
	}
	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}
	return filepath.Join(wd, "models.sock")
}

// Serve listens on path and answers the children's requests. tagger is
// called per request, so it can wait for a model still loading; faces may
// be nil (no face models on this device).
func Serve(path string, tagger func() Tagger, faces FaceDetector) error {
	return ServeLimited(path, tagger, faces, 0)
}

// ServeLimited is Serve answering at most limit requests at once (0: no
// limit), the others waiting their turn - within cCallTimeout - before
// their pixels are read: a full-resolution photo is ~48 MB as it arrives,
// and a device short of memory shouldn't hold one per child at once.
func ServeLimited(path string, tagger func() Tagger, faces FaceDetector, limit int) error {
	return ServeModels(path, Models{Tagger: tagger, Faces: faces}, limit)
}

// ServeModels is ServeLimited with every model the primary has.
func ServeModels(path string, models Models, limit int) error {
	var slots chan struct{}
	if limit > 0 {
		slots = make(chan struct{}, limit)
	}
	os.Remove(path) // a socket left by the previous run
	l, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	// The service's own user only: the children run as it.
	if err := os.Chmod(path, 0o600); err != nil { // perms: rw-------
		l.Close()
		return err
	}
	log.Info("serving the models to the other instances on", path)
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		go serveConn(conn, models, slots)
	}
}

func serveConn(conn net.Conn, models Models, slots chan struct{}) {
	tagger, faces := models.Tagger, models.Faces
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(cCallTimeout))
	// The pixels after the header are read from the same buffered reader
	// the header is: a gob decoder reads ahead on anything that isn't an
	// io.ByteReader, and those bytes would be lost to a read of conn.
	br := bufio.NewReader(conn)
	var req request
	if err := gob.NewDecoder(br).Decode(&req); err != nil {
		return
	}
	// A request with pixels waits for its turn before they are read; "info"
	// never waits.
	if slots != nil && req.Op != "info" {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		case <-time.After(cCallTimeout):
			return // the child has given up on it by now
		}
	}
	var resp response
	switch req.Op {
	case "info":
		resp.HasFaces = faces != nil
		resp.HasAesthetic = models.Aesthetic != nil
		resp.HasEmbedder = models.Embedder != nil
		resp.Proto = cProto
	case "embed-image":
		img, err := readImage(br, req)
		if err != nil {
			resp.Err = err.Error()
			break
		}
		if models.Embedder == nil {
			resp.Err = ErrNoEmbedder.Error()
			break
		}
		if img.Rect.Dx() != imagesembedder.ImageSize || img.Rect.Dy() != imagesembedder.ImageSize {
			resp.Err = "malformed image"
			break
		}
		vec, err := models.Embedder.EmbedResized(context.Background(), img)
		resp.Vec = vec
		if err != nil {
			resp.Err = err.Error()
		}
	case "embed-text":
		if models.Embedder == nil {
			resp.Err = ErrNoEmbedder.Error()
			break
		}
		if len(req.Text) > cMaxEmbedText {
			resp.Err = "text too long"
			break
		}
		vec, err := models.Embedder.EmbedText(context.Background(), req.Text)
		resp.Vec = vec
		if err != nil {
			resp.Err = err.Error()
		}
	case "aesthetic":
		img, err := readImage(br, req)
		if err != nil {
			resp.Err = err.Error()
			break
		}
		if models.Aesthetic == nil {
			resp.Err = ErrNoAesthetic.Error()
			break
		}
		if img.Rect.Dx() != imagesquality.InputSize || img.Rect.Dy() != imagesquality.InputSize {
			resp.Err = "malformed image"
			break
		}
		score, err := models.Aesthetic.AestheticResized(context.Background(), img)
		resp.Aesthetic = score
		if err != nil {
			resp.Err = err.Error()
		}
	case "tags", "tags-resized", "faces":
		img, err := readImage(br, req)
		if err != nil {
			resp.Err = err.Error()
			break
		}
		if req.Op == "tags" {
			tags, err := tagger().Tags(context.Background(), img, req.Opt)
			resp.Tags = tags
			if err != nil {
				resp.Err = err.Error()
			}
		} else if req.Op == "tags-resized" {
			rt, ok := tagger().(resizedTagger)
			if !ok {
				resp.Err = "this tagger can't take a resized image"
				break
			}
			if size := req.Opt.ImageSize; size <= 0 || img.Rect.Dx() != size || img.Rect.Dy() != size {
				resp.Err = "malformed image"
				break
			}
			tags, err := rt.TagsResized(context.Background(), img, req.Opt)
			resp.Tags = tags
			if err != nil {
				resp.Err = err.Error()
			}
		} else if faces == nil {
			resp.Err = "face recognition is not available on this device"
		} else {
			dets, err := faces.DetectFaces(img)
			resp.Faces = dets
			if err != nil {
				resp.Err = err.Error()
			}
		}
	default:
		resp.Err = "unknown request " + req.Op
	}
	gob.NewEncoder(conn).Encode(&resp)
}

// readImage is a request's image: its raw pixels from r (version 1),
// checked and sized before anything is allocated, or the ones in the
// message (version 0).
func readImage(r io.Reader, req request) (*image.RGBA, error) {
	if req.N == 0 {
		return toImage(req)
	}
	w, h := int64(req.W), int64(req.H)
	if w <= 0 || h <= 0 || w > cMaxPixelBytes/4 || h > cMaxPixelBytes/4 || req.N != 4*w*h || req.N >= cMaxPixelBytes {
		return nil, errors.New("malformed image")
	}
	pix := make([]byte, req.N)
	if _, err := io.ReadFull(r, pix); err != nil {
		return nil, fmt.Errorf("reading the image: %w", err)
	}
	return &image.RGBA{Pix: pix, Stride: 4 * req.W, Rect: image.Rect(0, 0, req.W, req.H)}, nil
}

func toImage(req request) (*image.RGBA, error) {
	if req.W <= 0 || req.H <= 0 || req.Stride < req.W*4 || len(req.Pix) < req.Stride*(req.H-1)+req.W*4 {
		return nil, errors.New("malformed image")
	}
	return &image.RGBA{Pix: req.Pix, Stride: req.Stride, Rect: image.Rect(0, 0, req.W, req.H)}, nil
}

// Client is a child's view of the primary's models.
type Client struct {
	Path string

	mu    sync.Mutex
	proto int  // the primary's socket version, once known
	known bool // proto has been asked
}

// protocol is the primary's socket version, asked once ("info"). Until it
// could be asked, version 0: every primary speaks it.
func (c *Client) protocol() int {
	c.mu.Lock()
	known, p := c.known, c.proto
	c.mu.Unlock()
	if known {
		return p
	}
	if _, err := c.HasFaces(); err != nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.proto
}

// call sends req, then pix as raw bytes (version 1) when there are any.
func (c *Client) call(req request, pix []byte) (*response, error) {
	conn, err := net.DialTimeout("unix", c.Path, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("the shared models aren't reachable: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(cCallTimeout))
	req.N = int64(len(pix))
	if err := gob.NewEncoder(conn).Encode(&req); err != nil {
		return nil, err
	}
	var werr error
	if len(pix) > 0 {
		_, werr = conn.Write(pix)
	}
	// Read even when the write failed: a refusal the primary sent before
	// it stopped reading is the better error.
	var resp response
	if err := gob.NewDecoder(conn).Decode(&resp); err != nil {
		if werr != nil {
			return nil, werr
		}
		return nil, err
	}
	if resp.Err != "" {
		if resp.Err == ErrNoTagger.Error() {
			return &resp, ErrNoTagger
		}
		// A primary older than the quality model doesn't know the request.
		if resp.Err == ErrNoAesthetic.Error() || (req.Op == "aesthetic" && resp.Err == "unknown request aesthetic") {
			return &resp, ErrNoAesthetic
		}
		// Nor one older than Smart search.
		if resp.Err == ErrNoEmbedder.Error() || (strings.HasPrefix(req.Op, "embed-") && resp.Err == "unknown request "+req.Op) {
			return &resp, ErrNoEmbedder
		}
		return &resp, errors.New(resp.Err)
	}
	return &resp, nil
}

// rgba is img as the RGBA a request carries - at full resolution, so the
// results are the same as local inference - starting at 0,0 with no
// padding between rows, as the raw pixels travel.
func rgba(img image.Image) *image.RGBA {
	if r, ok := img.(*image.RGBA); ok && r.Rect.Min == (image.Point{}) && r.Stride == 4*r.Rect.Dx() && len(r.Pix) == r.Stride*r.Rect.Dy() {
		return r
	}
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), img, b.Min, draw.Src)
	return out
}

// imageCall asks op for r, its pixels the way the primary takes them.
func (c *Client) imageCall(op string, r *image.RGBA, opt imagestagger.RAMOptions) (*response, error) {
	req := request{Op: op, W: r.Rect.Dx(), H: r.Rect.Dy(), Stride: r.Stride, Opt: opt}
	if c.protocol() >= 1 {
		return c.call(req, r.Pix)
	}
	req.Pix = r.Pix
	return c.call(req, nil)
}

func (c *Client) Tags(ctx context.Context, img image.Image, opt imagestagger.RAMOptions) ([]imagestagger.RAMTag, error) {
	if c.protocol() < 1 {
		resp, err := c.imageCall("tags", rgba(img), opt)
		if err != nil {
			return nil, err
		}
		return resp.Tags, nil
	}
	// All the model takes of a photo is this scale of it, made here: the
	// whole photo crossed the socket only to be scaled on the primary.
	if opt.ImageSize == 0 {
		opt.ImageSize = imagestagger.DefaultRAMOptions().ImageSize
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resp, err := c.imageCall("tags-resized", imagestagger.Resize(img, opt.ImageSize), opt)
	if err != nil {
		return nil, err
	}
	return resp.Tags, nil
}

func (c *Client) DetectFaces(img image.Image) ([]facerecognition.FaceDetection, error) {
	resp, err := c.imageCall("faces", rgba(img), imagestagger.RAMOptions{})
	if err != nil {
		return nil, err
	}
	return resp.Faces, nil
}

// HasFaces asks the primary whether it has face models (and, on the way,
// which socket version it speaks).
func (c *Client) HasFaces() (bool, error) {
	resp, err := c.info()
	if err != nil {
		return false, err
	}
	return resp.HasFaces, nil
}

// HasAesthetic asks the primary whether it has the quality model.
func (c *Client) HasAesthetic() (bool, error) {
	resp, err := c.info()
	if err != nil {
		return false, err
	}
	return resp.HasAesthetic, nil
}

// info asks the primary what it has, noting its socket version.
func (c *Client) info() (*response, error) {
	resp, err := c.call(request{Op: "info"}, nil)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.proto, c.known = resp.Proto, true
	c.mu.Unlock()
	return resp, nil
}

// HasEmbedder asks the primary whether it has Smart search's model.
func (c *Client) HasEmbedder() (bool, error) {
	resp, err := c.info()
	if err != nil {
		return false, err
	}
	return resp.HasEmbedder, nil
}

// EmbedResized is the primary's Smart search vector of img, already through
// imagesembedder.Resize: ErrNoEmbedder when the primary has no model.
func (c *Client) EmbedResized(ctx context.Context, img *image.RGBA) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resp, err := c.imageCall("embed-image", img, imagestagger.RAMOptions{})
	if err != nil {
		return nil, err
	}
	return resp.Vec, nil
}

// EmbedText is the primary's Smart search vector of a search's text.
func (c *Client) EmbedText(ctx context.Context, text string) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resp, err := c.call(request{Op: "embed-text", Text: text}, nil)
	if err != nil {
		return nil, err
	}
	return resp.Vec, nil
}

// AestheticResized is the primary's quality model's score of img, already
// through imagesquality.Resize: ErrNoAesthetic when the primary has none.
func (c *Client) AestheticResized(ctx context.Context, img *image.RGBA) (float32, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	resp, err := c.imageCall("aesthetic", rgba(img), imagestagger.RAMOptions{})
	if err != nil {
		return 0, err
	}
	return resp.Aesthetic, nil
}
