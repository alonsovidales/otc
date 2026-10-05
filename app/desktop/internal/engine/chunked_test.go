// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
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
	reads   int        // ReadFile/GetFile requests
	deletes []string   // DelFile paths
	onRead  func()     // runs on each ReadFile, as if the user did something meanwhile
}

func (d *fakeDevice) handle(req *pb.ReqEnvelope, pubDER []byte) *pb.RespEnvelope {
	d.mu.Lock()
	defer d.mu.Unlock()
	resp := &pb.RespEnvelope{Id: req.Id}
	switch p := req.Payload.(type) {
	case *pb.ReqEnvelope_ReqGetPubKey:
		resp.Payload = &pb.RespEnvelope_RespPubKey{RespPubKey: &pb.PubKey{PublicKey: pubDER}}
	case *pb.ReqEnvelope_ReqAuth:
		resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
	case *pb.ReqEnvelope_ReqHasFile:
		resp.Payload = &pb.RespEnvelope_RespFileExists{RespFileExists: &pb.FileExists{Exists: false}}
	case *pb.ReqEnvelope_ReqBeginUpload:
		id := "u1"
		d.pending[id] = &bytes.Buffer{}
		d.paths[id] = p.ReqBeginUpload.Path
		resp.Payload = &pb.RespEnvelope_RespUploadStarted{RespUploadStarted: &pb.UploadStarted{UploadId: id}}
	case *pb.ReqEnvelope_ReqUploadChunk:
		b := d.pending[p.ReqUploadChunk.UploadId]
		if int64(b.Len()) != p.ReqUploadChunk.Offset {
			resp.Error, resp.ErrorMessage = true, "out of order"
			break
		}
		b.Write(p.ReqUploadChunk.Data)
		d.chunks++
		resp.Payload = &pb.RespEnvelope_RespUploadProgress{RespUploadProgress: &pb.UploadProgress{Received: int64(b.Len())}}
	case *pb.ReqEnvelope_ReqFinishUpload:
		id := p.ReqFinishUpload.UploadId
		sum := sha256.Sum256(d.pending[id].Bytes())
		if hex.EncodeToString(sum[:]) != p.ReqFinishUpload.Sha256 {
			resp.Error, resp.ErrorMessage = true, "hash mismatch"
			break
		}
		d.files[d.paths[id]] = d.pending[id].Bytes()
		resp.Payload = &pb.RespEnvelope_RespFile{RespFile: &pb.File{Path: d.paths[id]}}
	case *pb.ReqEnvelope_ReqListFiles:
		resp.Payload = &pb.RespEnvelope_RespListOfFiles{RespListOfFiles: &pb.ListOfFiles{Files: d.list}}
	case *pb.ReqEnvelope_ReqDelFile:
		d.deletes = append(d.deletes, p.ReqDelFile.Path)
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
			Path: p.ReqReadFile.Path, Hash: hex.EncodeToString(sum[:]), Size: int64(len(data)), Offset: off, Data: data[off:end],
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
			b, _ := proto.Marshal(d.handle(req, pubDER))
			if c.WriteMessage(websocket.BinaryMessage, b) != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)

	e := &Engine{ws: wsclient.New()}
	connected := make(chan struct{}, 1)
	e.ws.OnConnect = func() { connected <- struct{}{} }
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
	if _, err := os.Stat(dest + ".otc-part"); !os.IsNotExist(err) {
		t.Error("temporary file left behind")
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
	if _, err := os.Stat(dest + ".otc-part"); !os.IsNotExist(err) {
		t.Error("temporary file left behind")
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
