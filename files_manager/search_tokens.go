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

// searchCursor is where a token is in its search's results: all[off:] not
// served yet, and all[prev:off] the rows its last page was served from (a
// few of them left out for want of a thumbnail, issue #147), kept so that
// page can be served again (resume). Never changed once stored - two
// requests naming the same token (a quick double scroll, issue #171) may
// read it at once - so each page stores a new one.
type searchCursor struct {
	all       []*pb.File
	off, prev int
	// SearchPhotos.have when the last page was asked for, and what that
	// page left the client with: before plus its photos.
	before, after int32
	// synced: the last page was asked for with the after of the page
	// before it, so this client counts what the token sends, photo for
	// photo, and a have of before means the page never reached it.
	synced bool
	// pages counts the pages the token went on by (a page served again
	// keeps it), and seq is when the request that stored this started
	// (searchTokenCache.begin): a request finishing late never stores an
	// older place over a newer one, nor, for the same page, its page over
	// that of a request started after it - the client's retry, whose
	// answer it keeps while the first one's is dropped. again, the times in
	// a row the last page was served again.
	pages, again int
	seq          uint64
	// deletes is the cache's count of deleted files when the request that
	// stored this started: a client lowers have by the photos it deletes,
	// so after a delete a have of before can be the page received and as
	// many photos deleted.
	deletes uint64
}

// cMaxServedAgain is how many times in a row a token serves its last page
// again (resume) before going on: a client that keeps asking with the
// same have although every answer reaches it (one that drops every photo
// of a page as a duplicate) is never held on one page.
const cMaxServedAgain = 5

// resume is where the page asked for with have, by a request that found
// the cache's count of deleted files at deletes, starts in cur.all, and
// whether it is cur's last page served again. A client that got the last
// page asks with its after and goes on. One asking with its before again
// never got it - an answer lost on the way: a timeout that drops late
// answers, a socket closed mid-reply, the bridge's 90 s answer - and is
// served it again, rather than the page after, which left a hole in its
// grid for good. Only once the client is in step (synced): a page it got
// whose photos it all had already (a search resumed from an unknown
// token, after uploads moved it on) leaves its have where it was too, and
// that page must go on, not come again. Nor after a file was deleted
// since that page was served (anywhere: the cache doesn't know whose):
// a client that deleted as many photos as the page brought asks with
// before too, and holds the page. Anything else goes on, as before
// this existed: no have (clients that don't send it, older apps), or a
// count the token can't place (it only knows the client's count at its
// last page, and serving again on a guess could only repeat photos, or
// loop on a page).
func (cur *searchCursor) resume(have int32, deletes uint64) (from int, again bool) {
	if have > 0 && have != cur.after && have == cur.before && cur.synced && cur.again < cMaxServedAgain && cur.deletes == deletes {
		return cur.prev, true
	}
	return cur.off, false
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
	// seq numbers the searches as they start (begin); deletes counts the
	// files deleted (noteDelete).
	seq, deletes uint64
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

// begin is what a search reads as it starts, after generation: its place
// in the order searches start in, and the count of files deleted so far,
// for its cursor (searchCursor.seq, deletes) and resume.
func (c *searchTokenCache) begin() (seq, deletes uint64) {
	if c == nil {
		return 0, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	return c.seq, c.deletes
}

// noteDelete counts a file deleted (delFile): no token's last page is
// served again across it (searchCursor.resume).
func (c *searchTokenCache) noteDelete() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deletes++
}

// store keeps cur under token, as used at now, then drops the least
// recently used other tokens while the rows kept are over the cap. gen is
// generation() from before the search read anything: a folder kept out of
// Images since (issue #192) is left out of cur first, or cur dropped when
// what was kept out isn't known - a page in flight must not put back what
// keepOut just took out. A cur behind the one stored is dropped instead:
// fewer pages (its request finished after a later one went on), or the
// same page by a request that started before the stored one's.
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
		if old.cur.pages > cur.pages || (old.cur.pages == cur.pages && old.cur.seq > cur.seq) {
			// A request that started later went on past this page, or
			// served it too, while this one was being served (a retry
			// overtook it): the token never goes back, and keeps the
			// page of the retry, the answer the client keeps - not one
			// from thumbnails read earlier, which can end elsewhere
			// (issue #147).
			return
		}
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
// now (issue #192) - out of what every token has left, its last page
// included: a client still scrolling one goes on from where it is, never
// handed a photo of a folder just kept out, even by a page it asks for
// again. Pages it got stay as they were. Content shown again, or still
// shown through a path uniqueByHash left out of the token, turns up at the
// next search, as an upload during a scroll does.
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

// withoutFolders is cur without the rows under folders from its last page
// on: neither served again nor served next. cur itself when there are
// none, so nothing is copied for a token they don't touch.
func withoutFolders(cur *searchCursor, folders []string) *searchCursor {
	rest := cur.all[cur.prev:]
	if !slices.ContainsFunc(rest, func(f *pb.File) bool { return underFolders(f.Path, false, folders) }) {
		return cur
	}
	kept := make([]*pb.File, 0, len(rest))
	off := 0
	for i, f := range rest {
		if underFolders(f.Path, false, folders) {
			continue
		}
		if cur.prev+i < cur.off {
			off++
		}
		kept = append(kept, f)
	}
	out := *cur
	out.all, out.prev, out.off = kept, 0, off
	return &out
}

func (c *searchTokenCache) drop(token string) {
	if e, ok := c.by[token]; ok {
		c.rows -= len(e.cur.all)
		delete(c.by, token)
	}
}

// pageCursor is what a token keeps once a request with have, that found
// last (nil: a new search), was served the rows all[from:from+next] as a
// page of n photos - last's page served again when again. The rows from
// the page on stay, so it can be served again; those before it only until
// they outnumber them: then the page and what follows it are copied once,
// so a full scroll copies O(n) rows in all instead of the whole rest at
// every page (O(n^2)).
func pageCursor(last *searchCursor, all []*pb.File, from, next int, have int32, n int, again bool) *searchCursor {
	cur := &searchCursor{all: all, prev: from, off: from + next, before: have, after: have + int32(n)}
	if from > len(all)-from {
		cur.all = append([]*pb.File(nil), all[from:]...)
		cur.prev, cur.off = 0, next
	}
	switch {
	case last == nil:
	case again:
		cur.synced, cur.pages, cur.again = last.synced, last.pages, last.again+1
	default:
		cur.synced = have > 0 && have == last.after
		cur.pages = last.pages + 1
	}
	return cur
}
