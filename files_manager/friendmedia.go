// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"sync"
	"time"
)

// cFriendMediaWait is the longest the friend sync waits for room in the
// content budget before pulling a file anyway.
const cFriendMediaWait = 2 * time.Minute

// ReserveFriendMedia holds the content budget while the friend sync pulls
// one file of a friend's post whole, declaredSize as the friend lists it:
// the read buffer while it grows, the decoded copy and the write. It was
// the one path carrying file content outside the budget. The wait is
// bounded: the friend's device may be waiting on this device's budget to
// serve it, and two unbounded waits locked both until the media timed out
// and was lost for good. Never nil.
func (mg *Manager) ReserveFriendMedia(declaredSize int64) func() {
	if mg == nil || mg.contentBudget == nil || declaredSize <= 0 {
		return func() {}
	}
	return mg.contentBudget.acquireWithin(declaredSize*cDownloadCopies, cFriendMediaWait)
}

// acquireWithin is acquire waiting at most d: then it takes the bytes
// anyway, even past max. Whatever arrives after it still waits for room.
func (b *memBudget) acquireWithin(n int64, d time.Duration) func() {
	if n <= 0 {
		return func() {}
	}
	if n > b.max {
		n = b.max
	}
	expired := false // guarded by b.mu
	timer := time.AfterFunc(d, func() {
		b.mu.Lock()
		expired = true
		b.mu.Unlock()
		b.cond.Broadcast()
	})
	b.mu.Lock()
	for !expired && b.used > 0 && b.used+n > b.max {
		b.cond.Wait()
	}
	b.used += n
	b.mu.Unlock()
	timer.Stop()

	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			b.used -= n
			b.mu.Unlock()
			b.cond.Broadcast()
		})
	}
}
