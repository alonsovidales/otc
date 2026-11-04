// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"sync"
	"time"

	gorilla "github.com/gorilla/websocket"
	"google.golang.org/protobuf/encoding/protowire"
)

// The bridge gives up on a request its device hasn't answered within 90 s
// and tells the client the device is unreachable - a backstop for a device
// that stopped answering. A post whose videos are cut and shrunk, a folder
// of thousands of files deleted, a large file going out over a slow line
// take longer than that while the device works on them, and the client was
// shown "unreachable" (and lost what it had open) for work that went on.
//
// On a connection it opened to the bridge, the device now says which of
// its requests are still running: every cBusyEvery, an unsolicited pong
// (RFC 6455 allows them) carrying BusyPrefix and the ids, as uvarints, of
// the requests running for cBusyAfter or more. A bridge from before this
// takes it for a pong like any other - a sign of life - and a request the
// device has forgotten is never named, so it still times out.

// BusyPrefix starts the payload of a pong that names requests still
// running (the bridge reads it: bridge/websocket's busyIDs).
const BusyPrefix = "otcbusy"

// cBusyWrite bounds the wait to send one: a connection that can't take
// a control frame in that long is the bridge's to time out.
const cBusyWrite = 10 * time.Second

// cMaxControlPayload is the largest payload a websocket control frame
// may carry.
const cMaxControlPayload = 125

// Vars so tests can shrink them.
var (
	cBusyEvery = 20 * time.Second
	cBusyAfter = 10 * time.Second
)

// busyRequests tracks the requests running on one bridge connection and
// names the long ones to the bridge. A nil one (a direct connection)
// tracks nothing.
type busyRequests struct {
	conn *gorilla.Conn

	mu      sync.Mutex
	running map[int32][]time.Time // by id: when each still running began
	timer   *time.Timer
	stopped bool
}

func newBusyRequests(conn *gorilla.Conn) *busyRequests {
	return &busyRequests{conn: conn, running: map[int32][]time.Time{}}
}

// track records request id as running, until the returned func is called.
func (b *busyRequests) track(id int32) (done func()) {
	if b == nil {
		return func() {}
	}
	start := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return func() {}
	}
	b.running[id] = append(b.running[id], start)
	if b.timer == nil {
		b.timer = time.AfterFunc(cBusyEvery, b.tell)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			starts := b.running[id]
			for i, s := range starts {
				if s.Equal(start) {
					starts = append(starts[:i], starts[i+1:]...)
					break
				}
			}
			if len(starts) == 0 {
				delete(b.running, id)
			} else {
				b.running[id] = starts
			}
		})
	}
}

// tell names to the bridge the requests running for cBusyAfter or more,
// and looks again in cBusyEvery while any request runs.
func (b *busyRequests) tell() {
	now := time.Now()
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return
	}
	var ids []int32
	for id, starts := range b.running {
		for _, s := range starts {
			if now.Sub(s) >= cBusyAfter {
				ids = append(ids, id)
				break
			}
		}
	}
	if len(b.running) > 0 {
		b.timer = time.AfterFunc(cBusyEvery, b.tell)
	} else {
		b.timer = nil
	}
	b.mu.Unlock()
	for _, payload := range busyPayloads(ids) {
		if err := b.conn.WriteControl(gorilla.PongMessage, payload, time.Now().Add(cBusyWrite)); err != nil {
			return // gone, or not taking frames: the bridge decides
		}
	}
}

// stop ends the tracking: the connection is done.
func (b *busyRequests) stop() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopped = true
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
}

// busyPayloads packs ids into as few pong payloads as fit a control frame.
func busyPayloads(ids []int32) [][]byte {
	var out [][]byte
	var cur []byte
	for _, id := range ids {
		v := uint64(uint32(id))
		if cur == nil || len(cur)+protowire.SizeVarint(v) > cMaxControlPayload {
			if cur != nil {
				out = append(out, cur)
			}
			cur = append(make([]byte, 0, cMaxControlPayload), BusyPrefix...)
		}
		cur = protowire.AppendVarint(cur, v)
	}
	if cur != nil {
		out = append(out, cur)
	}
	return out
}
