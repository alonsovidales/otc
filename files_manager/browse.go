// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"slices"
	"time"

	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// Issue #206: Images' plain browse - the whole library, with no tag,
// person, collection, map area, text or rating, the search that opening
// Images, a jump, scrolling up after one and the pickers make - no longer
// reads the whole library when it starts. Its token holds the rows of the
// pages it has served and is serving, and fetches the next chunk of the
// library (dao.BrowseMedia, by keyset) only when a page needs more: the
// same photos in the same order, a page at a time. Every other search
// keeps reading its rows whole when it starts (a tag's score, a person's
// faces, a collection's or an area's members and a text's ranking aren't
// in the order of an index), as does a folder's gallery.
//
// What a token holds (searchCursor.all) is then only what has been
// fetched: the pages served, as before (pageCursor), and the rest of the
// last chunk. A page served again (resume), a page missing a thumbnail
// (#147) and keepOut work on it as on a whole search; a page that runs
// past it fetches more. A token of either kind goes on as it began.
//
// Unlike a whole search, a chunk is read when it is needed: photos added
// below the place a scroll has reached (an upload of an older photo) come
// in this scroll, where a whole search left them for the next one; one
// deleted meanwhile doesn't come.

// browseFunc reads a plain browse's next chunk: dao.BrowseMedia.
type browseFunc func(b *dao.MediaBrowse, from *time.Time, want int) ([]*pb.File, *time.Time, bool, error)

// browseChunk is the fewest photos a token fetches when its page needs
// more (the first page of a search fetches only what it shows, to come
// fast): a page and a bit of the phones' largest (cMaxPageWithoutThumbnails),
// ~250 bytes a row. A variable so tests can make every page fetch.
var browseChunk = 240

// browseState is where a plain browse's token is in the library: what the
// next chunk is asked with. Never changed once a cursor holding it is
// stored (searchCursor), so each fetch makes a new one.
type browseState struct {
	query dao.MediaBrowse
	// from is where the next chunk starts (BrowseMedia's next).
	from *time.Time
	// done: every photo of the browse has been fetched.
	done bool
	// dropped is every folder keepOut has taken out of the token since the
	// search began (issue #192): a chunk's rows under them are left out,
	// as keepOut leaves out those it holds. The query keeps the folders
	// kept out when the search began, so the copy each photo is shown at
	// is the one the search began with, as for a whole search.
	dropped []string
}

// more says whether the browse has photos left to fetch.
func (st *browseState) more() bool { return st != nil && !st.done }

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

// plainBrowse says whether a new search is a plain browse, served by
// keyset (browseFunc set: the device's own Manager, Init).
func (mg *Manager) plainBrowse(path string, tags, personIDs []string, groupID string, area *dao.MediaArea, text string, rating *dao.RatingQuery) bool {
	return mg.browse != nil && path == "" && len(tags) == 0 && len(personIDs) == 0 && groupID == "" &&
		area == nil && text == "" && !rating.Active()
}

// fetchMore is all with at least want more photos of the browse after it
// (fewer when the library ends), and the browse's state after them. all
// is never written: it may be a stored cursor's, read by another request
// at once (issue #171), so the rows go into a new array.
func (mg *Manager) fetchMore(all []*pb.File, st *browseState, want int) ([]*pb.File, *browseState, error) {
	files, next, done, err := mg.browse(&st.query, st.from, want)
	if err != nil {
		return all, st, err
	}
	out := *st
	out.from, out.done = next, done
	grown := slices.Clip(all)
	for _, f := range files {
		if !underFolders(f.Path, false, st.dropped) {
			grown = append(grown, f)
		}
	}
	return grown, &out, nil
}
