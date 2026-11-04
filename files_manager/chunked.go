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
	"strings"
	"sync"
	"time"

	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
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
//
// Resuming (issue #205): an upload named by its content (BeginUpload's
// sha256, with its path and size: uploadKey) outlives the connection that
// began it. When that connection closes the upload is kept - its temp
// file, its open segment and its hash so far, all in this process - and
// the next BeginUpload of the same key, on any connection of this
// instance (each user's instance is its own process, under one data key),
// goes on with it from what was received. Only the connection that began
// it last drives it (owner): a BeginUpload of a key another connection
// still holds takes it over, and that connection's further UploadChunk and
// FinishUpload are refused as for an unknown upload - the device can't
// tell a phone's half-open socket (Wi-Fi to mobile data) from a live one
// for minutes, and the client beginning again is nearly always the one
// that lost its connection. Offsets keep the content in order whoever
// sends it, and FinishUpload's SHA-256 check covers all of it. Nothing
// survives a restart: the state is memory, and the next start's
// sweepOrphanedStorage removes the temp file.

const (
	// MaxChunk is the most one upload chunk or read returns.
	MaxChunk = 4 << 20
	// cMaxUploadSize is the largest upload BeginUpload takes: a pebibyte,
	// past any disk a device has. A size beyond is a client's mistake, and
	// one near MaxInt64 overflowed the free-space arithmetic (sealedSize).
	cMaxUploadSize = 1 << 50
	// cUploadIdle: an upload nobody has sent to for this long is dropped,
	// whether a connection still drives it or it is kept for a resume.
	cUploadIdle = 30 * time.Minute
	// cHeadBytes: kept from the start of an upload to tell its type.
	cHeadBytes = 64 << 10

	// The limits on unfinished uploads, per instance. Each holds an open
	// temp file and, once content arrives, the segment being sealed (1
	// MiB, segcrypt.SegmentSize) and its head (cHeadBytes) in memory; the
	// free-space checks count what it still has to write.
	//
	// cMaxUploads is all of them, driven or kept: ~34 MiB of buffers at
	// most. The phones send 3 at a time and the desktop apps 2, so a
	// user's clients together stay well under it. Past it a BeginUpload
	// drops the kept upload least recently sent to, or is refused
	// (ErrTooManyUploads) when every one is being driven.
	cMaxUploads = 32
	// cMaxKeptUploads is how many of them may wait for a resume with no
	// connection driving them (~17 MiB at most): past it, the least
	// recently sent to go as their connection closes. Kept uploads also
	// give way to a new write the disk has no room for (uploads.roomFor),
	// oldest first: what they still have to write is only reserved while
	// nothing else needs it.
	cMaxKeptUploads = 16
)

var (
	ErrUnknownUpload  = devtext.New(i18n.DevFilesUploadUnknown())
	ErrOutOfOrder     = devtext.New(i18n.DevFilesUploadOutOfOrder())
	ErrTooManyUploads = devtext.New(i18n.DevFilesUploadTooMany())
)

// uploadKey names an upload's content for a resume. sha256 "" never
// resumes: such an upload goes with its connection, as before issue #205.
type uploadKey struct {
	path   string
	size   int64
	sha256 string
}

// resumeHash is BeginUpload's sha256 as a key: lowercase hex of a SHA-256,
// or "" (no key) for anything else.
func resumeHash(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != sha256.Size*2 {
		return ""
	}
	if _, err := hex.DecodeString(s); err != nil {
		return ""
	}
	return s
}

type chunkedUpload struct {
	mu  sync.Mutex
	id  string
	key uploadKey
	ses *session.Session
	// owner is the connection driving the upload: the one whose
	// BeginUpload began or resumed it last (see ReleaseUploadsOf).
	owner any
	// kept: its connection closed, and it waits for a BeginUpload of its
	// key (owner is nil meanwhile).
	kept bool
	// gone: finished, dropped or failed - nothing more goes into it. Set
	// under mu before it leaves the maps.
	gone      bool
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

// size is what the upload will hold once complete.
func (up *chunkedUpload) size() int64 { return up.key.size }

// heldBy is whether owner (with ses) drives the upload now. Called with
// up.mu held.
func (up *chunkedUpload) heldBy(ses *session.Session, owner any) bool {
	return !up.gone && !up.kept && up.owner == owner && up.ses == ses
}

// abort discards what was received. Called with up.mu held.
func (up *chunkedUpload) abort() {
	up.gone = true
	up.w.Abort()
}

// uploads are this instance's unfinished uploads. Lock order: mu, then an
// upload's mu - never the other way.
type uploads struct {
	// begin makes each BeginUpload one step: the resume, or the room and
	// the slot a new upload takes and the upload itself.
	begin sync.Mutex
	mu    sync.Mutex
	by    map[string]*chunkedUpload
	byKey map[uploadKey]*chunkedUpload
}

var pendingUploads = newUploads()

func newUploads() *uploads {
	return &uploads{by: map[string]*chunkedUpload{}, byKey: map[uploadKey]*chunkedUpload{}}
}

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

// forgetLocked takes up out of the maps. Called with u.mu held.
func (u *uploads) forgetLocked(up *chunkedUpload) {
	if u.by[up.id] == up {
		delete(u.by, up.id)
	}
	if u.byKey[up.key] == up {
		delete(u.byKey, up.key)
	}
}

func (u *uploads) forget(up *chunkedUpload) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.forgetLocked(up)
}

// drop aborts and forgets every upload match picks (called with the
// upload's lock held).
func (u *uploads) drop(match func(up *chunkedUpload) bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, up := range u.by {
		up.mu.Lock()
		gone := !up.gone && match(up)
		if gone {
			up.abort()
		}
		up.mu.Unlock()
		if gone {
			u.forgetLocked(up)
		}
	}
}

// keptLocked lists the kept uploads, least recently sent to first.
// Called with u.mu held.
func (u *uploads) keptLocked() []*chunkedUpload {
	var kept []*chunkedUpload
	var touched []time.Time
	for _, up := range u.by {
		up.mu.Lock()
		if up.kept && !up.gone {
			kept, touched = append(kept, up), append(touched, up.lastTouch)
		}
		up.mu.Unlock()
	}
	// Insertion sort: there are at most cMaxUploads.
	for i := 1; i < len(kept); i++ {
		for j := i; j > 0 && touched[j].Before(touched[j-1]); j-- {
			kept[j], kept[j-1] = kept[j-1], kept[j]
			touched[j], touched[j-1] = touched[j-1], touched[j]
		}
	}
	return kept
}

// dropOldestKeptLocked drops the kept upload least recently sent to, and
// reports whether there was one. Called with u.mu held.
func (u *uploads) dropOldestKeptLocked() bool {
	for _, up := range u.keptLocked() {
		up.mu.Lock()
		ok := up.kept && !up.gone // a BeginUpload may have taken it back
		if ok {
			up.abort()
		}
		up.mu.Unlock()
		if ok {
			u.forgetLocked(up)
			log.Info("dropped an unfinished upload kept for a resume:", up.received>>20, "of", up.key.size>>20, "MB received")
			return true
		}
	}
	return false
}

func (u *uploads) dropOldestKept() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.dropOldestKeptLocked()
}

// roomFor checks the disk under dir can take a write of need bytes on
// top of what the uploads under way still have to write (checkFreeSpace).
// When it can't, the kept uploads give way, least recently sent to first:
// one no connection drives never stops a write that is happening.
func (u *uploads) roomFor(dir string, need int64) error {
	for {
		err := checkFreeSpace(dir, addSizes(need, u.remaining()))
		if err == nil || !IsStorageFull(err) || !u.dropOldestKept() {
			return err
		}
	}
}

// release lets owner's uploads go once its connection closed: kept for a
// resume when they have a key, dropped when they don't (nobody can name
// them again). Past cMaxKeptUploads the oldest kept ones go.
func (u *uploads) release(owner any) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, up := range u.by {
		up.mu.Lock()
		mine := !up.gone && !up.kept && up.owner == owner
		dropped := false
		if mine && up.key.sha256 == "" {
			up.abort()
			dropped = true
		} else if mine {
			up.kept, up.owner = true, nil
		}
		up.mu.Unlock()
		if dropped {
			u.forgetLocked(up)
		}
	}
	for len(u.keptLocked()) > cMaxKeptUploads {
		if !u.dropOldestKeptLocked() {
			break
		}
	}
}

// ReleaseUploadsOf lets go of the uploads owner (the connection given to
// BeginUpload) still drove when it closed. One with a resume key is kept
// for a BeginUpload of the same key from another connection - at once,
// not after a timeout - for as long as cUploadIdle allows; one without is
// dropped, its temp file with it (it used to stay, GBs for a video, for
// cUploadIdle). Call it after the connection's requests have all
// returned, so a FinishUpload already running still commits.
func (mg *Manager) ReleaseUploadsOf(owner any) {
	if owner == nil {
		return
	}
	pendingUploads.release(owner)
}

// resume hands the upload of key, if this instance holds it, to owner:
// its id and what it holds. The latest BeginUpload's metadata wins.
func (u *uploads) resume(key uploadKey, ses *session.Session, owner any, forceOverride bool, created, modified *timestamppb.Timestamp, cloudID string) (string, int64, bool) {
	u.mu.Lock()
	up := u.byKey[key]
	u.mu.Unlock()
	if up == nil {
		return "", 0, false
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if up.gone {
		return "", 0, false // finished or dropped meanwhile: a new one
	}
	if !up.kept && up.owner != owner {
		log.Info("an upload went on from another connection at", up.received>>20, "of", up.size()>>20, "MB")
	}
	up.ses, up.owner, up.kept = ses, owner, false
	up.force, up.created, up.modified, up.cloudID = forceOverride, created, modified, cloudID
	up.lastTouch = time.Now()
	return up.id, up.received, true
}

// slot makes room for one more upload under cMaxUploads, dropping the
// oldest kept one if needed. Called with u.begin held, so nothing else
// adds one before the caller does.
func (u *uploads) slot() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.by) < cMaxUploads || u.dropOldestKeptLocked() {
		return nil
	}
	return ErrTooManyUploads
}

// BeginUpload starts a chunked upload of size bytes to path and returns
// its id and what of it the device already holds (received: where the
// next UploadChunk goes); the content then comes with UploadChunk, in
// order, and FinishUpload makes it the file. sha256 (hex) names the
// content: an unfinished upload of the same path, size and sha256 goes on
// where it was, whichever connection began it (see the top of this file);
// "" always begins a new one. owner (comparable: the websocket layer
// passes its connection) is the connection driving it, which UploadChunk
// and FinishUpload must come from and ReleaseUploadsOf matches.
func (mg *Manager) BeginUpload(ses *session.Session, path string, size int64, forceOverride bool, created, modified *timestamppb.Timestamp, cloudID, sha256Hex string, owner any) (id string, received int64, err error) {
	if size < 0 || size > cMaxUploadSize {
		return "", 0, devtext.New(i18n.DevFilesBadSize())
	}
	noteUploadActivity() // the thumbnails pass waits for a sync to stop
	if path == "" {
		return "", 0, devtext.New(i18n.DevFilesNoPath())
	}
	// Refused before any content comes (paths.go), as FinishUpload would
	// once all of it had: a sync client sent it all again on every pass.
	if err := mg.checkNewPath(path); err != nil {
		return "", 0, err
	}
	if err := mg.spacesNameTaken(path, forceOverride); err != nil {
		return "", 0, err
	}
	u := pendingUploads
	u.begin.Lock()
	defer u.begin.Unlock()
	key := uploadKey{path: path, size: size, sha256: resumeHash(sha256Hex)}
	if key.sha256 != "" {
		if id, received, ok := u.resume(key, ses, owner, forceOverride, created, modified, cloudID); ok {
			return id, received, nil
		}
	}
	// Room for all of it, and for what the uploads already under way still
	// have to write: a sync starts several at once.
	storage := cfg.GetStr("otc", "storage-path")
	if err := u.roomFor(storage, sealedSize(size)); err != nil {
		return "", 0, err
	}
	if err := u.slot(); err != nil {
		return "", 0, err
	}
	w, err := blobstore.Create(fmt.Sprintf("%s/.pending", storage), ses)
	if err != nil {
		return "", 0, err
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		w.Abort()
		return "", 0, err
	}
	id = hex.EncodeToString(raw)
	up := &chunkedUpload{
		id: id, key: key, ses: ses, owner: owner, created: created, modified: modified,
		force: forceOverride, cloudID: cloudID, w: w, hasher: sha256.New(), lastTouch: time.Now(),
	}
	u.mu.Lock()
	u.by[id] = up
	if key.sha256 != "" {
		u.byKey[key] = up
	}
	u.mu.Unlock()
	return id, 0, nil
}

// remaining is what the uploads under way - kept ones too - still have to
// write, sealed.
func (u *uploads) remaining() int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	var n int64
	for _, up := range u.by {
		up.mu.Lock()
		if !up.gone {
			n = addSizes(n, sealedSize(up.size()-up.received))
		}
		up.mu.Unlock()
	}
	return n
}

func (u *uploads) get(id string) (*chunkedUpload, error) {
	u.mu.Lock()
	up, ok := u.by[id]
	u.mu.Unlock()
	if !ok {
		return nil, ErrUnknownUpload
	}
	return up, nil
}

// UploadChunk adds data at offset (the next byte expected: chunks come in
// order, so a client resends from where the device says it is). Only the
// connection driving the upload (owner, with its session) may send it.
func (mg *Manager) UploadChunk(ses *session.Session, id string, offset int64, data []byte, owner any) (received int64, err error) {
	if len(data) > MaxChunk {
		return 0, devtext.WithEnglish(fmt.Sprintf("chunk over %d bytes", MaxChunk), i18n.DevFilesChunkTooBig(MaxChunk)) // i18n-ignore: the English as it always read; the catalog has the other languages
	}
	up, err := pendingUploads.get(id)
	if err != nil {
		return 0, err
	}
	noteUploadActivity()
	up.mu.Lock()
	received, failed, err := up.add(ses, owner, offset, data)
	up.mu.Unlock()
	if failed {
		pendingUploads.forget(up)
	}
	return received, err
}

// add is UploadChunk with up.mu held; failed says the upload had to be
// dropped (a write that failed leaves the sealed file and the hash out of
// step with what was received: it can't go on, nor be resumed).
func (up *chunkedUpload) add(ses *session.Session, owner any, offset int64, data []byte) (received int64, failed bool, err error) {
	if !up.heldBy(ses, owner) {
		return 0, false, ErrUnknownUpload
	}
	up.lastTouch = time.Now()
	if offset != up.received {
		return up.received, false, ErrOutOfOrder
	}
	if up.received+int64(len(data)) > up.size() {
		return up.received, false, devtext.New(i18n.DevFilesUploadTooMuch())
	}
	if _, err := up.w.Write(data); err != nil {
		up.abort()
		return up.received, true, err
	}
	up.hasher.Write(data)
	if len(up.head) < cHeadBytes {
		up.head = append(up.head, data[:min(len(data), cHeadBytes-len(up.head))]...)
	}
	up.received += int64(len(data))
	return up.received, false, nil
}

// take hands the upload id to its FinishUpload: out of the maps, and gone
// for everyone else.
func (u *uploads) take(ses *session.Session, id string, owner any) (*chunkedUpload, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	up, ok := u.by[id]
	if !ok {
		return nil, ErrUnknownUpload
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if !up.heldBy(ses, owner) {
		return nil, ErrUnknownUpload
	}
	up.gone = true
	u.forgetLocked(up)
	return up, nil
}

// FinishUpload checks the upload is complete and its SHA-256 is what the
// client computed - and what its BeginUpload named, when it did - then
// makes it the file at its path (a new version in an upload-only folder,
// as UploadFile) and processes it in the background.
func (mg *Manager) FinishUpload(ses *session.Session, id, sha string, owner any) (*pb.File, error) {
	up, err := pendingUploads.take(ses, id, owner)
	if err != nil {
		return nil, err
	}
	// Gone for everyone else now; the lock only orders this after an
	// UploadChunk that got the upload just before.
	up.mu.Lock()
	defer up.mu.Unlock()
	if up.received != up.size() {
		up.w.Abort()
		return nil, devtext.WithEnglish(fmt.Sprintf("upload incomplete: %d of %d bytes", up.received, up.size()), i18n.DevFilesUploadIncomplete(int(up.received), int(up.size()))) // i18n-ignore: the English as it always read; the catalog has the other languages
	}
	hash := hex.EncodeToString(up.hasher.Sum(nil))
	if (sha != "" && sha != hash) || (up.key.sha256 != "" && up.key.sha256 != hash) {
		up.w.Abort()
		return nil, devtext.New(i18n.DevFilesUploadBadHash())
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
		mg.alert(i18n.DevAlertFileNotSaved, up.key.path, err)
		return nil, err
	}
	known := mg.contentKnown(hash)
	file, write, err := mg.registerUpload(ses, up.key.path, hash, mimetype.Detect(up.head).String(), up.size(), up.force, up.created, up.modified, up.cloudID)
	if err != nil {
		mg.removeBlobIfUnused(hash)
		return file, err
	}
	if !write {
		return file, nil
	}
	if !mg.hasBlob(hash) {
		// Taken as unused by a DelFile between the commit and the row.
		err = devtext.New(i18n.DevFilesUploadRemoved())
		mg.alert(i18n.DevAlertFileNotSaved, file.Path, err)
		return nil, err
	}
	// As UploadFile: content already processed isn't processed again -
	// unless it was kept out of Images until now (issue #192).
	if known && mg.hasThumbnail(hash) {
		mg.reconcileAnalysis(ses, []string{hash})
		return file, nil
	}

	mg.enqueueMedia(ses, file)
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
		return "", func() {}, errors.New("no way to stream this video to ffmpeg") // i18n-ignore: internal, before the websocket layer sets the source
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
				mg.alert(i18n.DevAlertFileNotRead, path, err)
			}
			missingBlobs.set(file.Hash, true)
		} else {
			mg.alert(i18n.DevAlertFileNotRead, path, err)
		}
		return nil, nil, 0, devtext.WithEnglish(fmt.Sprintf("the content of %s is missing or unreadable on this device", path), i18n.DevFilesContentUnavailable(path)) // i18n-ignore: the English as it always read; the catalog has the other languages
	}
	defer blob.Close()
	if offset < 0 || offset > blob.Size() {
		return nil, nil, 0, devtext.WithEnglish(fmt.Sprintf("offset %d outside the file", offset), i18n.DevFilesFileOffset(int(offset))) // i18n-ignore: the English as it always read; the catalog has the other languages
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
