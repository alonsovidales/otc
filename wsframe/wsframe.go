// SPDX-License-Identifier: AGPL-3.0-or-later

// Package wsframe reads websocket messages within a size limit and a
// shared memory budget, for the device and the bridge alike.
//
// Neither server bounded what it read: gorilla's default read limit is
// none, so anyone - before signing in - could send one message of several
// gigabytes and have it buffered whole, taking down the Raspberry Pi or
// the bridge. Now every message is capped (small until the connection has
// signed in), and the memory of large ones in flight is budgeted: a
// message past cFree bytes reserves as it grows, and a connection that
// can't get room within a while is closed rather than waiting on others
// that may be waiting on it.
package wsframe

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"time"

	gorilla "github.com/gorilla/websocket"
)

const (
	// cFree is read without touching the budget - every ordinary request.
	cFree = 4 << 20
	// cStep is how much a large message reserves at a time.
	cStep = 4 << 20
	// cWait is how long a message may wait for room before its
	// connection is closed.
	cWait = 30 * time.Second
)

// ErrNoRoom: the budget stayed full for too long.
var ErrNoRoom = errors.New("wsframe: no memory for this message")

// Budget bounds the bytes of large messages held at once.
type Budget struct {
	mu   sync.Mutex
	cond *sync.Cond
	used int64
	max  int64
}

func NewBudget(max int64) *Budget {
	b := &Budget{max: max}
	b.cond = sync.NewCond(&b.mu)
	return b
}

// acquire reserves n bytes, waiting up to cWait.
func (b *Budget) acquire(n int64) error {
	deadline := time.Now().Add(cWait)
	timer := time.AfterFunc(cWait, func() { b.cond.Broadcast() })
	defer timer.Stop()
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.used > 0 && b.used+n > b.max {
		if time.Now().After(deadline) {
			return ErrNoRoom
		}
		b.cond.Wait()
	}
	b.used += n
	return nil
}

func (b *Budget) release(n int64) {
	if n == 0 {
		return
	}
	b.mu.Lock()
	b.used -= n
	b.mu.Unlock()
	b.cond.Broadcast()
}

// Read reads the next message, at most limit bytes (the connection's read
// limit is set to it: a larger message closes the connection). The
// returned release gives back the message's share of the budget - call it
// once the message and anything built from it are done with. Never nil.
func Read(conn *gorilla.Conn, limit int64, b *Budget) (int, []byte, func(), error) {
	noop := func() {}
	conn.SetReadLimit(limit)
	typ, r, err := conn.NextReader()
	if err != nil {
		return 0, nil, noop, err
	}
	var buf bytes.Buffer
	var held int64
	release := func() { b.release(held); held = 0 }
	if _, err := io.CopyN(&buf, r, cFree); err != nil {
		if err == io.EOF {
			return typ, buf.Bytes(), noop, nil
		}
		return 0, nil, noop, err
	}
	for {
		if b != nil {
			if err := b.acquire(cStep); err != nil {
				release()
				return 0, nil, noop, err
			}
			held += cStep
		}
		if _, err := io.CopyN(&buf, r, cStep); err != nil {
			if err == io.EOF {
				var once sync.Once
				return typ, buf.Bytes(), func() { once.Do(release) }, nil
			}
			release()
			return 0, nil, noop, err
		}
	}
}
