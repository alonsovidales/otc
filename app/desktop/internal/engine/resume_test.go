// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alonsovidales/otc/app/desktop/internal/wsclient"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// Issue #205: resumable uploads, against the fake device of
// chunked_test.go.

// resumeFile writes n random bytes and returns the path, its hash and its
// stat.
func resumeFile(t *testing.T, n int) (string, []byte, string, os.FileInfo) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "big.mov")
	content := make([]byte, n)
	rand.Read(content)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	fi, _ := os.Stat(path)

	return path, content, hex.EncodeToString(sum[:]), fi
}

func newResumeDevice() *fakeDevice {
	return &fakeDevice{files: map[string][]byte{}, pending: map[string]*bytes.Buffer{}, paths: map[string]string{}}
}

// chunkNumber: whether req is an UploadChunk, and how many the device has
// seen - in dropOn, the ones before it; in dropAfter, it too.
func chunkNumber(d *fakeDevice, req *pb.ReqEnvelope) (int, bool) {
	if _, ok := req.Payload.(*pb.ReqEnvelope_ReqUploadChunk); !ok {
		return 0, false
	}

	return len(d.chunkOffsets), true
}

func (d *fakeDevice) uploads() (begins []*pb.BeginUpload, answers, offsets []int64) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]*pb.BeginUpload(nil), d.begins...), append([]int64(nil), d.beginAnswers...), append([]int64(nil), d.chunkOffsets...)
}

func (d *fakeDevice) stored(path string) []byte {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.files[path]
}

// Every BeginUpload names its content, and a device that already holds
// part of it is answered from there: the first chunk goes at that offset,
// the part it holds is never sent again, and the file is whole.
func TestUploadGoesOnFromWhatTheDeviceHolds(t *testing.T) {
	d := newResumeDevice()
	path, content, hash, fi := resumeFile(t, 2*cChunk+777)
	held := int64(cChunk + 100)
	d.pending["u1"] = bytes.NewBuffer(append([]byte(nil), content[:held]...))
	d.paths["u1"] = "/pc/big.mov"
	d.resumable = map[uploadKey]string{{"/pc/big.mov", int64(len(content)), hash}: "u1"}
	e := connectedEngine(t, d)

	if err := e.upload(path, "/pc/big.mov", hash, fi); err != nil {
		t.Fatalf("upload: %v", err)
	}
	begins, answers, offsets := d.uploads()
	if len(begins) != 1 || begins[0].Sha256 != hash || begins[0].Size != int64(len(content)) {
		t.Fatalf("BeginUpload %+v, want one with the content's sha256 and size", begins)
	}
	if answers[0] != held {
		t.Fatalf("answered received %d, want %d", answers[0], held)
	}
	want := []int64{held, held + cChunk}
	if len(offsets) != len(want) || offsets[0] != want[0] || offsets[1] != want[1] {
		t.Fatalf("chunks at %v, want %v", offsets, want)
	}
	if !bytes.Equal(d.stored("/pc/big.mov"), content) {
		t.Fatal("the stored file isn't the original")
	}
}

// A connection that drops mid-file - the device took the second piece but
// its answer was lost - comes back, BeginUpload goes again with the same
// key, and the file goes on from what the device holds, nothing sent
// twice.
func TestUploadResumesAfterADrop(t *testing.T) {
	d := newResumeDevice()
	path, content, hash, fi := resumeFile(t, 4*cChunk+5)
	dropped := false
	d.dropAfter = func(req *pb.ReqEnvelope) bool {
		if n, ok := chunkNumber(d, req); ok && n == 2 && !dropped {
			dropped = true

			return true
		}

		return false
	}
	e := connectedEngine(t, d)
	signIn := e.ws.SignIns()

	if err := e.upload(path, "/pc/big.mov", hash, fi); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if e.ws.SignIns() == signIn {
		t.Fatal("the connection never dropped")
	}
	begins, answers, offsets := d.uploads()
	if len(begins) != 2 || begins[1].Sha256 != hash || begins[1].Path != begins[0].Path || begins[1].Size != begins[0].Size {
		t.Fatalf("BeginUploads %+v, want the same key twice", begins)
	}
	if answers[1] != 2*cChunk {
		t.Fatalf("resumed at %d, want %d", answers[1], 2*cChunk)
	}
	want := []int64{0, cChunk, 2 * cChunk, 3 * cChunk, 4 * cChunk}
	if len(offsets) != len(want) {
		t.Fatalf("chunks at %v, want %v", offsets, want)
	}
	for i := range want {
		if offsets[i] != want[i] {
			t.Fatalf("chunks at %v, want %v", offsets, want)
		}
	}
	if !bytes.Equal(d.stored("/pc/big.mov"), content) {
		t.Fatal("the stored file isn't the original")
	}
}

// The piece cut off with the connection (never stored) is sent again from
// the device's count, not the client's.
func TestUploadResumesFromTheDevicesCount(t *testing.T) {
	d := newResumeDevice()
	path, content, hash, fi := resumeFile(t, 3*cChunk)
	dropped := false
	d.dropOn = func(req *pb.ReqEnvelope) bool {
		if n, ok := chunkNumber(d, req); ok && n == 1 && !dropped {
			dropped = true

			return true // the second piece never arrives
		}

		return false
	}
	e := connectedEngine(t, d)

	if err := e.upload(path, "/pc/big.mov", hash, fi); err != nil {
		t.Fatalf("upload: %v", err)
	}
	_, answers, offsets := d.uploads()
	if len(answers) != 2 || answers[1] != cChunk {
		t.Fatalf("answers %v, want the resume at %d", answers, cChunk)
	}
	want := []int64{0, cChunk, 2 * cChunk}
	if len(offsets) != len(want) {
		t.Fatalf("chunks at %v, want %v", offsets, want)
	}
	if !bytes.Equal(d.stored("/pc/big.mov"), content) {
		t.Fatal("the stored file isn't the original")
	}
}

// A device that holds nothing of it after the drop (one before release
// 166, or past its 30 idle minutes) answers received 0: the upload starts
// again from the first byte and still finishes.
func TestUploadRestartsWhenTheDeviceHoldsNothing(t *testing.T) {
	d := newResumeDevice()
	d.forgetful = true
	path, content, hash, fi := resumeFile(t, 3*cChunk+1)
	dropped := false
	d.dropAfter = func(req *pb.ReqEnvelope) bool {
		if n, ok := chunkNumber(d, req); ok && n == 2 && !dropped {
			dropped = true

			return true
		}

		return false
	}
	e := connectedEngine(t, d)

	if err := e.upload(path, "/pc/big.mov", hash, fi); err != nil {
		t.Fatalf("upload: %v", err)
	}
	_, answers, offsets := d.uploads()
	if len(answers) != 2 || answers[1] != 0 {
		t.Fatalf("answers %v, want a fresh upload the second time", answers)
	}
	want := []int64{0, cChunk, 0, cChunk, 2 * cChunk, 3 * cChunk}
	if len(offsets) != len(want) {
		t.Fatalf("chunks at %v, want %v", offsets, want)
	}
	for i := range want {
		if offsets[i] != want[i] {
			t.Fatalf("chunks at %v, want %v", offsets, want)
		}
	}
	if !bytes.Equal(d.stored("/pc/big.mov"), content) {
		t.Fatal("the stored file isn't the original")
	}
}

// A file that changed while the link was down isn't the content the key
// names any more: nothing more of it goes (a busy file), and its next
// upload, with the new hash, is a new key and starts fresh.
func TestUploadOfAFileChangedMeanwhileStartsFresh(t *testing.T) {
	d := newResumeDevice()
	path, _, hash, fi := resumeFile(t, 3*cChunk)
	changed := make([]byte, 3*cChunk+10)
	rand.Read(changed)
	dropped := false
	d.dropAfter = func(req *pb.ReqEnvelope) bool {
		if n, ok := chunkNumber(d, req); ok && n == 1 && !dropped {
			dropped = true
			// Written while the connection is down.
			if err := os.WriteFile(path, changed, 0o644); err != nil {
				t.Error(err)
			}
			later := time.Now().Add(time.Minute)
			_ = os.Chtimes(path, later, later)

			return true
		}

		return false
	}
	e := connectedEngine(t, d)

	if err := e.upload(path, "/pc/big.mov", hash, fi); !errors.Is(err, errBusyFile) {
		t.Fatalf("upload of a file changed meanwhile: %v, want a busy file", err)
	}
	if begins, _, _ := d.uploads(); len(begins) != 1 {
		t.Fatalf("%d BeginUploads, want no resume of the old content", len(begins))
	}

	// Its next pass hashes it again (the busy wait is over).
	e.noteSent(path)
	sum := sha256.Sum256(changed)
	newHash := hex.EncodeToString(sum[:])
	fi, _ = os.Stat(path)
	if err := e.upload(path, "/pc/big.mov", newHash, fi); err != nil {
		t.Fatalf("upload of the new content: %v", err)
	}
	begins, answers, offsets := d.uploads()
	if len(begins) != 2 || begins[1].Sha256 != newHash || answers[1] != 0 {
		t.Fatalf("BeginUploads %+v answered %v, want the new key answered 0", begins, answers)
	}
	if offsets[1] != 0 {
		t.Fatalf("chunks at %v, want the new content from 0", offsets)
	}
	if !bytes.Equal(d.stored("/pc/big.mov"), changed) {
		t.Fatal("the stored file isn't the new content")
	}
}

// A link that drops every time before anything gets through gives up after
// maxStalledResumes resumes in a row: bounded, as the pass's own retry
// takes over.
func TestUploadGivesUpOnDropsThatBringNothing(t *testing.T) {
	d := newResumeDevice()
	path, _, hash, fi := resumeFile(t, 2*cChunk)
	d.dropOn = func(req *pb.ReqEnvelope) bool {
		_, ok := chunkNumber(d, req)

		return ok
	}
	e := connectedEngine(t, d)

	err := e.upload(path, "/pc/big.mov", hash, fi)
	if !wsclient.Dropped(err) {
		t.Fatalf("upload: %v, want the dropped connection", err)
	}
	if begins, _, _ := d.uploads(); len(begins) != 1+maxStalledResumes {
		t.Fatalf("%d BeginUploads, want %d", len(begins), 1+maxStalledResumes)
	}
}

// A device that doesn't come back within resumeWait: the upload gives up
// with the drop (the device still keeps what it has for the next pass).
func TestUploadGivesUpWhenTheDeviceStaysAway(t *testing.T) {
	saved := resumeWait
	resumeWait = 1500 * time.Millisecond
	defer func() { resumeWait = saved }()
	d := newResumeDevice()
	path, _, hash, fi := resumeFile(t, 2*cChunk)
	d.dropAfter = func(req *pb.ReqEnvelope) bool {
		if _, ok := chunkNumber(d, req); ok {
			d.down = true

			return true
		}

		return false
	}
	e := connectedEngine(t, d)

	start := time.Now()
	err := e.upload(path, "/pc/big.mov", hash, fi)
	if !wsclient.Dropped(err) {
		t.Fatalf("upload: %v, want the dropped connection", err)
	}
	if took := time.Since(start); took < resumeWait || took > resumeWait+10*time.Second {
		t.Fatalf("gave up after %s, want about %s", took, resumeWait)
	}
	if begins, _, _ := d.uploads(); len(begins) != 1 {
		t.Fatalf("%d BeginUploads, want 1", len(begins))
	}
}

// A request that found no connection before the device began the upload
// is the pass's to retry, as before: no wait, no second BeginUpload.
func TestUploadNotBegunIsNotResumed(t *testing.T) {
	d := newResumeDevice()
	path, _, hash, fi := resumeFile(t, cChunk)
	d.dropOn = func(req *pb.ReqEnvelope) bool {
		_, ok := req.Payload.(*pb.ReqEnvelope_ReqBeginUpload)

		return ok
	}
	e := connectedEngine(t, d)

	start := time.Now()
	if err := e.upload(path, "/pc/big.mov", hash, fi); !wsclient.Dropped(err) {
		t.Fatalf("upload: %v, want the dropped connection", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("took %s: it waited for the connection", took)
	}
}

func TestUploadResumeAccount(t *testing.T) {
	var r uploadResume
	if r.dropped() {
		t.Fatal("resumed an upload the device never began")
	}
	if _, err := r.started(-1, 10); err == nil {
		t.Fatal("took a negative count")
	}
	if _, err := r.started(11, 10); err == nil {
		t.Fatal("took more than the file")
	}
	if at, err := r.started(0, 10); err != nil || at != 0 {
		t.Fatalf("started(0) = %d, %v", at, err)
	}
	for i := 0; i < maxStalledResumes; i++ {
		if !r.dropped() {
			t.Fatalf("gave up after %d drops", i+1)
		}
		if at, _ := r.started(0, 10); at != 0 {
			t.Fatal("not from the device's count")
		}
	}
	if r.dropped() {
		t.Fatal("went on after drops that brought nothing")
	}
	// Progress starts the count again: a link that keeps dropping goes on
	// while each connection gets something through.
	var p uploadResume
	if _, err := p.started(0, 100); err != nil {
		t.Fatal(err)
	}
	for held := int64(10); held <= 100; held += 10 {
		if !p.dropped() {
			t.Fatalf("gave up at %d with progress", held)
		}
		if at, _ := p.started(held, 100); at != held {
			t.Fatalf("went on from %d, want %d", at, held)
		}
	}
	if at, err := p.started(100, 100); err != nil || at != 100 {
		t.Fatalf("all held: %d, %v", at, err)
	}
}
