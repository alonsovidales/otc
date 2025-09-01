package api

import (
	"fmt"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/websocket"
	"net/http"
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

	return
}

// registerAPIs Recister all the handles into the corresponding endpoints
func (api *API) registerAPIs() {
	api.muxHTTPServer.HandleFunc(cHealtyPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("OK"))
	})

	api.muxHTTPServer.HandleFunc(websocket.CEndpoint, api.websocket.Listen)
	api.muxHTTPServer.HandleFunc(filesmanager.CGet, api.filesManager.Get)

	api.muxHTTPServer.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		filePath := r.URL.Path[1:]
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

		http.ServeFile(w, r, path)
	})
}
