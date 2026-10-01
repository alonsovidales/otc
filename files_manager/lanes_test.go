// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
)

// The analysis lane waits while the fast lane has anything queued or
// running, then works through what accumulated.
func TestLanesAnalysisWaitsForThumbnails(t *testing.T) {
	var mu sync.Mutex
	var order []string
	release := make(chan struct{})
	analysed := make(chan struct{}, 10)

	ls := newMediaLanes(2, func(j mediaJob) bool {
		<-release
		mu.Lock()
		order = append(order, "thumb:"+j.file.Hash)
		mu.Unlock()
		return j.file.Hash != "bad"
	}, func(j mediaJob) {
		mu.Lock()
		order = append(order, "analysis:"+j.file.Hash)
		mu.Unlock()
		analysed <- struct{}{}
	})

	for _, h := range []string{"a", "b", "bad", "c"} {
		ls.fast.push(mediaJob{file: &pb.File{Hash: h}})
	}
	// Thumbnails one at a time: no analysis may start before the last.
	for i := 0; i < 4; i++ {
		release <- struct{}{}
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		for _, o := range order {
			if strings.HasPrefix(o, "analysis:") && i < 3 {
				mu.Unlock()
				t.Fatalf("analysis ran while thumbnails were pending: %v", order)
			}
		}
		mu.Unlock()
	}
	for i := 0; i < 3; i++ {
		select {
		case <-analysed:
		case <-time.After(2 * time.Second):
			t.Fatalf("analysis never ran: %v", order)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, o := range order {
		if o == "analysis:bad" {
			t.Fatalf("an undecodable file went on to analysis: %v", order)
		}
	}
}

func TestIsMedia(t *testing.T) {
	for _, c := range []struct {
		f    *pb.File
		want bool
	}{
		{&pb.File{Mime: "image/jpeg", Path: "/a.jpg"}, true},
		{&pb.File{Mime: "video/mp4", Path: "/a.mp4"}, true},
		{&pb.File{Mime: "application/octet-stream", Path: "/a.HEIC"}, true},
		{&pb.File{Mime: "application/pdf", Path: "/a.pdf"}, false},
	} {
		if got := isMedia(c.f); got != c.want {
			t.Errorf("isMedia(%s) = %v", c.f.Path, got)
		}
	}
}
