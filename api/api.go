// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"fmt"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/staticassets"
	"github.com/alonsovidales/otc/websocket"
	"net/http"
	"time"
)

const (
	cHealtyPath = "/check_healty"
	// Issue #82: read by the primary instance's own supervisor to report
	// this user's live connection count for the Users management panel -
	// never called from a browser/app. Gated on a shared local token
	// (this instance's own [otc] supervisor-token, set only on a spawned
	// child's ini - see supervisor/config.go), not a real authenticated
	// RPC, the same "plain HTTP, no session" tier check_healty already
	// uses above.
	cInternalMetricsPath = "/internal/metrics"
	// Issue #110: streams one media file, addressed by a short-lived
	// token minted over the authenticated socket. See serveMedia.
	cMediaPath = "GET /media/{token}"
)

// API Structure that manage the HTTP API
type API struct {
	filesManager *filesmanager.Manager
	websocket    *websocket.Manager
	staticPath   string
	dao          *dao.Dao

	muxHTTPServer *http.ServeMux
}

// newServer is an HTTP server with the timeouts that are safe here (issue
// #173 - there were none, so a client sending its headers a byte at a
// time held a connection forever). Only the header read is bounded, plus
// idle keep-alives: a whole-request read or write timeout would cut the
// websockets, which live for hours, and a long video stream.
func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
}

// Init Initializes the API and starts listening on the specified ports serving
// both the HTTP API and the static content
func Init(filesManager *filesmanager.Manager, webSocket *websocket.Manager, dao *dao.Dao, staticPath string, httpPort, httpsPort int, cert, key string) (api *API, sslAPI *API) {
	api = &API{
		websocket:     webSocket,
		filesManager:  filesManager,
		muxHTTPServer: http.NewServeMux(),
		staticPath:    staticPath,
	}
	api.registerAPIs()
	log.Info("Starting API server on port:", httpPort)
	// Logged, not fatal: a port another process still holds left this
	// instance running with no listener and nothing in the log, while it
	// can still serve through the bridge.
	go func() {
		if err := newServer(fmt.Sprintf(":%d", httpPort), api.muxHTTPServer).ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("could not listen on API port", httpPort, "(LAN clients and video processing will fail):", err)
		}
	}()
	// Without a certificate there is nothing to serve TLS with (it failed
	// straight away on opening the empty path).
	if cert != "" && key != "" {
		go func() {
			if err := newServer(fmt.Sprintf(":%d", httpsPort), api.muxHTTPServer).ListenAndServeTLS(cert, key); err != nil && err != http.ErrServerClosed {
				log.Error("could not listen on TLS port", httpsPort, ":", err)
			}
		}()
	}

	// Issue #38: also listen on plain port 80, best-effort. iOS/Android/
	// Windows all probe a well-known URL over port 80 to detect a captive
	// portal (e.g. a fresh device's own temporary WiFi AP) and pop up a
	// mini sign-in browser automatically when the response isn't what a
	// real internet connection would give back — this mux already serves
	// the same page for any Host/path, so just being reachable on 80 is
	// enough. otc.service's CapabilityBoundingSet grants CAP_NET_BIND_SERVICE
	// specifically so this can bind a privileged port unprivileged
	// otherwise; logged rather than fatal since anywhere else (a laptop
	// running this without that capability, or something already on 80)
	// this simply isn't available, and that's fine — httpPort still works.
	go func() {
		if err := newServer(":80", api.muxHTTPServer).ListenAndServe(); err != nil {
			log.Error("could not also listen on :80 (captive-portal probes won't be caught):", err)
		}
	}()

	return
}

// registerAPIs Recister all the handles into the corresponding endpoints
func (api *API) registerAPIs() {
	api.muxHTTPServer.HandleFunc(cHealtyPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("OK"))
	})

	api.muxHTTPServer.HandleFunc(cInternalMetricsPath, api.internalMetrics)

	api.muxHTTPServer.HandleFunc(cMediaPath, api.serveMedia)

	// WebSocket
	api.muxHTTPServer.HandleFunc(websocket.CEndpoint, api.websocket.Listen)

	// Static content server
	api.muxHTTPServer.HandleFunc("/", api.serveStatic)
}

// internalMetrics answers the primary instance's supervisor with this
// instance's own live usage - see cInternalMetricsPath's doc comment
// above for why this is a plain-HTTP, token-gated endpoint rather than a
// real websocket RPC (there is no authenticated session here at all: the
// supervisor calling this doesn't know, and has no need to know, this
// user's own sign-in password).
func (api *API) internalMetrics(w http.ResponseWriter, r *http.Request) {
	token := cfg.GetStr("otc", "supervisor-token")
	if token == "" || r.Header.Get("X-Supervisor-Token") != token {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"active_connections": %d}`, api.websocket.ActiveConnections())
}

// serveStatic serves files from staticPath, appending ".html" to
// extension-less paths so client-side routes (e.g. "/social") resolve to
// their matching page, and falling back to the SPA's index.html for
// anything that still doesn't resolve to a real file (issue #38: a
// client-side route the ".html" guess didn't match, or a captive-portal
// probe path like /generate_204, which was never going to be a real file
// either - either way, showing the SPA shell beats a bare 404). Requests
// containing ".." are refused outright. Issue #95: this resolution is
// shared with ReqGetStaticAsset (see staticassets.Resolve's own doc
// comment) - the bridge now reaches this same lookup remotely on a
// browser's behalf instead of keeping its own separate, driftable copy of
// these files.
func (api *API) serveStatic(w http.ResponseWriter, r *http.Request) {
	path, err := staticassets.Resolve(api.staticPath, r.URL.Path[1:])
	if err != nil {
		http.NotFound(w, r)
		return
	}

	log.Debug("Serving static:", path)
	http.ServeFile(w, r, path)
}
