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
// tagging model takes, scaled by the child (imagestagger.Resize).
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
	"sync"
	"time"

	facerecognition "github.com/alonsovidales/otc/face_recognition"
	imagestagger "github.com/alonsovidales/otc/images_tagger"
	"github.com/alonsovidales/otc/log"
)

// EnvSocket is set by the supervisor on a child it spawns: the primary's
// model socket. Its presence is what makes a child use the shared models.
const EnvSocket = "OTC_MODELS_SOCKET"

// cCallTimeout bounds one inference, queueing included: the primary runs
// every instance's requests, and a Reprocess on one can keep it busy.
const cCallTimeout = 10 * time.Minute

// Tagger is what files_manager tags with: the local RAM++ model, or Client.
type Tagger interface {
	Tags(ctx context.Context, img image.Image, opt imagestagger.RAMOptions) ([]imagestagger.RAMTag, error)
}

// FaceDetector is what files_manager finds faces with: the local
// Recognizer, or Client.
type FaceDetector interface {
	DetectFaces(img image.Image) ([]facerecognition.FaceDetection, error)
}

// cProto is the socket version this side speaks (see the package doc).
const cProto = 1

// cMaxPixelBytes bounds the raw pixels one request may announce: gob's own
// cap on a message (1 GiB << 1 on 64-bit), so the largest image accepted
// is what it always was.
const cMaxPixelBytes = 1 << 31

type request struct {
	Op     string // "tags", "tags-resized" (version 1), "faces", "info"
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
		go serveConn(conn, tagger, faces)
	}
}

func serveConn(conn net.Conn, tagger func() Tagger, faces FaceDetector) {
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
	var resp response
	switch req.Op {
	case "info":
		resp.HasFaces = faces != nil
		resp.Proto = cProto
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
	resp, err := c.call(request{Op: "info"}, nil)
	if err != nil {
		return false, err
	}
	c.mu.Lock()
	c.proto, c.known = resp.Proto, true
	c.mu.Unlock()
	return resp.HasFaces, nil
}
