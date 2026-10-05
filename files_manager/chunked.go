// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"sync"
	"time"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"github.com/gabriel-vasile/mimetype"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Chunked transfers (security advisory: memory exhaustion): a file moves
// in pieces of at most MaxChunk, both ways, so neither the device nor the
// bridge ever holds more than a few MB of it per request. Uploads are
// encrypted segment by segment as they arrive (blobstore), hashed on the
// way, and become the file only when complete; reads decrypt only the
// segments a range covers.

const (
	// MaxChunk is the most one upload chunk or read returns.
	MaxChunk = 4 << 20
	// cUploadIdle: an upload nobody has sent to for this long is dropped.
	cUploadIdle = 30 * time.Minute
	// cHeadBytes: kept from the start of an upload to tell its type.
	cHeadBytes = 64 << 10
)

var (
	ErrUnknownUpload = errors.New("unknown or expired upload")
	ErrOutOfOrder    = errors.New("upload chunk out of order")
)

type chunkedUpload struct {
	mu  sync.Mutex
	ses *session.Session
	// owner is the connection that began the upload (see AbortUploadsOf).
	owner     any
	path      string
	size      int64
	created   *timestamppb.Timestamp
	modified  *timestamppb.Timestamp
	force     bool
	cloudID   string
	w         *blobstore.Writer
	hasher    hash.Hash
	head      []byte
	received  int64
	lastTouch time.Time
}

type uploads struct {
	mu sync.Mutex
	by map[string]*chunkedUpload
}

var pendingUploads = &uploads{by: map[string]*chunkedUpload{}}

func init() {
	go func() {
		for range time.Tick(time.Minute) {
			pendingUploads.sweep()
		}
	}()
}

func (u *uploads) sweep() {
	u.drop(func(up *chunkedUpload) bool { return time.Since(up.lastTouch) > cUploadIdle })
}

// drop aborts and forgets every upload match picks (called with the
// upload's lock held).
func (u *uploads) drop(match func(up *chunkedUpload) bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for id, up := range u.by {
		up.mu.Lock()
		gone := match(up)
		if gone {
			up.w.Abort()
		}
		up.mu.Unlock()
		if gone {
			delete(u.by, id)
		}
	}
}

// AbortUploadsOf drops the uploads owner (the connection given to
// BeginUpload) left unfinished, once it has closed: no client resumes an
// upload on another connection - they all begin again from the start - so
// each dropped connection used to leave an open file, a segment buffer and
// a partial temp file (GBs, for a video) for cUploadIdle. Call it after the
// connection's requests have all returned, so a FinishUpload already
// running still commits.
func (mg *Manager) AbortUploadsOf(owner any) {
	if owner == nil {
		return
	}
	pendingUploads.drop(func(up *chunkedUpload) bool { return up.owner == owner })
}

// BeginUpload starts a chunked upload of size bytes to path and returns
// its id; the content then comes with UploadChunk, in order, and
// FinishUpload makes it the file. owner (comparable: the websocket layer
// passes its connection) is what AbortUploadsOf matches.
func (mg *Manager) BeginUpload(ses *session.Session, path string, size int64, forceOverride bool, created, modified *timestamppb.Timestamp, cloudID string, owner any) (string, error) {
	if size < 0 {
		return "", errors.New("bad size")
	}
	if path == "" {
		return "", errors.New("no path")
	}
	w, err := blobstore.Create(fmt.Sprintf("%s/.pending", cfg.GetStr("otc", "storage-path")), ses)
	if err != nil {
		return "", err
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		w.Abort()
		return "", err
	}
	id := hex.EncodeToString(raw)
	pendingUploads.mu.Lock()
	pendingUploads.by[id] = &chunkedUpload{
		ses: ses, owner: owner, path: path, size: size, created: created, modified: modified,
		force: forceOverride, cloudID: cloudID, w: w, hasher: sha256.New(), lastTouch: time.Now(),
	}
	pendingUploads.mu.Unlock()
	return id, nil
}

func (u *uploads) get(ses *session.Session, id string) (*chunkedUpload, error) {
	u.mu.Lock()
	up, ok := u.by[id]
	u.mu.Unlock()
	if !ok || up.ses != ses {
		return nil, ErrUnknownUpload
	}
	return up, nil
}

// UploadChunk adds data at offset (the next byte expected: chunks come in
// order, so a client resends from where the device says it is).
func (mg *Manager) UploadChunk(ses *session.Session, id string, offset int64, data []byte) (received int64, err error) {
	if len(data) > MaxChunk {
		return 0, fmt.Errorf("chunk over %d bytes", MaxChunk)
	}
	up, err := pendingUploads.get(ses, id)
	if err != nil {
		return 0, err
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	up.lastTouch = time.Now()
	if offset != up.received {
		return up.received, ErrOutOfOrder
	}
	if up.received+int64(len(data)) > up.size {
		return up.received, errors.New("more data than the upload's size")
	}
	if _, err := up.w.Write(data); err != nil {
		return up.received, err
	}
	up.hasher.Write(data)
	if len(up.head) < cHeadBytes {
		up.head = append(up.head, data[:min(len(data), cHeadBytes-len(up.head))]...)
	}
	up.received += int64(len(data))
	return up.received, nil
}

// FinishUpload checks the upload is complete and its SHA-256 is what the
// client computed, then makes it the file at its path (a new version in
// an upload-only folder, as UploadFile) and processes it in the background.
func (mg *Manager) FinishUpload(ses *session.Session, id, sha string) (*pb.File, error) {
	up, err := pendingUploads.get(ses, id)
	if err != nil {
		return nil, err
	}
	pendingUploads.mu.Lock()
	delete(pendingUploads.by, id)
	pendingUploads.mu.Unlock()

	up.mu.Lock()
	defer up.mu.Unlock()
	if up.received != up.size {
		up.w.Abort()
		return nil, fmt.Errorf("upload incomplete: %d of %d bytes", up.received, up.size)
	}
	hash := hex.EncodeToString(up.hasher.Sum(nil))
	if sha != "" && sha != hash {
		up.w.Abort()
		return nil, errors.New("the upload's content doesn't match its hash")
	}
	if err := up.w.Seal(); err != nil {
		up.w.Abort()
		return nil, err
	}

	// Into place before the row exists, as UploadFile: a restart between
	// the two then leaves nothing, never a listed file without content.
	target := blobPath(hash)
	unlock := lockBlob(hash)
	err = up.w.CommitAs(target)
	unlock()
	if err != nil {
		mg.alert("could not be saved to disk", up.path, err)
		return nil, err
	}
	known := mg.contentKnown(hash)
	file, write, err := mg.registerUpload(ses, up.path, hash, mimetype.Detect(up.head).String(), up.size, up.force, up.created, up.modified, up.cloudID)
	if err != nil {
		mg.removeBlobIfUnused(hash)
		return file, err
	}
	if !write {
		return file, nil
	}
	if !mg.hasBlob(hash) {
		// Taken as unused by a DelFile between the commit and the row.
		err = errors.New("the upload's content was removed before it was recorded - send it again")
		mg.alert("could not be saved to disk", file.Path, err)
		return nil, err
	}
	// As UploadFile: content already processed isn't processed again.
	if known && mg.hasThumbnail(hash) {
		return file, nil
	}

	mg.enqueueMedia(ses, file, target)
	return file, nil
}

// VideoSourceFunc makes a stored video readable by ffmpeg: an address it
// can read with range requests (the device's loopback stream, which
// decrypts only the segments asked for), and done, to call once finished.
type VideoSourceFunc func(ses *session.Session, file *pb.File) (src string, done func(), err error)

// SetVideoSource is set once at start by the websocket layer, which owns
// the stream server.
func (mg *Manager) SetVideoSource(fn VideoSourceFunc) { mg.videoSourceFn = fn }

func (mg *Manager) videoSource(ses *session.Session, file *pb.File) (string, func(), error) {
	if mg.videoSourceFn == nil {
		return "", func() {}, errors.New("no way to stream this video to ffmpeg")
	}
	return mg.videoSourceFn(ses, file)
}

// ReadFile returns up to length bytes (at most MaxChunk) of the file at
// path from offset - its original bytes, never converted - decrypting only
// the segments they cover, plus the file's row (size, mime, dates).
// versionHash reads an older version instead.
func (mg *Manager) ReadFile(ses *session.Session, path, versionHash string, offset int64, length int) (*pb.File, []byte, int64, error) {
	var file *pb.File
	var err error
	if versionHash != "" {
		file, err = mg.dao.GetFileVersion(path, versionHash)
	} else {
		file, err = mg.dao.GetFileByPath(path)
	}
	if err != nil {
		return nil, nil, 0, err
	}
	blob, err := blobstore.Open(blobPath(file.Hash), ses)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Alerted once: a client asking again for content the device
			// already knows is gone (a sync client retrying every pass)
			// raised a new alert every five minutes, all day.
			if !missingBlobs.has(file.Hash) {
				mg.alert("could not be read", path, err)
			}
			missingBlobs.set(file.Hash, true)
		} else {
			mg.alert("could not be read", path, err)
		}
		return nil, nil, 0, fmt.Errorf("the content of %s is missing or unreadable on this device", path)
	}
	defer blob.Close()
	if offset < 0 || offset > blob.Size() {
		return nil, nil, 0, fmt.Errorf("offset %d outside the file", offset)
	}
	if length <= 0 || length > MaxChunk {
		length = MaxChunk
	}
	if rest := blob.Size() - offset; int64(length) > rest {
		length = int(rest)
	}
	buf := make([]byte, length)
	n, err := blob.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, nil, 0, err
	}
	log.Debug("read", n, "bytes of", path, "at", offset)
	return file, buf[:n], blob.Size(), nil
}
