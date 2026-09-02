package api

import (
	"fmt"
	"github.com/alonsovidales/otc/bridge/admin"
	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/bridge/websocket"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	"net/http"
	"strings"
)

const (
	cHealtyPath = "/check_healty"
)

// API Structure that manage the HTTP API
type API struct {
	websocket  *websocket.Manager
	staticPath string
	dao        *dao.Dao
	admin      *admin.Admin

	muxHTTPServer *http.ServeMux
}

// Init Initializes the API and starts listening on the specified ports serving
// both the HTTP API and the static content
func Init(webSocket *websocket.Manager, dao *dao.Dao, adm *admin.Admin, staticPath string, httpPort, httpsPort int, cert, key string) (api *API, sslAPI *API) {
	api = &API{
		websocket:     webSocket,
		dao:           dao,
		admin:         adm,
		muxHTTPServer: http.NewServeMux(),
		staticPath:    staticPath,
	}

	api.registerAPIs()
	go func() {
		log.Info("Starting http API server on port:", httpPort, cert, key)
		err := http.ListenAndServe(fmt.Sprintf(":%d", httpPort), api.muxHTTPServer)
		if err != nil {
			log.Fatal("Error:", err)
		}
	}()
	go func() {
		log.Info("Starting https API server on port:", httpsPort, cert, key)
		err := http.ListenAndServeTLS(fmt.Sprintf(":%d", httpsPort), cert, key, api.muxHTTPServer)
		if err != nil {
			log.Fatal("Error:", err)
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

	api.muxHTTPServer.HandleFunc(websocket.CEndpoint, api.websocket.Listen)

	api.registerAdminAPIs()

	api.muxHTTPServer.HandleFunc("/", api.serveStatic)
}

// registerAdminAPIs wires the bridge admin panel's JSON API (issues #7/#8):
// login is open, everything else requires a valid session cookie. The
// panel's own UI (admin.html) is just a static file served by serveStatic,
// same as any other page.
func (api *API) registerAdminAPIs() {
	if api.admin == nil {
		// Nil in tests that construct API directly without an admin
		// manager; nothing under /admin/api is reachable there.
		return
	}

	api.muxHTTPServer.HandleFunc("POST /admin/api/login", api.admin.Login)
	api.muxHTTPServer.HandleFunc("POST /admin/api/logout", api.admin.Logout)
	api.muxHTTPServer.HandleFunc("GET /admin/api/devices", api.admin.RequireAuth(api.admin.ListDevices))
	api.muxHTTPServer.HandleFunc("POST /admin/api/devices", api.admin.RequireAuth(api.admin.AddDevice))
	api.muxHTTPServer.HandleFunc("DELETE /admin/api/devices/{domain}", api.admin.RequireAuth(api.admin.DeleteDevice))
	api.muxHTTPServer.HandleFunc("GET /admin/api/metrics", api.admin.RequireAuth(api.admin.Metrics))
	api.muxHTTPServer.HandleFunc("GET /admin/api/auth-events", api.admin.RequireAuth(api.admin.AuthEvents))
}

// serveStatic serves files from staticPath: the bare domain (matching the
// [otc-api] tld config) gets landing.html, extension-less paths get
// ".html" appended so client-side routes resolve to their matching page,
// and requests containing ".." are refused outright.
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

	if filePath == "" && r.Host == cfg.GetStr("otc-api", "tld") {
		path += "landing.html"
	}
	if filePath != "" && lastPosDot < lastPosSlash {
		path += ".html"
	}

	log.Debug("Serving static:", path, "FilePath:", filePath, "HostName:", r.Host)

	http.ServeFile(w, r, path)
}
