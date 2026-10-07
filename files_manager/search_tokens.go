// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"slices"
	"sync"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
)

// A search token holds the rest of a search's results, so the next page
// is a slice of them instead of the query again (see ImageSearch). Every
// gallery mount, filter change, date jump and picker starts a new search,
// and the client simply drops the token it had: those stayed until
// cToeknsTTL after their last use, with nothing bounding how many - about
// 40 MB each for a 100k-photo library. Past cSearchTokensMaxRows rows in
// all, the least recently used tokens go; a token used again after that
// is an unknown one, which ImageSearch already answers by searching again
// (skipping what the client has, when it says).
const cSearchTokensMaxRows = 250_000

// searchCursor is a token's results not served yet: all[off:]. Never
// changed once stored - two requests naming the same token (a quick double
// scroll, issue #171) may read it at once - so each page stores a new one.
type searchCursor struct {
	all []*pb.File
	off int
}

type searchTokenEntry struct {
	cur  *searchCursor
	used time.Time
}

type searchTokenCache struct {
	mu   sync.Mutex
	by   map[string]*searchTokenEntry
	rows int // len(cur.all) over every entry: what they keep in memory
	max  int
	// Issue #192: gen counts keepOut and clear calls, excluded is the
	// folders the last one left out (nil after a clear: unknown). A search
	// that started before one stores what it found through them.
	gen      uint64
	excluded []string
}

func newSearchTokenCache(maxRows int) *searchTokenCache {
	return &searchTokenCache{by: map[string]*searchTokenEntry{}, max: maxRows}
}

func (c *searchTokenCache) load(token string) (*searchCursor, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.by[token]
	if !ok {
		return nil, false
	}
	return e.cur, true
}

// generation is what a search reads before it reads the folders kept out
// of Images or loads its token, for store.
func (c *searchTokenCache) generation() uint64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

// store keeps cur under token, as used at now, then drops the least
// recently used other tokens while the rows kept are over the cap. gen is
// generation() from before the search read anything: a folder kept out of
// Images since (issue #192) is left out of cur first, or cur dropped when
// what was kept out isn't known - a page in flight must not put back what
// keepOut just took out.
func (c *searchTokenCache) store(token string, cur *searchCursor, now time.Time, gen uint64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		if c.excluded == nil {
			c.drop(token)
			return
		}
		cur = withoutFolders(cur, c.excluded)
	}
	if old, ok := c.by[token]; ok {
		c.rows -= len(old.cur.all)
	}
	c.by[token] = &searchTokenEntry{cur: cur, used: now}
	c.rows += len(cur.all)
	for c.rows > c.max {
		oldest := ""
		for t, e := range c.by {
			if t != token && (oldest == "" || e.used.Before(c.by[oldest].used)) {
				oldest = t
			}
		}
		if oldest == "" {
			return // only this one: it stays, whatever its size
		}
		c.drop(oldest)
	}
}

// expire drops the tokens not used for ttl.
func (c *searchTokenCache) expire(now time.Time, ttl time.Duration) (gone []string) {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for t, e := range c.by {
		if now.Sub(e.used) > ttl {
			c.drop(t)
			gone = append(gone, t)
		}
	}
	return gone
}

// keepOut leaves the rows under folders - every folder kept out of Images
// now (issue #192) - out of what every token has left: a client still
// scrolling one goes on from where it is, never handed a photo of a
// folder just kept out. Rows served already stay as they were. Content
// shown again, or still shown through a path uniqueByHash left out of the
// token, turns up at the next search, as an upload during a scroll does.
func (c *searchTokenCache) keepOut(folders []string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.excluded = append([]string{}, folders...)
	for _, e := range c.by {
		cur := withoutFolders(e.cur, c.excluded)
		c.rows += len(cur.all) - len(e.cur.all)
		e.cur = cur
	}
}

// clear drops every token: keepOut when the folders kept out of Images
// can't be read. A client resuming one searches again from what it has,
// as for an expired token.
func (c *searchTokenCache) clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.excluded = nil
	c.by = map[string]*searchTokenEntry{}
	c.rows = 0
}

// withoutFolders is cur without the rows not served yet under folders;
// cur itself when there are none, so nothing is copied for a token they
// don't touch.
func withoutFolders(cur *searchCursor, folders []string) *searchCursor {
	rest := cur.all[cur.off:]
	if !slices.ContainsFunc(rest, func(f *pb.File) bool { return underFolders(f.Path, false, folders) }) {
		return cur
	}
	kept := make([]*pb.File, 0, len(rest))
	for _, f := range rest {
		if !underFolders(f.Path, false, folders) {
			kept = append(kept, f)
		}
	}
	return &searchCursor{all: kept}
}

func (c *searchTokenCache) drop(token string) {
	if e, ok := c.by[token]; ok {
		c.rows -= len(e.cur.all)
		delete(c.by, token)
	}
}

// nextCursor is what a token keeps once next more of cur's rows were
// served. The rows already served stay pinned only while fewer than those
// left: then what is left is copied once, so a full scroll copies O(n)
// rows in all instead of the whole rest at every page (O(n^2)).
func nextCursor(all []*pb.File, off, next int) *searchCursor {
	off += next
	if off > len(all)-off {
		all = append([]*pb.File(nil), all[off:]...)
		off = 0
	}
	return &searchCursor{all: all, off: off}
}
