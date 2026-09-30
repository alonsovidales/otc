// SPDX-License-Identifier: AGPL-3.0-or-later

// Package modelserver shares the machine-learning models between the
// instances on one device (issue #167). Each supervised user instance (see
// package supervisor) used to load its own RAM++ tagger - ~870 MB of native
// memory, outside the Go memory limit - and its own face models; with two
// or three users that alone could take the whole Pi. The primary instance
// loads them once and serves them over a Unix socket; the children send it
// their images and get back tags and faces, computed exactly as they would
// have been locally (the full-resolution image travels: face embeddings are
// cropped from it).
package modelserver

import (
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"net"
	"os"
	"path/filepath"
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

type request struct {
	Op     string // "tags", "faces", "info"
	W, H   int
	Stride int
	Pix    []byte
	Opt    imagestagger.RAMOptions
}

type response struct {
	Tags  []imagestagger.RAMTag
	Faces []facerecognition.FaceDetection
	// HasFaces: the primary has face models (info).
	HasFaces bool
	Err      string
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
	var req request
	if err := gob.NewDecoder(conn).Decode(&req); err != nil {
		return
	}
	var resp response
	switch req.Op {
	case "info":
		resp.HasFaces = faces != nil
	case "tags", "faces":
		img, err := toImage(req)
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

func toImage(req request) (image.Image, error) {
	if req.W <= 0 || req.H <= 0 || req.Stride < req.W*4 || len(req.Pix) < req.Stride*(req.H-1)+req.W*4 {
		return nil, errors.New("malformed image")
	}
	return &image.RGBA{Pix: req.Pix, Stride: req.Stride, Rect: image.Rect(0, 0, req.W, req.H)}, nil
}

// Client is a child's view of the primary's models.
type Client struct {
	Path string
}

func (c *Client) call(req request) (*response, error) {
	conn, err := net.DialTimeout("unix", c.Path, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("the shared models aren't reachable: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(cCallTimeout))
	if err := gob.NewEncoder(conn).Encode(&req); err != nil {
		return nil, err
	}
	var resp response
	if err := gob.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, err
	}
	if resp.Err != "" {
		return &resp, errors.New(resp.Err)
	}
	return &resp, nil
}

// rgba is img as the RGBA a request carries - at full resolution, so the
// results are the same as local inference.
func rgba(img image.Image) *image.RGBA {
	if r, ok := img.(*image.RGBA); ok && r.Rect.Min == (image.Point{}) {
		return r
	}
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), img, b.Min, draw.Src)
	return out
}

func imageRequest(op string, img image.Image) request {
	r := rgba(img)
	return request{Op: op, W: r.Rect.Dx(), H: r.Rect.Dy(), Stride: r.Stride, Pix: r.Pix}
}

func (c *Client) Tags(ctx context.Context, img image.Image, opt imagestagger.RAMOptions) ([]imagestagger.RAMTag, error) {
	req := imageRequest("tags", img)
	req.Opt = opt
	resp, err := c.call(req)
	if err != nil {
		return nil, err
	}
	return resp.Tags, nil
}

func (c *Client) DetectFaces(img image.Image) ([]facerecognition.FaceDetection, error) {
	resp, err := c.call(imageRequest("faces", img))
	if err != nil {
		return nil, err
	}
	return resp.Faces, nil
}

// HasFaces asks the primary whether it has face models.
func (c *Client) HasFaces() (bool, error) {
	resp, err := c.call(request{Op: "info"})
	if err != nil {
		return false, err
	}
	return resp.HasFaces, nil
}
