// SPDX-License-Identifier: AGPL-3.0-or-later

package wsclient

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	pb "github.com/alonsovidales/otc/proto/generated"
)

// storageDevice stands in for a device that can't reach its disks (issue
// #207): every request answered with error_code "storage_unavailable" and
// its words until disksBack, then a device that signs in. It notes when
// each GetPubKey came.
type storageDevice struct {
	srv       *httptest.Server
	disksBack atomic.Bool
	// After a sign-in, the next request is answered with the code once
	// (the disks lost under a session).
	loseUnderSession atomic.Bool
	auths            atomic.Int32
	mu               sync.Mutex
	asks             []time.Time
}

const storageWords = "Your device can't reach its storage disks (test)."

func newStorageDevice(t *testing.T) *storageDevice {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	d := &storageDevice{}
	up := websocket.Upgrader{}
	d.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			req := &pb.ReqEnvelope{}
			if proto.Unmarshal(data, req) != nil {
				return
			}
			resp := &pb.RespEnvelope{Id: req.Id}
			_, isKey := req.Payload.(*pb.ReqEnvelope_ReqGetPubKey)
			if isKey {
				d.mu.Lock()
				d.asks = append(d.asks, time.Now())
				d.mu.Unlock()
			}
			switch {
			case !d.disksBack.Load():
				resp.Error = true
				resp.ErrorCode = "storage_unavailable"
				resp.ErrorMessage = storageWords
			case isKey:
				resp.Payload = &pb.RespEnvelope_RespPubKey{RespPubKey: &pb.PubKey{PublicKey: pubDER}}
			default:
				if _, ok := req.Payload.(*pb.ReqEnvelope_ReqAuth); ok {
					d.auths.Add(1)
					resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}

					break
				}
				if d.loseUnderSession.CompareAndSwap(true, false) {
					resp.Error = true
					resp.ErrorCode = "storage_unavailable"
					resp.ErrorMessage = storageWords

					break
				}
				resp.Payload = &pb.RespEnvelope_RespAck{RespAck: &pb.Ack{Ok: true}}
			}
			b, _ := proto.Marshal(resp)
			if c.WriteMessage(websocket.BinaryMessage, b) != nil {
				return
			}
		}
	}))
	t.Cleanup(d.srv.Close)

	return d
}

func (d *storageDevice) url() string { return "ws" + strings.TrimPrefix(d.srv.URL, "http") + "/ws" }

func (d *storageDevice) askTimes() []time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]time.Time(nil), d.asks...)
}

func waitFor(t *testing.T, what string, within time.Duration, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Issue #207: a device that answers GetPubKey with storage_unavailable is
// neither a wrong password nor unreachable. The client says so with the
// device's words, keeps its credentials and its reconnecting, asks again
// every storageRetry - not sooner, and not on the growing delay - and
// signs in as soon as the device answers otherwise.
func TestStorageUnavailableIsAskedAgainEveryInterval(t *testing.T) {
	d := newStorageDevice(t)
	c := New()
	const retry = 400 * time.Millisecond
	c.storageRetry = retry
	c.Configure(d.url(), "test", "secret")
	var said []string
	var mu sync.Mutex
	var authFailed, unreachable, connected atomic.Int32
	c.OnStorageUnavailable = func(msg string) {
		mu.Lock()
		said = append(said, msg)
		mu.Unlock()
	}
	c.OnAuthFailed = func(string, int) { authFailed.Add(1) }
	c.OnUnreachable = func(string) { unreachable.Add(1) }
	c.OnConnect = func() { connected.Add(1) }
	c.Connect()
	defer c.Disconnect()

	waitFor(t, "three asks", 5*time.Second, func() bool { return len(d.askTimes()) >= 3 })
	asks := d.askTimes()
	for i := 1; i < 3; i++ {
		gap := asks[i].Sub(asks[i-1])
		// Never sooner than the interval; and not the 1 s doubling the
		// other failures wait (1 s, then 2 s).
		if gap < retry-20*time.Millisecond || gap >= 1100*time.Millisecond {
			t.Fatalf("ask %d came %v after the one before; want about %v", i+1, gap, retry)
		}
	}
	mu.Lock()
	if len(said) < 2 || said[0] != storageWords {
		t.Fatalf("OnStorageUnavailable got %q; want the device's words at each answer", said)
	}
	mu.Unlock()
	if authFailed.Load() != 0 || unreachable.Load() != 0 || connected.Load() != 0 {
		t.Fatalf("auth failures %d, unreachable %d, connects %d; want none", authFailed.Load(), unreachable.Load(), connected.Load())
	}
	c.mu.Lock()
	pw, recon := c.password, c.autoRecon
	c.mu.Unlock()
	if pw != "secret" || !recon {
		t.Fatalf("password %q, reconnecting %v: the credentials and the reconnecting must stay", pw, recon)
	}

	// The disks are back: the next ask signs in, within the interval.
	d.disksBack.Store(true)
	waitFor(t, "the sign-in once the disks are back", 2*time.Second, func() bool { return connected.Load() == 1 })
	if d.auths.Load() != 1 {
		t.Fatalf("sign-ins %d, want 1", d.auths.Load())
	}
	c.mu.Lock()
	backoff := c.backoff
	c.mu.Unlock()
	if backoff != initialBackoff {
		t.Fatalf("backoff %v after the sign-in, want %v", backoff, initialBackoff)
	}
}

// A network change or a wake (Reconnect) asks at once, not at the end of
// the interval.
func TestStorageUnavailableReconnectAsksAtOnce(t *testing.T) {
	d := newStorageDevice(t)
	c := New()
	c.storageRetry = time.Minute
	c.Configure(d.url(), "test", "secret")
	var said atomic.Int32
	c.OnStorageUnavailable = func(string) { said.Add(1) }
	c.Connect()
	defer c.Disconnect()

	waitFor(t, "the first answer", 5*time.Second, func() bool { return said.Load() == 1 })
	if !c.Reconnect() {
		t.Fatal("Reconnect refused while waiting for the disks")
	}
	waitFor(t, "the ask Reconnect makes", 2*time.Second, func() bool { return len(d.askTimes()) == 2 })
}

// The disks lost under a session: a signed-in answer with the code is
// reported, and the client signs in again - into the storage cycle.
func TestStorageUnavailableUnderASessionSignsInAgain(t *testing.T) {
	d := newStorageDevice(t)
	d.disksBack.Store(true)
	c := New()
	c.storageRetry = 300 * time.Millisecond
	c.Configure(d.url(), "test", "secret")
	var connected, said atomic.Int32
	c.OnConnect = func() { connected.Add(1) }
	c.OnStorageUnavailable = func(string) { said.Add(1) }
	c.Connect()
	defer c.Disconnect()
	waitFor(t, "the first sign-in", 5*time.Second, func() bool { return connected.Load() == 1 })

	d.loseUnderSession.Store(true)
	if _, err := c.Request(t.Context(), func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqListFiles{ReqListFiles: &pb.ListFiles{Path: "/"}}
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the report", 2*time.Second, func() bool { return said.Load() == 1 })
	waitFor(t, "signing in again", 5*time.Second, func() bool { return connected.Load() == 2 })
}
