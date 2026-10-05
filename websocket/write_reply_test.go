// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gorilla "github.com/gorilla/websocket"
)

// replyServer serves one connection that gets payload through writeReply,
// with small socket buffers so a reader that stops is felt at once.
func replyServer(t *testing.T, payload []byte) (url string, result chan error) {
	t.Helper()
	result = make(chan error, 1)
	up := gorilla.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			result <- err
			return
		}
		defer conn.Close()
		conn.NetConn().(*net.TCPConn).SetWriteBuffer(16 << 10)
		result <- writeReply(conn, payload)
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), result
}

func dialSmallBuffer(t *testing.T, url string) *gorilla.Conn {
	t.Helper()
	d := gorilla.Dialer{NetDial: func(network, addr string) (net.Conn, error) {
		c, err := net.Dial(network, addr)
		if err == nil {
			c.(*net.TCPConn).SetReadBuffer(16 << 10)
		}
		return c, err
	}}
	c, _, err := d.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// A peer that stops reading used to hold the writer, and every request
// queued behind it with its memory budget, for as long as the socket
// lived. Now the reply fails once the peer takes nothing for cReplyStall.
func TestWriteReplyGivesUpOnAPeerThatStopsReading(t *testing.T) {
	orig := cReplyStall
	cReplyStall = 300 * time.Millisecond
	defer func() { cReplyStall = orig }()

	payload := make([]byte, 16<<20)
	url, result := replyServer(t, payload)
	dialSmallBuffer(t, url) // and never read

	select {
	case err := <-result:
		if err == nil {
			t.Fatal("a 16 MB reply to a peer that reads nothing was reported written")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the write never gave up on a peer that stopped reading")
	}
}

// A slow reader is not a stalled one: a large reply still arrives whole,
// however long it takes, as long as the peer keeps taking it.
func TestWriteReplyDeliversALargeReplyToASlowReader(t *testing.T) {
	orig := cReplyStall
	cReplyStall = 300 * time.Millisecond
	defer func() { cReplyStall = orig }()

	payload := make([]byte, 3<<20+12345)
	rand.Read(payload)
	url, result := replyServer(t, payload)
	c := dialSmallBuffer(t, url)

	start := time.Now()
	typ, r, err := c.NextReader()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got bytes.Buffer
	for {
		n, err := io.CopyN(&got, r, 32<<10)
		if err == io.EOF {
			break
		}
		if err != nil || n == 0 {
			t.Fatalf("read: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if typ != gorilla.BinaryMessage || !bytes.Equal(got.Bytes(), payload) {
		t.Fatalf("got %d bytes (type %d), want the %d sent", got.Len(), typ, len(payload))
	}
	if err := <-result; err != nil {
		t.Fatalf("writeReply: %v", err)
	}
	if time.Since(start) < cReplyStall {
		t.Fatal("the reader was meant to take longer than one stall period in all")
	}

	// A small reply is still one frame, as before.
	url, result = replyServer(t, []byte("small"))
	c = dialSmallBuffer(t, url)
	if _, b, err := c.ReadMessage(); err != nil || string(b) != "small" {
		t.Fatalf("small reply: %q, %v", b, err)
	}
	if err := <-result; err != nil {
		t.Fatalf("writeReply: %v", err)
	}
}
