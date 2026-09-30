// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	gorilla "github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"

	"github.com/alonsovidales/otc/bridge/cluster"
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
