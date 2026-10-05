// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
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
}

// A reply of up to cReplyPiece still goes out as one frame, as before.
// gorilla's ReadMessage joins continuation frames, so this reads the
// frame header off the socket itself.
func TestWriteReplySendsASmallReplyAsOneFrame(t *testing.T) {
	boundary := make([]byte, cReplyPiece)
	rand.Read(boundary)
	for _, payload := range [][]byte{[]byte("small"), boundary} {
		url, result := replyServer(t, payload)
		br := dialRaw(t, url)

		fin, opcode, size := readFrameHeader(t, br)
		if !fin || opcode != gorilla.BinaryMessage || size != uint64(len(payload)) {
			t.Fatalf("%d-byte reply: first frame fin=%v opcode=%d length=%d, want one binary frame of %d",
				len(payload), fin, opcode, size, len(payload))
		}
		got := make([]byte, size)
		if _, err := io.ReadFull(br, got); err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("%d-byte reply: payload differs (%v)", len(payload), err)
		}
		if err := <-result; err != nil {
			t.Fatalf("writeReply: %v", err)
		}
	}
}

// dialRaw opens a websocket by hand and returns the socket's reader just
// past the handshake, so frames can be read as they were sent.
func dialRaw(t *testing.T, url string) *bufio.Reader {
	t.Helper()
	host := strings.TrimPrefix(url, "ws://")
	c, err := net.Dial("tcp", host)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", host)
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake: status %d", resp.StatusCode)
	}
	return br
}

// readFrameHeader reads one unmasked (server to client) frame header.
func readFrameHeader(t *testing.T, br *bufio.Reader) (fin bool, opcode int, size uint64) {
	t.Helper()
	var h [2]byte
	if _, err := io.ReadFull(br, h[:]); err != nil {
		t.Fatalf("frame header: %v", err)
	}
	if h[1]&0x80 != 0 {
		t.Fatal("a reply frame from the device was masked")
	}
	fin, opcode, size = h[0]&0x80 != 0, int(h[0]&0x0f), uint64(h[1]&0x7f)
	switch size {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(br, ext[:]); err != nil {
			t.Fatalf("frame length: %v", err)
		}
		size = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(br, ext[:]); err != nil {
			t.Fatalf("frame length: %v", err)
		}
		size = binary.BigEndian.Uint64(ext[:])
	}
	return fin, opcode, size
}
