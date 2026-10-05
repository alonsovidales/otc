// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"

	"github.com/alonsovidales/otc/bridge/cluster"
	"github.com/alonsovidales/otc/bridge/limits"
	"github.com/alonsovidales/otc/log"
)

// Issue #144: a request for a device's subdomain - its websocket, a static
// asset, a media range - is served by a node with a free connection to that
// device. The node it lands on serves it itself when it has one; otherwise
// it hands the whole request, websocket upgrade included, to a node that
// does (cluster.Locate), over the private network. The receiving node
// serves it exactly like one of its own: the handlers never know.

// localHolder is the part of websocket.Manager this needs.
type localHolder interface {
	HasLocal(domain string) bool
}

// clusterRouter forwards device requests this node can't serve.
type clusterRouter struct {
	cluster *cluster.Cluster
	local   localHolder
	tld     string
	next    http.Handler

	mu      sync.Mutex
	proxies map[string]*httputil.ReverseProxy // by node address
}

func newClusterRouter(c *cluster.Cluster, local localHolder, tld string, next http.Handler) http.Handler {
	if !c.Enabled() {
		return next
	}
	return &clusterRouter{cluster: c, local: local, tld: tld, next: next, proxies: map[string]*httputil.ReverseProxy{}}
}

func (cr *clusterRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// A request another node already forwarded is served here, whatever
	// the state here: never forwarded twice, so never in a loop.
	if r.Header.Get(cluster.HopHeader) != "1" && r.Host != cr.tld && !cr.local.HasLocal(r.Host) {
		if addr, ok := cr.cluster.Locate(r.Host); ok {
			cr.proxy(addr).ServeHTTP(withWriteDeadline(w, r), r)
			return
		}
	}
	cr.next.ServeHTTP(w, r)
}

func (cr *clusterRouter) proxy(addr string) *httputil.ReverseProxy {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	if p, ok := cr.proxies[addr]; ok {
		return p
	}
	p := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http" // inside the tunnel
			pr.Out.URL.Host = addr
			pr.Out.Host = pr.In.Host // the device's domain: what the handlers route by
			pr.Out.Header.Del(cluster.HopHeader)
			pr.Out.Header.Set(cluster.TokenHeader, cr.cluster.Token())
			// Who the client is, as this node saw it - never what the
			// client said (the device's password-attempt limit is kept by
			// this address, see websocket.clientAddr).
			pr.Out.Header.Set("X-Forwarded-For", remoteIP(pr.In))
		},
		// Media ranges and websocket frames go through as they come.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			// The other node is gone or unreachable: answer from here,
			// which says the device is unreachable the usual way.
			log.Error("cluster: forwarding", r.Host, "to", addr, "failed, serving it here:", err)
			cr.next.ServeHTTP(w, r)
		},
	}
	cr.proxies[addr] = p
	return p
}

// deadlineWriter renews the client's write deadline before every write
// of a forwarded response. The server has no WriteTimeout (it would cut
// websockets), so otherwise a client that stops reading holds the
// forwarding goroutine and its connection to the other node for good.
type deadlineWriter struct {
	http.ResponseWriter
	rc    *http.ResponseController
	stall time.Duration
}

// withWriteDeadline wraps w for a forwarded request, except a websocket
// upgrade, which must stay open while idle (and needs the plain writer to
// hijack).
func withWriteDeadline(w http.ResponseWriter, r *http.Request) http.ResponseWriter {
	if r.Header.Get("Upgrade") != "" || r.Method == http.MethodConnect {
		return w
	}
	stall := limits.WriteIdleTimeout
	if strings.HasPrefix(r.URL.Path, "/media/") {
		stall = cMediaWriteStall
	}
	return &deadlineWriter{ResponseWriter: w, rc: http.NewResponseController(w), stall: stall}
}

func (d *deadlineWriter) Write(p []byte) (int, error) {
	_ = d.rc.SetWriteDeadline(time.Now().Add(d.stall))
	return d.ResponseWriter.Write(p)
}

// Flush keeps the proxy's FlushInterval -1 flushing through the wrapper.
func (d *deadlineWriter) Flush() {
	_ = d.rc.SetWriteDeadline(time.Now().Add(d.stall))
	_ = d.rc.Flush()
}

func (d *deadlineWriter) Unwrap() http.ResponseWriter { return d.ResponseWriter }

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// stripClusterHeaders removes the cluster's own headers from a request that
// came from outside: only the internal listener may set them.
func stripClusterHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del(cluster.TokenHeader)
		r.Header.Del(cluster.HopHeader)
		next.ServeHTTP(w, r)
	})
}

// internalHandler serves what other nodes forward: only with the cluster
// token, and marked as forwarded (cluster.HopHeader).
func internalHandler(c *cluster.Cluster, mux http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !c.ValidToken(r.Header.Get(cluster.TokenHeader)) {
			log.Error("cluster: refused a request without the cluster token from", r.RemoteAddr)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		r.Header.Del(cluster.TokenHeader)
		r.Header.Set(cluster.HopHeader, "1")
		mux.ServeHTTP(w, r)
	})
}

// serveInternal starts the listener other nodes forward to, on the
// private network only ([cluster] internal-addr).
func (api *API) serveInternal() {
	go func() {
		log.Info("Starting the cluster's internal listener on", api.cluster.InternalAddr())
		srv := &http.Server{
			Addr:              api.cluster.InternalAddr(),
			Handler:           internalHandler(api.cluster, api.muxHTTPServer),
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       2 * time.Minute,
		}
		if err := srv.ListenAndServe(); err != nil {
			log.Fatal("Error:", err)
		}
	}()
}
