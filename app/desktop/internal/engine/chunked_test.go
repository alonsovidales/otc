// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/alonsovidales/otc/app/desktop/internal/wsclient"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// fakeDevice answers the chunked transfer requests the way the device
// does (issue #168): uploads arrive in order and are checked against the
// hash at the end; ReadFile serves a stored file's original bytes.
type fakeDevice struct {
	mu      sync.Mutex
	files   map[string][]byte
	pending map[string]*bytes.Buffer
	paths   map[string]string
	chunks  int
	list    []*pb.File // what ListFiles answers
	// listStored: ListFiles answers what is stored now (files, under the
	// path asked for, with their hashes, and upload_only while delAnswer
	// refuses deletes) instead of list.
	listStored bool
	reads      int                    // ReadFile/GetFile requests
	deletes    []string               // DelFile paths
	onRead     func()                 // runs on each ReadFile, as if the user did something meanwhile
	modTime    *timestamppb.Timestamp // the files' date, when set
	onHas      func()                 // runs on each HasFile (the start of an upload)
	// afterList runs once a recursive listing has been answered, as if the
	// device's files changed right after (a phone, during a long pass).
	afterList func()
	// pointLists counts the listings of one file's path (deviceStillAsListed).
	pointLists int
	// SetUploadOnly requests answered, and whether they are refused.
	uploadOnly       int
	refuseUploadOnly bool
	// Issue #132: the SetUploadOnly requests in order, how they are
	// answered ("" ok, else that error_code), and the error_code DelFile
	// answers with ("" deletes; "upload_only" refuses, as the device does
	// under an upload-only folder).
	uploadOnlySet    []*pb.SetUploadOnly
	uploadOnlyAnswer string
	delAnswer        string
	// Uploads begun and not finished, now and at most.
	open, maxOpen int
	// Issue #192: the SetOutOfImages requests in order, how they are
	// answered ("" ok, else that error_code - "unknown_payload" answers
	// ListOutOfImages the same way), the folders kept out (with their
	// slash), a hook run on each SetOutOfImages, and the order requests
	// came in ("images", "list", "has").
	imagesSet    []*pb.SetOutOfImages
	imagesAnswer string
	keptOut      []string
	onSetImages  func()
	events       []string
	// Issue #205: unfinished uploads by their resume key (path, size and
	// sha256), as release 166 keeps them across connections; forgetful
	// answers received 0 to every BeginUpload, as a device before it. The
	// BeginUploads in order with what each was answered, and every
	// UploadChunk's offset.
	resumable    map[uploadKey]string
	forgetful    bool
	begins       []*pb.BeginUpload
	beginAnswers []int64
	chunkOffsets []int64
	// dropOn, when it says so, closes the connection on that request
	// without handling it; dropAfter once it is handled (its answer lost).
	// down refuses every new connection.
	dropOn    func(*pb.ReqEnvelope) bool
	dropAfter func(*pb.ReqEnvelope) bool
	down      bool
	conns     int
}

// uploadKey is BeginUpload's resume key (issue #205).
type uploadKey struct {
	path string
	size int64
	sha  string
}

// handle answers req; nil closes the connection instead (dropOn,
// dropAfter).
func (d *fakeDevice) handle(req *pb.ReqEnvelope, pubDER []byte) *pb.RespEnvelope {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dropOn != nil && d.dropOn(req) {
		return nil
	}
	resp := d.answer(req, pubDER)
	if d.dropAfter != nil && d.dropAfter(req) {
		return nil
	}

	return resp
}

func (d *fakeDevice) answer(req *pb.ReqEnvelope, pubDER []byte) *pb.RespEnvelope {
	resp := &pb.RespEnvelope{Id: req.Id}
	switch p := req.Payload.(type) {
	case *pb.ReqEnvelope_ReqGetPubKey:
		resp.Payload = &pb.RespEnvelope_RespPubKey{RespPubKey: &pb.PubKey{PublicKey: pubDER}}
	case *pb.ReqEnvelope_ReqAuth:
		resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
	case *pb.ReqEnvelope_ReqHasFile:
		d.events = append(d.events, "has")
		if d.onHas != nil {
			d.onHas()
		}
		resp.Payload = &pb.RespEnvelope_RespFileExists{RespFileExists: &pb.FileExists{Exists: false}}
	case *pb.ReqEnvelope_ReqBeginUpload:
		bu := p.ReqBeginUpload
		d.begins = append(d.begins, bu)
		key := uploadKey{bu.Path, bu.Size, bu.Sha256}
		if id, ok := d.resumable[key]; ok && bu.Sha256 != "" && !d.forgetful {
			received := int64(d.pending[id].Len())
			d.beginAnswers = append(d.beginAnswers, received)
			resp.Payload = &pb.RespEnvelope_RespUploadStarted{RespUploadStarted: &pb.UploadStarted{UploadId: id, Received: received}}

			break
		}
		id := fmt.Sprintf("u%d", len(d.pending)+1)
		d.open++
		d.maxOpen = max(d.maxOpen, d.open)
		d.pending[id] = &bytes.Buffer{}
		d.paths[id] = bu.Path
		if d.resumable == nil {
			d.resumable = map[uploadKey]string{}
		}
		d.resumable[key] = id
		d.beginAnswers = append(d.beginAnswers, 0)
		resp.Payload = &pb.RespEnvelope_RespUploadStarted{RespUploadStarted: &pb.UploadStarted{UploadId: id}}
	case *pb.ReqEnvelope_ReqUploadChunk:
		d.chunkOffsets = append(d.chunkOffsets, p.ReqUploadChunk.Offset)
		b := d.pending[p.ReqUploadChunk.UploadId]
		if b == nil {
			resp.Error, resp.ErrorMessage = true, "no such upload"
			break
		}
		if int64(b.Len()) != p.ReqUploadChunk.Offset {
			resp.Error, resp.ErrorMessage = true, "out of order"
			break
		}
		b.Write(p.ReqUploadChunk.Data)
		d.chunks++
		resp.Payload = &pb.RespEnvelope_RespUploadProgress{RespUploadProgress: &pb.UploadProgress{Received: int64(b.Len())}}
	case *pb.ReqEnvelope_ReqFinishUpload:
		d.open--
		id := p.ReqFinishUpload.UploadId
		sum := sha256.Sum256(d.pending[id].Bytes())
		if hex.EncodeToString(sum[:]) != p.ReqFinishUpload.Sha256 {
			resp.Error, resp.ErrorMessage = true, "hash mismatch"
			break
		}
		for k, v := range d.resumable {
			if v == id {
				delete(d.resumable, k)
			}
		}
		d.files[d.paths[id]] = d.pending[id].Bytes()
		resp.Payload = &pb.RespEnvelope_RespFile{RespFile: &pb.File{Path: d.paths[id]}}
	case *pb.ReqEnvelope_ReqListFiles:
		d.events = append(d.events, "list")
		files := d.list
		if d.listStored {
			files = nil
			for path, data := range d.files {
				if strings.HasPrefix(path, p.ReqListFiles.Path) {
					sum := sha256.Sum256(data)
					// Under an upload-only folder, as the device lists it:
					// the one that refuses deletes.
					files = append(files, &pb.File{Path: path, Hash: hex.EncodeToString(sum[:]), Size64: int64(len(data)), Modified: d.modTime,
						UploadOnly: d.delAnswer == "upload_only"})
				}
			}
		}
		resp.Payload = &pb.RespEnvelope_RespListOfFiles{RespListOfFiles: &pb.ListOfFiles{Files: files}}
		if !p.ReqListFiles.Recursive && !strings.HasSuffix(p.ReqListFiles.Path, "/") {
			d.pointLists++
		}
		if p.ReqListFiles.Recursive && d.afterList != nil {
			hook := d.afterList
			d.afterList = nil
			hook()
		}
	case *pb.ReqEnvelope_ReqSetOutOfImages:
		d.events = append(d.events, "images")
		d.imagesSet = append(d.imagesSet, p.ReqSetOutOfImages)
		if d.onSetImages != nil {
			d.onSetImages()
		}
		switch d.imagesAnswer {
		case "":
			folder := strings.TrimSuffix(p.ReqSetOutOfImages.Path, "/") + "/"
			kept := d.keptOut[:0:0]
			for _, k := range d.keptOut {
				if k != folder {
					kept = append(kept, k)
				}
			}
			if p.ReqSetOutOfImages.OutOfImages {
				kept = append(kept, folder)
			}
			d.keptOut = kept
			resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
		case "unknown_payload":
			resp.Error, resp.ErrorCode, resp.ErrorMessage = true, "unknown_payload", "This device does not understand that request"
		case "out_of_images_by_parent":
			resp.Error, resp.ErrorCode, resp.ErrorMessage = true, "out_of_images_by_parent", "Trip is inside /Photos, which is kept out of Images - show /Photos in Images to show Trip"
		default:
			resp.Error, resp.ErrorMessage = true, "error updating the folder: "+d.imagesAnswer
		}
	case *pb.ReqEnvelope_ReqListOutOfImages:
		if d.imagesAnswer == "unknown_payload" {
			resp.Error, resp.ErrorCode, resp.ErrorMessage = true, "unknown_payload", "This device does not understand that request"
			break
		}
		resp.Payload = &pb.RespEnvelope_RespOutOfImagesFolders{RespOutOfImagesFolders: &pb.OutOfImagesFolders{Paths: append([]string(nil), d.keptOut...)}}
	case *pb.ReqEnvelope_ReqSetUploadOnly:
		d.events = append(d.events, "upload_only")
		d.uploadOnly++
		d.uploadOnlySet = append(d.uploadOnlySet, p.ReqSetUploadOnly)
		if d.refuseUploadOnly {
			resp.Error, resp.ErrorCode, resp.ErrorMessage = true, "unknown_payload", "unknown request"
			break
		}
		switch d.uploadOnlyAnswer {
		case "":
			resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
		case "unknown_payload":
			resp.Error, resp.ErrorCode, resp.ErrorMessage = true, "unknown_payload", "This device does not understand that request"
		case "locked_by_parent":
			resp.Error, resp.ErrorCode, resp.ErrorMessage = true, "locked_by_parent", "Docs is inside the upload-only folder /Archive - unlock /Archive to unlock it"
		default:
			resp.Error, resp.ErrorMessage = true, "error updating the folder: "+d.uploadOnlyAnswer
		}
	case *pb.ReqEnvelope_ReqDelFile:
		d.deletes = append(d.deletes, p.ReqDelFile.Path)
		if d.delAnswer == "upload_only" {
			resp.Error, resp.ErrorCode, resp.ErrorMessage = true, "upload_only", "this folder is upload only: nothing in it can be deleted"
			break
		}
		delete(d.files, p.ReqDelFile.Path)
		resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
	case *pb.ReqEnvelope_ReqGetFile:
		d.reads++
		resp.Error, resp.ErrorMessage = true, "the content is missing on this device"
	case *pb.ReqEnvelope_ReqReadFile:
		d.reads++
		if d.onRead != nil {
			d.onRead()
		}
		data, ok := d.files[p.ReqReadFile.Path]
		if !ok {
			resp.Error, resp.ErrorMessage = true, "no such file"
			break
		}
		sum := sha256.Sum256(data)
		off := p.ReqReadFile.Offset
		end := min(off+int64(p.ReqReadFile.Length), int64(len(data)))
		resp.Payload = &pb.RespEnvelope_RespFileChunk{RespFileChunk: &pb.FileChunk{
			Path: p.ReqReadFile.Path, Hash: hex.EncodeToString(sum[:]), Size: int64(len(data)), Offset: off, Data: data[off:end], Modified: d.modTime,
		}}
	default:
		resp.Error, resp.ErrorMessage = true, "unexpected request"
	}
	return resp
}

func connectedEngine(t *testing.T, d *fakeDevice) *Engine {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		down := d.down
		d.conns++
		d.mu.Unlock()
		if down {
			http.Error(w, "down", http.StatusServiceUnavailable)

			return
		}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		c.SetReadLimit(64 << 20)
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			req := &pb.ReqEnvelope{}
			if proto.Unmarshal(data, req) != nil {
				return
			}
			resp := d.handle(req, pubDER)
			if resp == nil {
				return // dropped (dropOn, dropAfter)
			}
			b, _ := proto.Marshal(resp)
			if c.WriteMessage(websocket.BinaryMessage, b) != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)

	e := &Engine{ws: wsclient.New()}
	connected := make(chan struct{}, 1)
	e.ws.OnConnect = func() {
		select {
		case connected <- struct{}{}:
		default: // signed in again after a drop
		}
	}
	e.ws.Configure("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", "test", "secret")
	e.ws.Connect()
	t.Cleanup(e.ws.Disconnect)
	select {
	case <-connected:
	case <-time.After(10 * time.Second):
		t.Fatal("never connected")
	}
	return e
}

// Issue #168: a file goes up in pieces and comes back byte for byte - the
// original, with the hash the listing has.
func TestChunkedUploadAndDownloadRoundTrip(t *testing.T) {
	d := &fakeDevice{files: map[string][]byte{}, pending: map[string]*bytes.Buffer{}, paths: map[string]string{}}
	e := connectedEngine(t, d)

	dir := t.TempDir()
	src := filepath.Join(dir, "big.heic")
	content := make([]byte, 2*cChunk+12345) // three pieces
	rand.Read(content)
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	fi, _ := os.Stat(src)

	if err := e.upload(src, "/pc/big.heic", hash, fi); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if d.chunks != 3 {
		t.Errorf("sent in %d pieces, want 3", d.chunks)
	}
	dest := filepath.Join(dir, "back", "big.heic")
	if err := e.download("/pc/big.heic", dest, hash); err != nil {
		t.Fatalf("download: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, content) {
		t.Fatal("downloaded bytes differ from the original")
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(dest), "*.otc-part")); len(left) > 0 {
		t.Errorf("temporary file left behind: %v", left)
	}
}

// A file whose content isn't what the listing said is never written.
func TestChunkedDownloadRefusesAnotherHash(t *testing.T) {
	d := &fakeDevice{files: map[string][]byte{"/pc/a.txt": []byte("hello")}, pending: map[string]*bytes.Buffer{}, paths: map[string]string{}}
	e := connectedEngine(t, d)
	dest := filepath.Join(t.TempDir(), "a.txt")
	if err := e.download("/pc/a.txt", dest, strings.Repeat("0", 64)); err == nil {
		t.Fatal("wrote a file with the wrong hash")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("the file was written")
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(dest), "*.otc-part")); len(left) > 0 {
		t.Errorf("temporary file left behind: %v", left)
	}
}

// A 0-byte file is one request and an empty file.
func TestChunkedDownloadEmptyFile(t *testing.T) {
	d := &fakeDevice{files: map[string][]byte{"/pc/e": {}}, pending: map[string]*bytes.Buffer{}, paths: map[string]string{}}
	e := connectedEngine(t, d)
	sum := sha256.Sum256(nil)
	dest := filepath.Join(t.TempDir(), "e")
	if err := e.download("/pc/e", dest, hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("download: %v", err)
	}
	if fi, err := os.Stat(dest); err != nil || fi.Size() != 0 {
		t.Fatalf("empty file: %v %v", fi, err)
	}
}
