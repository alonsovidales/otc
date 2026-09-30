// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import "sync"

// Issue #173: the content hashes this device knows are missing from the
// disk. A listing used to stat every file's blob (issue #141: a file whose
// content is gone is listed without a hash, so a client that has it sends
// it again) - 6,000 stats for one folder, and the desktop clients list
// their folders every minute. hasBlob, the one place that looks at the
// disk, records each answer here: the storage integrity check (a minute
// after start, then daily) visits every hash, reads that find a blob gone
// add it, and the upload that brings it back removes it. Listings consult
// this and never touch the disk.
type blobSet struct {
	mu sync.RWMutex
	m  map[string]bool
}

var missingBlobs = &blobSet{m: map[string]bool{}}

func (s *blobSet) set(hash string, missing bool) {
	s.mu.Lock()
	if missing {
		s.m[hash] = true
	} else {
		delete(s.m, hash)
	}
	s.mu.Unlock()
}

func (s *blobSet) has(hash string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.m[hash]
}
