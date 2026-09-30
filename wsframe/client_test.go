// SPDX-License-Identifier: AGPL-3.0-or-later

package wsframe

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gorilla "github.com/gorilla/websocket"
)

// Issue #169: a peer that accepts and then never answers, or answers too
// much, must fail the read instead of holding the caller forever.
func TestClientDeadlineAndLimit(t *testing.T) {
	up := gorilla.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_, msg, err := c.ReadMessage()
		if err != nil {
			return
		}
		if string(msg) == "big" {
			c.WriteMessage(gorilla.BinaryMessage, make([]byte, 2048))
		}
		time.Sleep(2 * time.Second) // "silent" gets nothing
	}))
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	c, err := Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.WriteMessage(gorilla.BinaryMessage, []byte("silent"))
	start := time.Now()
	if _, _, err := c.ReadMessageUpTo(Limit, 200*time.Millisecond); err == nil {
		t.Fatal("a silent peer's read succeeded")
	}
	if time.Since(start) > time.Second {
		t.Fatal("the read outlived its deadline")
	}
	c.Close()

	c, err = Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.WriteMessage(gorilla.BinaryMessage, []byte("big"))
	if _, _, err := c.ReadMessageUpTo(1024, time.Second); err == nil {
		t.Fatal("an answer over the limit was read")
	}
}
