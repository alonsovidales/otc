// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/segcrypt"
	"github.com/alonsovidales/otc/session"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Issue #205: uploads that outlive their connection.

func shaHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func randomContent(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// sendFrom sends content from offset on in chunks of at most n bytes, as
// owner, and fails the test on any refusal.
func sendFrom(t *testing.T, mg *Manager, ses *session.Session, id string, content []byte, from, n int, owner any) {
	t.Helper()
	for at := from; at < len(content); at += n {
		end := min(at+n, len(content))
		got, err := mg.UploadChunk(ses, id, int64(at), content[at:end], owner)
		if err != nil || got != int64(end) {
			t.Fatalf("chunk at %d: received %d, %v", at, got, err)
		}
	}
}

// upload is the upload id is, for a test to look at or age.
func upload(t *testing.T, id string) *chunkedUpload {
	t.Helper()
	up, err := pendingUploads.get(id)
	if err != nil {
		t.Fatalf("upload %s: %v", id, err)
	}
	return up
}

// age makes the upload id look last sent to d ago.
func age(t *testing.T, id string, d time.Duration) {
	up := upload(t, id)
	up.mu.Lock()
	up.lastTouch = time.Now().Add(-d)
	up.mu.Unlock()
}

// keptCount is how many uploads wait for a resume.
func keptCount() int {
	pendingUploads.mu.Lock()
	defer pendingUploads.mu.Unlock()
	return len(pendingUploads.keptLocked())
}

// A dropped connection's upload goes on, on the next BeginUpload of the
// same path, size and sha256 from another connection, from what the device
// holds - in the middle of a segment, whose start is only in memory - with
// that BeginUpload's metadata; FinishUpload checks all of the content, and
// what is stored is all of it.
func TestUploadResumesAfterADrop(t *testing.T) {
	_, ses := galleryTestEnv(t)
	noUploads(t)
	content := randomContent(t, 2*segcrypt.SegmentSize+12345)
	hash := shaHex(content)
	size := int64(len(content))
	t.Cleanup(func() { os.Remove(blobPath(hash)) })
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mg := keptOut(&Manager{dao: dao.NewWithDB(db)})
	connA, connB := new(int), new(int)

	id, received, err := mg.BeginUpload(ses, "/Videos/clip.bin", size, false, nil, nil, "cloud-1", hash, connA)
	if err != nil || received != 0 {
		t.Fatalf("a new upload: received %d, %v", received, err)
	}
	first := segcrypt.SegmentSize + 100
	sendFrom(t, mg, ses, id, content[:first], 0, 300000, connA)
	mg.ReleaseUploadsOf(connA) // the connection dropped

	modified := timestamppb.New(time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC))
	again, received, err := mg.BeginUpload(ses, "/Videos/clip.bin", size, false, nil, modified, "cloud-2", strings.ToUpper(hash), connB)
	if err != nil || again != id || received != int64(first) {
		t.Fatalf("resumed: id %s (want %s), received %d (want %d), %v", again, id, received, first, err)
	}
	if _, err := mg.UploadChunk(ses, id, int64(first), content[first:first+10], connA); err != ErrUnknownUpload {
		t.Errorf("the dropped connection still sends to it: %v", err)
	}
	if got, err := mg.UploadChunk(ses, id, 0, content[:10], connB); err != ErrOutOfOrder || got != int64(first) {
		t.Errorf("from the start again: received %d, %v", got, err)
	}
	sendFrom(t, mg, ses, id, content, first, 700000, connB)

	mock.ExpectQuery(reReferenced).WithArgs(hash, hash).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectExec("insert into `files`").
		WithArgs(hash, sqlmock.AnyArg(), sqlmock.AnyArg(), modified.AsTime(), "/Videos/clip.bin", size, "cloud-2").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("update `files` set `cloud_id`").WithArgs("cloud-2", hash).WillReturnResult(sqlmock.NewResult(0, 0))
	f, err := mg.FinishUpload(ses, id, hash, connB)
	if err != nil || f.Hash != hash {
		t.Fatalf("FinishUpload: %+v, %v", f, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
	stored, err := blobstore.ReadAll(blobPath(hash), ses)
	if err != nil || !bytes.Equal(stored, content) {
		t.Fatalf("stored content: %d bytes, %v", len(stored), err)
	}

	// Finished: the key begins a new upload now.
	next, received, err := mg.BeginUpload(ses, "/Videos/clip.bin", size, false, nil, nil, "", hash, connB)
	if err != nil || next == id || received != 0 {
		t.Errorf("after it finished: id %s, received %d, %v", next, received, err)
	}
}

// Only the same path, size and sha256 resume an upload: anything else -
// no sha256, or one that isn't a SHA-256 - is a new one, and the kept one
// still waits for its own key.
func TestResumeNeedsTheSameKey(t *testing.T) {
	_, ses := galleryTestEnv(t)
	noUploads(t)
	mg := &Manager{}
	shaA, shaB := strings.Repeat("a", 64), strings.Repeat("b", 64)
	connA, connB, connC := new(int), new(int), new(int)
	id, _, err := mg.BeginUpload(ses, "/a.bin", 10, false, nil, nil, "", shaA, connA)
	if err != nil {
		t.Fatal(err)
	}
	sendFrom(t, mg, ses, id, []byte("12345"), 0, 5, connA)
	mg.ReleaseUploadsOf(connA)

	for _, c := range []struct {
		path string
		size int64
		sha  string
	}{
		{"/a.bin", 10, shaB},
		{"/a.bin", 11, shaA},
		{"/b.bin", 10, shaA},
		{"/a.bin", 10, ""},
		{"/a.bin", 10, "not a hash"},
		{"/a.bin", 10, shaA[:63]},
	} {
		other, received, err := mg.BeginUpload(ses, c.path, c.size, false, nil, nil, "", c.sha, connB)
		if err != nil || other == id || received != 0 {
			t.Errorf("%s, %d bytes, sha256 %q: id %s (the kept one is %s), received %d, %v", c.path, c.size, c.sha, other, id, received, err)
		}
	}
	again, received, err := mg.BeginUpload(ses, "/a.bin", 10, false, nil, nil, "", shaA, connC)
	if err != nil || again != id || received != 5 {
		t.Errorf("the kept one: id %s, received %d, %v", again, received, err)
	}
}

// One connection drives an upload at a time: the latest BeginUpload of
// its key, whether the connection that had it closed or not (a phone's
// socket can look open for minutes after it moved to mobile data). The
// one that had it is refused from then on; its close changes nothing for
// the new one. A BeginUpload again on the same connection goes on too.
func TestOneConnectionDrivesAnUpload(t *testing.T) {
	_, ses := galleryTestEnv(t)
	noUploads(t)
	mg := &Manager{}
	content := []byte("0123456789abcdefghij")
	sha := shaHex(content)
	connA, connB := new(int), new(int)
	id, _, err := mg.BeginUpload(ses, "/c.txt", int64(len(content)), false, nil, nil, "", sha, connA)
	if err != nil {
		t.Fatal(err)
	}
	sendFrom(t, mg, ses, id, content[:5], 0, 5, connA)

	// A still open: B takes it over.
	taken, received, err := mg.BeginUpload(ses, "/c.txt", int64(len(content)), false, nil, nil, "", sha, connB)
	if err != nil || taken != id || received != 5 {
		t.Fatalf("taken over: id %s, received %d, %v", taken, received, err)
	}
	if _, err := mg.UploadChunk(ses, id, 5, content[5:10], connA); err != ErrUnknownUpload {
		t.Errorf("the connection it was taken from still sends: %v", err)
	}
	if _, err := mg.FinishUpload(ses, id, sha, connA); err != ErrUnknownUpload {
		t.Errorf("the connection it was taken from finished it: %v", err)
	}
	mg.ReleaseUploadsOf(connA)
	sendFrom(t, mg, ses, id, content[:10], 5, 5, connB)

	// B asks again on the same connection (a retry): the same upload.
	same, received, err := mg.BeginUpload(ses, "/c.txt", int64(len(content)), false, nil, nil, "", sha, connB)
	if err != nil || same != id || received != 10 {
		t.Errorf("again on the same connection: id %s, received %d, %v", same, received, err)
	}
	if keptCount() != 0 {
		t.Errorf("%d uploads kept while a connection drives the only one", keptCount())
	}

	// Many at once: each gets the upload, and only the last one to get it
	// can send to it.
	const n = 8
	owners := make([]*int, n)
	var wg sync.WaitGroup
	for i := range owners {
		owners[i] = new(int)
		wg.Add(1)
		go func(owner *int) {
			defer wg.Done()
			got, received, err := mg.BeginUpload(ses, "/c.txt", int64(len(content)), false, nil, nil, "", sha, owner)
			if err != nil || got != id || received != 10 {
				t.Errorf("at once: id %s, received %d, %v", got, received, err)
			}
		}(owners[i])
	}
	wg.Wait()
	drivers := 0
	for _, owner := range owners {
		if _, err := mg.UploadChunk(ses, id, 10, nil, owner); err == nil {
			drivers++
		}
	}
	if drivers != 1 {
		t.Errorf("%d connections drive the upload, want 1", drivers)
	}
}

// An upload nobody sent to for cUploadIdle goes, kept or driven, its temp
// file with it; one sent to recently stays.
func TestIdleUploadsAreDropped(t *testing.T) {
	storage, ses := galleryTestEnv(t)
	noUploads(t)
	mg := &Manager{}
	conn, live := new(int), new(int)
	before := tempBlobs(storage)
	old, _, _ := mg.BeginUpload(ses, "/old.bin", 10, false, nil, nil, "", strings.Repeat("1", 64), conn)
	recent, _, _ := mg.BeginUpload(ses, "/recent.bin", 10, false, nil, nil, "", strings.Repeat("2", 64), conn)
	driven, _, _ := mg.BeginUpload(ses, "/driven.bin", 10, false, nil, nil, "", "", live)
	for _, id := range []string{old, recent} {
		sendFrom(t, mg, ses, id, []byte("123"), 0, 3, conn)
	}
	mg.ReleaseUploadsOf(conn)
	age(t, old, cUploadIdle+time.Minute)
	age(t, recent, cUploadIdle-time.Minute)
	age(t, driven, cUploadIdle+time.Minute)

	pendingUploads.sweep()
	if got := tempBlobs(storage); got != before+1 {
		t.Errorf("%d temp files after the sweep, want %d", got, before+1)
	}
	if _, err := mg.UploadChunk(ses, driven, 0, []byte("1"), live); err != ErrUnknownUpload {
		t.Errorf("an idle driven upload stayed: %v", err)
	}
	if _, received, _ := mg.BeginUpload(ses, "/old.bin", 10, false, nil, nil, "", strings.Repeat("1", 64), conn); received != 0 {
		t.Errorf("the idle kept upload resumed at %d", received)
	}
	if id, received, _ := mg.BeginUpload(ses, "/recent.bin", 10, false, nil, nil, "", strings.Repeat("2", 64), conn); id != recent || received != 3 {
		t.Errorf("the recent kept upload: id %s, received %d", id, received)
	}
}

// FinishUpload checks the whole content against its hash after a resume:
// bytes that differ from what the first connection had begun with are
// refused, and so is content that isn't what BeginUpload's key named even
// when it is what FinishUpload says. Either way the upload is gone.
func TestFinishUploadChecksAResumedUpload(t *testing.T) {
	_, ses := galleryTestEnv(t)
	noUploads(t)
	mg := &Manager{}
	badHash := devtext.New(i18n.DevFilesUploadBadHash()).Error()
	content := randomContent(t, segcrypt.SegmentSize+500)
	sha := shaHex(content)
	other := bytes.Clone(content)
	other[len(other)-1] ^= 0xff

	for _, c := range []struct {
		name     string
		rest     []byte // what the second connection sends
		finishAs string // FinishUpload's sha256
	}{
		{"other bytes after the resume", other, sha},
		{"content the key didn't name", other, shaHex(other)},
	} {
		connA, connB := new(int), new(int)
		id, _, err := mg.BeginUpload(ses, "/check.bin", int64(len(content)), false, nil, nil, "", sha, connA)
		if err != nil {
			t.Fatal(err)
		}
		sendFrom(t, mg, ses, id, content[:1000], 0, 1000, connA)
		mg.ReleaseUploadsOf(connA)
		if again, received, err := mg.BeginUpload(ses, "/check.bin", int64(len(content)), false, nil, nil, "", sha, connB); err != nil || again != id || received != 1000 {
			t.Fatalf("%s: resumed %s at %d, %v", c.name, again, received, err)
		}
		sendFrom(t, mg, ses, id, c.rest, 1000, 400000, connB)
		if f, err := mg.FinishUpload(ses, id, c.finishAs, connB); err == nil || err.Error() != badHash {
			t.Errorf("%s: %+v, %v", c.name, f, err)
		}
		if _, err := mg.UploadChunk(ses, id, 0, nil, connB); err != ErrUnknownUpload {
			t.Errorf("%s: the refused upload is still there: %v", c.name, err)
		}
	}
}

// A write that failed leaves the sealed file out of step with what was
// received: the upload is dropped, never resumed from there.
func TestAFailedWriteDropsTheUpload(t *testing.T) {
	_, ses := galleryTestEnv(t)
	noUploads(t)
	mg := &Manager{}
	conn := new(int)
	sha := strings.Repeat("3", 64)
	id, _, err := mg.BeginUpload(ses, "/broken.bin", 3<<20, false, nil, nil, "", sha, conn)
	if err != nil {
		t.Fatal(err)
	}
	sendFrom(t, mg, ses, id, []byte("start"), 0, 5, conn)
	upload(t, id).w.Abort() // its file is closed under it
	if _, err := mg.UploadChunk(ses, id, 5, make([]byte, segcrypt.SegmentSize+10), conn); err == nil {
		t.Fatal("a write to a closed file went through")
	}
	if _, err := mg.UploadChunk(ses, id, 5, []byte("x"), conn); err != ErrUnknownUpload {
		t.Errorf("after a failed write: %v", err)
	}
	if again, received, err := mg.BeginUpload(ses, "/broken.bin", 3<<20, false, nil, nil, "", sha, conn); err != nil || again == id || received != 0 {
		t.Errorf("its key resumed it: id %s, received %d, %v", again, received, err)
	}
}

// The limits: at most cMaxKeptUploads wait for a resume (the least
// recently sent to go first), and at most cMaxUploads are unfinished at
// once - a new one takes a kept one's place, and is refused when every
// one is being driven.
func TestUploadLimits(t *testing.T) {
	storage, ses := galleryTestEnv(t)
	noUploads(t)
	mg := &Manager{}
	key := func(i int) string { return strings.Repeat("0", 60) + hex.EncodeToString([]byte{byte(i >> 8), byte(i)}) }
	before := tempBlobs(storage)

	conn := new(int)
	var ids []string
	for i := 0; i <= cMaxKeptUploads; i++ {
		id, _, err := mg.BeginUpload(ses, "/kept.bin", 10, false, nil, nil, "", key(i), conn)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		age(t, id, time.Duration(cMaxKeptUploads+1-i)*time.Minute) // the first is the oldest
	}
	mg.ReleaseUploadsOf(conn)
	if keptCount() != cMaxKeptUploads || tempBlobs(storage) != before+cMaxKeptUploads {
		t.Fatalf("%d kept, %d temp files, want %d", keptCount(), tempBlobs(storage)-before, cMaxKeptUploads)
	}
	if _, err := pendingUploads.get(ids[0]); err != ErrUnknownUpload {
		t.Errorf("the oldest wasn't the one dropped")
	}
	pendingUploads.drop(func(*chunkedUpload) bool { return true })

	// All of them: cMaxUploads-1 driven, one kept.
	driver, keeper, late := new(int), new(int), new(int)
	for i := 0; i < cMaxUploads-1; i++ {
		if _, _, err := mg.BeginUpload(ses, "/driven.bin", 10, false, nil, nil, "", "", driver); err != nil {
			t.Fatalf("upload %d: %v", i, err)
		}
	}
	kept, _, err := mg.BeginUpload(ses, "/kept.bin", 10, false, nil, nil, "", key(1), keeper)
	if err != nil {
		t.Fatal(err)
	}
	mg.ReleaseUploadsOf(keeper)
	if _, _, err := mg.BeginUpload(ses, "/late.bin", 10, false, nil, nil, "", "", late); err != nil {
		t.Fatalf("a new upload didn't take the kept one's place: %v", err)
	}
	if _, err := pendingUploads.get(kept); err != ErrUnknownUpload {
		t.Error("the kept upload is still there")
	}
	_, _, err = mg.BeginUpload(ses, "/later.bin", 10, false, nil, nil, "", "", late)
	if err != ErrTooManyUploads || err.Error() != "too many uploads under way at once" {
		t.Fatalf("one past the limit, all driven: %v", err)
	}
	if es := devtext.Text("es", err); es != "demasiadas subidas en curso a la vez" {
		t.Errorf("in Spanish: %q", es)
	}
	mg.ReleaseUploadsOf(driver)
	if _, _, err := mg.BeginUpload(ses, "/later.bin", 10, false, nil, nil, "", "", late); err != nil {
		t.Errorf("with room again: %v", err)
	}
	mg.ReleaseUploadsOf(late)
	if got := tempBlobs(storage); got != before {
		t.Errorf("%d temp files left, want %d", got, before)
	}
}

// A kept upload still counts toward the free-space checks - it will write
// the rest when it resumes - but gives way, oldest first, to a new write
// that doesn't fit with it; an upload a connection drives never does.
func TestKeptUploadsGiveWayForRoom(t *testing.T) {
	storage, ses := galleryTestEnv(t)
	noUploads(t)
	withDisk(t, 4_000_000_000_000, 1<<30+10<<20) // 10 MiB over the reserve
	mg := &Manager{}
	connA, connB, connC := new(int), new(int), new(int)
	sha := strings.Repeat("4", 64)
	kept, _, err := mg.BeginUpload(ses, "/big.mov", 6<<20, false, nil, nil, "", sha, connA)
	if err != nil {
		t.Fatal(err)
	}
	sendFrom(t, mg, ses, kept, make([]byte, 1<<20), 0, 1<<20, connA)
	mg.ReleaseUploadsOf(connA)
	if got, want := pendingUploads.remaining(), sealedSize(5<<20); got != want {
		t.Errorf("a kept upload reserves %d, want %d", got, want)
	}

	// Fits with it: it stays.
	if _, _, err := mg.BeginUpload(ses, "/b.mov", 2<<20, false, nil, nil, "", "", connB); err != nil {
		t.Fatal(err)
	}
	if keptCount() != 1 {
		t.Fatal("dropped though there was room")
	}
	// Doesn't fit with it: it goes.
	if _, _, err := mg.BeginUpload(ses, "/c.mov", 4<<20, false, nil, nil, "", "", connC); err != nil {
		t.Fatalf("a kept upload stood in the way: %v", err)
	}
	if keptCount() != 0 {
		t.Error("the kept upload stayed")
	}
	if _, received, _ := mg.BeginUpload(ses, "/big.mov", 6<<20, false, nil, nil, "", sha, connA); received != 0 {
		t.Errorf("the dropped upload resumed at %d", received)
	}
	mg.ReleaseUploadsOf(connA)
	pendingUploads.drop(func(up *chunkedUpload) bool { return up.key.path == "/big.mov" })

	// Doesn't fit with the driven ones: refused, and they go on.
	if _, _, err := mg.BeginUpload(ses, "/d.mov", 5<<20, false, nil, nil, "", "", connC); !IsStorageFull(err) {
		t.Fatalf("got %v, want storage_full", err)
	}
	// UploadFile's check is the same.
	if err := pendingUploads.roomFor(storage, 5<<20); !IsStorageFull(err) {
		t.Errorf("UploadFile's check: %v", err)
	}
	if pendingUploads.remaining() != sealedSize(2<<20)+sealedSize(4<<20) {
		t.Error("a driven upload was dropped for room")
	}
}

// A kept upload's temp file was written by this run, so the start-up sweep
// never takes it, however long it has waited; after a restart the upload
// is gone (resuming doesn't survive one) and the next start's sweep takes
// the file.
func TestOrphanSweepLeavesAKeptUploadsFile(t *testing.T) {
	storage, ses := galleryTestEnv(t)
	noUploads(t)
	started := time.Now().Add(-time.Hour)
	before, _ := filepath.Glob(filepath.Join(storage, ".blob-*"))
	mg := &Manager{}
	conn := new(int)
	id, _, err := mg.BeginUpload(ses, "/waiting.mov", 10, false, nil, nil, "", strings.Repeat("5", 64), conn)
	if err != nil {
		t.Fatal(err)
	}
	sendFrom(t, mg, ses, id, []byte("12345"), 0, 5, conn)
	mg.ReleaseUploadsOf(conn)
	after, _ := filepath.Glob(filepath.Join(storage, ".blob-*"))
	var file string
	for _, p := range after {
		if !strings.Contains(strings.Join(before, "\n"), p) {
			file = p
		}
	}
	if file == "" {
		t.Fatal("no temp file for the upload")
	}
	waited := time.Now().Add(-25 * time.Minute)
	os.Chtimes(file, waited, waited)
	sweepOrphans(storage, "", nil, started.Add(-cOrphanMinAge))
	if _, err := os.Stat(file); err != nil {
		t.Errorf("the sweep took a kept upload's file: %v", err)
	}
	if _, received, _ := mg.BeginUpload(ses, "/waiting.mov", 10, false, nil, nil, "", strings.Repeat("5", 64), conn); received != 5 {
		t.Errorf("resumed at %d after the sweep, want 5", received)
	}
}
