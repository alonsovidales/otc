// SPDX-License-Identifier: AGPL-3.0-or-later

package social

import (
	"crypto/sha256"
	"encoding/binary"
	"math"
	"sync"
	"time"

	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// A post sent again is made once. Making a post can take minutes - a long
// phone video is cut and shrunk by ffmpeg before the device answers - and
// its answer can be lost on the way: the socket drops, or the bridge gives
// up after 90 s and answers device_unreachable. The device carries on and
// makes the post, while the app shows a failure with Publish enabled, and
// publishing again used to make it twice, for every friend. NewSocial-
// Publication carries no id of the client's, so the device recognises the
// same post by what it is: its text, its files and their cuts (postKey) -
// the device has one owner, whichever of their apps or sockets it comes
// from. Sent while the first is still being made, it waits for it and gets
// its answer; within cPostAgainWindow of it being made, the post's uuid at
// once. A post that failed is made again. Deleting a post forgets it, so
// the same one can be posted again.

// cPostAgainWindow is how long after a post is made the same one, sent
// again, is taken for it. A var for tests.
var cPostAgainWindow = 10 * time.Minute

// cMaxRecentPosts bounds what is remembered: older ones go first.
const cMaxRecentPosts = 64

// posting is a post being made, for whoever sends it again meanwhile.
type posting struct {
	done chan struct{}
	uuid string
	err  error
}

type recentPost struct {
	uuid string
	made time.Time
}

// postsOnce holds the posts being made and those made lately, by postKey.
type postsOnce struct {
	mu     sync.Mutex
	making map[[sha256.Size]byte]*posting
	made   map[[sha256.Size]byte]recentPost
}

// postKey is what makes two posts the same one: the text, then each path
// and its cut, in order.
func postKey(text string, paths []string, trims []*pb.VideoTrim) [sha256.Size]byte {
	h := sha256.New()
	field := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	number := func(f float64) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], math.Float64bits(f))
		h.Write(n[:])
	}
	field(text)
	cuts := map[string]*pb.VideoTrim{}
	for _, t := range trims {
		if t != nil {
			cuts[t.Path] = t
		}
	}
	for _, p := range paths {
		field(p)
		if t := cuts[p]; t != nil {
			number(t.StartSecs)
			number(t.EndSecs)
		} else {
			number(-1)
			number(-1)
		}
	}
	var key [sha256.Size]byte
	copy(key[:], h.Sum(nil))
	return key
}

// once makes the post key names with run, unless it is being made or was
// made within cPostAgainWindow: then the answer of that one.
func (po *postsOnce) once(key [sha256.Size]byte, run func() (string, error)) (string, error) {
	po.mu.Lock()
	if r, ok := po.made[key]; ok && time.Since(r.made) < cPostAgainWindow {
		po.mu.Unlock()
		log.Info("a post sent again was made already:", r.uuid)
		return r.uuid, nil
	}
	if p, ok := po.making[key]; ok {
		po.mu.Unlock()
		log.Info("a post sent again is being made: waiting for it")
		<-p.done
		return p.uuid, p.err
	}
	if po.making == nil {
		po.making = map[[sha256.Size]byte]*posting{}
	}
	p := &posting{done: make(chan struct{})}
	po.making[key] = p
	po.mu.Unlock()

	defer close(p.done)
	p.uuid, p.err = run()

	po.mu.Lock()
	defer po.mu.Unlock()
	delete(po.making, key)
	if p.err != nil {
		return p.uuid, p.err
	}
	if po.made == nil {
		po.made = map[[sha256.Size]byte]recentPost{}
	}
	for k, r := range po.made {
		if time.Since(r.made) >= cPostAgainWindow {
			delete(po.made, k)
		}
	}
	if len(po.made) >= cMaxRecentPosts {
		var oldest [sha256.Size]byte
		first := true
		for k, r := range po.made {
			if first || r.made.Before(po.made[oldest].made) {
				oldest, first = k, false
			}
		}
		delete(po.made, oldest)
	}
	po.made[key] = recentPost{uuid: p.uuid, made: time.Now()}
	return p.uuid, nil
}

// forget drops what is remembered of the post uuid (it was deleted).
func (po *postsOnce) forget(uuid string) {
	po.mu.Lock()
	defer po.mu.Unlock()
	for k, r := range po.made {
		if r.uuid == uuid {
			delete(po.made, k)
		}
	}
}
