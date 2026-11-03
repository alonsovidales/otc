// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/alonsovidales/otc/app/desktop/internal/wsclient"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// Issue #205: an upload cut by a dropped connection goes on where the
// device's copy of it ends, instead of from byte 0 at the next pass.
// BeginUpload names the content - path, size and SHA-256 are the resume
// key - and the device answers how much of it it already holds
// (UploadStarted.received: 0 for a new upload, and always from a device
// before release 166, which drops an upload with its connection). Once
// the client has signed in again (it reconnects by itself, 1 s doubling up
// to 30 s), the same BeginUpload goes again and the file is read on from
// there: the part the device holds is never read again to be sent.
// FinishUpload still checks the whole content's hash. A file that changed
// meanwhile is a busy file (busy.go); with another hash it is another key,
// and its next upload starts fresh. A pass whose upload gave up still
// resumes it: the device keeps an unfinished upload for 30 idle minutes.
// As ChunkedUpload.swift.

// resumeWait is how long an upload cut by a drop waits for the client to
// sign in again before it gives up (a var for the tests).
var resumeWait = 2 * time.Minute

// signInPoll is how often that wait looks.
const signInPoll = 250 * time.Millisecond

// maxStalledResumes bounds the drops in a row after which the device holds
// no more of the file than before: a link that keeps dropping goes on for
// as long as each connection gets some of it through.
const maxStalledResumes = 3

// uploadResume is the account of one upload's resumes (as
// ChunkedUpload.swift's UploadResume).
type uploadResume struct {
	begun   bool  // the device has answered a BeginUpload
	held    int64 // the most of the file it has said it holds
	stalled int   // drops since it last held more
}

// started takes the device's answer to a BeginUpload of size bytes and
// says where to go on from.
func (r *uploadResume) started(received, size int64) (int64, error) {
	if received < 0 || received > size {
		return 0, fmt.Errorf("the device holds %d bytes of a %d-byte upload", received, size) // i18n-ignore: a file's error, only logged
	}
	r.begun = true
	if received > r.held {
		r.held, r.stalled = received, 0
	}

	return received, nil
}

// dropped notes a dropped connection and says whether to resume: not
// before the device has begun the upload (a request that found no
// connection is the pass's to retry, as before), nor after
// maxStalledResumes drops in a row that brought nothing.
func (r *uploadResume) dropped() bool {
	if !r.begun {
		return false
	}
	r.stalled++

	return r.stalled <= maxStalledResumes
}

// uploadChunked sends the file in pieces read from the disk as they go -
// BeginUpload, UploadChunk in order, FinishUpload with the hash - instead
// of one UploadFile with the whole file in memory. The device checks the
// hash before the content becomes the file. A file that isn't the one
// hashed any more (hashed holds the stat the hash was taken with), or that
// changed while it was read, never gets its FinishUpload: it is a busy
// file (busy.go), and the device would only refuse what doesn't match the
// hash. A dropped connection resumes the upload (see above); a drop during
// FinishUpload doesn't - the device may have made the file already, and
// the next pass's HasFile, or its BeginUpload, finds out without sending
// it again.
func (e *Engine) uploadChunked(path, remotePath, hash string, hashed os.FileInfo, created, modified *timestamppb.Timestamp, check func() error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if hashed != nil && !sameStat(st, hashed) {
		e.noteBusy(path)

		return errBusyFile
	}
	e.mu.Lock()
	domain := e.domainLocked()
	e.mu.Unlock()
	buf := make([]byte, cChunk)
	var resume uploadResume
	var id string
	for {
		signIn := e.ws.SignIns()
		id, err = e.sendFrom(f, path, remotePath, hash, st, created, modified, buf, &resume)
		if err == nil {
			break
		}
		if !wsclient.Dropped(err) || !resume.dropped() {
			return err
		}
		log.Printf("%s: the connection dropped during its upload - going on once it is back", filepath.Base(path))
		if !e.awaitSignIn(signIn, domain) {
			return err
		}
		// Changed meanwhile: not the content the key names any more.
		if now, err := os.Stat(path); err != nil || !sameStat(now, st) {
			e.noteBusy(path)

			return errBusyFile
		}
	}
	if now, err := os.Stat(path); err != nil || !sameStat(now, st) {
		e.noteBusy(path)

		return errBusyFile
	}
	if check != nil {
		if err := check(); err != nil {
			return err
		}
	}
	resp, err := e.requestWithin(transferRequest, func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqFinishUpload{ReqFinishUpload: &pb.FinishUpload{UploadId: id, Sha256: hash}}
	})
	if err != nil {
		return err
	}

	return wsclient.RespError(resp, "upload rejected") // i18n-ignore: a file's error, only logged
}

// sendFrom begins the upload - or resumes it, when the device holds some
// of this content already - and sends the rest of the file from where the
// device's copy ends, read from the disk from there on. It returns the
// upload's id once the device holds all of it.
func (e *Engine) sendFrom(f *os.File, path, remotePath, hash string, st os.FileInfo, created, modified *timestamppb.Timestamp, buf []byte, resume *uploadResume) (string, error) {
	resp, err := e.requestWithin(smallRequest, func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqBeginUpload{ReqBeginUpload: &pb.BeginUpload{
			Path: remotePath, Size: st.Size(), Created: created, Modified: modified, ForceOverride: true, Sha256: hash,
		}}
	})
	if err != nil {
		return "", err
	}
	if err := wsclient.RespError(resp, "upload rejected"); err != nil { // i18n-ignore: a file's error, only logged
		return "", err
	}
	started, ok := resp.Payload.(*pb.RespEnvelope_RespUploadStarted)
	if !ok {
		return "", errors.New("unexpected response to BeginUpload") // i18n-ignore: a file's error, only logged
	}
	id := started.RespUploadStarted.UploadId
	offset, err := resume.started(started.RespUploadStarted.Received, st.Size())
	if err != nil {
		return "", err
	}
	if offset > 0 {
		log.Printf("%s: the device already holds %d of %d bytes - going on from there", filepath.Base(path), offset, st.Size())
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return "", err
	}
	for offset < st.Size() {
		n, err := io.ReadFull(f, buf)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			return "", err
		}
		if n == 0 {
			e.noteBusy(path) // it shrank while it was being sent

			return "", errBusyFile
		}
		chunk := buf[:n]
		at := offset
		resp, err := e.requestWithin(transferRequest, func(r *pb.ReqEnvelope) {
			r.Payload = &pb.ReqEnvelope_ReqUploadChunk{ReqUploadChunk: &pb.UploadChunk{UploadId: id, Offset: at, Data: chunk}}
		})
		if err != nil {
			return "", err
		}
		if err := wsclient.RespError(resp, "upload rejected"); err != nil { // i18n-ignore: a file's error, only logged
			return "", err
		}
		offset += int64(n)
	}

	return id, nil
}

// awaitSignIn waits, resumeWait at most, for the client to sign in again
// after its sign-in number n (wsclient.SignIns). False when it doesn't, or
// when the engine stops or is pointed at another device (or none)
// meanwhile: nothing more of the file goes there.
func (e *Engine) awaitSignIn(n int64, domain string) bool {
	end := time.Now().Add(resumeWait)
	for {
		e.mu.Lock()
		over := e.stopped || e.domainLocked() != domain
		e.mu.Unlock()
		if over {
			return false
		}
		if e.ws.IsConnected() && e.ws.SignIns() != n {
			return true
		}
		if time.Now().After(end) {
			return false
		}
		time.Sleep(signInPoll)
	}
}

// domainLocked is the device the settings point at ("" with none); e.mu
// must be held.
func (e *Engine) domainLocked() string {
	if e.cfg == nil {
		return ""
	}

	return e.cfg.Domain
}
