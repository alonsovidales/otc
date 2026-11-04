// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	gorilla "github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"

	"github.com/alonsovidales/otc/bridge/cluster"
	"github.com/alonsovidales/otc/bridge/limits"
)

type fakeLocal map[string]bool

func (f fakeLocal) HasLocal(d string) bool { return f[d] }

const testToken = "0123456789abcdef0123456789abcdef"

// Two nodes on one Redis: bridge2 holds cala's device; a client lands on
// bridge1, which has no connection to it.
func clusterPair(t *testing.T) (front http.Handler, frontLocal http.Handler, seen *http.Request) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := func() *redis.Client { return redis.NewClient(&redis.Options{Addr: mr.Addr()}) }

	var got http.Request
	upgrader := gorilla.Upgrader{}
	backMux := http.NewServeMux()
	backMux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		got = *r
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			mt, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			c.WriteMessage(mt, append([]byte("bridge2:"), msg...))
		}
	})
	backMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		got = *r
		io.WriteString(w, "served by bridge2 for "+r.Host)
	})
	node2 := cluster.New("bridge2", "", testToken, rdb())
	back := httptest.NewServer(internalHandler(node2, backMux))
	t.Cleanup(back.Close)
	node2 = cluster.New("bridge2", strings.TrimPrefix(back.URL, "http://"), testToken, rdb())
	if err := node2.Announce(); err != nil {
		t.Fatal(err)
	}
	if err := node2.Hold("cala.off-the.cloud"); err != nil {
		t.Fatal(err)
	}

	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "served by bridge1")
	})
	node1 := cluster.New("bridge1", "127.0.0.1:1", testToken, rdb())
	return stripClusterHeaders(newClusterRouter(node1, fakeLocal{}, "off-the.cloud", local)), local, &got
}

// A device request the node can't serve is served by the node that holds
// the device - with the device's domain, the real client address, and
// marked as forwarded; the token never reaches the handlers.
func TestClusterForwardsDeviceRequests(t *testing.T) {
	front, _, seen := clusterPair(t)
	r := httptest.NewRequest("GET", "https://cala.off-the.cloud/index.html", nil)
	r.RemoteAddr = "203.0.113.7:5555"
	r.Header.Set("X-Forwarded-For", "6.6.6.6") // a client's own claim
	w := httptest.NewRecorder()
	front.ServeHTTP(w, r)
	if got := w.Body.String(); got != "served by bridge2 for cala.off-the.cloud" {
		t.Fatalf("got %q", got)
	}
	if seen.Header.Get(cluster.HopHeader) != "1" {
		t.Error("not marked as forwarded")
	}
	if xff := seen.Header.Get("X-Forwarded-For"); xff != "203.0.113.7" {
		t.Errorf("X-Forwarded-For %q, want the address bridge1 saw", xff)
	}
	if seen.Header.Get(cluster.TokenHeader) != "" {
		t.Error("the cluster token reached the handler")
	}

	// The bridge's own pages, and devices nobody holds, stay here.
	for _, host := range []string{"off-the.cloud", "nobody.off-the.cloud"} {
		w := httptest.NewRecorder()
		front.ServeHTTP(w, httptest.NewRequest("GET", "https://"+host+"/", nil))
		if w.Body.String() != "served by bridge1" {
			t.Errorf("%s: %q, want served locally", host, w.Body.String())
		}
	}
}

// The websocket - how clients talk to their device - goes through whole.
func TestClusterForwardsWebsockets(t *testing.T) {
	front, _, _ := clusterPair(t)
	srv := httptest.NewServer(front)
	defer srv.Close()
	d := gorilla.Dialer{}
	conn, resp, err := d.Dial(strings.Replace(srv.URL, "http", "ws", 1)+"/ws", http.Header{"Host": {"cala.off-the.cloud"}})
	if err != nil {
		t.Fatalf("dial: %v (%v)", err, resp)
	}
	defer conn.Close()
	if err := conn.WriteMessage(gorilla.BinaryMessage, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	_, msg, err := conn.ReadMessage()
	if err != nil || string(msg) != "bridge2:ping" {
		t.Fatalf("got %q %v", msg, err)
	}
}

// From outside, the cluster's headers are dropped: nobody can claim to be
// a node (and have their X-Forwarded-For believed), and a request that
// says it was already forwarded is still routed normally.
func TestClusterHeadersFromOutside(t *testing.T) {
	front, _, seen := clusterPair(t)
	r := httptest.NewRequest("GET", "https://cala.off-the.cloud/x", nil)
	r.Header.Set(cluster.HopHeader, "1")
	r.Header.Set(cluster.TokenHeader, testToken)
	w := httptest.NewRecorder()
	front.ServeHTTP(w, r)
	if !strings.HasPrefix(w.Body.String(), "served by bridge2") {
		t.Fatalf("a forged hop header stopped the routing: %q", w.Body.String())
	}
	if seen.Header.Get(cluster.TokenHeader) != "" {
		t.Error("a client's token header reached the handler")
	}
}

// The internal listener refuses anything without the cluster token.
func TestInternalListenerNeedsToken(t *testing.T) {
	c := cluster.New("bridge2", "", testToken, nil)
	h := internalHandler(c, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("reached the handlers without the token")
	}))
	for _, tok := range []string{"", "wrong", testToken[:31]} {
		r := httptest.NewRequest("GET", "http://10.10.0.3:8444/", nil)
		if tok != "" {
			r.Header.Set(cluster.TokenHeader, tok)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("token %q: %d, want 403", tok, w.Code)
		}
	}
}

// A forwarded response is written under a write deadline - media with the
// player-sized one - and still flushes through the wrapper (a websocket's:
// TestForwardedRequestsKeepTheWriteDeadline).
func TestForwardedResponsesGetAWriteDeadline(t *testing.T) {
	for path, want := range map[string]time.Duration{"/assets/index-CaKPC84J.js": limits.WriteIdleTimeout, "/media/tok": cMediaWriteStall} {
		rec := httptest.NewRecorder()
		dw, ok := withWriteDeadline(rec, httptest.NewRequest("GET", "https://cala.off-the.cloud"+path, nil)).(*deadlineWriter)
		if !ok || dw.stall != want {
			t.Fatalf("%s: not wrapped with a %v deadline", path, want)
		}
		io.WriteString(dw, "x")
		if err := http.NewResponseController(dw).Flush(); err != nil || !rec.Flushed || rec.Body.String() != "x" {
			t.Errorf("%s: write/flush through the wrapper: %v", path, err)
		}
	}
}

// Redis is asked which node holds an address only when it can be a
// device's: a made-up Host is served here even if Redis named a node.
func TestClusterAsksOnlyAboutDeviceAddresses(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	node2 := cluster.New("bridge2", "", testToken, rdb)
	back := httptest.NewServer(internalHandler(node2, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "served by bridge2")
	})))
	t.Cleanup(back.Close)
	node2 = cluster.New("bridge2", strings.TrimPrefix(back.URL, "http://"), testToken, rdb)
	if err := node2.Announce(); err != nil {
		t.Fatal(err)
	}
	if err := node2.Hold("198.51.100.20", "x.y.off-the.cloud", "evil.example", "cala.off-the.cloud"); err != nil {
		t.Fatal(err)
	}
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "served by bridge1") })
	front := newClusterRouter(cluster.New("bridge1", "127.0.0.1:2", testToken, rdb), fakeLocal{}, "off-the.cloud", local)
	for host, want := range map[string]string{
		"198.51.100.20":      "served by bridge1",
		"x.y.off-the.cloud":  "served by bridge1",
		"evil.example":       "served by bridge1",
		"cala.off-the.cloud": "served by bridge2",
	} {
		w := httptest.NewRecorder()
		front.ServeHTTP(w, httptest.NewRequest("GET", "https://"+host+"/", nil))
		if w.Body.String() != want {
			t.Errorf("%s: %q, want %q", host, w.Body.String(), want)
		}
	}
}

// Every forwarded request keeps the write deadline: an Upgrade header
// alone is an ordinary request (ReverseProxy forwards it as one), and a
// real websocket keeps it on the connection it hands over.
func TestForwardedRequestsKeepTheWriteDeadline(t *testing.T) {
	for _, c := range []struct {
		name    string
		headers map[string]string
		stall   time.Duration
	}{
		{"plain", nil, limits.WriteIdleTimeout},
		{"a stray Upgrade header", map[string]string{"Upgrade": "x"}, limits.WriteIdleTimeout},
		{"a websocket", map[string]string{"Upgrade": "websocket", "Connection": "keep-alive, Upgrade"}, cUpgradeWriteStall},
	} {
		r := httptest.NewRequest("GET", "https://cala.off-the.cloud/assets/index-CaKPC84J.js", nil)
		for k, v := range c.headers {
			r.Header.Set(k, v)
		}
		dw, ok := withWriteDeadline(httptest.NewRecorder(), r).(*deadlineWriter)
		if !ok || dw.stall != c.stall {
			t.Errorf("%s: wrapped %v, stall %v; want %v", c.name, ok, dw, c.stall)
		}
	}
	if media, ok := withWriteDeadline(httptest.NewRecorder(), httptest.NewRequest("GET", "/media/tok", nil)).(*deadlineWriter); !ok || media.stall != cMediaWriteStall {
		t.Error("a media range lost its own stall")
	}
}

// A forwarded websocket whose client stops reading is cut once a write to
// it makes no progress for cUpgradeWriteStall - before, the forwarding
// node's copy blocked for as long as the client kept its window shut, and
// with it the connection to the node holding the device.
func TestAForwardedWebsocketWhoseClientStopsReadingIsCut(t *testing.T) {
	setStall := cUpgradeWriteStall
	cUpgradeWriteStall = 300 * time.Millisecond
	t.Cleanup(func() { cUpgradeWriteStall = setStall })

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cut := make(chan struct{})
	upgrader := gorilla.Upgrader{}
	backMux := http.NewServeMux()
	backMux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		chunk := make([]byte, 256<<10)
		for i := 0; i < 4096; i++ { // up to 1 GiB, far past every buffer between
			// Longer than the test waits: only the front can end this.
			c.SetWriteDeadline(time.Now().Add(time.Minute))
			if err := c.WriteMessage(gorilla.BinaryMessage, chunk); err != nil {
				close(cut)
				return
			}
		}
	})
	node2 := cluster.New("bridge2", "", testToken, rdb)
	back := httptest.NewServer(internalHandler(node2, backMux))
	t.Cleanup(back.Close)
	node2 = cluster.New("bridge2", strings.TrimPrefix(back.URL, "http://"), testToken, rdb)
	if err := node2.Announce(); err != nil {
		t.Fatal(err)
	}
	if err := node2.Hold("cala.off-the.cloud"); err != nil {
		t.Fatal(err)
	}
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "served by bridge1") })
	front := httptest.NewServer(newClusterRouter(cluster.New("bridge1", "127.0.0.1:2", testToken, rdb), fakeLocal{}, "off-the.cloud", local))
	t.Cleanup(front.Close)

	conn, _, err := (&gorilla.Dialer{ReadBufferSize: 1024}).Dial(strings.Replace(front.URL, "http", "ws", 1)+"/ws", http.Header{"Host": {"cala.off-the.cloud"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	// ... and never reads.
	select {
	case <-cut:
	case <-time.After(10 * time.Second):
		t.Fatal("the forwarded websocket is still held by a client that stopped reading")
	}
}
