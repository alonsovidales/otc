// SPDX-License-Identifier: AGPL-3.0-or-later

// Package wsclient is WSClient.swift: one WebSocket to the device (or the
// bridge in front of it), protobuf envelopes correlated by id, auth on
// connect, and reconnection with backoff.
package wsclient

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	pb "github.com/alonsovidales/otc/proto/generated"
)

const (
	initialBackoff = time.Second
	maxBackoff     = 30 * time.Second
	// A file upload is one message; the device accepts large ones.
	maxMessageSize = 1000 * 1024 * 1024
)

// handshakeTO is how long a dial may take (a var for the tests).
var handshakeTO = 20 * time.Second

// ErrNotConnected is what a request gets while the socket is down.
var ErrNotConnected = errors.New("not connected")

// Client is safe to use from any goroutine.
type Client struct {
	// OnConnect runs once the connection is up and authenticated; OnDisconnect
	// whenever it goes away (err is nil for a deliberate close).
	OnConnect    func()
	OnDisconnect func(err error)
	// OnAuthFailed reports a rejected password, so the UI can say so rather
	// than just "disconnected". retryAfter is non-zero when the device
	// refused to even check it because this address made too many attempts
	// (the device's rate limit, issue #117).
	OnAuthFailed func(msg string, retryAfter int)
	// OnUnreachable reports that the bridge could not reach the device
	// (switched off, offline): not a wrong password - the client keeps
	// retrying with backoff and signs in once the device is back.
	OnUnreachable func(msg string)

	mu       sync.Mutex
	url      string
	clientID string
	password string
	conn     *websocket.Conn
	open     bool
	// signedIn: the device accepted the password on this socket.
	// IsConnected is both - an open socket still signing in used to count,
	// so a folder's first ListFiles raced the Auth and got "not
	// authenticated", which readLoop takes for a lost session and closes
	// the socket on, cutting its own sign-in short (as WSClient.swift).
	signedIn  bool
	waiters   map[int32]chan *pb.RespEnvelope
	nextID    int32
	autoRecon bool
	backoff   time.Duration
	gen       int64 // bumped on every connect, so a stale loop can tell it is stale
	writeMu   sync.Mutex
}

// New makes an unconfigured client.
func New() *Client {
	return &Client{waiters: map[int32]chan *pb.RespEnvelope{}, backoff: initialBackoff}
}

// Configure takes a bare host (bridge) or a full ws:// / wss:// URL, and the
// credentials Auth sends (SettingsStore's domain/password, plus this
// install's id).
func (c *Client) Configure(domain, clientID, password string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d := strings.TrimSpace(domain)
	if strings.Contains(d, "://") {
		c.url = d
	} else {
		c.url = "wss://" + d + "/ws"
	}
	c.clientID = clientID
	c.password = password
}

// Connect dials (asynchronously) and keeps reconnecting until Disconnect.
func (c *Client) Connect() {
	c.mu.Lock()
	c.autoRecon = true
	if c.url == "" || c.open {
		c.mu.Unlock()

		return
	}
	c.gen++
	gen := c.gen
	c.mu.Unlock()
	go c.dial(gen)
}

// Disconnect closes the socket and stops reconnecting; Connect re-enables.
func (c *Client) Disconnect() {
	c.mu.Lock()
	c.autoRecon = false
	conn := c.conn
	c.conn = nil
	c.open = false
	c.signedIn = false
	c.gen++
	c.failAllLocked(errors.New("closed"))
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	if c.OnDisconnect != nil {
		c.OnDisconnect(nil)
	}
}

// IsConnected is whether a request can be sent right now.
func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.open && c.signedIn
}

// current: the attempt gen is still this client's own (and, with conn,
// still on that socket). One that was superseded - Disconnect, or a
// reconfigure and Connect while it was still dialing or signing in -
// reports nothing and changes nothing: its late failure used to mark the
// newer, working connection "Disconnected", stop its RAID polling, or tear
// it down. Failures are judged by the generation alone, so an answer about
// the password is still reported when the socket dropped just after it.
func (c *Client) current(gen int64, conn *websocket.Conn) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.currentLocked(gen, conn)
}

func (c *Client) currentLocked(gen int64, conn *websocket.Conn) bool {
	return gen == c.gen && (conn == nil || c.conn == conn)
}

func (c *Client) dial(gen int64) {
	c.mu.Lock()
	u := c.url
	c.mu.Unlock()
	parsed, err := url.Parse(u)
	if err != nil {
		if c.OnDisconnect != nil && c.current(gen, nil) {
			c.OnDisconnect(fmt.Errorf("bad address %q: %w", u, err))
		}

		return
	}
	dialer := websocket.Dialer{HandshakeTimeout: handshakeTO}
	conn, _, err := dialer.Dial(parsed.String(), nil)
	if err != nil {
		if c.OnDisconnect != nil && c.current(gen, nil) {
			c.OnDisconnect(err)
		}
		c.scheduleReconnect(gen)

		return
	}
	conn.SetReadLimit(maxMessageSize)

	c.mu.Lock()
	if gen != c.gen || !c.autoRecon {
		c.mu.Unlock()
		_ = conn.Close()

		return
	}
	c.conn = conn
	c.open = true
	c.signedIn = false
	c.mu.Unlock()

	go c.readLoop(conn, gen)

	err = c.auth(gen, conn)
	var ue *UnreachableError
	if errors.As(err, &ue) {
		// The bridge is up, the device isn't: say so and close - the read
		// loop's error path reconnects with a growing delay (reset only
		// after a sign-in succeeds, so this doesn't retry every second).
		if c.OnUnreachable != nil && c.current(gen, nil) {
			c.OnUnreachable(ue.Message)
		}
		_ = conn.Close()

		return
	}
	var ae *AuthError
	if err != nil && !errors.As(err, &ae) {
		// Not an answer about the password - a timeout, or the socket
		// dropping mid sign-in (a busy device, another folder's upload
		// filling the link). Reporting that as a wrong password stopped
		// the client for good; close and let the read loop reconnect.
		_ = conn.Close()

		return
	}
	if err != nil {
		// A wrong password is not a reason to hammer the device: the
		// socket stays down until the settings change and Connect is
		// called again. Checked and changed in one go: a superseded
		// attempt's answer must not take down the connection after it.
		c.mu.Lock()
		if !c.currentLocked(gen, nil) {
			c.mu.Unlock()
			_ = conn.Close()

			return
		}
		c.autoRecon = false
		c.conn = nil
		c.open = false
		c.signedIn = false
		c.failAllLocked(err)
		c.mu.Unlock()
		_ = conn.Close()
		if c.OnAuthFailed != nil {
			c.OnAuthFailed(err.Error(), ae.RetryAfter)
		}
		if c.OnDisconnect != nil {
			c.OnDisconnect(err)
		}

		return
	}
	c.mu.Lock()
	if !c.currentLocked(gen, conn) {
		// Superseded while signing in: not this client's connection any
		// more - no sign-in to mark, no second OnConnect.
		c.mu.Unlock()
		_ = conn.Close()

		return
	}
	c.backoff = initialBackoff
	c.signedIn = true
	c.mu.Unlock()
	if c.OnConnect != nil {
		c.OnConnect()
	}
}

// errSuperseded ends a sign-in whose attempt is no longer the client's.
var errSuperseded = errors.New("connection replaced")

func (c *Client) auth(gen int64, conn *websocket.Conn) error {
	pk, err := c.Request(context.Background(), func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqGetPubKey{ReqGetPubKey: &pb.GetPubKey{}}
	})
	if err != nil {
		return err
	}
	pub, ok := pk.Payload.(*pb.RespEnvelope_RespPubKey)
	if !ok {
		if ack := pk.GetRespAck(); ack != nil && (ack.Code == "device_unreachable" || ack.Code == "device_disabled") {
			return &UnreachableError{Message: ack.ErrorMsg}
		}
		return errors.New("unable to fetch the connection's public key")
	}
	// Requests go on whichever socket is current: a superseded attempt's
	// Auth, encrypted for the old socket's key, would be rejected on the
	// new one and counted against this address's attempts (issue #117).
	if !c.current(gen, conn) {
		return errSuperseded
	}
	c.mu.Lock()
	pw, id := c.password, c.clientID
	c.mu.Unlock()
	enc, err := EncryptPassword(pw, pub.RespPubKey.PublicKey)
	if err != nil {
		return err
	}
	resp, err := c.Request(context.Background(), func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqAuth{ReqAuth: &pb.Auth{Uuid: id, Key: enc, Create: false}}
	})
	if err != nil {
		return err
	}
	ack, ok := resp.Payload.(*pb.RespEnvelope_RespAck)
	if !ok || !ack.RespAck.Ok {
		ae := &AuthError{Message: "authentication rejected"}
		if ok {
			if ack.RespAck.ErrorMsg != "" {
				ae.Message = ack.RespAck.ErrorMsg
			}
			if ack.RespAck.Code == "too_many_attempts" {
				ae.RetryAfter = int(ack.RespAck.RetryAfterSeconds)
			}
		}

		return ae
	}

	return nil
}

// UnreachableError is the bridge answering for a device it can't reach.
type UnreachableError struct{ Message string }

func (e *UnreachableError) Error() string { return e.Message }

// AuthError is the device's answer to a password it did not accept.
type AuthError struct {
	Message    string
	RetryAfter int // seconds, when the address is locked out for too many attempts
}

func (e *AuthError) Error() string { return e.Message }

func (c *Client) readLoop(conn *websocket.Conn, gen int64) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			c.mu.Lock()
			stale := gen != c.gen
			if !stale {
				c.conn = nil
				c.open = false
				c.signedIn = false
				c.failAllLocked(err)
			}
			c.mu.Unlock()
			if !stale {
				if c.OnDisconnect != nil {
					c.OnDisconnect(err)
				}
				c.scheduleReconnect(gen)
			}

			return
		}
		resp := &pb.RespEnvelope{}
		if err := proto.Unmarshal(data, resp); err != nil {
			continue
		}
		c.mu.Lock()
		ch := c.waiters[resp.Id]
		delete(c.waiters, resp.Id)
		c.mu.Unlock()
		if ch != nil {
			ch <- resp
		}
		// The device no longer knows this session - it restarted (an
		// update) while the bridge kept this socket, pairing it with a
		// fresh, signed-out connection. Every request would now fail with
		// "not authenticated" until something reconnected: close, and the
		// read error above reconnects and signs in again. Same as
		// WSClient.swift.
		c.mu.Lock()
		signedIn := c.signedIn
		c.mu.Unlock()
		if ack := resp.GetRespAck(); ack != nil && ack.Code == "not_authenticated" && signedIn {
			log.Printf("the device no longer knows this session - reconnecting to sign in again")
			_ = conn.Close()
		}
	}
}

func (c *Client) failAllLocked(err error) {
	for id, ch := range c.waiters {
		close(ch)
		delete(c.waiters, id)
	}
	_ = err
}

func (c *Client) scheduleReconnect(gen int64) {
	c.mu.Lock()
	if !c.autoRecon || gen != c.gen {
		c.mu.Unlock()

		return
	}
	delay := c.backoff
	c.backoff = min(c.backoff*2, maxBackoff)
	c.mu.Unlock()
	time.AfterFunc(delay, func() {
		c.mu.Lock()
		ok := c.autoRecon && gen == c.gen && !c.open
		c.mu.Unlock()
		if ok {
			c.dial(gen)
		}
	})
}

// Request sends one envelope and waits for the reply with the same id.
func (c *Client) Request(ctx context.Context, build func(*pb.ReqEnvelope)) (*pb.RespEnvelope, error) {
	c.mu.Lock()
	if !c.open || c.conn == nil {
		c.mu.Unlock()

		return nil, ErrNotConnected
	}
	conn := c.conn
	id := atomic.AddInt32(&c.nextID, 1)
	req := &pb.ReqEnvelope{Id: id}
	build(req)
	req.Id = id
	ch := make(chan *pb.RespEnvelope, 1)
	c.waiters[id] = ch
	c.mu.Unlock()

	data, err := proto.Marshal(req)
	if err != nil {
		c.dropWaiter(id)

		return nil, err
	}
	c.writeMu.Lock()
	err = conn.WriteMessage(websocket.BinaryMessage, data)
	c.writeMu.Unlock()
	if err != nil {
		c.dropWaiter(id)

		return nil, err
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, ErrNotConnected
		}

		return resp, nil
	case <-ctx.Done():
		c.dropWaiter(id)

		return nil, ctx.Err()
	}
}

func (c *Client) dropWaiter(id int32) {
	c.mu.Lock()
	delete(c.waiters, id)
	c.mu.Unlock()
}

// RespError is the device's own rejection of a request, which still comes
// back as a normal envelope (see SyncModel's upload/delete comments).
func RespError(resp *pb.RespEnvelope, fallback string) error {
	if resp == nil {
		return errors.New(fallback)
	}
	if resp.Error {
		if resp.ErrorMessage != "" {
			return errors.New(resp.ErrorMessage)
		}

		return errors.New(fallback)
	}

	return nil
}
