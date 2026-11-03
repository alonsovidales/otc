// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/websocket"
)

// cUnavailableRetry is how often ServeUnavailable tries a port another
// process still holds.
var cUnavailableRetry = 5 * time.Second

// ServeUnavailable serves the device without its storage (issue #207) on
// its HTTP port: the web app's files, as always (they are on the boot
// disk, so the page loads and can say why), ws answering every request
// storage_unavailable (websocket.Unavailable), and 503 for the health
// check, a video stream and the users' metrics - nothing that reads the
// storage or the database. A port still held (the full instance just
// stopping) is tried again every few seconds. stop closes the listener,
// so the full instance can take the port.
func ServeUnavailable(port int, staticPath string, ws http.HandlerFunc) (stop func()) {
	mux := http.NewServeMux()
	unavailable := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable) // i18n-ignore: a status for scripts (update.sh's health check)
	}
	mux.HandleFunc(cHealtyPath, unavailable)
	mux.HandleFunc(cInternalMetricsPath, unavailable)
	mux.HandleFunc(cMediaPath, unavailable)
	mux.HandleFunc(websocket.CEndpoint, ws)
	mux.HandleFunc("/", StaticHandler(staticPath))

	var mu sync.Mutex
	stopped := false
	var srv *http.Server
	go func() {
		logged := false
		for {
			ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
			mu.Lock()
			if stopped {
				mu.Unlock()
				if err == nil {
					ln.Close()
				}
				return
			}
			if err == nil {
				srv = newServer("", mux)
				mu.Unlock()
				log.Info("answering on port", port, "that the storage can't be reached")
				if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
					log.Error("the port", port, "stopped serving:", err)
				}
				return
			}
			mu.Unlock()
			if !logged {
				log.Error("could not listen on port", port, "yet, trying again:", err)
				logged = true
			}
			time.Sleep(cUnavailableRetry)
		}
	}()
	return func() {
		mu.Lock()
		defer mu.Unlock()
		stopped = true
		if srv != nil {
			srv.Close()
		}
	}
}

// StaticHandler serves the web app's files from staticPath, as the full
// API does (serveStatic).
func StaticHandler(staticPath string) http.HandlerFunc {
	return (&API{staticPath: staticPath}).serveStatic
}
