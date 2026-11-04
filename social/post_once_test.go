// SPDX-License-Identifier: AGPL-3.0-or-later

package social

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
)

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
	key := postKey("our trip", []string{"/Phone/v.mov"}, []*pb.VideoTrim{{Path: "/Phone/v.mov", StartSecs: 2, EndSecs: 9}})

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
	other := postKey("our trip", []string{"/Phone/v.mov"}, []*pb.VideoTrim{{Path: "/Phone/v.mov", StartSecs: 3, EndSecs: 9}})
	if other == key || postKey("our trip!", []string{"/Phone/v.mov"}, nil) == key || postKey("our trip", []string{"/Phone/v.mov", "/a.jpg"}, nil) == postKey("our trip", []string{"/a.jpg", "/Phone/v.mov"}, nil) {
		t.Fatal("different posts share a key")
	}
	if postKey("a", []string{"bc"}, nil) == postKey("ab", []string{"c"}, nil) {
		t.Fatal("the text and the paths run into each other")
	}

	// Deleted: posted again, it is made again.
	po.forget("post-1")
	if uuid, _ := po.once(key, func() (string, error) { return "post-2", nil }); uuid != "post-2" {
		t.Fatalf("after deleting it: %q", uuid)
	}

	// One that failed is made again.
	failing := postKey("fails", nil, nil)
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
		po.once(postKey(fmt.Sprint(i), nil, nil), func() (string, error) { return fmt.Sprint(i), nil })
	}
	if n := len(po.made); n != cMaxRecentPosts {
		t.Fatalf("%d remembered", n)
	}
}
