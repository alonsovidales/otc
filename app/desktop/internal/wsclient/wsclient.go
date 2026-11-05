// SPDX-License-Identifier: AGPL-3.0-or-later

// Package wsclient is WSClient.swift: one WebSocket to the device (or the
// bridge in front of it), protobuf envelopes correlated by id, auth on
// connect, and reconnection with backoff.
package wsclient

import (
	"bytes"
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

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/app/desktop/internal/oslang"
	"github.com/alonsovidales/otc/i18n"
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

// Issue #207: a device that can't reach its storage disks answers every
// request, GetPubKey first, with this error code and its own words. It
// is asked again every storageRetry (Client.storageRetry: a test makes it
// short), not on the growing delay: the disks come back when someone plugs
// them in, and the device starts again by itself then. A network change or
// a wake asks at once (Reconnect).
const (
	codeStorageUnavailable = "storage_unavailable"
	storageRetry           = 30 * time.Second
)

// storageAnswer reports whether resp is the device saying it can't reach
// its storage, with its words: the envelope's error_code, or an Ack's
// code.
func storageAnswer(resp *pb.RespEnvelope) (string, bool) {
	if resp.GetErrorCode() == codeStorageUnavailable {
		if msg := resp.GetErrorMessage(); msg != "" {
			return msg, true
		}

		return resp.GetRespAck().GetErrorMsg(), true
	}
	if ack := resp.GetRespAck(); ack != nil && ack.GetCode() == codeStorageUnavailable {
		if msg := ack.GetErrorMsg(); msg != "" {
			return msg, true
		}

		return resp.GetErrorMessage(), true
	}

	return "", false
}

// The client's own keep-alive: a ping every pingEvery, and a
// connection that has received nothing - no answer, no pong - for
// deadAfter while nothing is being sent is taken for dead and dialled
// again. A connection that died without a word (the computer asleep, the
// Wi-Fi changed, a router that forgot it) used to look alive until TCP
// gave up, or a request's own timeout ran out - half an hour. While a
// message is being written the pong can't come back before it is out: a
// write has its own deadline instead (writeTimeout).
const (
	pingEvery = 20 * time.Second
	deadAfter = 45 * time.Second
)

// writeTimeout bounds writing one message of n bytes: half a minute, and
// as long again as the bytes take at 32 KiB/s (a 4 MiB piece of a file:
// about two and a half minutes more).
func writeTimeout(n int) time.Duration {
	return 30*time.Second + time.Duration(n)*time.Second/(32<<10)
}

// ErrNotConnected is what a request gets while the socket is down. A
// folder's line in the tray says so in the user's language (MsgError).
var ErrNotConnected error = &MsgError{M: i18n.DeskMainNotConnectedError()}

// MsgError is an error of ours that the tray words in the user's language
// (docs/i18n.md): Msg is the catalog's message, rendered when it is shown;
// Error is its English, for the command line and the log. Only for keys
// whose arguments are text or numbers below 1000, whose English rendering
// is the plain text (a user argument would come with isolation marks).
type MsgError struct{ M i18n.Msg }

func (e *MsgError) Error() string { return e.M.Render("") }

// Msg is the message, for the tray to render in its language.
func (e *MsgError) Msg() i18n.Msg { return e.M }

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
	// OnStorageUnavailable reports that the device answered it can't reach
	// its storage disks (issue #207), with its words ("" when it gave
	// none): not a wrong password and not unreachable - the client asks
	// again every storageRetry and signs in once the device answers
	// otherwise. Nothing about the credentials changes.
	OnStorageUnavailable func(msg string)
	// OnLocalEndpoint reports a change in what the device at domain (as
	// given to Configure) says about its home-network endpoint (issue
	// #190): one to keep, or nil to forget the one kept. Not called when
	// the question went unanswered.
	OnLocalEndpoint func(domain string, ep *LocalEndpoint)

	mu       sync.Mutex
	domain   string
	url      string
	clientID string
	password string
	// local is tried before url on every dial (SetLocalEndpoint, or
	// learnt), except the one after a sign-in there failed (skipLocal);
	// route is the signed-in connection's.
	local     *LocalEndpoint
	skipLocal bool
	route     Route
	// dialFailed is why the configured address last didn't connect, as
	// logged (logDialFailure): a new reason is logged, the same one at
	// every retry isn't.
	dialFailed string
	// sessionPin is the device's certificate pin as this sign-in confirmed
	// it (SessionPin): the home route's handshake checked it, or the
	// device itself answered it (GetLocalEndpoint). pinKnown says whether
	// there is one; pinAsked is when ConfirmPin last asked.
	sessionPin []byte
	pinKnown   bool
	pinAsked   time.Time
	conn       *websocket.Conn
	open       bool
	// signedIn: the device accepted the password on this socket.
	// IsConnected is both - an open socket still signing in used to count,
	// so a folder's first ListFiles raced the Auth and got "not
	// authenticated", which readLoop takes for a lost session and closes
	// the socket on, cutting its own sign-in short (as WSClient.swift).
	signedIn bool
	// signIns counts the sign-ins (SignIns).
	signIns   int64
	waiters   map[int32]chan *pb.RespEnvelope
	nextID    int32
	autoRecon bool
	backoff   time.Duration
	gen       int64 // bumped on every connect, so a stale loop can tell it is stale
	writeMu   sync.Mutex
	// The keep-alive's view of the socket (keepAlive): when something last
	// came in or a write last ended, and how many writes are under way.
	lastSeen atomic.Int64
	writing  atomic.Int32
	// pingEvery and deadAfter, per client (a test makes them short).
	pingEvery, deadAfter time.Duration
	// storageRetry, per client likewise.
	storageRetry time.Duration
	// lang is ReqEnvelope.lang, the language otc-sync shows (SetLang).
	lang string
}

// New makes an unconfigured client, sending the computer's language
// until SetLang says otherwise.
func New() *Client {
	return &Client{waiters: map[int32]chan *pb.RespEnvelope{}, backoff: initialBackoff, lang: oslang.System(),
		pingEvery: pingEvery, deadAfter: deadAfter, storageRetry: storageRetry}
}

// SetLang sets the language every request carries as ReqEnvelope.lang:
// the one otc-sync shows (oslang.Effective of the choice), so the device
// answers in it (docs/i18n.md).
func (c *Client) SetLang(code string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lang = code
}

// Configure takes a bare host (bridge) or a full ws:// / wss:// URL, and the
// credentials Auth sends (SettingsStore's domain/password, plus this
// install's id). An address saved before otc-sync put typed ones right
// (http://, no /ws, a home host with no scheme) is dialled as
// config.NormalizeAddress makes it.
func (c *Client) Configure(domain, clientID, password string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if domain != c.domain {
		c.local = nil // another device's
		c.skipLocal = false
	}
	c.domain = domain
	d := config.NormalizeAddress(domain)
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

// SignIns is how many times this client has signed in. Read before a
// request, a different value afterwards means the connection it went out
// on has been replaced by a new one (issue #205: an upload cut by a drop
// goes on once there is one). As WSClient.signInCount.
func (c *Client) SignIns() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.signIns
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
	local := c.local.clone()
	if c.skipLocal {
		local = nil
		c.skipLocal = false
	}
	c.mu.Unlock()
	// Issue #190: the home network first, where the device has given one.
	if local != nil {
		conn, lu, err := dialLocal(context.Background(), local)
		if err == nil {
			log.Printf("connected over the home network (%s)", lu)
			c.run(gen, conn, RouteLocal, local.Pin)

			return
		}
		log.Printf("the device did not answer on the home network, connecting through the configured address: %v", err)
		if !c.current(gen, nil) {
			return
		}
	}
	parsed, err := url.Parse(u)
	if err != nil {
		c.logDialFailure(u, err)
		if c.OnDisconnect != nil && c.current(gen, nil) {
			c.OnDisconnect(fmt.Errorf("bad address %q: %w", u, err)) // i18n-ignore: the tray shows Disconnected; the command line prints it
		}

		return
	}
	dialer := websocket.Dialer{HandshakeTimeout: handshakeTO}
	conn, _, err := dialer.Dial(parsed.String(), nil)
	if err != nil {
		c.logDialFailure(parsed.Redacted(), err)
		if c.OnDisconnect != nil && c.current(gen, nil) {
			c.OnDisconnect(err)
		}
		c.scheduleReconnect(gen)

		return
	}
	c.mu.Lock()
	c.dialFailed = ""
	c.mu.Unlock()
	c.run(gen, conn, RouteRemote, nil)
}

// logDialFailure logs why the configured address didn't connect - each new
// reason once, not at every retry - so Show Log says what was wrong: a TLS
// handshake on a port that has none, a page where the device's socket isn't
// (2026-11-05: otc-sync said only "Disconnected", and the log nothing).
func (c *Client) logDialFailure(addr string, err error) {
	why := err.Error()
	c.mu.Lock()
	same := why == c.dialFailed
	c.dialFailed = why
	c.mu.Unlock()
	if !same {
		log.Printf("could not connect to %s: %v", addr, err)
	}
}

// run signs in on a fresh socket and, once it is in, announces it. pin is
// the certificate the home route's handshake checked (nil elsewhere).
func (c *Client) run(gen int64, conn *websocket.Conn, route Route, pin []byte) {
	conn.SetReadLimit(maxMessageSize)
	c.seen()
	conn.SetPongHandler(func(string) error {
		c.seen()

		return nil
	})

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
	go c.keepAlive(conn, gen)

	ctx := context.Background()
	if route == RouteLocal {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, localSignInTO)
		defer cancel()
	}
	err := c.auth(ctx, gen, conn)
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
	var se *StorageError
	if errors.As(err, &se) {
		// The device is there but can't reach its disks: say so, close,
		// and ask again in storageRetry - the read loop's reconnect takes
		// the delay set here (a sign-in puts it back to the first one).
		c.mu.Lock()
		current := c.currentLocked(gen, nil)
		if current {
			c.backoff = c.storageRetry
		}
		c.mu.Unlock()
		if current && c.OnStorageUnavailable != nil {
			c.OnStorageUnavailable(se.Message)
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
		// One that failed at home goes through the configured address
		// next: a home route that connects but can't sign in must not
		// keep the client from the one that works.
		if route == RouteLocal {
			c.mu.Lock()
			if c.currentLocked(gen, nil) {
				c.skipLocal = true
			}
			c.mu.Unlock()
		}
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
		// Reported before the close: that wakes the read loop, whose
		// "use of closed network connection" would otherwise reach
		// OnDisconnect first - connectOnce takes the first answer, and a
		// wrong password showed as a socket error.
		if c.OnAuthFailed != nil {
			c.OnAuthFailed(err.Error(), ae.RetryAfter)
		}
		_ = conn.Close()
		if c.OnDisconnect != nil {
			c.OnDisconnect(err)
		}

		return
	}
	if route == RouteRemote {
		lconn, lu, learnt := c.learnLocal(gen, conn)
		if lconn != nil {
			c.moveLocal(gen, conn, lconn, lu, learnt.Pin)

			return
		}
		if learnt != nil {
			pin = learnt.Pin // the device's own answer, on this sign-in
		}
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
	c.signIns++
	c.route = route
	c.sessionPin, c.pinKnown, c.pinAsked = bytes.Clone(pin), len(pin) > 0, time.Time{}
	c.mu.Unlock()
	if c.OnConnect != nil {
		c.OnConnect()
	}
}

// moveLocal swaps a connection signed in at the configured address, on
// which nothing else was sent, for lconn on the home network, and signs
// in there. The old one is closed unreported: its attempt is superseded.
func (c *Client) moveLocal(gen int64, conn, lconn *websocket.Conn, lu string, pin []byte) {
	c.mu.Lock()
	if !c.currentLocked(gen, conn) || !c.autoRecon {
		c.mu.Unlock()
		_ = lconn.Close()

		return
	}
	c.gen++
	ngen := c.gen
	c.conn = nil
	c.open = false
	c.failAllLocked(errSuperseded)
	c.mu.Unlock()
	_ = conn.Close()
	log.Printf("the device is on this network: moving to %s", lu)
	c.run(ngen, lconn, RouteLocal, pin)
}

// errSuperseded ends a sign-in whose attempt is no longer the client's.
var errSuperseded = errors.New("connection replaced") // i18n-ignore: an attempt the client dropped, never shown

func (c *Client) auth(ctx context.Context, gen int64, conn *websocket.Conn) error {
	pk, err := c.Request(ctx, func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqGetPubKey{ReqGetPubKey: &pb.GetPubKey{}}
	})
	if err != nil {
		return err
	}
	// Issue #207: the device's own answer that it can't reach its disks,
	// before any sign-in - not a refusal of the password.
	if msg, ok := storageAnswer(pk); ok {
		return &StorageError{Message: msg}
	}
	pub, ok := pk.Payload.(*pb.RespEnvelope_RespPubKey)
	if !ok {
		if ack := pk.GetRespAck(); ack != nil && (ack.Code == "device_unreachable" || ack.Code == "device_disabled") {
			return &UnreachableError{Message: ack.ErrorMsg}
		}
		return errors.New("unable to fetch the connection's public key") // i18n-ignore: the tray shows Disconnected; the command line prints it
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
	resp, err := c.Request(ctx, func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqAuth{ReqAuth: &pb.Auth{Uuid: id, Key: enc, Create: false}}
	})
	if err != nil {
		return err
	}
	// The disks went between the key and the password: the password was
	// never looked at.
	if msg, ok := storageAnswer(resp); ok {
		return &StorageError{Message: msg}
	}
	ack, ok := resp.Payload.(*pb.RespEnvelope_RespAck)
	if !ok || !ack.RespAck.Ok {
		ae := &AuthError{Message: "authentication rejected"} // i18n-ignore: the tray shows Wrong password; the command line prints it
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

// StorageError is the device answering that it can't reach its storage
// disks (issue #207): Message is its words, "" when it gave none.
type StorageError struct{ Message string }

func (e *StorageError) Error() string {
	if e.Message == "" {
		return codeStorageUnavailable
	}

	return e.Message
}

// AuthError is the device's answer to a password it did not accept.
type AuthError struct {
	Message    string
	RetryAfter int // seconds, when the address is locked out for too many attempts
}

func (e *AuthError) Error() string { return e.Message }

// seen notes that the socket is alive: something came in, or a write ended.
func (c *Client) seen() { c.lastSeen.Store(time.Now().UnixNano()) }

// keepAlive pings the device (or the bridge in front of it) every
// pingEvery and closes conn once it has been silent for deadAfter while
// nothing was being written: the read loop then reports the drop and
// dials again. It ends with the connection.
func (c *Client) keepAlive(conn *websocket.Conn, gen int64) {
	every, dead := c.pingEvery, c.deadAfter
	check := time.NewTicker(min(time.Second, every))
	defer check.Stop()
	lastPing := time.Now()
	for range check.C {
		if !c.current(gen, conn) {
			return
		}
		if time.Since(lastPing) >= every {
			lastPing = time.Now()
			// WriteControl may go out between the frames of a message
			// being written; one that can't is left for the next.
			_ = conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
		}
		quiet := time.Since(time.Unix(0, c.lastSeen.Load()))
		if c.writing.Load() == 0 && quiet > dead {
			log.Printf("no answer from the device for %s - connecting again", quiet.Round(time.Second))
			_ = conn.Close()

			return
		}
	}
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
		c.seen()
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
		// Issue #207: the device lost its disks under a session: say so
		// and sign in again, which lands in the storageRetry cycle until
		// they are back.
		if msg, ok := storageAnswer(resp); ok && signedIn && c.current(gen, conn) {
			log.Printf("the device can't reach its storage - signing in again")
			if c.OnStorageUnavailable != nil {
				c.OnStorageUnavailable(msg)
			}
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
	// Set after the build, which may replace the whole envelope: the one
	// place lang is set (docs/i18n.md).
	req.Id = id
	req.Lang = c.lang
	ch := make(chan *pb.RespEnvelope, 1)
	c.waiters[id] = ch
	c.mu.Unlock()

	data, err := proto.Marshal(req)
	if err != nil {
		c.dropWaiter(id)

		return nil, err
	}
	c.writeMu.Lock()
	c.writing.Add(1)
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout(len(data))))
	err = conn.WriteMessage(websocket.BinaryMessage, data)
	c.writing.Add(-1)
	c.seen()
	c.writeMu.Unlock()
	if err != nil {
		c.dropWaiter(id)

		return nil, &sendError{err: err}
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

// sendError is a request that could not be written: its connection is
// gone (Dropped), even while the socket doesn't say so yet - a failed
// write breaks it for good, and the keep-alive closes it.
type sendError struct{ err error }

func (e *sendError) Error() string { return e.err.Error() }
func (e *sendError) Unwrap() error { return e.err }

// Dropped: err is a request's connection going away under it, or not
// there to begin with - neither the device's answer nor an answer that
// took too long (the request's own deadline). As WSClient.isDropped.
func Dropped(err error) bool {
	var se *sendError

	return errors.Is(err, ErrNotConnected) || errors.As(err, &se)
}

func (c *Client) dropWaiter(id int32) {
	c.mu.Lock()
	delete(c.waiters, id)
	c.mu.Unlock()
}

// RespErrorMsg is RespError whose fallback, for a refusal without the
// device's words, the tray shows in the user's language (MsgError).
func RespErrorMsg(resp *pb.RespEnvelope, fallback i18n.Msg) error {
	if resp != nil && resp.Error && resp.ErrorMessage != "" {
		return errors.New(resp.ErrorMessage)
	}
	if resp == nil || resp.Error {
		return &MsgError{M: fallback}
	}

	return nil
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
