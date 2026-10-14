// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"slices"
	"time"

	pb "github.com/alonsovidales/otc/proto/generated"
)

// Issue #194: after a jump (SearchPhotos.before), scrolling up asks for the
// photos newer than the jump's instant (SearchPhotos.newer_than). The two
// halves, with the same instant, must hold every photo of a plain search
// exactly once.

// searchCutoff is an instant a search's photos are compared with (before,
// newer_than), cut to the whole second below it: `files`.`created` is a
// DATETIME, whole seconds, so for every row c <= t exactly when
// c <= searchCutoff(t), and c > t exactly when c > searchCutoff(t). The
// query is then sent no fraction - which MariaDB truncates, but MySQL may
// round up into the next second, where a photo at that second would be
// in both halves.
func searchCutoff(t time.Time) time.Time { return t.Truncate(time.Second) }

// SearchMediaNewerThan is SearchMedia's photos dated strictly after
// newerThan, OLDEST first (issue #194): the other half of SearchMedia with
// before = newerThan. A photo's date is its oldest copy's (issue #201,
// oldestCopies), so the photos are picked here, from SearchMedia's one row
// per photo, never by a `created` > ? in the query: a copy synced back
// after newerThan would bring back a photo whose oldest copy is before it,
// which the before half already shows. Oldest first is by date, then path:
// a plain search's order reversed (its rows go newest first and, at one
// instant, by path backwards), and a tag search, ordered by score, sorted
// so.
func (dao *Dao) SearchMediaNewerThan(path string, tags []string, personIDs []string, groupID string, imagesOnly bool, newerThan time.Time, excluded []string) ([]*pb.File, error) {
	files, err := dao.SearchMedia(path, tags, personIDs, groupID, imagesOnly, nil, excluded)
	if err != nil {
		return nil, err
	}
	return newerThanOldestFirst(files, newerThan, len(tags) > 0), nil
}

// newerThanOldestFirst is files, SearchMedia's photos, dated after t,
// oldest first; byScore when they come in a tag search's order, not by
// date. Shares files' array.
func newerThanOldestFirst(files []*pb.File, t time.Time, byScore bool) []*pb.File {
	cutoff := searchCutoff(t)
	out := files[:0]
	for _, f := range files {
		if f.GetCreated().AsTime().After(cutoff) {
			out = append(out, f)
		}
	}
	if byScore {
		slices.SortFunc(out, func(a, b *pb.File) int {
			if c := a.GetCreated().AsTime().Compare(b.GetCreated().AsTime()); c != 0 {
				return c
			}
			if a.Path < b.Path {
				return -1
			}
			if a.Path > b.Path {
				return 1
			}
			return 0
		})
	} else {
		slices.Reverse(out)
	}
	return out
}
