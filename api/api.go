package api

import (
	"fmt"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/websocket"
	"net/http"
	"os"
	"strings"
)

const (
	cHealtyPath = "/check_healty"
)

// API Structure that manage the HTTP API
type API struct {
	filesManager *filesmanager.Manager
	websocket    *websocket.Manager
	staticPath   string
	dao          *dao.Dao

	muxHTTPServer *http.ServeMux
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
	go http.ListenAndServe(fmt.Sprintf(":%d", httpPort), api.muxHTTPServer)
	go http.ListenAndServeTLS(fmt.Sprintf(":%d", httpsPort), cert, key, api.muxHTTPServer)

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
		if err := http.ListenAndServe(":80", api.muxHTTPServer); err != nil {
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

	// WebSocket
	api.muxHTTPServer.HandleFunc(websocket.CEndpoint, api.websocket.Listen)

	// Static content server
	api.muxHTTPServer.HandleFunc("/", api.serveStatic)
}

// serveStatic serves files from staticPath, appending ".html" to
// extension-less paths so client-side routes (e.g. "/social") resolve to
// their matching page. Requests containing ".." are refused outright.
func (api *API) serveStatic(w http.ResponseWriter, r *http.Request) {
	filePath := r.URL.Path[1:]

	if strings.Contains(filePath, "..") {
		return
	}

	path := api.staticPath + filePath
	lastPosSlash := -1
	lastPosDot := -1

	for i := 0; i < len(path); i++ {
		switch path[i] {
		case '/':
			lastPosSlash = i
		case '.':
			lastPosDot = i
		}
	}

	if filePath != "" && lastPosDot < lastPosSlash {
		path += ".html"
	}

	// Issue #38: anything that still doesn't resolve to a real file falls
	// back to the SPA's index.html instead of a bare 404 — a client-side
	// route the ".html" guess above didn't match (unchanged behavior), but
	// also now a captive-portal probe path like /generate_204 or
	// /hotspot-detect.html, which was never going to be a real file. Those
	// already made every OS's captive-portal check correctly detect "this
	// isn't real internet" from the 404 alone; this just means whatever
	// page it then shows the person is the actual setup wizard instead of
	// a dead end.
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		path = api.staticPath + "index.html"
	}

	log.Debug("Serving static:", path)
	http.ServeFile(w, r, path)
}
