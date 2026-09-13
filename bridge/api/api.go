// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"fmt"
	"github.com/alonsovidales/otc/bridge/admin"
	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/bridge/websocket"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	cHealtyPath = "/check_healty"

	// cContactCooldown throttles the public contact form to one accepted
	// submission per remote address per window - not real anti-spam, just
	// enough to stop a form left open in a tab (or a trivial retry loop)
	// from flooding the table.
	cContactCooldown  = 60 * time.Second
	cContactMaxLen    = 4000
	cContactNameMax   = 150
	cContactEmailMax  = 255
	cContactReasonMax = 64
)

// API Structure that manage the HTTP API
type API struct {
	websocket  *websocket.Manager
	staticPath string
	dao        *dao.Dao
	admin      *admin.Admin

	muxHTTPServer *http.ServeMux

	contactMu         sync.Mutex
	lastContactByAddr map[string]time.Time
}

// Init Initializes the API and starts listening on the specified ports serving
// both the HTTP API and the static content
func Init(webSocket *websocket.Manager, dao *dao.Dao, adm *admin.Admin, staticPath string, httpPort, httpsPort int, cert, key string) (api *API, sslAPI *API) {
	api = &API{
		websocket:         webSocket,
		dao:               dao,
		admin:             adm,
		muxHTTPServer:     http.NewServeMux(),
		staticPath:        staticPath,
		lastContactByAddr: map[string]time.Time{},
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

	api.muxHTTPServer.HandleFunc("POST /api/contact", api.submitContact)

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
	api.muxHTTPServer.HandleFunc("GET /admin/api/contact-requests", api.admin.RequireAuth(api.admin.ContactRequests))
	api.muxHTTPServer.HandleFunc("POST /admin/api/contact-requests/{id}/read", api.admin.RequireAuth(api.admin.SetContactRequestRead))
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

// submitContact handles the public landing page's contact form (issue
// #57): general enquiries and "give me bridge access" requests both land
// here, unauthenticated, and just get stored for an operator to read from
// the admin panel's Messages tab - nothing here triggers an email or any
// other automated action.
func (api *API) submitContact(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string `json:"name"`
		Email   string `json:"email"`
		Reason  string `json:"reason"`
		Message string `json:"message"`
		// Website is a honeypot field: real visitors never see or fill it
		// (hidden + off-screen in the form), so anything landing here is
		// almost certainly a bot filling in every field it can find.
		Website string `json:"website"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if strings.TrimSpace(body.Website) != "" {
		// Silently pretend success so the bot doesn't learn to leave it blank.
		w.WriteHeader(http.StatusOK)
		return
	}

	name := strings.TrimSpace(body.Name)
	email := strings.TrimSpace(body.Email)
	reason := strings.TrimSpace(body.Reason)
	message := strings.TrimSpace(body.Message)

	if name == "" || email == "" || message == "" {
		writeJSONErr(w, http.StatusBadRequest, "name, email and message are required")
		return
	}
	if !strings.Contains(email, "@") || strings.ContainsAny(email, " \t\n") {
		writeJSONErr(w, http.StatusBadRequest, "that doesn't look like a valid email address")
		return
	}
	if len(name) > cContactNameMax || len(email) > cContactEmailMax ||
		len(reason) > cContactReasonMax || len(message) > cContactMaxLen {
		writeJSONErr(w, http.StatusBadRequest, "one of the fields is too long")
		return
	}
	if reason == "" {
		reason = "general"
	}

	remoteAddr := r.RemoteAddr
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		remoteAddr = strings.TrimSpace(strings.SplitN(fwd, ",", 2)[0])
	}

	api.contactMu.Lock()
	if last, ok := api.lastContactByAddr[remoteAddr]; ok && time.Since(last) < cContactCooldown {
		api.contactMu.Unlock()
		writeJSONErr(w, http.StatusTooManyRequests, "please wait a moment before sending another message")
		return
	}
	api.lastContactByAddr[remoteAddr] = time.Now()
	api.contactMu.Unlock()

	if err := api.dao.NewContactRequest(name, email, reason, message); err != nil {
		log.Error("error storing contact request:", err)
		writeJSONErr(w, http.StatusInternalServerError, "internal error, please try again")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"ok": "true"})
}

func writeJSONErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
