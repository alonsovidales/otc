// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	pb "github.com/alonsovidales/otc/proto/generated"
)

// passLibrary is a library for the pass: n media files by hash, plus a
// document it must not touch.
func passLibrary(n int) []*pb.File {
	var files []*pb.File
	for i := 0; i < n; i++ {
		files = append(files, &pb.File{Hash: fmt.Sprintf("%064x", i+1), Mime: "image/jpeg", Path: fmt.Sprintf("/p/%d.jpg", i)})
	}
	files = append(files, &pb.File{Hash: fmt.Sprintf("%064x", n+1), Mime: "image/vnd.djvu", Path: "/scan.djvu"})
	sort.Slice(files, func(i, j int) bool { return files[i].Hash < files[j].Hash })
	return files
}

type passRecorder struct {
	fixed  []string
	afters []string
}

func testPass(t *testing.T, storage string, files []*pb.File, rec *passRecorder, idle func() bool) *thumbPass {
	t.Helper()
	if idle == nil {
		idle = func() bool { return true }
	}
	return &thumbPass{
		storage: storage,
		maxSide: 1000,
		// As ListMediaForReprocess: by hash, after the cursor, n at most.
		list: func(after string, n int) ([]*pb.File, error) {
			rec.afters = append(rec.afters, after)
			var out []*pb.File
			for _, f := range files {
				if f.Hash > after && len(out) < n {
					out = append(out, f)
				}
			}
			return out, nil
		},
		idle: idle,
		fix: func(hash string) thumbFix {
			rec.fixed = append(rec.fixed, hash)
			return thumbFixed
		},
	}
}

func passState(t *testing.T, storage string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(storage, cThumbPassState))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// The pass goes through every photo and video once, records that it is
// done, and doesn't run again.
func TestThumbnailPassRunsOnce(t *testing.T) {
	storage := t.TempDir()
	files := passLibrary(5)
	rec := &passRecorder{}
	testPass(t, storage, files, rec, nil).run()
	if len(rec.fixed) != 5 {
		t.Errorf("fixed %d files, want the 5 photos (not the document)", len(rec.fixed))
	}
	if got := passState(t, storage); got != "1000 400 done" {
		t.Errorf("state %q", got)
	}
	rec2 := &passRecorder{}
	testPass(t, storage, files, rec2, nil).run()
	if len(rec2.fixed) != 0 || len(rec2.afters) != 0 {
		t.Errorf("ran again: %d fixed, %d listings", len(rec2.fixed), len(rec2.afters))
	}
}

// Stopped (a full reprocess), it records where it was and continues there
// at the next start.
func TestThumbnailPassResumesWhereItStopped(t *testing.T) {
	storage := t.TempDir()
	files := passLibrary(6)
	rec := &passRecorder{}
	calls := 0
	testPass(t, storage, files, rec, func() bool { calls++; return calls <= 3 }).run()
	if len(rec.fixed) != 3 {
		t.Fatalf("fixed %d before stopping, want 3", len(rec.fixed))
	}
	last := rec.fixed[2]
	if got := passState(t, storage); got != "1000 400 "+last {
		t.Errorf("state %q, want it to name the last file done", got)
	}

	rec2 := &passRecorder{}
	testPass(t, storage, files, rec2, nil).run()
	if len(rec2.afters) == 0 || rec2.afters[0] != last {
		t.Fatalf("resumed after %q, want %q", rec2.afters, last)
	}
	for _, h := range rec2.fixed {
		for _, done := range rec.fixed {
			if h == done {
				t.Errorf("%s fixed twice", h)
			}
		}
	}
	if len(rec.fixed)+len(rec2.fixed) != 6 {
		t.Errorf("fixed %d in all, want 6", len(rec.fixed)+len(rec2.fixed))
	}
	if got := passState(t, storage); got != "1000 400 done" {
		t.Errorf("state %q", got)
	}
}

// The state says which sizes the thumbnails were checked against: another
// small size (a later release) or a lower cap (the ini) checks everything
// again; a higher cap needs nothing; an unreadable state runs the pass.
func TestThumbnailPassRunsAgainWhenTheSizesChange(t *testing.T) {
	for _, c := range []struct {
		name, state string
		runs        bool
		after       string
	}{
		{"another small size", "1000 300 done", true, "1000 400 done"},
		{"a lower cap", "1200 400 done", true, "1000 400 done"},
		{"a higher cap", "800 400 done", false, "1000 400 done"},
		{"garbage", "done", true, "1000 400 done"},
		{"started, nothing done", "1000 400 -", true, "1000 400 done"},
	} {
		t.Run(c.name, func(t *testing.T) {
			storage := t.TempDir()
			if err := os.WriteFile(filepath.Join(storage, cThumbPassState), []byte(c.state+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			rec := &passRecorder{}
			testPass(t, storage, passLibrary(2), rec, nil).run()
			if ran := len(rec.fixed) > 0; ran != c.runs {
				t.Errorf("ran: %v, want %v", ran, c.runs)
			}
			if got := passState(t, storage); got != c.after {
				t.Errorf("state %q, want %q", got, c.after)
			}
		})
	}
}

// The real thing, end to end on the disk: only what is needed changes, and
// a second pass (state removed) finds nothing to do.
func TestThumbnailPassFixesTheLibrary(t *testing.T) {
	_, ses := galleryTestEnv(t)
	storage := t.TempDir()
	mg := &Manager{}
	var files []*pb.File
	for i, big := range []wh{{1000, 750}, {1000, 1333}, {100, 3000}} {
		h := fmt.Sprintf("%064x", 0xfa000+i)
		withBlob(t, h)
		sealedThumb(t, ses, thumbnailPath(h), big.w, big.h)
		files = append(files, &pb.File{Hash: h, Mime: "image/jpeg"})
	}
	run := func() (fixed int) {
		p := testPass(t, storage, files, &passRecorder{}, nil)
		p.fix = func(hash string) thumbFix {
			r := mg.fixThumbnailsSafely(ses, hash, 1000)
			if r == thumbFixed {
				fixed++
			}
			return r
		}
		p.run()
		return fixed
	}
	if n := run(); n != 3 {
		t.Errorf("fixed %d, want 3", n)
	}
	for i, want := range []wh{{1000, 750}, {750, 1000}, {33, 1000}} {
		if w, h := storedSize(t, ses, thumbnailPath(files[i].Hash)); w != want.w || h != want.h {
			t.Errorf("file %d: big %dx%d, want %dx%d", i, w, h, want.w, want.h)
		}
		if _, err := os.Stat(smallThumbnailPath(files[i].Hash)); err != nil {
			t.Errorf("file %d has no small thumbnail", i)
		}
	}
	os.Remove(filepath.Join(storage, cThumbPassState))
	if n := run(); n != 0 {
		t.Errorf("a second pass fixed %d", n)
	}
}
