// SPDX-License-Identifier: AGPL-3.0-or-later

// Package wsclient is WSClient.swift: one WebSocket to the device (or the
// bridge in front of it), protobuf envelopes correlated by id, auth on
// connect, and reconnection with backoff.
package wsclient

import (
	"context"
	"errors"
	"fmt"
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
	handshakeTO    = 20 * time.Second
)

// ErrNotConnected is what a request gets while the socket is down.
var ErrNotConnected = errors.New("not connected")

// Client is safe to use from any goroutine.
type Client struct {
	// OnConnect runs once the connection is up and authenticated; OnDisconnect
	// whenever it goes away (err is nil for a deliberate close).
	OnConnect    func()
	OnDisconnect func(err error)
	// OnAuthFailed reports a rejected password, so the UI can say so rather
	// than just "disconnected".
	OnAuthFailed func(msg string)

	mu        sync.Mutex
	url       string
	clientID  string
	password  string
	conn      *websocket.Conn
	open      bool
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

	return c.open
}

func (c *Client) dial(gen int64) {
	c.mu.Lock()
	u := c.url
	c.mu.Unlock()
	parsed, err := url.Parse(u)
	if err != nil {
		if c.OnDisconnect != nil {
			c.OnDisconnect(fmt.Errorf("bad address %q: %w", u, err))
		}

		return
	}
	dialer := websocket.Dialer{HandshakeTimeout: handshakeTO}
	conn, _, err := dialer.Dial(parsed.String(), nil)
	if err != nil {
		if c.OnDisconnect != nil {
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
	c.backoff = initialBackoff
	c.mu.Unlock()

	go c.readLoop(conn, gen)

	if err := c.auth(); err != nil {
		if c.OnAuthFailed != nil {
			c.OnAuthFailed(err.Error())
		}
		// A wrong password is not a reason to hammer the device: the
		// socket stays down until the settings change and Connect is
		// called again.
		c.mu.Lock()
		c.autoRecon = false
		c.conn = nil
		c.open = false
		c.failAllLocked(err)
		c.mu.Unlock()
		_ = conn.Close()
		if c.OnDisconnect != nil {
			c.OnDisconnect(err)
		}

		return
	}
	if c.OnConnect != nil {
		c.OnConnect()
	}
}

func (c *Client) auth() error {
	pk, err := c.Request(context.Background(), func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqGetPubKey{ReqGetPubKey: &pb.GetPubKey{}}
	})
	if err != nil {
		return err
	}
	pub, ok := pk.Payload.(*pb.RespEnvelope_RespPubKey)
	if !ok {
		return errors.New("unable to fetch the connection's public key")
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
		msg := "authentication rejected"
		if ok && ack.RespAck.ErrorMsg != "" {
			msg = ack.RespAck.ErrorMsg
		}

		return errors.New(msg)
	}

	return nil
}

func (c *Client) readLoop(conn *websocket.Conn, gen int64) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			c.mu.Lock()
			stale := gen != c.gen
			if !stale {
				c.conn = nil
				c.open = false
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
