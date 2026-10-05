// SPDX-License-Identifier: AGPL-3.0-or-later

package wsframe

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gorilla "github.com/gorilla/websocket"
)

// serve reads messages with Read and reports each outcome.
func serve(t *testing.T, limit int64, b *Budget, got chan<- error, sizes chan<- int) *httptest.Server {
	up := gorilla.Upgrader{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, msg, release, err := Read(conn, limit, b)
			if err != nil {
				got <- err
				return
			}
			sizes <- len(msg)
			release()
		}
	}))
}

func dial(t *testing.T, srv *httptest.Server) *gorilla.Conn {
	c, _, err := gorilla.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestReadEnforcesTheLimitAndReleasesTheBudget(t *testing.T) {
	b := NewBudget(64 << 20)
	errs, sizes := make(chan error, 1), make(chan int, 4)
	srv := serve(t, 12<<20, b, errs, sizes)
	defer srv.Close()
	c := dial(t, srv)
	defer c.Close()

	small := make([]byte, 1000)
	big := make([]byte, 10<<20) // over cFree: budgeted
	for _, m := range [][]byte{small, big} {
		if err := c.WriteMessage(gorilla.BinaryMessage, m); err != nil {
			t.Fatal(err)
		}
	}
	if n := <-sizes; n != len(small) {
		t.Fatalf("small message: got %d bytes", n)
	}
	if n := <-sizes; n != len(big) {
		t.Fatalf("large message: got %d bytes", n)
	}
	b.mu.Lock()
	used := b.used
	b.mu.Unlock()
	if used != 0 {
		t.Errorf("budget left at %d after release", used)
	}

	// Over the limit: the connection is refused.
	_ = c.WriteMessage(gorilla.BinaryMessage, make([]byte, 13<<20))
	if err := <-errs; err == nil {
		t.Error("a message over the limit was read")
	}
}

// A budgeted message comes back byte for byte, at every boundary of the
// chunked read, in a slice of exactly its size, and gives its share of the
// budget back.
func TestReadRoundTripsLargeMessagesExactly(t *testing.T) {
	b := NewBudget(64 << 20)
	type got struct {
		msg []byte
		cap int
	}
	msgs := make(chan got, 8)
	up := gorilla.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, msg, release, err := Read(conn, 32<<20, b)
			if err != nil {
				return
			}
			msgs <- got{append([]byte(nil), msg...), cap(msg)}
			release()
		}
	}))
	defer srv.Close()
	c := dial(t, srv)
	defer c.Close()

	for _, size := range []int{cFree - 1, cFree, cFree + 1, cFree + 2, cFree + cStep - 1, cFree + cStep, cFree + cStep + 1, cFree + 2*cStep, 10<<20 + 123} {
		m := make([]byte, size)
		for i := range m {
			m[i] = byte(i*7 + i>>13)
		}
		if err := c.WriteMessage(gorilla.BinaryMessage, m); err != nil {
			t.Fatal(err)
		}
		g := <-msgs
		if !bytes.Equal(g.msg, m) {
			t.Fatalf("%d bytes: the message changed on the way (got %d bytes)", size, len(g.msg))
		}
		if size > cFree && g.cap != size {
			t.Errorf("%d bytes: returned with capacity %d, want exactly its size", size, g.cap)
		}
	}
	b.mu.Lock()
	used := b.used
	b.mu.Unlock()
	if used != 0 {
		t.Errorf("budget left at %d after release", used)
	}
}
