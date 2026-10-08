// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/blobstore"
	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
)

// sealedThumb stores a w x h JPEG sealed at path, gone at the end.
func sealedThumb(t *testing.T, ses *session.Session, path string, w, h int) []byte {
	t.Helper()
	b := testJPEG(t, w, h, 0)
	if err := blobstore.WriteBytes(path, ses, b); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(path) })
	return b
}

// withBlob writes a stand-in blob for hash (fixThumbnails only checks it
// is there), gone at the end with whatever is derived from it.
func withBlob(t *testing.T, hash string) {
	t.Helper()
	if err := os.WriteFile(blobPath(hash), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Remove(blobPath(hash))
		removeThumbnails(blobPath(hash))
	})
}

func readSealed(t *testing.T, ses *session.Session, path string) []byte {
	t.Helper()
	b, err := blobstore.ReadAll(path, ses)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return b
}

func mtime(t *testing.T, path string) time.Time {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.ModTime()
}

// waitForFile waits for the queue's worker to write path.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s was never written", path)
}

// waitQueueIdle waits for mg's thumbnail queue to finish.
func waitQueueIdle(t *testing.T, mg *Manager) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mg.thumbFixes.mu.Lock()
		running := mg.thumbFixes.running
		mg.thumbFixes.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the thumbnail queue never finished")
}

// queueWaitsOn replaces what the queue's worker waits on before each file
// for this test (the real one waits a minute after any upload, which other
// tests make).
func queueWaitsOn(t *testing.T, f func(*Manager) bool) {
	t.Helper()
	old := thumbFixWaitQuiet
	thumbFixWaitQuiet = f
	t.Cleanup(func() { thumbFixWaitQuiet = old })
}

func quietQueue(t *testing.T) { queueWaitsOn(t, func(*Manager) bool { return true }) }

// The pass and the queue change only what is wrong, from the big
// thumbnail: a missing or wrongly sized small one, a big one over the cap
// on its longest side. Once done, running again changes nothing.
func TestFixThumbnailsOnlyWhatIsNeeded(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg := &Manager{}
	type tc struct {
		name           string
		big, small     wh // 0x0: none
		want           thumbFix
		wantBig        wh
		wantSmall      wh // 0x0: none
		bigUntouched   bool
		smallUntouched bool
	}
	cases := []tc{
		{name: "both right", big: wh{1000, 750}, small: wh{533, 400}, want: thumbOK, wantBig: wh{1000, 750}, wantSmall: wh{533, 400}, bigUntouched: true, smallUntouched: true},
		{name: "old portrait, no small", big: wh{1000, 1333}, want: thumbFixed, wantBig: wh{750, 1000}, wantSmall: wh{400, 533}},
		{name: "fits, no small", big: wh{640, 480}, want: thumbFixed, wantBig: wh{640, 480}, wantSmall: wh{533, 400}, bigUntouched: true},
		{name: "small of another size", big: wh{1000, 750}, small: wh{200, 150}, want: thumbFixed, wantBig: wh{1000, 750}, wantSmall: wh{533, 400}, bigUntouched: true},
		{name: "old long screenshot", big: wh{100, 3000}, want: thumbFixed, wantBig: wh{33, 1000}, wantSmall: wh{26, 800}},
		{name: "smaller than a tile", big: wh{300, 200}, want: thumbFixed, wantBig: wh{300, 200}, wantSmall: wh{300, 200}, bigUntouched: true},
		{name: "no thumbnail yet", want: thumbSkipped},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := fmt.Sprintf("%064x", 0xf1000+i)
			withBlob(t, h)
			if c.big.w > 0 {
				sealedThumb(t, ses, thumbnailPath(h), c.big.w, c.big.h)
			}
			if c.small.w > 0 {
				sealedThumb(t, ses, smallThumbnailPath(h), c.small.w, c.small.h)
			}
			var bigAt, smallAt time.Time
			if c.big.w > 0 {
				bigAt = mtime(t, thumbnailPath(h))
			}
			if c.small.w > 0 {
				smallAt = mtime(t, smallThumbnailPath(h))
			}
			time.Sleep(5 * time.Millisecond) // a rewrite gets a new mtime

			if got := mg.fixThumbnails(ses, h, 1000); got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			if c.big.w == 0 {
				if _, err := os.Stat(smallThumbnailPath(h)); !os.IsNotExist(err) {
					t.Error("a small thumbnail was made without a big one")
				}
				return
			}
			if w, hh := storedSize(t, ses, thumbnailPath(h)); w != c.wantBig.w || hh != c.wantBig.h {
				t.Errorf("big %dx%d, want %dx%d", w, hh, c.wantBig.w, c.wantBig.h)
			}
			if w, hh := storedSize(t, ses, smallThumbnailPath(h)); w != c.wantSmall.w || hh != c.wantSmall.h {
				t.Errorf("small %dx%d, want %dx%d", w, hh, c.wantSmall.w, c.wantSmall.h)
			}
			if c.bigUntouched && !mtime(t, thumbnailPath(h)).Equal(bigAt) {
				t.Error("the big thumbnail was rewritten for nothing")
			}
			if c.smallUntouched && !mtime(t, smallThumbnailPath(h)).Equal(smallAt) {
				t.Error("the small thumbnail was rewritten for nothing")
			}
			// Idempotent.
			if got := mg.fixThumbnails(ses, h, 1000); got != thumbOK {
				t.Errorf("a second run gave %v, want nothing to do", got)
			}
		})
	}
}

// A thumbnail whose header is over the decode limit is left alone, never
// decoded.
func TestFixThumbnailsSkipsWhatIsTooLargeToDecode(t *testing.T) {
	_, ses := galleryTestEnv(t)
	h := fmt.Sprintf("%064x", 0xf2000)
	withBlob(t, h)
	b := testJPEG(t, 16, 16, 0)
	// The SOF0 segment's height and width, as a header claims them:
	// 2000 x 65535 is 131 MP.
	i := bytes.Index(b, []byte{0xff, 0xc0})
	if i < 0 {
		t.Fatal("no SOF0")
	}
	b[i+5], b[i+6] = 0xff, 0xff
	b[i+7], b[i+8] = 0x07, 0xd0
	if w, h := jpegSize(t, b); w != 2000 || h != 65535 {
		t.Fatalf("the header says %dx%d", w, h)
	}
	if err := blobstore.WriteBytes(thumbnailPath(h), ses, b); err != nil {
		t.Fatal(err)
	}
	if got := (&Manager{}).fixThumbnails(ses, h, 1000); got != thumbSkipped {
		t.Fatalf("got %v, want it skipped", got)
	}
	if _, err := os.Stat(smallThumbnailPath(h)); !os.IsNotExist(err) {
		t.Error("something was written")
	}
}

// Issue #141's rule: what a fix writes is decided under the hash's lock.
// Content deleted while the fix decoded its thumbnail gets no small one
// back; content processed again keeps what processing wrote.
func TestFixThumbnailsUnderTheHashLock(t *testing.T) {
	_, ses := galleryTestEnv(t)
	t.Cleanup(func() { beforeThumbFixWrite = nil })

	t.Run("deleted meanwhile", func(t *testing.T) {
		h := fmt.Sprintf("%064x", 0xf3001)
		withBlob(t, h)
		sealedThumb(t, ses, thumbnailPath(h), 1000, 1333)
		beforeThumbFixWrite = func(hash string) {
			unlock := lockBlob(hash) // as removeBlobIfUnused does
			os.Remove(blobPath(hash))
			removeThumbnails(blobPath(hash))
			unlock()
		}
		if got := (&Manager{}).fixThumbnails(ses, h, 1000); got != thumbSkipped {
			t.Fatalf("got %v", got)
		}
		for _, p := range []string{thumbnailPath(h), smallThumbnailPath(h)} {
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Errorf("%s came back after the delete", p)
			}
		}
	})

	t.Run("processed again meanwhile", func(t *testing.T) {
		h := fmt.Sprintf("%064x", 0xf3002)
		withBlob(t, h)
		sealedThumb(t, ses, thumbnailPath(h), 1000, 1333)
		var fresh []byte
		beforeThumbFixWrite = func(hash string) {
			time.Sleep(5 * time.Millisecond)
			fresh = testJPEG(t, 750, 1000, 0)
			if err := blobstore.WriteBytes(thumbnailPath(hash), ses, fresh); err != nil {
				t.Error(err)
			}
		}
		if got := (&Manager{}).fixThumbnails(ses, h, 1000); got != thumbSkipped {
			t.Fatalf("got %v", got)
		}
		if !bytes.Equal(readSealed(t, ses, thumbnailPath(h)), fresh) {
			t.Error("the new thumbnail was replaced")
		}
		if _, err := os.Stat(smallThumbnailPath(h)); !os.IsNotExist(err) {
			t.Error("a small one from the old thumbnail was written")
		}
	})
}

// Decoding a thumbnail holds the content budget for its pixels, so a
// 1000x30000 one waits for downloads rather than adding to them.
func TestFixThumbnailsWaitsForTheMemoryBudget(t *testing.T) {
	_, ses := galleryTestEnv(t)
	h := fmt.Sprintf("%064x", 0xf4000)
	withBlob(t, h)
	sealedThumb(t, ses, thumbnailPath(h), 1000, 1333)
	mg := &Manager{contentBudget: newMemBudget(1 << 20)}
	held := mg.contentBudget.acquire(1 << 20)
	done := make(chan thumbFix, 1)
	go func() { done <- mg.fixThumbnails(ses, h, 1000) }()
	select {
	case <-done:
		t.Fatal("decoded with the budget taken")
	case <-time.After(150 * time.Millisecond):
	}
	held()
	select {
	case got := <-done:
		if got != thumbFixed {
			t.Errorf("got %v", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("never ran once the budget was free")
	}
}

// The queue takes a hash once, and no more than its bound.
func TestThumbnailQueueDedupsAndIsBounded(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg := &Manager{}
	mg.thumbFixes.running = true // no worker: look at the queue
	h := fmt.Sprintf("%064x", 0xf5000)
	mg.queueThumbnailFix(ses, h)
	mg.queueThumbnailFix(ses, h)
	mg.queueThumbnailFix(ses, "../etc")
	mg.queueThumbnailFix(nil, fmt.Sprintf("%064x", 0xf5001))
	if n := len(mg.thumbFixes.jobs); n != 1 {
		t.Fatalf("%d jobs, want 1", n)
	}
	for i := 0; i < cThumbFixQueueMax+10; i++ {
		mg.queueThumbnailFix(ses, fmt.Sprintf("%064x", 0xf6000+i))
	}
	if n := len(mg.thumbFixes.jobs); n != cThumbFixQueueMax {
		t.Errorf("%d jobs, want the bound %d", n, cThumbFixQueueMax)
	}
}

// Content whose blob is gone (issue #141) is skipped before its big
// thumbnail is decoded: nothing would be written for it, and each grid
// asking for its small one queued that whole decode again. Once the blob
// is back, it is fixed.
func TestFixThumbnailsSkipsContentWithoutItsBlob(t *testing.T) {
	_, ses := galleryTestEnv(t)
	mg := &Manager{}
	h := fmt.Sprintf("%064x", 0xf6100)
	sealedThumb(t, ses, thumbnailPath(h), 1000, 750)
	t.Cleanup(func() { removeThumbnails(blobPath(h)) })
	decoded := 0
	old := beforeThumbFixWrite
	beforeThumbFixWrite = func(string) { decoded++ }
	t.Cleanup(func() { beforeThumbFixWrite = old })

	if got := mg.fixThumbnails(ses, h, ThumbnailMaxSide()); got != thumbSkipped || decoded != 0 {
		t.Errorf("without its blob: %v after %d decodes, want skipped before any", got, decoded)
	}
	if _, err := os.Stat(smallThumbnailPath(h)); err == nil {
		t.Error("a small thumbnail was written for content that is gone")
	}
	withBlob(t, h)
	if got := mg.fixThumbnails(ses, h, ThumbnailMaxSide()); got != thumbFixed || decoded != 1 {
		t.Errorf("with its blob back: %v after %d decodes, want fixed after one", got, decoded)
	}
}

// smallFixture is three photos: a with both thumbnails, b with only the
// big one (processed before the small ones existed), c with only a small
// one (a half-done delete: not shown).
type smallFixture struct {
	a, b, c            string
	aBig, aSmall, bBig []byte
}

func newSmallFixture(t *testing.T, ses *session.Session, base int) smallFixture {
	t.Helper()
	f := smallFixture{a: fmt.Sprintf("%064x", base), b: fmt.Sprintf("%064x", base+1), c: fmt.Sprintf("%064x", base+2)}
	for _, h := range []string{f.a, f.b, f.c} {
		withBlob(t, h)
	}
	f.aBig = sealedThumb(t, ses, thumbnailPath(f.a), 1000, 750)
	f.aSmall = sealedThumb(t, ses, smallThumbnailPath(f.a), 533, 400)
	f.bBig = sealedThumb(t, ses, thumbnailPath(f.b), 1000, 1333)
	sealedThumb(t, ses, smallThumbnailPath(f.c), 533, 400)
	return f
}

func searchRows(f smallFixture) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"hash", "mime", "created", "modified", "path", "size"})
	for i, h := range []string{f.a, f.b, f.c} {
		rows.AddRow(h, "image/jpeg", time.Now(), time.Now(), fmt.Sprintf("/p/%d.jpg", i), 1)
	}
	return rows
}

// tile is a grid row's thumbnail as a test expects it: its content and
// whether the row says it is the small one (File.thumbnail_small).
type tile struct {
	content []byte
	small   bool
}

func sameTiles(t *testing.T, what string, got []*pb.File, want ...tile) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d photos, want %d", what, len(got), len(want))
	}
	for i, w := range want {
		if !bytes.Equal(got[i].Content, w.content) {
			t.Errorf("%s: photo %d is not the expected thumbnail", what, i)
		}
		if got[i].ThumbnailSmall == nil || got[i].GetThumbnailSmall() != w.small {
			t.Errorf("%s: photo %d says small %v, want %v", what, i, got[i].ThumbnailSmall, w.small)
		}
	}
}

// SearchPhotos.small_thumbnails: the small thumbnail when asked for, the
// big one when not; the big one for content with no small one yet, which
// is then made in the background for the next look. Only processed
// content (a big thumbnail) is shown either way. Each row says which one
// it carries.
func TestImageSearchSmallThumbnails(t *testing.T) {
	_, ses := galleryTestEnv(t)
	quietQueue(t)
	f := newSmallFixture(t, ses, 0xf7000)
	db, mock, _ := sqlmock.New()
	defer db.Close()
	for i := 0; i < 3; i++ {
		mock.ExpectQuery("select `f`.`hash`, `f`.`mime`").WillReturnRows(searchRows(f))
	}
	mg := keptOut(&Manager{dao: dao.NewWithDB(db), searchTokens: newSearchTokenCache(1000)})
	search := func(small bool) []*pb.File {
		t.Helper()
		files, _, err := mg.ImageSearch(ses, "", nil, "", false, nil, "", nil, 0, 0, small, false)
		if err != nil {
			t.Fatal(err)
		}
		return files
	}

	sameTiles(t, "not asked", search(false), tile{f.aBig, false}, tile{f.bBig, false})
	sameTiles(t, "asked, b has none yet", search(true), tile{f.aSmall, true}, tile{f.bBig, false})
	waitForFile(t, smallThumbnailPath(f.b))
	waitQueueIdle(t, mg)
	if w, h := storedSize(t, ses, smallThumbnailPath(f.b)); w != 400 || h != 533 {
		t.Errorf("b's small thumbnail is %dx%d", w, h)
	}
	if w, h := storedSize(t, ses, thumbnailPath(f.b)); w != 750 || h != 1000 {
		t.Errorf("b's big thumbnail is %dx%d, want it within the cap", w, h)
	}
	sameTiles(t, "asked again", search(true), tile{f.aSmall, true}, tile{readSealed(t, ses, smallThumbnailPath(f.b)), true})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// GetThumbnails.small_thumbnails, the Files grid: the same rule, the same
// marker.
func TestFilesGridSmallThumbnails(t *testing.T) {
	_, ses := galleryTestEnv(t)
	quietQueue(t)
	f := newSmallFixture(t, ses, 0xf8000)
	db, mock, _ := sqlmock.New()
	defer db.Close()
	cols := []string{"hash", "mime", "created", "modified", "path", "size"}
	paths := []string{"/a.jpg", "/b.jpg", "/c.jpg"}
	expect := func() {
		for i, h := range []string{f.a, f.b, f.c} {
			mock.ExpectQuery("from `files` where `path` = \\?").WithArgs(paths[i]).
				WillReturnRows(sqlmock.NewRows(cols).AddRow(h, "image/jpeg", time.Now(), time.Now(), paths[i], 1))
		}
	}
	mg := &Manager{dao: dao.NewWithDB(db)}
	grid := func(small bool) []*pb.File {
		t.Helper()
		expect()
		files, askAgainFrom := mg.Thumbnails(ses, paths, small)
		if askAgainFrom != 0 {
			t.Errorf("ask again from %d, with every path looked at", askAgainFrom)
		}
		return files
	}

	sameTiles(t, "not asked", grid(false), tile{f.aBig, false}, tile{f.bBig, false})
	sameTiles(t, "asked", grid(true), tile{f.aSmall, true}, tile{f.bBig, false})
	waitForFile(t, smallThumbnailPath(f.b))
	waitQueueIdle(t, mg)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// ListImageGroups.small_thumbnails: the cover's small thumbnail.
func TestImageGroupCoverSmallThumbnail(t *testing.T) {
	_, ses := galleryTestEnv(t)
	quietQueue(t)
	f := newSmallFixture(t, ses, 0xf9000)
	mg := &Manager{}
	if g := mg.imageGroupToPB(ses, &dao.ImageGroup{ID: "g", Name: "Trip", CoverHash: f.a}, true); !bytes.Equal(g.CoverThumbnail, f.aSmall) {
		t.Error("asked: not the small cover")
	}
	if g := mg.imageGroupToPB(ses, &dao.ImageGroup{ID: "g", Name: "Trip", CoverHash: f.a}, false); !bytes.Equal(g.CoverThumbnail, f.aBig) {
		t.Error("not asked: not the big cover")
	}
	if g := mg.imageGroupToPB(ses, &dao.ImageGroup{ID: "g", Name: "Trip", CoverHash: f.b}, true); !bytes.Equal(g.CoverThumbnail, f.bBig) {
		t.Error("no small one yet: not the big cover")
	}
	waitForFile(t, smallThumbnailPath(f.b))
	waitQueueIdle(t, mg)
}

// The queue's worker gives way as the pass does (a grid scrolled during a
// sync must not keep the lanes' spare core busy): it waits for quiet
// before each file, and rests after each fix at least cThumbPassMinPause.
func TestThumbnailQueueGivesWay(t *testing.T) {
	_, ses := galleryTestEnv(t)
	quiet := make(chan struct{})
	var mu sync.Mutex
	var asked []time.Time
	queueWaitsOn(t, func(*Manager) bool {
		mu.Lock()
		asked = append(asked, time.Now())
		mu.Unlock()
		<-quiet
		return true
	})
	a, b := fmt.Sprintf("%064x", 0xfa001), fmt.Sprintf("%064x", 0xfa002)
	for _, h := range []string{a, b} {
		withBlob(t, h)
		sealedThumb(t, ses, thumbnailPath(h), 1000, 1333)
	}
	mg := &Manager{}
	mg.queueThumbnailFix(ses, a)
	mg.queueThumbnailFix(ses, b)
	time.Sleep(150 * time.Millisecond)
	if _, err := os.Stat(smallThumbnailPath(a)); !os.IsNotExist(err) {
		t.Fatal("fixed while uploads were arriving or the lanes were busy")
	}
	close(quiet)
	waitForFile(t, smallThumbnailPath(b))
	waitQueueIdle(t, mg)
	mu.Lock()
	defer mu.Unlock()
	// Before a, before b, and once more to find the queue empty.
	if len(asked) != 3 {
		t.Fatalf("waited for quiet %d times, want 3", len(asked))
	}
	// asked[0] waited until close(quiet); the rest come after a fix.
	if gap := asked[2].Sub(asked[1]); gap < cThumbPassMinPause {
		t.Errorf("only %v between two fixes, want a rest of at least %v", gap, cThumbPassMinPause)
	}
}

// A full reprocess empties the queue: it makes every thumbnail again.
func TestThumbnailQueueDropsForAReprocess(t *testing.T) {
	_, ses := galleryTestEnv(t)
	queueWaitsOn(t, func(*Manager) bool { return false })
	h := fmt.Sprintf("%064x", 0xfb001)
	withBlob(t, h)
	sealedThumb(t, ses, thumbnailPath(h), 1000, 1333)
	mg := &Manager{}
	mg.queueThumbnailFix(ses, h)
	waitQueueIdle(t, mg)
	if _, err := os.Stat(smallThumbnailPath(h)); !os.IsNotExist(err) {
		t.Error("fixed during a reprocess")
	}
	mg.thumbFixes.mu.Lock()
	n, again := len(mg.thumbFixes.jobs), mg.thumbFixes.queued[h]
	mg.thumbFixes.mu.Unlock()
	if n != 0 || again {
		t.Errorf("%d jobs left, %v still marked queued: the next look could not queue it", n, again)
	}
}

// What a fix holds from the content budget covers what it allocates -
// the kernel scaler's buffer (32 bytes x dstW x srcH) is most of it.
func TestThumbFixReservationCoversWhatItAllocates(t *testing.T) {
	_, ses := galleryTestEnv(t)
	for i, c := range []wh{{1000, 1333}, {1000, 750}, {640, 480}, {100, 3000}, {300, 200}, {1000, 30000}} {
		t.Run(fmt.Sprintf("%dx%d", c.w, c.h), func(t *testing.T) {
			path := thumbnailPath(fmt.Sprintf("%064x", 0xfc000+i))
			sealedThumb(t, ses, path, c.w, c.h)
			tw, th := fitWithin(c.w, c.h, 1000)
			shrink := tw != c.w || th != c.h
			// The least of three runs: a stray allocation elsewhere in
			// the test binary only adds.
			var alloc uint64
			for run := 0; run < 3; run++ {
				var before, after runtime.MemStats
				runtime.GC()
				runtime.ReadMemStats(&before)
				if _, _, err := remakeThumbnails(path, ses, 1000, shrink); err != nil {
					t.Fatal(err)
				}
				runtime.ReadMemStats(&after)
				if d := after.TotalAlloc - before.TotalAlloc; run == 0 || d < alloc {
					alloc = d
				}
			}
			reserved := thumbFixReserve(c.w, c.h, 1000)
			t.Logf("allocated %.1f MB, reserved %.1f MB", float64(alloc)/1e6, float64(reserved)/1e6)
			if int64(alloc) > reserved {
				t.Errorf("allocated %d bytes, reserved only %d", alloc, reserved)
			}
		})
	}
}
