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

	ls := newMediaLanes(2, 2, func(j mediaJob) bool {
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

// The fast lane runs a job per 2 GB at most and the analysis lane three,
// both with a CPU left free: a Raspberry Pi 5 does as before, a 32-thread
// PC no longer runs 31 of each (more than its 32 GB hold).
func TestProcessingWorkersByCPUsAndMemory(t *testing.T) {
	gb := func(n float64) MemoryProfile { return MemoryProfile{Total: int64(n * (1 << 30))} }
	for _, c := range []struct {
		name           string
		cpus           int
		mem            MemoryProfile
		fast, analysis int
	}{
		{"Pi 5, 8 GB", 4, gb(7.9), 3, 3},
		{"Pi 5, 16 GB", 4, gb(15.8), 3, 3},
		{"Pi 5, 4 GB", 4, gb(3.7), 2, 3},
		{"PC, 32 threads, 32 GB", 32, gb(31.3), 16, 3},
		{"PC, 16 threads, 16 GB", 16, gb(15.5), 8, 3},
		{"PC, 16 threads, 64 GB", 16, gb(62.7), 15, 3},
		{"PC, 8 threads, 8 GB", 8, gb(7.7), 4, 3},
		{"one CPU", 1, gb(7.9), 1, 1},
		{"two CPUs", 2, gb(1.8), 1, 1},
		{"memory unknown", 32, MemoryProfile{}, 3, 3},
	} {
		fast, analysis := processingWorkersFor(c.cpus, c.mem)
		if fast != c.fast || analysis != c.analysis {
			t.Errorf("%s: %d fast, %d analysis; want %d, %d", c.name, fast, analysis, c.fast, c.analysis)
		}
	}
}
