// SPDX-License-Identifier: AGPL-3.0-or-later

// Package admin implements the bridge's admin panel backend: operator
// login, device management, and per-device metrics/security event
// reporting (see GitHub issues #7 and #8). It's deliberately a plain
// JSON-over-HTTP API rather than the device-facing protobuf/WebSocket
// protocol - this is a human operator's browser talking to the bridge
// itself, not a device or friend client.
package admin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/alonsovidales/otc/bridge/limits"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/bridge/fleet"
	"github.com/alonsovidales/otc/log"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const (
	// __Host- (so Path must be /): see accounts.cSessionCookie.
	cSessionCookie        = "__Host-otc_admin"
	cSessionTTL           = 12 * time.Hour
	cDefaultMetricsWindow = 7 * 24 * time.Hour
)

// Admin holds everything the admin HTTP handlers need: DB access and the
// key used to sign session tokens.
type Admin struct {
	dao           *dao.Dao
	sessionSecret []byte
	// Issue #99: per-IP login throttling, see ratelimit.go.
	loginLimiter *loginLimiter
	// IsOnline reports whether a device is dialled in right now (the
	// relay pool, set by main once the websocket manager exists). Nil
	// means unknown - every device shows offline.
	IsOnline func(domain string) bool
	// FleetView reads the servers' reports for the Fleet tab (fleet.Read
	// on the cluster's Redis, set by main). Nil on a bridge without a
	// cluster: the tab says there is nothing to show.
	FleetView func(ctx context.Context) (*fleet.View, error)
}

// Init builds the admin manager. sessionSecret signs session tokens, so it
// must be stable across restarts (config-provided) or every restart logs
// everyone out; it does not need to be secret from the DB, only from
// clients.
func Init(d *dao.Dao, sessionSecret []byte) *Admin {
	// Its own key, derived from the shared secret: account sessions
	// (accounts.Init) are signed from the same secret in the same
	// "<id>|<expiry>" format, and an account cookie used to pass as an
	// admin one.
	return &Admin{dao: d, sessionSecret: DeriveKey(sessionSecret, "admin-session"), loginLimiter: newLoginLimiter()}
}

// DeriveKey gives each use of the configured session secret its own key,
// so a token signed for one purpose never verifies for another.
func DeriveKey(secret []byte, purpose string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(purpose))
	return mac.Sum(nil)
}

// ---------------------------------------------------------------------
// Session tokens: username + expiry, HMAC-signed, stateless (no DB-backed
// session table - the token itself is the proof, checked on every request).
// ---------------------------------------------------------------------

func signToken(secret []byte, payload string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Issue #164: the admin's session_epoch goes in too; logging out bumps it,
// which ends every session of that admin, on every browser.
func newSessionToken(secret []byte, username string, epoch int, now time.Time) string {
	payload := fmt.Sprintf("%s|%d|%d", username, epoch, now.Add(cSessionTTL).Unix())
	return payload + "." + signToken(secret, payload)
}

// verifySessionToken checks the signature and expiry, returning the
// username it was issued for.
func verifySessionToken(secret []byte, token string, now time.Time) (username string, epoch int, ok bool) {
	payload, sig, found := strings.Cut(token, ".")
	if !found {
		return "", 0, false
	}
	if subtle.ConstantTimeCompare([]byte(sig), []byte(signToken(secret, payload))) != 1 {
		return "", 0, false
	}

	parts := strings.Split(payload, "|")
	if len(parts) != 3 {
		return "", 0, false // cookies from before issue #164 included
	}
	epoch, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", 0, false
	}
	expUnix, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return "", 0, false
	}
	if now.After(time.Unix(expUnix, 0)) {
		return "", 0, false
	}

	return parts[0], epoch, true
}

// ---------------------------------------------------------------------
// Password hashing
// ---------------------------------------------------------------------

func hashPassword(plain string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), limits.BcryptCost)
	return string(hash), err
}

// adminDummyHash is compared against when the username is unknown, at the
// same cost as a real admin hash so the timing doesn't tell them apart.
var adminDummyHash = func() string {
	h, err := bcrypt.GenerateFromPassword([]byte("not a password"), limits.BcryptCost)
	if err != nil {
		panic(err)
	}
	return string(h)
}()

func checkPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// SetPassword creates or updates an admin account. Exported so it can be
// driven from a one-off CLI bootstrap command (see bridge/bin/otc_bridge.go)
// rather than needing its own HTTP endpoint - nothing should be able to
// create admin accounts over the network.
func (a *Admin) SetPassword(username, plain string) error {
	if username == "" || plain == "" {
		return errors.New("username and password are required")
	}
	hash, err := hashPassword(plain)
	if err != nil {
		return err
	}
	return a.dao.SetAdminPassword(username, hash)
}

// ---------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (a *Admin) sessionCookie(token string, now time.Time, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     cSessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  now.Add(cSessionTTL),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	}
}

// Login checks username/password and, on success, sets a session cookie.
// Issue #99: throttled per source IP (see ratelimit.go) - bcrypt alone
// slows a guesser down but never actually stops one.
func (a *Admin) Login(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	ip := clientIP(r)

	// Checked before the body is even decoded, so a locked-out caller
	// can't keep the bridge doing bcrypt work on its behalf.
	if ok, retryAfter := a.loginLimiter.allow(ip, now); !ok {
		log.Error("admin login attempt from a rate-limited address:", ip, "- retry in", retryAfter.Round(time.Second))
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "too many failed attempts, try again later")
		return
	}

	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := limits.DecodeJSON(w, r, &body, limits.MaxJSONBody); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	hash, found, err := a.dao.GetAdminPasswordHash(body.Username)
	if err != nil {
		log.Error("error looking up admin user:", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Always run bcrypt, even for an unknown username, so the response
	// time doesn't reveal whether the username exists.
	if !found {
		hash = adminDummyHash
	}
	if !checkPassword(hash, body.Password) || !found {
		// Counted against the IP, not the username: a guesser picks the
		// username, so counting per account would let them sidestep this
		// by rotating it (and would hand anyone a way to lock the real
		// operator out of their own panel).
		a.loginLimiter.recordFailure(ip, now)
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	a.loginLimiter.recordSuccess(ip)
	// Older hashes move to the current cost while the password is at hand
	// (as account passwords do): a cheaper real hash would be told apart
	// from the decoy by its speed.
	if cost, err := bcrypt.Cost([]byte(hash)); err == nil && cost < limits.BcryptCost {
		if h, err := hashPassword(body.Password); err == nil {
			if err := a.dao.SetAdminPassword(body.Username, h); err != nil {
				log.Error("error rehashing an admin password:", err)
			}
		}
	}
	epoch, _, err := a.dao.AdminSessionEpoch(body.Username)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not sign in right now")
		return
	}
	token := newSessionToken(a.sessionSecret, body.Username, epoch, now)
	http.SetCookie(w, a.sessionCookie(token, now, r.TLS != nil))
	writeJSON(w, http.StatusOK, map[string]string{"username": body.Username})
}

// Logout clears the session cookie and (issue #164) ends every session of
// that admin - a panel left signed in on another machine included.
func (a *Admin) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(cSessionCookie); err == nil {
		if user, _, ok := verifySessionToken(a.sessionSecret, cookie.Value, time.Now()); ok {
			if err := a.dao.BumpAdminSessionEpoch(user); err != nil {
				log.Error("could not end the other admin sessions:", err)
			}
		}
	}
	http.SetCookie(w, a.sessionCookie("", time.Unix(0, 0), r.TLS != nil))
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// cMinPasswordLen is the shortest admin password accepted, here and by the
// command-line bootstrap (readAdminPassword in bin/otc_bridge.go).
const cMinPasswordLen = 12

// ChangePassword lets the signed-in admin change their own password, from
// the panel (POST /admin/api/password, behind RequireAuth). The current
// password is asked again - a panel left open must not be enough to take
// the account over - and a wrong one counts against the address like a
// failed login. Every other session of the admin ends; this one gets a
// fresh cookie and carries on.
func (a *Admin) ChangePassword(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	ip := clientIP(r)
	if ok, retryAfter := a.loginLimiter.allow(ip, now); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "too many failed attempts, try again later")
		return
	}
	cookie, err := r.Cookie(cSessionCookie)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	user, _, ok := verifySessionToken(a.sessionSecret, cookie.Value, now)
	if !ok {
		writeError(w, http.StatusUnauthorized, "session expired")
		return
	}
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := limits.DecodeJSON(w, r, &body, limits.MaxJSONBody); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(body.New) < cMinPasswordLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("use at least %d characters for the new password", cMinPasswordLen))
		return
	}
	if len(body.New) > 72 {
		// bcrypt reads only the first 72 bytes; longer would look accepted
		// and silently not count.
		writeError(w, http.StatusBadRequest, "use at most 72 characters for the new password")
		return
	}
	hash, found, err := a.dao.GetAdminPasswordHash(user)
	if err != nil {
		log.Error("error looking up admin user:", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !found || !checkPassword(hash, body.Current) {
		a.loginLimiter.recordFailure(ip, now)
		writeError(w, http.StatusUnauthorized, "the current password is wrong")
		return
	}
	a.loginLimiter.recordSuccess(ip)
	if body.New == body.Current {
		writeError(w, http.StatusBadRequest, "the new password is the same as the current one")
		return
	}
	newHash, err := hashPassword(body.New)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not change the password right now")
		return
	}
	if err := a.dao.SetAdminPassword(user, newHash); err != nil {
		log.Error("error changing an admin password:", err)
		writeError(w, http.StatusInternalServerError, "could not change the password right now")
		return
	}
	if err := a.dao.BumpAdminSessionEpoch(user); err != nil {
		log.Error("could not end the other admin sessions:", err)
	}
	epoch, _, err := a.dao.AdminSessionEpoch(user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "the password was changed; sign in again")
		return
	}
	log.Info("admin password changed")
	http.SetCookie(w, a.sessionCookie(newSessionToken(a.sessionSecret, user, epoch, now), now, r.TLS != nil))
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// RequireAuth wraps a handler so it only runs for a request carrying a
// valid, unexpired session cookie.
func (a *Admin) RequireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(cSessionCookie)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "not logged in")
			return
		}
		user, epoch, ok := verifySessionToken(a.sessionSecret, cookie.Value, time.Now())
		if !ok {
			writeError(w, http.StatusUnauthorized, "session expired")
			return
		}
		// Still an admin: a removed admin's session ends with the removal,
		// not when the cookie expires - and (issue #164) one issued before
		// the admin last logged out ends too.
		current, found, err := a.dao.AdminSessionEpoch(user)
		if err != nil || !found {
			writeError(w, http.StatusUnauthorized, "not an admin")
			return
		}
		if current != epoch {
			writeError(w, http.StatusUnauthorized, "session expired")
			return
		}
		next(w, r)
	}
}

// ListDevices returns every registered device.
// Issue #143: every list in the panel is paged and searchable, with the
// same query parameters (q, page from 1, size) and the same answer.
const (
	cDefaultPageSize = 25
	cMaxPageSize     = 200
)

// Page is one page of a list and how many items match in all.
type Page struct {
	Items any
	Total int
	Page  int
	Size  int
}

// pageParams reads ?q=&page=&size= and returns the search text and the
// limit/offset to query with.
func pageParams(r *http.Request) (q string, page, size, offset int) {
	q = strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 200 {
		q = q[:200]
	}
	page, size = 1, cDefaultPageSize
	if n, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && n > 0 {
		page = n
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("size")); err == nil && n > 0 {
		size = min(n, cMaxPageSize)
	}
	return q, page, size, (page - 1) * size
}

// ListDevices is the Devices tab (issue #139): owner, online now, last
// client connection and relayed traffic per device.
func (a *Admin) ListDevices(w http.ResponseWriter, r *http.Request) {
	q, page, size, offset := pageParams(r)
	devices, total, err := a.adminDevices("", q, size, offset)
	if err != nil {
		log.Error("error listing devices:", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, Page{Items: devices, Total: total, Page: page, Size: size})
}

// ListDomains is every registered domain, for the Metrics and Security
// tabs' device pickers (the device list itself is paged).
func (a *Admin) ListDomains(w http.ResponseWriter, r *http.Request) {
	domains, err := a.dao.AllDomains()
	if err != nil {
		log.Error("error listing domains:", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, domains)
}

func (a *Admin) adminDevices(accountID, q string, limit, offset int) ([]dao.AdminDevice, int, error) {
	devices, total, err := a.dao.ListAdminDevices(accountID, q, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	if a.IsOnline != nil {
		for i := range devices {
			devices[i].Online = a.IsOnline(devices[i].Domain)
		}
	}
	return devices, total, nil
}

// ListAccounts is the Accounts tab (issue #139).
func (a *Admin) ListAccounts(w http.ResponseWriter, r *http.Request) {
	q, page, size, offset := pageParams(r)
	accounts, total, err := a.dao.ListAdminAccounts("", q, size, offset)
	if err != nil {
		log.Error("error listing accounts:", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, Page{Items: accounts, Total: total, Page: page, Size: size})
}

// GetAccount is one account with all its devices, what the Accounts tab
// shows when a row is opened.
func (a *Admin) GetAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	accounts, _, err := a.dao.ListAdminAccounts(id, "", 1, 0)
	if err != nil {
		log.Error("error reading account", id, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if len(accounts) == 0 {
		writeError(w, http.StatusNotFound, "no such account")
		return
	}
	devices, _, err := a.adminDevices(id, "", 0, 0)
	if err != nil {
		log.Error("error listing the devices of account", id, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"Account": accounts[0], "Devices": devices})
}

// cMinRelaySecretLen is the shortest secret AddDevice will accept. The
// admin panel generates 32 random bytes (64 hex chars) and install.sh's
// own `secrets` step uses `openssl rand -hex 24` (48), so this only ever
// rejects something typed by hand - which is exactly the point, since
// this secret is the whole of what gates a domain onto the relay.
const cMinRelaySecretLen = 32

// AddDevice registers a new device: an operator pre-provisioning a domain
// before the physical device that will claim it exists. ownerUuid is
// generated when omitted (it's an identifier, not a credential), but the
// secret must be supplied by the caller.
//
// Issue #100: this used to generate the secret here and echo it back in
// the 201 body, which put a long-lived relay credential into a response
// body - somewhere it lands in far more places than the one operator who
// needed it (proxy/access logs that capture bodies, browser memory and
// devtools history, any intermediary between here and them). The caller
// generating it instead (see admin.html's crypto.getRandomValues) means
// whoever needs the secret already has it, so the bridge never has to
// hand one back and this endpoint has no secret to leak at all.
func (a *Admin) AddDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Domain    string `json:"domain"`
		OwnerUuid string `json:"ownerUuid"`
		Secret    string `json:"secret"`
	}
	if err := limits.DecodeJSON(w, r, &body, limits.MaxJSONBody); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	body.Domain = strings.TrimSpace(body.Domain)
	if body.Domain == "" {
		writeError(w, http.StatusBadRequest, "domain is required")
		return
	}
	body.Secret = strings.TrimSpace(body.Secret)
	if len(body.Secret) < cMinRelaySecretLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("secret is required and must be at least %d characters", cMinRelaySecretLen))
		return
	}
	if body.OwnerUuid == "" {
		body.OwnerUuid = uuid.New().String()
	}

	if err := a.dao.RegistreDevice(body.OwnerUuid, body.Domain, body.Secret); err != nil {
		if isDuplicateKeyErr(err) {
			writeError(w, http.StatusConflict, "domain already registered")
			return
		}
		log.Error("error adding device:", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Deliberately no secret in the response - see the doc comment above.
	writeJSON(w, http.StatusCreated, map[string]string{
		"domain":    body.Domain,
		"ownerUuid": body.OwnerUuid,
	})
}

// DeleteDevice removes a device by domain (path: /admin/api/devices/{domain}).
func (a *Admin) DeleteDevice(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	if domain == "" {
		writeError(w, http.StatusBadRequest, "domain is required")
		return
	}
	if err := a.dao.DeleteDevice(domain); err != nil {
		log.Error("error deleting device:", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// Metrics returns hourly request/bandwidth buckets, optionally filtered by
// ?domain=, over the last ?hours= (default 7 days, i.e. 168).
func (a *Admin) Metrics(w http.ResponseWriter, r *http.Request) {
	domain := r.URL.Query().Get("domain")
	since := time.Now().Add(-cDefaultMetricsWindow)
	if h := r.URL.Query().Get("hours"); h != "" {
		if hours, err := strconv.Atoi(h); err == nil && hours > 0 {
			since = time.Now().Add(-time.Duration(hours) * time.Hour)
		}
	}

	buckets, err := a.dao.GetDeviceMetrics(domain, since)
	if err != nil {
		log.Error("error reading metrics:", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, buckets)
}

// AuthEvents returns the most recent auth failures, optionally filtered by
// ?domain=, capped by ?limit= (default 200, max 1000).
func (a *Admin) AuthEvents(w http.ResponseWriter, r *http.Request) {
	domain := r.URL.Query().Get("domain")
	q, page, size, offset := pageParams(r)
	events, total, err := a.dao.GetAuthEvents(domain, q, size, offset)
	if err != nil {
		log.Error("error reading auth events:", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, Page{Items: events, Total: total, Page: page, Size: size})
}

// ContactRequests returns the most recent public-site contact form
// submissions (issue #57), optionally capped by ?limit= (default 200, max
// 1000, same convention as AuthEvents).
func (a *Admin) ContactRequests(w http.ResponseWriter, r *http.Request) {
	q, page, size, offset := pageParams(r)
	requests, total, unread, err := a.dao.ListContactRequests(q, size, offset)
	if err != nil {
		log.Error("error reading contact requests:", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Unread counts every message, whatever the page or search: it is the
	// tab's badge.
	writeJSON(w, http.StatusOK, map[string]any{"Items": requests, "Total": total, "Page": page, "Size": size, "Unread": unread})
}

// SetContactRequestRead marks a contact request read/unread (path:
// /admin/api/contact-requests/{id}/read).
func (a *Admin) SetContactRequestRead(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body struct {
		Read bool `json:"read"`
	}
	if err := limits.DecodeJSON(w, r, &body, limits.MaxJSONBody); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := a.dao.SetContactRequestRead(id, body.Read); err != nil {
		log.Error("error updating contact request:", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// DeleteContactRequest deletes a contact message (DELETE
// /admin/api/contact-requests/{id}).
func (a *Admin) DeleteContactRequest(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := a.dao.DeleteContactRequest(id); err != nil {
		log.Error("error deleting contact request:", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// fleetAnswer is GET /admin/api/fleet's body: the View, and whether there
// is a cluster to show at all.
type fleetAnswer struct {
	Enabled bool `json:"enabled"`
	*fleet.View
}

// Fleet is the Fleet tab: every server of the cluster, its figures and its
// checks (fleet.Evaluate - the same the email alerts use). Counts only:
// nothing in it names a device, an account or an address.
func (a *Admin) Fleet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if a.FleetView == nil {
		writeJSON(w, http.StatusOK, fleetAnswer{View: &fleet.View{Time: time.Now().UTC(), Hosts: []fleet.HostView{}}})
		return
	}
	v, err := a.FleetView(r.Context())
	if err != nil {
		log.Error("fleet: could not read the servers' reports:", err)
		writeError(w, http.StatusServiceUnavailable, "could not read the servers' reports from Redis")
		return
	}
	writeJSON(w, http.StatusOK, fleetAnswer{Enabled: true, View: v})
}

func isDuplicateKeyErr(err error) bool {
	return dao.IsDuplicateKey(err)
}
