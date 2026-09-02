// Package admin implements the bridge's admin panel backend: operator
// login, device management, and per-device metrics/security event
// reporting (see GitHub issues #7 and #8). It's deliberately a plain
// JSON-over-HTTP API rather than the device-facing protobuf/WebSocket
// protocol - this is a human operator's browser talking to the bridge
// itself, not a device or friend client.
package admin

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/log"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const (
	cSessionCookie        = "otc_admin_session"
	cSessionTTL           = 12 * time.Hour
	cDefaultMetricsWindow = 7 * 24 * time.Hour
	cDefaultEventsLimit   = 200
	cMaxEventsLimit       = 1000
)

// Admin holds everything the admin HTTP handlers need: DB access and the
// key used to sign session tokens.
type Admin struct {
	dao           *dao.Dao
	sessionSecret []byte
}

// Init builds the admin manager. sessionSecret signs session tokens, so it
// must be stable across restarts (config-provided) or every restart logs
// everyone out; it does not need to be secret from the DB, only from
// clients.
func Init(d *dao.Dao, sessionSecret []byte) *Admin {
	return &Admin{dao: d, sessionSecret: sessionSecret}
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

func newSessionToken(secret []byte, username string, now time.Time) string {
	payload := fmt.Sprintf("%s|%d", username, now.Add(cSessionTTL).Unix())
	return payload + "." + signToken(secret, payload)
}

// verifySessionToken checks the signature and expiry, returning the
// username it was issued for.
func verifySessionToken(secret []byte, token string, now time.Time) (username string, ok bool) {
	payload, sig, found := strings.Cut(token, ".")
	if !found {
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(sig), []byte(signToken(secret, payload))) != 1 {
		return "", false
	}

	user, expStr, found := strings.Cut(payload, "|")
	if !found {
		return "", false
	}
	expUnix, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return "", false
	}
	if now.After(time.Unix(expUnix, 0)) {
		return "", false
	}

	return user, true
}

// ---------------------------------------------------------------------
// Password hashing
// ---------------------------------------------------------------------

func hashPassword(plain string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	return string(hash), err
}

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
		Path:     "/admin",
		Expires:  now.Add(cSessionTTL),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	}
}

// Login checks username/password and, on success, sets a session cookie.
func (a *Admin) Login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
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
		hash = "$2a$10$invalidinvalidinvaliduinvalidinvalidinvalidinvalidin"
	}
	if !checkPassword(hash, body.Password) || !found {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	now := time.Now()
	token := newSessionToken(a.sessionSecret, body.Username, now)
	http.SetCookie(w, a.sessionCookie(token, now, r.TLS != nil))
	writeJSON(w, http.StatusOK, map[string]string{"username": body.Username})
}

// Logout clears the session cookie.
func (a *Admin) Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, a.sessionCookie("", time.Unix(0, 0), r.TLS != nil))
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
		if _, ok := verifySessionToken(a.sessionSecret, cookie.Value, time.Now()); !ok {
			writeError(w, http.StatusUnauthorized, "session expired")
			return
		}
		next(w, r)
	}
}

// ListDevices returns every registered device.
func (a *Admin) ListDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := a.dao.ListDevices()
	if err != nil {
		log.Error("error listing devices:", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, devices)
}

// AddDevice registers a new device. If ownerUuid/secret are omitted, ones
// are generated - the common case is an operator pre-provisioning a
// domain before handing the owner_uuid/secret pair to whoever is setting
// up the physical device.
func (a *Admin) AddDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Domain    string `json:"domain"`
		OwnerUuid string `json:"ownerUuid"`
		Secret    string `json:"secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	body.Domain = strings.TrimSpace(body.Domain)
	if body.Domain == "" {
		writeError(w, http.StatusBadRequest, "domain is required")
		return
	}
	if body.OwnerUuid == "" {
		body.OwnerUuid = uuid.New().String()
	}
	if body.Secret == "" {
		body.Secret = uuid.New().String() + uuid.New().String()
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

	writeJSON(w, http.StatusCreated, map[string]string{
		"domain":    body.Domain,
		"ownerUuid": body.OwnerUuid,
		"secret":    body.Secret,
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
	limit := cDefaultEventsLimit
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= cMaxEventsLimit {
			limit = n
		}
	}

	events, err := a.dao.GetAuthEvents(domain, limit)
	if err != nil {
		log.Error("error reading auth events:", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func isDuplicateKeyErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Duplicate entry")
}
