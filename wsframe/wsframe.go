// SPDX-License-Identifier: AGPL-3.0-or-later

// Package wsframe reads websocket messages within a size limit and a
// shared memory budget, for the device and the bridge alike.
//
// Neither server bounded what it read: gorilla's default read limit is
// none, so anyone - before signing in - could send one message of several
// gigabytes and have it buffered whole, taking down the Raspberry Pi or
// the bridge. Now every message is capped (small until the connection has
// signed in), and the memory of large ones in flight is budgeted: a
// message past its free bytes (DefaultFree, or what the caller passes to
// ReadFree - the device lets a connection that hasn't signed in have
// 64 KiB) reserves as it grows, and a connection that can't get room
// within a while is closed rather than waiting on others that may be
// waiting on it.
package wsframe

import (
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
	// cMinStep is the least a large message reserves at a time, however
	// small its free part.
	cMinStep = 64 << 10
	// cWait is how long a message may wait for room before its
	// connection is closed.
	cWait = 30 * time.Second
	// cFirstHead is where a message's buffer starts: it doubles from
	// there, up to the free part, as the message turns out longer.
	cFirstHead = 512
)

// DefaultFree is what Read reads of a message outside the budget.
const DefaultFree = cFree

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
// The first DefaultFree bytes of a message are read outside the budget.
func Read(conn *gorilla.Conn, limit int64, b *Budget) (int, []byte, func(), error) {
	return ReadFree(conn, limit, DefaultFree, b)
}

// ReadFree is Read with free bytes of a message read outside the budget.
// Everything past them is reserved by what its buffers take (whole
// chunks), not only by what they hold, and the free part is read into a
// buffer that never grows past free - a bytes.Buffer doubled a 4 MiB
// message's to 8 MiB, outside the budget: what a message holds outside it
// is never more than free.
func ReadFree(conn *gorilla.Conn, limit, free int64, b *Budget) (int, []byte, func(), error) {
	noop := func() {}
	if free <= 0 {
		free = DefaultFree
	}
	conn.SetReadLimit(limit)
	typ, r, err := conn.NextReader()
	if err != nil {
		return 0, nil, noop, err
	}
	head, err := readHead(r, free)
	if err != nil {
		return 0, nil, noop, err
	}
	if int64(len(head)) < free {
		return typ, head, noop, nil // the whole message
	}
	// The head is full: one byte more says whether the message goes on.
	var first [1]byte
	if _, err := io.ReadFull(r, first[:]); err != nil {
		if err == io.EOF {
			return typ, head, noop, nil // exactly free bytes
		}
		return 0, nil, noop, err
	}
	var held int64
	release := func() { b.release(held); held = 0 }
	reserve := func(n int64) error {
		if b == nil {
			return nil
		}
		if err := b.acquire(n); err != nil {
			return err
		}
		held += n
		return nil
	}
	// The rest is read in chunks of step, each reserved before it's read,
	// and joined once at the end. A bytes.Buffer doubled its capacity as
	// it grew: a 600 MiB message ended in a 1 GiB array, with the 512 MiB
	// one before it still live at the last grow - far past what the
	// budget held for it.
	step := min(max(free, cMinStep), cStep)
	var chunks [][]byte
	total := len(head)
	ahead := first[:] // the byte read to find the message went on
	for {
		if err := reserve(step); err != nil {
			release()
			return 0, nil, noop, err
		}
		c := make([]byte, step)
		k := copy(c, ahead)
		n, err := io.ReadFull(r, c[k:])
		chunks = append(chunks, c[:k+n])
		total += k + n
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break // the end of the message
		}
		if err != nil {
			release()
			return 0, nil, noop, err
		}
		// The chunk is full. One byte first: a message that ended exactly
		// at its end allocates no empty chunk to find that out.
		if _, err := io.ReadFull(r, first[:]); err != nil {
			if err == io.EOF {
				break
			}
			release()
			return 0, nil, noop, err
		}
		ahead = first[:]
	}
	// The join holds the message twice for a moment, and only the chunks
	// are reserved: reserving the copy too would halve the largest message
	// the budget admits (a 600 MB upload against a 4 GB Pi's ~800 MB
	// budget would fail with ErrNoRoom where it works now).
	var once sync.Once
	out := make([]byte, total)
	k := copy(out, head)
	for i := range chunks {
		k += copy(out[k:], chunks[i])
		chunks[i] = nil
	}
	return typ, out, func() { once.Do(release) }, nil
}

// readHead reads up to free bytes of a message into a buffer that doubles
// from cFirstHead as the message turns out longer, never past free. It
// holds less than free only when the message ended.
func readHead(r io.Reader, free int64) ([]byte, error) {
	head := make([]byte, 0, min(free, cFirstHead))
	for {
		n, err := io.ReadFull(r, head[len(head):cap(head)])
		head = head[:len(head)+n]
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return head, nil // the end of the message
		}
		if err != nil {
			return nil, err
		}
		if int64(len(head)) == free {
			return head, nil
		}
		grown := make([]byte, len(head), min(2*int64(cap(head)), free))
		copy(grown, head)
		head = grown
	}
}
