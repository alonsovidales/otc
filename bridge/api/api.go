// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"fmt"
	"github.com/alonsovidales/otc/bridge/admin"
	"github.com/alonsovidales/otc/bridge/clientaddr"
	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/bridge/websocket"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/staticassets"
	"google.golang.org/protobuf/proto"
	"net/http"
	"os"
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
	cContactCooldown = 60 * time.Second
	// cContactMapTTL bounds how long a lastContactByAddr entry lingers
	// after it stops actually blocking anything (cContactCooldown later) -
	// well past that, but nowhere near "forever", which is what an
	// unbounded map would otherwise do.
	cContactMapTTL    = 10 * time.Minute
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

	// Issue #110: video streaming. Registered ahead of the "/" catch-all
	// so it doesn't fall through to proxyStaticAsset, which would fetch
	// the whole file in one piece - the exact thing this replaces.
	api.muxHTTPServer.HandleFunc("GET /media/{token}", api.proxyMedia)
	api.muxHTTPServer.HandleFunc("HEAD /media/{token}", api.proxyMedia)

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

// serveStatic serves the bridge's own site (the bare domain matching
// [otc-api] tld - the public landing page and the admin panel) from
// staticPath, same as always. Anything else is a device's own subdomain,
// and (issue #95) is no longer served from a local copy at all: the
// bridge has never rebuilt its own web app, only redeployed the same
// build that goes to every device, and keeping a separate copy of that
// here meant a device running an older (or newer) build than whatever
// the bridge happened to have could be served assets that didn't match
// its own backend. Proxied to the device instead, via proxyStaticAsset.
func (api *API) serveStatic(w http.ResponseWriter, r *http.Request) {
	if r.Host != cfg.GetStr("otc-api", "tld") {
		// Issue #93: a disabled additional user (issue #90) has its own
		// process actually stopped - nothing on the device is left running
		// to explain that, so this is checked before ever attempting to
		// proxy anything (which would otherwise just look like the device
		// being offline, indistinguishable from any other outage).
		if disabled, err := api.dao.IsDeviceDisabled(r.Host); err != nil {
			log.Error("error checking disabled state for", r.Host, ":", err)
		} else if disabled {
			api.serveOwnPage(w, r, "disabled.html", http.StatusServiceUnavailable)
			return
		}
		api.proxyStaticAsset(w, r)
		return
	}

	filePath := r.URL.Path[1:]
	if filePath == "" {
		filePath = "landing.html"
	}

	path, err := staticassets.Resolve(api.staticPath, filePath)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	log.Debug("Serving static:", path, "FilePath:", filePath, "HostName:", r.Host)
	http.ServeFile(w, r, path)
}

// serveOwnPage writes one of the bridge's own static pages (disabled.html,
// unavailable.html) with a status of this handler's choosing, for a
// request aimed at a device subdomain that the bridge is answering itself
// rather than proxying.
//
// Not http.ServeFile: that sets its own 200 (or writes a header on the
// range/conditional-request path) before the caller ever gets a chance to,
// so reading the file directly is what keeps the intended status the one
// and only status written.
//
// Only pages a person is meant to read get the HTML body. Everything else
// the browser asks for while the page is failing - a stylesheet, a script,
// a favicon - gets the bare status instead, so an HTML error page never
// arrives claiming to be a .js file (which a browser would refuse, noisily
// and confusingly, on top of whatever actually went wrong).
func (api *API) serveOwnPage(w http.ResponseWriter, r *http.Request, page string, status int) {
	if !strings.Contains(r.Header.Get("Accept"), "text/html") {
		w.WriteHeader(status)
		return
	}
	content, err := os.ReadFile(api.staticPath + page)
	if err != nil {
		log.Error("error reading", page, ":", err)
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write(content)
}

// proxyStaticAsset (issue #95) fetches r.URL.Path from r.Host's own
// device over the same bridge tunnel every other request already uses,
// rather than a local copy - see serveStatic's own doc comment for why.
// Security-relevant boundary, not just an implementation detail: nothing
// about this path is trusted beyond "ask this one device for it and
// relay back whatever it says" - the device itself (staticassets.Resolve,
// reached via ReqGetStaticAsset) is the one and only place that decides
// whether the path names a real file inside its own assets directory.
// This function never touches the filesystem, never inspects the path
// beyond handing it to that RPC, and never falls back to any locally-held
// copy of anything if the device can't answer.
func (api *API) proxyStaticAsset(w http.ResponseWriter, r *http.Request) {
	log.Debug("proxying static asset:", r.Host, r.URL.Path)
	req := &pb.ReqEnvelope{
		Id: 1,
		Payload: &pb.ReqEnvelope_ReqGetStaticAsset{
			ReqGetStaticAsset: &pb.ReqGetStaticAsset{Path: r.URL.Path},
		},
	}
	frame, err := proto.Marshal(req)
	if err != nil {
		log.Error("error marshaling static asset request:", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	respFrame, err := api.websocket.ForwardOneOff(r.Host, frame)
	if err != nil {
		// Issue #97: a device that's switched off, offline, or still
		// booting used to surface as a bare 502 with an empty body -
		// nothing telling whoever opened the link what had happened or
		// whether it was worth trying again. It gets the bridge's own
		// "not available right now" page instead.
		//
		// 503, not 502: the device isn't a broken upstream, it's a
		// temporarily absent one, and that's the difference between
		// "something is wrong with this service" and "try again shortly"
		// - for a person reading the page, and for anything machine-read
		// (a crawler, an uptime check) that acts on the status alone.
		// Retry-After says the same thing in the terms those clients use,
		// and matches the page's own countdown.
		log.Debug("device unreachable while proxying static asset:", r.Host, r.URL.Path, err)
		w.Header().Set("Retry-After", "30")
		api.serveOwnPage(w, r, "unavailable.html", http.StatusServiceUnavailable)
		return
	}

	var resp pb.RespEnvelope
	if err := proto.Unmarshal(respFrame, &resp); err != nil {
		// A device that answered, but with something unintelligible, is a
		// genuinely broken upstream - 502 is the honest status for that,
		// unlike the unreachable case above.
		log.Error("bad proto from device while proxying static asset:", err)
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	asset, ok := resp.Payload.(*pb.RespEnvelope_RespStaticAsset)
	if resp.Error || !ok {
		http.NotFound(w, r)
		return
	}

	if asset.RespStaticAsset.ContentType != "" {
		w.Header().Set("Content-Type", asset.RespStaticAsset.ContentType)
	}
	w.Write(asset.RespStaticAsset.Content)
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

	// Issue #99: this used to key on r.RemoteAddr *including the port*,
	// which left the cooldown below bypassable by exactly the senders it
	// was meant to stop - a script opening a fresh connection per request
	// draws a fresh ephemeral port each time, so every submission landed
	// in its own bucket and the map grew an entry per port, the very thing
	// its own comment said it was avoiding. clientaddr.Of strips the port
	// (and still never trusts a forwarded-for header - see its doc
	// comment for why that matters here).
	remoteAddr := clientaddr.Of(r)

	api.contactMu.Lock()
	now := time.Now()
	if last, ok := api.lastContactByAddr[remoteAddr]; ok && now.Sub(last) < cContactCooldown {
		api.contactMu.Unlock()
		writeJSONErr(w, http.StatusTooManyRequests, "please wait a moment before sending another message")
		return
	}
	api.lastContactByAddr[remoteAddr] = now
	// Opportunistic cleanup: entries past the TTL aren't doing anything for
	// the cooldown check above anymore, so drop them rather than letting
	// this map grow for as long as the process runs.
	for addr, t := range api.lastContactByAddr {
		if now.Sub(t) > cContactMapTTL {
			delete(api.lastContactByAddr, addr)
		}
	}
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
