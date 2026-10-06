// SPDX-License-Identifier: AGPL-3.0-or-later

package wsclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/gorilla/websocket"

	pb "github.com/alonsovidales/otc/proto/generated"
)

// Issue #190: at home the device is reached on the home network, over TLS
// pinned to the device's own self-signed certificate, rather than through
// the bridge, where every byte crossed the home uplink twice. The pin is
// learnt over a signed-in session at the configured address (GetLocalEndpoint),
// so the bridge is trusted exactly as before; anything that doesn't
// answer with that certificate in time leaves the client on the
// configured address, as before.

// Route is which way the signed-in connection reaches the device.
type Route string

const (
	RouteNone Route = ""
	// RouteLocal is the device's home-network address, pinned TLS.
	RouteLocal Route = "local"
	// RouteRemote is the configured address: the bridge, or a custom one.
	RouteRemote Route = "remote"
)

// localBudget is how long all the home-network addresses together get
// before the configured address is dialled (a var for the tests).
var localBudget = 2500 * time.Millisecond

// localAskTO bounds GetLocalEndpoint, asked before OnConnect: a device
// that doesn't answer delays the sign-in by this much at most.
var localAskTO = 5 * time.Second

// localSignInTO bounds a sign-in at home (the device derives the key with
// Argon2id, two at a time): one that stalls there is given up, and the
// next dial goes through the configured address (Client.skipLocal).
var localSignInTO = 30 * time.Second

const maxLocalAddresses = 8

// LocalEndpoint is the device's answer to GetLocalEndpoint.
type LocalEndpoint struct {
	Addresses []string // bare IPs, IPv4 first, as the device lists them
	Port      int
	Pin       []byte // SHA-256 of the certificate's DER bytes
}

// NewLocalEndpoint keeps the usable part of what the device (or the
// store) says: IP literals only (nothing to resolve), a port, and a pin
// of the right length. Nil when nothing usable is left.
func NewLocalEndpoint(addresses []string, port int, pin []byte) *LocalEndpoint {
	if port <= 0 || port > 65535 || len(pin) != sha256.Size {
		return nil
	}
	ep := &LocalEndpoint{Port: port, Pin: bytes.Clone(pin)}
	seen := map[string]bool{}
	for _, a := range addresses {
		ip := net.ParseIP(a)
		if ip == nil || seen[ip.String()] || len(ep.Addresses) == maxLocalAddresses {
			continue
		}
		seen[ip.String()] = true
		ep.Addresses = append(ep.Addresses, ip.String())
	}
	if len(ep.Addresses) == 0 {
		return nil
	}

	return ep
}

func endpointFromProto(le *pb.LocalEndpoint) *LocalEndpoint {
	return NewLocalEndpoint(le.GetAddresses(), int(le.GetPort()), le.GetCertSha256())
}

// Equal: the same addresses (in any order: they are tried at once), port
// and pin; nil equals nil.
func (ep *LocalEndpoint) Equal(o *LocalEndpoint) bool {
	if ep == nil || o == nil {
		return ep == o
	}
	if ep.Port != o.Port || !bytes.Equal(ep.Pin, o.Pin) || len(ep.Addresses) != len(o.Addresses) {
		return false
	}
	have := map[string]bool{}
	for _, a := range ep.Addresses {
		have[a] = true
	}
	for _, a := range o.Addresses {
		if !have[a] {
			return false
		}
	}

	return true
}

func (ep *LocalEndpoint) clone() *LocalEndpoint {
	if ep == nil {
		return nil
	}

	return &LocalEndpoint{Addresses: append([]string(nil), ep.Addresses...), Port: ep.Port, Pin: bytes.Clone(ep.Pin)}
}

// urls are the WebSocket addresses to try; an IPv6 one goes in brackets.
func (ep *LocalEndpoint) urls() []string {
	out := make([]string, 0, len(ep.Addresses))
	for _, a := range ep.Addresses {
		out = append(out, "wss://"+net.JoinHostPort(a, strconv.Itoa(ep.Port))+"/ws")
	}

	return out
}

var errPinMismatch = errors.New("not the device's certificate")

// verifyPin accepts only a leaf certificate whose DER hashes to pin. It
// runs inside the TLS handshake, so a server that fails it never gets the
// HTTP upgrade, let alone the password.
func verifyPin(pin []byte) func(tls.ConnectionState) error {
	return func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errPinMismatch
		}
		sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
		if len(pin) != sha256.Size || subtle.ConstantTimeCompare(sum[:], pin) != 1 {
			return errPinMismatch
		}

		return nil
	}
}

// pinnedTLS trusts the one certificate whose DER hashes to pin, whatever
// its name or issuer: no CA can vouch for a private address.
// InsecureSkipVerify only turns off the CA and hostname checks, and is
// safe solely because VerifyConnection replaces them (it runs for every
// handshake, resumptions included; there is no session cache anyway).
func pinnedTLS(pin []byte) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, //nolint:gosec // pinned by VerifyConnection
		VerifyConnection:   verifyPin(bytes.Clone(pin)),
	}
}

// dialLocal tries every address at once, within localBudget: the first
// to finish the WebSocket handshake wins, the others are cancelled (and
// closed, should one still get through).
func dialLocal(ctx context.Context, ep *LocalEndpoint) (*websocket.Conn, string, error) {
	ctx, cancel := context.WithTimeout(ctx, localBudget)
	defer cancel()
	dialer := websocket.Dialer{HandshakeTimeout: localBudget, TLSClientConfig: pinnedTLS(ep.Pin)}
	type result struct {
		conn *websocket.Conn
		url  string
		err  error
	}
	urls := ep.urls()
	results := make(chan result, len(urls))
	for _, u := range urls {
		go func() {
			conn, _, err := dialer.DialContext(ctx, u, nil)
			results <- result{conn, u, err}
		}()
	}
	var errs []error
	for left := len(urls); left > 0; left-- {
		r := <-results
		if r.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.url, r.err))

			continue
		}
		cancel()
		go func(left int) {
			for ; left > 0; left-- {
				if late := <-results; late.conn != nil {
					_ = late.conn.Close()
				}
			}
		}(left - 1)

		return r.conn, r.url, nil
	}
	if len(errs) == 0 {
		return nil, "", errors.New("no home-network address")
	}

	return nil, "", errors.Join(errs...)
}

// SetLocalEndpoint is the endpoint the configured device last gave (nil:
// none), loaded from the store before Connect. It only applies to the
// next dial; Configure for another device drops it.
func (c *Client) SetLocalEndpoint(ep *LocalEndpoint) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.local = ep.clone()
	c.skipLocal = false
}

// HasLocalEndpoint is whether there is a home-network route to try.
func (c *Client) HasLocalEndpoint() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.local != nil
}

// Route is the way the signed-in connection goes; RouteNone when there
// is none.
func (c *Client) Route() Route {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.open || !c.signedIn {
		return RouteNone
	}

	return c.route
}

// Pending is how many requests are waiting for their answer.
func (c *Client) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.waiters)
}

// ErrReconnecting is what OnDisconnect gets for a Reconnect.
var ErrReconnecting = errors.New("reconnecting")

// Reconnect drops the connection, if there is one, and dials again at
// once, choosing the route afresh (a network change). It does nothing
// after Disconnect or a rejected password, and reports whether it
// reconnects.
func (c *Client) Reconnect() bool {
	c.mu.Lock()
	if !c.autoRecon || c.url == "" {
		c.mu.Unlock()

		return false
	}
	conn := c.conn
	c.conn = nil
	c.open = false
	c.signedIn = false
	c.gen++
	gen := c.gen
	c.backoff = initialBackoff
	c.failAllLocked(ErrReconnecting)
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	if c.OnDisconnect != nil {
		c.OnDisconnect(ErrReconnecting)
	}
	go c.dial(gen)

	return true
}

// localVerdict is what a GetLocalEndpoint answer means for the store.
type localVerdict int

const (
	localKeep  localVerdict = iota // no answer, or a refusal unrelated to the endpoint
	localStore                     // a usable endpoint
	localClear                     // the device has none to give
)

// judgeLocal reads the answer to GetLocalEndpoint. A device from before
// this request answers error_code "unknown_payload", one with no listener
// or no private address "local_unavailable": nothing to use at home.
// Anything else - the bridge's device_unreachable, a lost session - says
// nothing about the endpoint.
func judgeLocal(resp *pb.RespEnvelope) (localVerdict, *LocalEndpoint) {
	if le := resp.GetRespLocalEndpoint(); le != nil && !resp.GetError() {
		if ep := endpointFromProto(le); ep != nil {
			return localStore, ep
		}

		return localClear, nil // answered, with nothing usable
	}
	if resp.GetError() {
		switch resp.GetErrorCode() {
		case "unknown_payload", "local_unavailable":
			return localClear, nil
		}
	}

	return localKeep, nil
}

// learnLocal asks a device just signed in to at the configured address
// where it answers at home, keeps that (OnLocalEndpoint), and tries it
// right away when it is new: OnConnect hasn't run, so nothing is in
// flight that moving could interrupt. It returns the home-network
// connection to move to, or nil to stay.
func (c *Client) learnLocal(gen int64, conn *websocket.Conn) (*websocket.Conn, string) {
	ctx, cancel := context.WithTimeout(context.Background(), localAskTO)
	resp, err := c.Request(ctx, func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqGetLocalEndpoint{ReqGetLocalEndpoint: &pb.GetLocalEndpoint{}}
	})
	cancel()
	if err != nil {
		return nil, "" // no answer: what is stored stays
	}
	verdict, ep := judgeLocal(resp)
	if verdict == localKeep {
		return nil, ""
	}
	c.mu.Lock()
	if !c.currentLocked(gen, conn) {
		c.mu.Unlock()

		return nil, ""
	}
	changed := !ep.Equal(c.local)
	c.local = ep.clone()
	domain := c.domain
	c.mu.Unlock()
	if changed && c.OnLocalEndpoint != nil {
		c.OnLocalEndpoint(domain, ep.clone())
	}
	// Unchanged, it was tried at the start of this dial already.
	if !changed || ep == nil {
		return nil, ""
	}
	lconn, u, err := dialLocal(context.Background(), ep)
	if err != nil {
		return nil, ""
	}

	return lconn, u
}
