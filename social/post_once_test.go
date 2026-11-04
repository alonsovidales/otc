// SPDX-License-Identifier: AGPL-3.0-or-later

package social

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
)

// noFiles is contentOf for a device with no file at any path.
func noFiles(string) string { return "" }

// The same post sent again - its answer lost while a long video was being
// cut - is made once: sent while the first is being made it gets that
// one's answer, and after it, within the window, its uuid. One that failed
// is made again, and a deleted one may be posted again.
func TestAPostSentAgainIsMadeOnce(t *testing.T) {
	var po postsOnce
	var made atomic.Int32
	release := make(chan struct{})
	slow := func() (string, error) {
		n := made.Add(1)
		<-release
		return fmt.Sprintf("post-%d", n), nil
	}
	key := postKey("our trip", []string{"/Phone/v.mov"}, []*pb.VideoTrim{{Path: "/Phone/v.mov", StartSecs: 2, EndSecs: 9}}, noFiles)

	// The first is being made; two more come meanwhile (a retry from a new
	// socket, a second tab).
	var wg sync.WaitGroup
	answers := make([]string, 3)
	for i := range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			uuid, err := po.once(key, slow)
			if err != nil {
				t.Error(err)
			}
			answers[i] = uuid
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if made.Load() != 1 || answers[0] != "post-1" || answers[1] != "post-1" || answers[2] != "post-1" {
		t.Fatalf("made %d, answered %v", made.Load(), answers)
	}
	// After it, within the window: the same uuid, nothing made.
	if uuid, err := po.once(key, slow); err != nil || uuid != "post-1" || made.Load() != 1 {
		t.Fatalf("again: %q %v, made %d", uuid, err, made.Load())
	}
	// Another post - other text, another cut - is another.
	other := postKey("our trip", []string{"/Phone/v.mov"}, []*pb.VideoTrim{{Path: "/Phone/v.mov", StartSecs: 3, EndSecs: 9}}, noFiles)
	if other == key || postKey("our trip!", []string{"/Phone/v.mov"}, nil, noFiles) == key || postKey("our trip", []string{"/Phone/v.mov", "/a.jpg"}, nil, noFiles) == postKey("our trip", []string{"/a.jpg", "/Phone/v.mov"}, nil, noFiles) {
		t.Fatal("different posts share a key")
	}
	if postKey("a", []string{"bc"}, nil, noFiles) == postKey("ab", []string{"c"}, nil, noFiles) {
		t.Fatal("the text and the paths run into each other")
	}

	// Deleted: posted again, it is made again.
	po.forget("post-1")
	if uuid, _ := po.once(key, func() (string, error) { return "post-2", nil }); uuid != "post-2" {
		t.Fatalf("after deleting it: %q", uuid)
	}

	// One that failed is made again.
	failing := postKey("fails", nil, nil, noFiles)
	tries := 0
	fail := func() (string, error) { tries++; return "", errors.New("the video could not be cut") }
	po.once(failing, fail)
	po.once(failing, fail)
	if tries != 2 {
		t.Fatalf("a failed post was tried %d times", tries)
	}

	// Past the window, the same text and files are a new post.
	saved := cPostAgainWindow
	cPostAgainWindow = 0
	t.Cleanup(func() { cPostAgainWindow = saved })
	if uuid, _ := po.once(key, func() (string, error) { return "post-3", nil }); uuid != "post-3" {
		t.Fatalf("past the window: %q", uuid)
	}
}

// At most cMaxRecentPosts are remembered: the oldest goes.
func TestRecentPostsAreBounded(t *testing.T) {
	var po postsOnce
	for i := range cMaxRecentPosts + 10 {
		po.once(postKey(fmt.Sprint(i), nil, nil, noFiles), func() (string, error) { return fmt.Sprint(i), nil })
	}
	if n := len(po.made); n != cMaxRecentPosts {
		t.Fatalf("%d remembered", n)
	}
}

// A post whose making panics (the request's handler recovers it) gives
// whoever waited for it an error, and is made again when sent again rather
// than staying "being made" for good.
func TestAPostThatPanickedIsMadeAgain(t *testing.T) {
	var po postsOnce
	key := postKey("boom", nil, nil, noFiles)
	release := make(chan struct{})
	waited := make(chan error, 1)
	go func() {
		defer func() { recover() }()
		po.once(key, func() (string, error) {
			go func() {
				_, err := po.once(key, func() (string, error) { return "never", nil })
				waited <- err
			}()
			<-release
			panic("a bug while cutting the video")
		})
	}()
	time.Sleep(50 * time.Millisecond)
	close(release)
	select {
	case err := <-waited:
		if err == nil {
			t.Fatal("the one waiting got no error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the one waiting never got an answer")
	}
	if uuid, err := po.once(key, func() (string, error) { return "post-2", nil }); err != nil || uuid != "post-2" {
		t.Fatalf("sent again: %q %v", uuid, err)
	}
}

// A post is its files' content, not where they were uploaded: the web app
// sends a file from the computer to a new path each time, and the same
// video sent again from a composer opened anew is the same post. Other
// content at the same path is another post, and a cut goes with its file.
func TestAPostIsItsFilesContent(t *testing.T) {
	content := map[string]string{
		"/web/1730000000-abc123-trip.mov": "hash-of-the-video",
		"/web/1730000300-def456-trip.mov": "hash-of-the-video",
		"/Phone/trip.mov":                 "hash-of-the-video",
		"/web/1730000400-ghi789-trip.mov": "hash-of-another-video",
		"/a.jpg":                          "hash-of-a-photo",
	}
	contentOf := func(p string) string { return content[p] }
	cut := func(p string, start, end float64) []*pb.VideoTrim {
		return []*pb.VideoTrim{{Path: p, StartSecs: start, EndSecs: end}}
	}
	first := postKey("our trip", []string{"/web/1730000000-abc123-trip.mov"}, cut("/web/1730000000-abc123-trip.mov", 2, 9), contentOf)
	if again := postKey("our trip", []string{"/web/1730000300-def456-trip.mov"}, cut("/web/1730000300-def456-trip.mov", 2, 9), contentOf); again != first {
		t.Fatal("the same video uploaded again to another path is another post")
	}
	if postKey("our trip", []string{"/Phone/trip.mov"}, cut("/Phone/trip.mov", 2, 9), contentOf) != first {
		t.Fatal("the same video in the library is another post")
	}
	for name, other := range map[string][sha256.Size]byte{
		"another video":   postKey("our trip", []string{"/web/1730000400-ghi789-trip.mov"}, cut("/web/1730000400-ghi789-trip.mov", 2, 9), contentOf),
		"another cut":     postKey("our trip", []string{"/web/1730000300-def456-trip.mov"}, cut("/web/1730000300-def456-trip.mov", 3, 9), contentOf),
		"the cut lost":    postKey("our trip", []string{"/web/1730000300-def456-trip.mov"}, cut("/web/1730000000-abc123-trip.mov", 2, 9), contentOf),
		"one file more":   postKey("our trip", []string{"/Phone/trip.mov", "/a.jpg"}, cut("/Phone/trip.mov", 2, 9), contentOf),
		"a path for none": postKey("our trip", []string{"/gone.mov"}, cut("/gone.mov", 2, 9), contentOf),
	} {
		if other == first {
			t.Errorf("%s: the same post", name)
		}
	}
	if postKey("our trip", []string{"/Phone/trip.mov", "/a.jpg"}, nil, contentOf) == postKey("our trip", []string{"/a.jpg", "/Phone/trip.mov"}, nil, contentOf) {
		t.Error("files in another order are the same post")
	}
	// A path named like a hash is not that file.
	if postKey("x", []string{"hash-of-the-video"}, nil, contentOf) == postKey("x", []string{"/Phone/trip.mov"}, nil, contentOf) {
		t.Error("a missing file's path taken for a file's content")
	}
	// A path the device has no file at is still told from another.
	if postKey("x", []string{"/gone-1"}, nil, contentOf) == postKey("x", []string{"/gone-2"}, nil, contentOf) {
		t.Error("two missing files are one")
	}
}
