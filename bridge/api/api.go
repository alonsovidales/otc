package api

import (
	"fmt"
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

	muxHTTPServer *http.ServeMux
}

// Init Initializes the API and starts listening on the specified ports serving
// both the HTTP API and the static content
func Init(webSocket *websocket.Manager, dao *dao.Dao, staticPath string, httpPort, httpsPort int, cert, key string) (api *API, sslAPI *API) {
	api = &API{
		websocket:     webSocket,
		muxHTTPServer: http.NewServeMux(),
		staticPath:    staticPath,
	}
	api.registerAPIs()
	log.Info("Starting API server on port:", httpPort)
	go http.ListenAndServe(fmt.Sprintf(":%d", httpPort), api.muxHTTPServer)
	log.Info("Starting https API server on port:", httpsPort, cert, key)
	go http.ListenAndServeTLS(fmt.Sprintf(":%d", httpsPort), cert, key, api.muxHTTPServer)

	return
}

// registerAPIs Recister all the handles into the corresponding endpoints
func (api *API) registerAPIs() {
	api.muxHTTPServer.HandleFunc(cHealtyPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("OK"))
	})

	api.muxHTTPServer.HandleFunc(websocket.CEndpoint, api.websocket.Listen)

	api.muxHTTPServer.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
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
	})
}
