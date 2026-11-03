// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"slices"
	"sort"
	"time"

	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
)

// Issue #206: no Images search reads the whole library, or all its own
// rows, when it starts - opening Images, a jump, scrolling up after one,
// the pickers, a tag, a person, a collection, a map area, a text, a rating
// or a folder's gallery. Its token holds the rows of the pages it has
// served and is serving, and fetches the next chunk of the search only
// when a page needs more: the same photos in the same order, a page at a
// time. How a chunk is read depends on the search (searchFor):
//
//   - In date order (the plain browse, people, a collection, an area, a
//     rating's filters, a folder, a tag going up) by keyset over the
//     `created` index (dao.BrowseMedia) - unless the filters keep few
//     photos (cListMax by dao.EstimateMedia's counts), which a keyset
//     would find only after reading most of the library: then through a
//     list of them by date, read once from the smallest filter's own table
//     (dao.DatedMedia: a hash and a date each).
//   - Ranked by the tags' score or the collection's stars through a list
//     of them in that order (dao.RankMedia), from the tags', faces' and
//     collections' tables alone.
//   - A text (Smart search) through the list of its photos' cosines to it,
//     best first, cut where rankScores cuts (textList).
//
// A list's chunks are read by dao.MediaListChunk: the photos of the next
// hashes, each a whole tie (a second, a score), SearchMediaRated's rows for
// them. So a token holds what it fetched (searchCursor.all): the pages
// served, as before (pageCursor), and the rest of the last chunk - and a
// list's hashes. A page served again (resume), a page missing a thumbnail
// (#147) and keepOut work on it as on a whole search; a page that runs
// past it fetches more. A token goes on as it began.
//
// Unlike a whole search, a chunk is read when it is needed: photos added
// below the place a scroll has reached (an upload of an older photo) come
// in this scroll, where a whole search left them for the next one (a
// list's photos are the ones it found when the search began); one deleted
// meanwhile doesn't come.

// browseFunc reads a keyset search's next chunk: dao.BrowseMedia.
type browseFunc func(b *dao.MediaBrowse, from *time.Time, want int) ([]*pb.File, *time.Time, bool, error)

// browseChunk is the fewest photos a token fetches when its page needs
// more (the first page of a search fetches only what it shows, to come
// fast): a page and a bit of the phones' largest (cMaxPageWithoutThumbnails),
// ~250 bytes a row. A variable so tests can make every page fetch.
var browseChunk = 240

// cListMax is the most photos a date-ordered search's filters may keep,
// by dao.EstimateMedia's count, to be read through a list (dao.DatedMedia)
// rather than by keyset. 200k photos (Lima VM, MariaDB 11.8): a list of
// 20,000 takes 50-80 ms to read and ~2 MB to keep, and its pages ~0.5 ms;
// by keyset the first page of a collection of 20,000 imported years down
// read 400 ms of the library, of 100 photos 300 ms. Past it the filters
// keep a good share of the library, which a keyset finds as it goes
// (an area of 90,000 photos: 50 ms, where its list took 265 ms).
var cListMax = 30000

// cListRows is how many of a list's hashes the token cache counts as one
// row (searchTokenCache's cap): a hash and its rank take ~100 bytes, a row
// ~250.
const cListRows = 2

// browseState is where a search's token is in its photos: what the next
// chunk is asked with. Never changed once a cursor holding it is stored
// (searchCursor), so each fetch makes a new one.
type browseState struct {
	query dao.MediaBrowse
	// By keyset (list nil): from is where the next chunk starts
	// (BrowseMedia's next).
	from *time.Time
	// list, when the search is read through one, and at where its next
	// chunk starts in it; end is where it stops (a text's cut, once a
	// chunk found its best photo: cut, below).
	list    *searchList
	at, end int
	cut     bool
	// done: every photo of the search has been fetched.
	done bool
	// dropped is every folder keepOut has taken out of the token since the
	// search began (issue #192): a chunk's rows under them are left out,
	// as keepOut leaves out those it holds. The query keeps the folders
	// kept out when the search began, so the copy each photo is shown at
	// is the one the search began with, as for a whole search.
	dropped []string
}

// searchList is a search's photos in its order, by hash (shared by every
// cursor of its token, never changed).
type searchList struct {
	hashes []dao.ListedHash
	order  dao.ListOrder
}

// more says whether the search has photos left to fetch.
func (st *browseState) more() bool { return st != nil && !st.done }

// rows is what st holds for the token cache's cap, in rows: its list.
func (st *browseState) rows() int {
	if st == nil || st.list == nil {
		return 0
	}
	return len(st.list.hashes) / cListRows
}

// dropping is st with folders added to what it leaves out; st itself when
// it leaves them out already.
func (st *browseState) dropping(folders []string) *browseState {
	if st == nil {
		return nil
	}
	var add []string
	for _, f := range folders {
		if !slices.Contains(st.dropped, f) && !slices.Contains(add, f) {
			add = append(add, f)
		}
	}
	if len(add) == 0 {
		return st
	}
	out := *st
	out.dropped = append(slices.Clip(slices.Clone(st.dropped)), add...)
	return &out
}

// readsAsItScrolls says whether new searches are read a chunk at a time
// (browseFunc set: the device's own Manager, Init). A Manager without one
// (the tests' mocked databases) reads every search whole, as before.
func (mg *Manager) readsAsItScrolls() bool { return mg.browse != nil }

// searchFor is how a new search b (with text, a Smart search) is read:
// its state before its first chunk.
func (mg *Manager) searchFor(ses *session.Session, b *dao.MediaBrowse, text string) (*browseState, error) {
	st := &browseState{query: *b}
	list := func(hashes []dao.ListedHash, order dao.ListOrder) {
		st.list = &searchList{hashes: hashes, order: order}
		st.end = len(hashes)
		st.done = len(hashes) == 0
	}
	switch rating := b.Rating; {
	case text != "":
		hashes, err := mg.textList(ses, text, b)
		if err != nil {
			return nil, err
		}
		list(hashes, dao.ListText)
	case b.GroupID != "" && rating.Active() && rating.ByRating, len(b.Tags) > 0 && b.NewerThan == nil:
		hashes, err := mg.dao.RankMedia(&b.MediaFilters)
		if err != nil {
			return nil, err
		}
		list(hashes, dao.ListRanked)
	case b.Filtered():
		hashes, ok, err := mg.dao.DatedMedia(b, cListMax)
		if err != nil {
			return nil, err
		}
		if ok {
			list(hashes, dao.ListDated)
		}
	}
	return st, nil
}

// fetchMore is all with at least want more photos of the search after it
// (fewer when the search ends), and the search's state after them. all is
// never written: it may be a stored cursor's, read by another request at
// once (issue #171), so the rows go into a new array.
func (mg *Manager) fetchMore(all []*pb.File, st *browseState, want int) ([]*pb.File, *browseState, error) {
	out := *st
	var files []*pb.File
	if st.list == nil {
		var next *time.Time
		var done bool
		var err error
		if files, next, done, err = mg.browse(&st.query, st.from, want); err != nil {
			return all, st, err
		}
		out.from, out.done = next, done
	} else {
		var err error
		if files, out.at, err = mg.dao.MediaListChunk(&st.query, st.list.hashes[:st.end], st.at, want, st.list.order); err != nil {
			return all, st, err
		}
		if st.list.order == dao.ListText && !st.cut && len(files) > 0 {
			// The first photo found is the best (the list is best first):
			// what the text search keeps is now known (rankScores' cut).
			files = out.cutAt(files)
		}
		out.done = out.at >= out.end
	}
	grown := slices.Clip(all)
	for _, f := range files {
		if !underFolders(f.Path, false, st.dropped) {
			grown = append(grown, f)
		}
	}
	return grown, &out, nil
}

// cutAt sets st's text cut from files, a text list's first chunk with
// photos, best first: rankScores keeps a cosine of at least cSmartFloor
// and cSmartShare of the best photo's. files without those under it.
func (st *browseState) cutAt(files []*pb.File) []*pb.File {
	hashes := st.list.hashes
	rank := make(map[string]float64, st.at)
	for _, h := range hashes[:st.at] {
		rank[h.Hash] = h.Rank[0]
	}
	cut := max(float64(float32(cSmartFloor)), float64(float32(cSmartShare)*float32(rank[files[0].Hash])))
	st.cut = true
	st.end = sort.Search(len(hashes), func(i int) bool { return hashes[i].Rank[0] < cut })
	st.at = min(st.at, st.end)
	// What is under the cut is never read: the token keeps only the rest
	// (the list of a whole library's cosines over the floor goes once the
	// first page is stored).
	st.list = &searchList{hashes: slices.Clone(hashes[:st.end]), order: st.list.order}
	kept := files[:0:0]
	for _, f := range files {
		if rank[f.Hash] >= cut {
			kept = append(kept, f)
		}
	}
	return kept
}
