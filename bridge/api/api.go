// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"fmt"
	"github.com/alonsovidales/otc/bridge/accounts"
	"github.com/alonsovidales/otc/bridge/admin"
	"github.com/alonsovidales/otc/bridge/clientaddr"
	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/bridge/websocket"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/staticassets"
	"google.golang.org/protobuf/proto"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
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

	// Issue #38: the setup wizard on a freshly flashed device reserves its
	// name here before installing anything. One claim per address per
	// cClaimCooldown keeps a script from squatting the whole namespace.
	cClaimCooldown = 10 * time.Second
	cNameMaxLen    = 63

	// Issue #38: a device being set up over its hotspot joins the owner's
	// WiFi and, with it, loses the phone that was driving the wizard. It
	// reports its new LAN address here under a one-time token the wizard
	// page already holds; the page polls for it and follows. Stored in
	// the database (ten-minute expiry, see dao.SetSetupBeacon) so any
	// bridge instance can answer the poll.
	cSetupTokenMinLen = 32
	cSetupTokenMaxLen = 128
)

// cNamePattern is a valid device name: one DNS label, lower case, no
// leading/trailing hyphen - the same shape scripts/install.sh accepts.
var cNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// cReservedNames can never be device names: they are (or may one day be)
// the bridge's own hosts.
var cReservedNames = map[string]bool{
	"www": true, "api": true, "admin": true, "mail": true, "static": true,
	"downloads": true, "bridge": true, "ns1": true, "ns2": true,
}

// API Structure that manage the HTTP API
type API struct {
	websocket  *websocket.Manager
	staticPath string
	dao        *dao.Dao
	admin      *admin.Admin
	// accounts is issue #124; nil (tests) behaves like open registration.
	accounts *accounts.Accounts

	muxHTTPServer *http.ServeMux

	contactMu         sync.Mutex
	lastContactByAddr map[string]time.Time

	claimMu         sync.Mutex
	lastClaimByAddr map[string]time.Time
	// tld is [otc-api] tld, read once at Init; device domains are <name>.<tld>.
	tld string
}

// Init Initializes the API and starts listening on the specified ports serving
// both the HTTP API and the static content
func Init(webSocket *websocket.Manager, dao *dao.Dao, adm *admin.Admin, acc *accounts.Accounts, staticPath string, httpPort, httpsPort int, cert, key string) (api *API, sslAPI *API) {
	api = &API{
		websocket:         webSocket,
		dao:               dao,
		admin:             adm,
		accounts:          acc,
		muxHTTPServer:     http.NewServeMux(),
		staticPath:        staticPath,
		lastContactByAddr: map[string]time.Time{},
		lastClaimByAddr:   map[string]time.Time{},
		tld:               cfg.GetStr("otc-api", "tld"),
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

	// Issue #38: the flashable image's setup wizard checks and reserves the
	// device's name here, before the install script runs - so "that name
	// is taken" is something the person sees while they can still change
	// it, not a failed bridge registration after a 20-minute install.
	api.muxHTTPServer.HandleFunc("GET /api/name-available", api.nameAvailable)
	api.muxHTTPServer.HandleFunc("POST /api/claim", api.claimName)

	// Issue #124: accounts. Identity and sessions live in package
	// accounts; the domains an account owns are handled here, next to the
	// claim, since they share the name rules.
	if api.accounts != nil {
		acc := api.accounts
		api.muxHTTPServer.HandleFunc("POST /api/account/signup", acc.Signup)
		api.muxHTTPServer.HandleFunc("POST /api/account/login", acc.Login)
		api.muxHTTPServer.HandleFunc("POST /api/account/logout", acc.Logout)
		api.muxHTTPServer.HandleFunc("GET /api/account/me", acc.RequireAuth(acc.Me))
		api.muxHTTPServer.HandleFunc("PUT /api/account/me", acc.RequireAuth(acc.UpdateProfile))
		api.muxHTTPServer.HandleFunc("PUT /api/account/password", acc.RequireAuth(acc.SetPassword))
		api.muxHTTPServer.HandleFunc("GET /api/account/setup-token", acc.RequireAuth(acc.SetupToken))
		api.muxHTTPServer.HandleFunc("GET /api/account/setup-token-info", acc.SetupTokenInfo)
		api.muxHTTPServer.HandleFunc("GET /api/account/continue", acc.RequireAuth(acc.ContinueSetup))
		api.muxHTTPServer.HandleFunc("GET /api/account/countries", acc.CountryList)
		api.muxHTTPServer.HandleFunc("GET /api/account/providers", acc.Providers)
		api.muxHTTPServer.HandleFunc("GET /api/account/domains", acc.RequireAuth(api.accountDomains))
		api.muxHTTPServer.HandleFunc("POST /api/account/domains", acc.RequireAuth(api.accountAddDomain))
		api.muxHTTPServer.HandleFunc("POST /api/account/domains/{domain}/identity", acc.RequireAuth(api.accountNewIdentity))
		api.muxHTTPServer.HandleFunc("DELETE /api/account/domains/{domain}", acc.RequireAuth(api.accountReleaseDomain))
		api.muxHTTPServer.HandleFunc("GET /account/auth/{provider}/start", acc.OAuthStart)
		api.muxHTTPServer.HandleFunc("GET /account/auth/{provider}/callback", acc.OAuthCallback)
		api.muxHTTPServer.HandleFunc("POST /account/auth/{provider}/callback", acc.OAuthCallback)
	}
	api.muxHTTPServer.HandleFunc("GET /api/device-online", api.deviceOnline)
	api.muxHTTPServer.HandleFunc("POST /api/setup-beacon", api.setupBeacon)
	api.muxHTTPServer.HandleFunc("GET /api/setup-lookup", api.setupLookup)
	api.muxHTTPServer.HandleFunc("OPTIONS /api/setup-lookup", api.setupLookup)

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

// deviceDomain is the full bridge domain for a device name.
func (api *API) deviceDomain(name string) string {
	tld := api.tld
	if tld == "" {
		tld = "off-the.cloud"
	}
	return name + "." + tld
}

// nameAvailable (issue #38) answers {"available": bool} for ?name=. Public
// and unauthenticated on purpose - it tells nothing a friend request to
// that domain wouldn't, and the wizard calls it as the person types.
func (api *API) nameAvailable(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("name")))
	if !cNamePattern.MatchString(name) {
		writeJSONErr(w, http.StatusBadRequest, "a name is lower-case letters, digits and hyphens, up to 63 characters")
		return
	}
	if cReservedNames[name] {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "domain": api.deviceDomain(name)})
		return
	}
	registered, err := api.dao.IsDomainRegistered(api.deviceDomain(name))
	if err != nil {
		log.Error("error checking name availability:", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not check that name right now")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": !registered, "domain": api.deviceDomain(name)})
}

// claimName (issue #38) reserves a device name for the identity the setup
// wizard just generated - exactly what ReqBridgeRegister does the first
// time an unknown device dials in, only before the device exists. From
// then on the device authenticates with that owner_uuid + secret like any
// other; nothing else about it is special. 409 if the name is taken.
//
// Issue #124: the claim names its account, with a setup token (the body's
// setup_token, or "Authorization: Bearer <token>") or the account page's
// own session. A name the same account already owns is handed to the new
// identity - that is how a lost device is replaced: run setup again,
// signed in, pick the same name. Without an account the claim is refused
// (401 login_required) unless [accounts] open-registration is on.
func (api *API) claimName(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name       string `json:"name"`
		OwnerUUID  string `json:"owner_uuid"`
		Secret     string `json:"secret"`
		SetupToken string `json:"setup_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	accountID := api.claimAccount(r, body.SetupToken)
	if accountID == "" && (api.accounts != nil && !api.accounts.OpenRegistration()) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "sign in to register a name", "code": "login_required"})
		return
	}
	name := strings.ToLower(strings.TrimSpace(body.Name))
	if !cNamePattern.MatchString(name) || cReservedNames[name] {
		writeJSONErr(w, http.StatusBadRequest, "that name can't be used")
		return
	}
	// The identity is what the device will present forever after, so it
	// has to be a real one - not a blank the wizard forgot to fill in.
	if len(body.OwnerUUID) < 16 || len(body.OwnerUUID) > 64 || len(body.Secret) < 32 || len(body.Secret) > 128 {
		writeJSONErr(w, http.StatusBadRequest, "owner_uuid and secret are required")
		return
	}

	remoteAddr := clientaddr.Of(r)
	api.claimMu.Lock()
	now := time.Now()
	if last, ok := api.lastClaimByAddr[remoteAddr]; ok && now.Sub(last) < cClaimCooldown {
		api.claimMu.Unlock()
		writeJSONErr(w, http.StatusTooManyRequests, "please wait a moment before trying another name")
		return
	}
	api.lastClaimByAddr[remoteAddr] = now
	for addr, t := range api.lastClaimByAddr {
		if now.Sub(t) > cContactMapTTL {
			delete(api.lastClaimByAddr, addr)
		}
	}
	api.claimMu.Unlock()

	domain := api.deviceDomain(name)
	owner, registered, err := api.dao.DomainAccount(domain)
	if err != nil {
		log.Error("error checking name before claim:", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not reserve that name right now")
		return
	}
	if registered {
		if accountID != "" && owner == accountID {
			// The account's own name on a new device: the old device's
			// identity stops working, this one takes over.
			if ok, err := api.dao.ReplaceDeviceIdentity(accountID, domain, body.OwnerUUID, body.Secret); err != nil || !ok {
				log.Error("error handing", domain, "to a new device:", err)
				writeJSONErr(w, http.StatusInternalServerError, "could not reserve that name right now")
				return
			}
			log.Info("name handed to a new device by its account:", domain, "from", remoteAddr)
			writeJSON(w, http.StatusCreated, map[string]any{"domain": domain, "replaced": true})
			return
		}
		writeJSONErr(w, http.StatusConflict, "that name is already taken")
		return
	}
	if accountID != "" {
		if code, msg := api.domainLimitReached(accountID); code != 0 {
			writeJSON(w, code, map[string]any{"error": msg, "code": "domain_limit"})
			return
		}
		err = api.dao.RegisterAccountDevice(accountID, body.OwnerUUID, domain, body.Secret)
	} else {
		err = api.dao.RegistreDevice(body.OwnerUUID, domain, body.Secret)
	}
	if err != nil {
		// Lost a race with another claim for the same name, most likely.
		log.Error("error claiming name", domain, ":", err)
		writeJSONErr(w, http.StatusConflict, "that name is already taken")
		return
	}
	log.Info("name claimed by the setup wizard:", domain, "from", remoteAddr)
	writeJSON(w, http.StatusCreated, map[string]any{"domain": domain})
}

// claimAccount is the account behind a claim: a setup token from the
// body or the Authorization header, or the account page's own session.
func (api *API) claimAccount(r *http.Request, bodyToken string) string {
	if api.accounts == nil {
		return ""
	}
	token := bodyToken
	if auth := r.Header.Get("Authorization"); token == "" && strings.HasPrefix(auth, "Bearer ") {
		token = strings.TrimPrefix(auth, "Bearer ")
	}
	if token != "" {
		if id, ok := api.accounts.AccountForSetupToken(token); ok {
			return id
		}
		return ""
	}
	if id, ok := api.accounts.AccountFromRequest(r); ok {
		return id
	}

	return ""
}

// domainLimitReached is the terms' cap (accounts.MaxDomains): 0 when the
// account may add one, else the status and message to answer with.
func (api *API) domainLimitReached(accountID string) (int, string) {
	n, err := api.dao.CountAccountDomains(accountID)
	if err != nil {
		log.Error("error counting an account's domains:", err)
		return http.StatusInternalServerError, "could not check your account right now"
	}
	if n >= accounts.MaxDomains {
		return http.StatusForbidden, fmt.Sprintf("an account can register up to %d domains - for more, write to %s", accounts.MaxDomains, accounts.ContactEmail)
	}

	return 0, ""
}

// --- Issue #124: the account page's domains ---

type accountDomainJSON struct {
	Domain   string    `json:"domain"`
	Created  time.Time `json:"created"`
	Disabled bool      `json:"disabled"`
	Online   bool      `json:"online"`
}

func (api *API) accountDomainList(accountID string) ([]accountDomainJSON, error) {
	domains, err := api.dao.ListAccountDomains(accountID)
	if err != nil {
		return nil, err
	}
	out := []accountDomainJSON{}
	for _, d := range domains {
		out = append(out, accountDomainJSON{Domain: d.Domain, Created: d.Created, Disabled: d.Disabled, Online: api.websocket != nil && api.websocket.IsOnline(d.Domain)})
	}

	return out, nil
}

// accountDomains lists the signed-in account's domains, with whether each
// device is connected right now. GET /api/account/domains.
func (api *API) accountDomains(w http.ResponseWriter, r *http.Request, accountID string) {
	out, err := api.accountDomainList(accountID)
	if err != nil {
		log.Error("error listing an account's domains:", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not list your domains right now")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": out, "max_domains": accounts.MaxDomains})
}

// accountAddDomain registers a name for a device installed by hand (the
// install script rather than the wizard): the identity is generated here
// and shown once, for the installer's environment file. POST
// /api/account/domains {name}.
func (api *API) accountAddDomain(w http.ResponseWriter, r *http.Request, accountID string) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := strings.ToLower(strings.TrimSpace(body.Name))
	if !cNamePattern.MatchString(name) || cReservedNames[name] {
		writeJSONErr(w, http.StatusBadRequest, "a name is lower-case letters, digits and hyphens, up to 63 characters")
		return
	}
	domain := api.deviceDomain(name)
	if _, registered, err := api.dao.DomainAccount(domain); err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "could not register that name right now")
		return
	} else if registered {
		writeJSONErr(w, http.StatusConflict, "that name is already taken")
		return
	}
	if code, msg := api.domainLimitReached(accountID); code != 0 {
		writeJSON(w, code, map[string]any{"error": msg, "code": "domain_limit"})
		return
	}
	owner, secret := uuid.New().String(), newSecret()
	if err := api.dao.RegisterAccountDevice(accountID, owner, domain, secret); err != nil {
		writeJSONErr(w, http.StatusConflict, "that name is already taken")
		return
	}
	log.Info("name registered from the account page:", domain)
	writeJSON(w, http.StatusCreated, map[string]any{"domain": domain, "owner_uuid": owner, "secret": secret})
}

// accountNewIdentity gives one of the account's domains a fresh owner
// uuid and secret, shown once: the old device is locked out the moment
// this answers. For a device installed by hand; the wizard does the same
// on its own when setup runs again with the same name. POST
// /api/account/domains/{domain}/identity.
func (api *API) accountNewIdentity(w http.ResponseWriter, r *http.Request, accountID string) {
	domain := r.PathValue("domain")
	owner, secret := uuid.New().String(), newSecret()
	ok, err := api.dao.ReplaceDeviceIdentity(accountID, domain, owner, secret)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "could not re-issue that domain right now")
		return
	}
	if !ok {
		writeJSONErr(w, http.StatusNotFound, "that domain is not yours")
		return
	}
	log.Info("domain re-issued from the account page:", domain)
	writeJSON(w, http.StatusOK, map[string]any{"domain": domain, "owner_uuid": owner, "secret": secret})
}

// accountReleaseDomain deletes one of the account's domains; the device
// behind it loses the bridge, the name becomes free. DELETE
// /api/account/domains/{domain}.
func (api *API) accountReleaseDomain(w http.ResponseWriter, r *http.Request, accountID string) {
	domain := r.PathValue("domain")
	ok, err := api.dao.DeleteAccountDomain(accountID, domain)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "could not release that domain right now")
		return
	}
	if !ok {
		writeJSONErr(w, http.StatusNotFound, "that domain is not yours")
		return
	}
	log.Info("domain released from the account page:", domain)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// newSecret is a device's bridge secret as the wizard makes them: 48 hex
// characters.
func newSecret() string {
	return strings.ReplaceAll(uuid.New().String()+uuid.New().String(), "-", "")[:48]
}

// deviceOnline (issue #38) answers {"online": bool} for ?name= - whether
// that device holds a live connection to this bridge right now. The setup
// wizard polls it after installing; nothing here a friend request to the
// same domain wouldn't reveal.
func (api *API) deviceOnline(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("name")))
	if !cNamePattern.MatchString(name) {
		writeJSONErr(w, http.StatusBadRequest, "invalid name")
		return
	}
	online := api.websocket != nil && api.websocket.IsOnline(api.deviceDomain(name))
	writeJSON(w, http.StatusOK, map[string]any{"online": online, "domain": api.deviceDomain(name)})
}

var cSetupTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// setupBeacon (issue #38): the device reports {token, addr} once it is on
// the owner's network. Only private (LAN) addresses are accepted - that
// is all this is for, and it keeps the store from being used to point a
// page at anything else.
func (api *API) setupBeacon(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
		Addr  string `json:"addr"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(body.Token) < cSetupTokenMinLen || len(body.Token) > cSetupTokenMaxLen || !cSetupTokenPattern.MatchString(body.Token) {
		writeJSONErr(w, http.StatusBadRequest, "invalid token")
		return
	}
	ip := net.ParseIP(body.Addr)
	if ip == nil || !ip.IsPrivate() {
		writeJSONErr(w, http.StatusBadRequest, "addr must be a private (LAN) IPv4 or IPv6 address")
		return
	}
	if err := api.dao.SetSetupBeacon(body.Token, ip.String()); err != nil {
		log.Error("error storing setup beacon:", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not store the report")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setupLookup (issue #38) is polled by the wizard page - served by the
// device, so from another origin - until the device has reported in.
// 404 until then; the token is unguessable, so a hit is the device's
// own report. Answered with CORS open to any origin: the page's origin is
// whatever the hotspot's captive DNS made it.
func (api *API) setupLookup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	token := r.URL.Query().Get("token")
	if len(token) < cSetupTokenMinLen || len(token) > cSetupTokenMaxLen || !cSetupTokenPattern.MatchString(token) {
		writeJSONErr(w, http.StatusBadRequest, "invalid token")
		return
	}
	addr, found, err := api.dao.GetSetupBeacon(token)
	if err != nil {
		log.Error("error looking up setup beacon:", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not look that up right now")
		return
	}
	if !found {
		writeJSONErr(w, http.StatusNotFound, "not reported yet")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"addr": addr})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
