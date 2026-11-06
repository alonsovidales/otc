// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"os"
	"slices"
	"strings"

	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"google.golang.org/protobuf/proto"
)

// A slideshow's photos in a random order (RandomPhotos, the web app's
// Slideshow): a collection's, or the whole library's, each once a round
// (dao/random_media.go for the order). A page is the round's next hashes,
// then their rows as Images shows them (SearchMediaOf: each at its oldest
// copy, outside the folders kept out), photos only, in the round's order,
// and only those processed (#147) - a page may come short, or empty,
// before the round ends; its token says whether it has.

// cRandomPage is a page's photos when the request says none; cRandomMax
// the most it may ask for. A page lasts its photos' seconds on screen.
const (
	cRandomPage = 20
	cRandomMax  = 50
)

// cMaxSeed is the most of a seed that counts: the order only needs it to
// differ from round to round.
const cMaxSeed = 64

// RandomPhotos is a page of a slideshow's round (RandomPhotos): at most
// limit photos of groupID (the whole library for "") in seed's order,
// after the page whose token was after ("" for the first). small sends
// each photo's small thumbnail as its content (thumbnail_small saying
// which came); without it the Files come without content. token is the
// next page's after, "" once the round has given every photo - also for
// an after that can't be a page's (the round is over: the client starts
// another). total, on a round's first page only, is how many photos it
// holds (ListOfFiles.random_total).
func (mg *Manager) RandomPhotos(ses *session.Session, groupID, seed, after string, limit int32, small bool) (files []*pb.File, token string, total int32, err error) {
	if !dao.RandomKeyValid(after) {
		log.Debug("a slideshow page asked after a key no page gave")
		return nil, "", 0, nil
	}
	n := int(limit)
	if n <= 0 {
		n = cRandomPage
	}
	n = min(n, cRandomMax)
	if len(seed) > cMaxSeed {
		seed = seed[:cMaxSeed]
	}
	excluded, err := mg.OutOfImagesFolders()
	if err != nil {
		return nil, "", 0, err
	}
	if after == "" {
		count, err := mg.dao.RandomMediaCount(groupID, excluded)
		if err != nil {
			return nil, "", 0, err
		}
		total = int32(min(count, 1<<31-1))
	}
	hashes, last, done, err := mg.dao.RandomMedia(groupID, excluded, seed, after, n)
	if err != nil {
		return nil, "", 0, err
	}
	if !done {
		token = last
	}
	if len(hashes) == 0 {
		return nil, token, total, nil
	}
	rows, err := mg.dao.SearchMediaOf(&dao.MediaBrowse{
		MediaFilters: dao.MediaFilters{GroupID: groupID},
		ImagesOnly:   true,
		Excluded:     excluded,
	}, hashes)
	if err != nil {
		return nil, "", 0, err
	}
	place := make(map[string]int, len(hashes))
	for i, h := range hashes {
		place[h] = i
	}
	// Photos only: a collection's rows aren't kept to images by the search
	// (its members may be videos).
	rows = slices.DeleteFunc(rows, func(f *pb.File) bool { return !strings.HasPrefix(f.Mime, "image") })
	slices.SortFunc(rows, func(a, b *pb.File) int { return place[a.Hash] - place[b.Hash] })
	for _, f := range rows {
		var content []byte
		var isSmall bool
		if small {
			var thumbErr error
			if content, isSmall, thumbErr = mg.readGridThumbnail(ses, f, true); thumbErr != nil {
				if !os.IsNotExist(thumbErr) {
					log.Error("error reading the thumbnail of", f.Hash, thumbErr)
				}
				continue
			}
		} else if shown, _ := mg.gridThumbnailState(ses, f, false); !shown {
			continue
		}
		f = proto.Clone(f).(*pb.File)
		f.Content = content
		if small {
			f.ThumbnailSmall = proto.Bool(isSmall)
		}
		files = append(files, f)
	}
	return files, token, total, nil
}
