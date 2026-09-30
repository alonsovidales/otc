// SPDX-License-Identifier: AGPL-3.0-or-later

package wsframe

import (
	"net/http"
	"time"

	gorilla "github.com/gorilla/websocket"
)

// Issue #169: a connection this device opens to someone else - a friend's
// device, the bridge - had no deadline and no read limit, so one peer that
// accepted the socket and then said nothing stalled the whole friend sync
// (and every push behind it) until a restart, and one that answered with
// gigabytes had them buffered whole. Client gives every write and read a
// deadline and every read a limit; its methods mirror gorilla's so call
// sites don't change.
const (
	// DialTimeout covers the TCP connect, TLS and websocket handshake.
	DialTimeout = 15 * time.Second
	// Timeout is how long one request/response may take.
	Timeout = 30 * time.Second
	// MediaTimeout is for a message carrying a whole file.
	MediaTimeout = 10 * time.Minute
	// Limit is the largest ordinary answer: a page of events, a profile
	// with its picture.
	Limit = 64 << 20
	// MediaLimit is the largest answer carrying a whole file - the same
	// cap the device puts on a file sent to it.
	MediaLimit = 1000<<20 + 1<<20
)

var dialer = &gorilla.Dialer{
	Proxy:            http.ProxyFromEnvironment,
	HandshakeTimeout: DialTimeout,
}

// Client is an outgoing websocket with deadlines and read limits.
type Client struct {
	*gorilla.Conn
}

// Dial opens a Client, giving up after DialTimeout.
func Dial(url string, h http.Header) (*Client, error) {
	c, _, err := dialer.Dial(url, h)
	if err != nil {
		return nil, err
	}
	return &Client{Conn: c}, nil
}

// WriteMessage writes one message within Timeout.
func (c *Client) WriteMessage(messageType int, data []byte) error {
	c.Conn.SetWriteDeadline(time.Now().Add(Timeout))
	return c.Conn.WriteMessage(messageType, data)
}

// ReadMessage reads one ordinary answer: Limit bytes within Timeout.
func (c *Client) ReadMessage() (int, []byte, error) {
	return c.ReadMessageUpTo(Limit, Timeout)
}

// ReadMedia reads one answer carrying a whole file.
func (c *Client) ReadMedia() (int, []byte, error) {
	return c.ReadMessageUpTo(MediaLimit, MediaTimeout)
}

// ReadMessageUpTo reads one message of at most limit bytes within d; a
// larger or later one fails and the connection is done with.
func (c *Client) ReadMessageUpTo(limit int64, d time.Duration) (int, []byte, error) {
	c.Conn.SetReadLimit(limit)
	c.Conn.SetReadDeadline(time.Now().Add(d))
	return c.Conn.ReadMessage()
}
