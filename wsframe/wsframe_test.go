// SPDX-License-Identifier: AGPL-3.0-or-later

package wsframe

import (
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
