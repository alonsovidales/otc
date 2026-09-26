// SPDX-License-Identifier: AGPL-3.0-or-later

// Package accounts is issue #124: the bridge's user accounts. A person
// signs up with an email and password, or with Google or Apple (oidc.go),
// and every domain registered from then on belongs to their account - the
// setup wizard asks them to sign in (or type a setup code from their
// account page) before it reserves a name, and the account page at
// /account lists their domains, releases them, re-issues a lost device's
// identity, and shows the terms. Sessions are HMAC-signed cookies like the
// admin panel's; the domains themselves are handled next door in package
// api, which owns the name rules and the claim.
package accounts

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
)

const (
	cSessionCookie = "otc_account"
	cSessionTTL    = 30 * 24 * time.Hour
	// A setup token (also typed as a setup code) is good for this long.
	cSetupTokenTTL = 15 * time.Minute
	cPurposeSetup  = "setup"
	cMinPassword   = 8
	cNameMax       = 150
	cEmailMax      = 255

	// MaxDomains is the terms' limit per account; more is by arrangement
	// (ContactEmail).
	MaxDomains = 5
	// FreeYears is how long a new account uses the bridge for free.
	FreeYears = 2
	// ContactEmail is where to ask for more domains.
	ContactEmail = "info@off-the.cloud"

	// Login throttling: this many failed passwords from one address in
	// the window lock that address out for the window.
	cLoginFailures = 8
	cLoginWindow   = 10 * time.Minute

	// cSetupCodeAlphabet has no 0/O/1/I, so a code read out loud or typed
	// from a screen has no lookalikes.
	cSetupCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	cSetupCodeLen      = 8
)

// Terms is what the account page and the wizard show at sign-up.
var Terms = map[string]any{
	"free_years":         FreeYears,
	"price_per_year_eur": 19.99,
	"max_domains":        MaxDomains,
	"extra_domain_eur":   5,
	"inactive_months":    6,
	"contact":            ContactEmail,
}

// Accounts holds what the account handlers need.
type Accounts struct {
	dao    *dao.Dao
	secret []byte
	// tld is the bridge's own host (off-the.cloud): redirect URIs are built
	// on it, and device domains are <name>.<tld>.
	tld string
	// openRegistration keeps the old behaviour where an unknown device
	// dialling in registers its domain on the spot, with no account
	// ([accounts] open-registration, for forks and development).
	openRegistration bool
	providers        map[string]*provider

	limiterMu sync.Mutex
	failures  map[string][]time.Time
	jwks      jwksCache
}

// Init reads [accounts] from the config: open-registration, and the
// provider credentials (google-client-id / google-client-secret,
// apple-client-id / apple-team-id / apple-key-id / apple-private-key, a
// path to the .p8) - a provider without credentials is simply not offered.
func Init(d *dao.Dao, sessionSecret []byte, tld string) *Accounts {
	a := &Accounts{
		dao:              d,
		secret:           sessionSecret,
		tld:              tld,
		openRegistration: cfg.GetStr("accounts", "open-registration") == "true",
		providers:        map[string]*provider{},
		failures:         map[string][]time.Time{},
	}
	a.loadProviders()
	go a.pruneLoop()

	return a
}

func (a *Accounts) pruneLoop() {
	for {
		time.Sleep(10 * time.Minute)
		if err := a.dao.PruneAccountTokens(); err != nil {
			log.Error("error pruning account tokens:", err)
		}
	}
}

// OpenRegistration is whether a device may still register a domain by
// dialling in, with no account behind it.
func (a *Accounts) OpenRegistration() bool { return a.openRegistration }

// ---------------------------------------------------------------------
// Sessions: HMAC-signed "accountID|expiry" cookies, no session table.
// ---------------------------------------------------------------------

func sign(secret []byte, payload string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))

	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *Accounts) sessionToken(accountID string, now time.Time) string {
	payload := fmt.Sprintf("%s|%d", accountID, now.Add(cSessionTTL).Unix())

	return payload + "." + sign(a.secret, payload)
}

func (a *Accounts) verifySession(token string, now time.Time) (accountID string, ok bool) {
	payload, sig, found := strings.Cut(token, ".")
	if !found || subtle.ConstantTimeCompare([]byte(sig), []byte(sign(a.secret, payload))) != 1 {
		return "", false
	}
	id, expStr, found := strings.Cut(payload, "|")
	if !found {
		return "", false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || now.After(time.Unix(exp, 0)) {
		return "", false
	}

	return id, true
}

func (a *Accounts) setSession(w http.ResponseWriter, r *http.Request, accountID string) {
	now := time.Now()
	http.SetCookie(w, &http.Cookie{
		Name: cSessionCookie, Value: a.sessionToken(accountID, now), Path: "/",
		Expires: now.Add(cSessionTTL), HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode,
	})
}

func (a *Accounts) clearSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cSessionCookie, Value: "", Path: "/", Expires: time.Unix(0, 0), HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode})
}

// AccountFromRequest is the signed-in account, if the request carries a
// live session cookie.
func (a *Accounts) AccountFromRequest(r *http.Request) (accountID string, ok bool) {
	c, err := r.Cookie(cSessionCookie)
	if err != nil {
		return "", false
	}

	return a.verifySession(c.Value, time.Now())
}

// RequireAuth wraps a handler so it only runs for a signed-in account.
func (a *Accounts) RequireAuth(next func(w http.ResponseWriter, r *http.Request, accountID string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := a.AccountFromRequest(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		next(w, r, id)
	}
}

// ---------------------------------------------------------------------
// Setup tokens: what ties a wizard's claim to an account.
// ---------------------------------------------------------------------

// IssueSetupToken makes a short one-time code (8 characters, no
// lookalikes) the account page shows and the wizard accepts typed by
// hand, and that sign-in through the wizard hands over directly.
func (a *Accounts) IssueSetupToken(accountID string) (string, error) {
	buf := make([]byte, cSetupCodeLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	code := make([]byte, cSetupCodeLen)
	for i, b := range buf {
		code[i] = cSetupCodeAlphabet[int(b)%len(cSetupCodeAlphabet)]
	}
	token := string(code)
	if err := a.dao.SaveAccountToken(token, accountID, cPurposeSetup, time.Now().Add(cSetupTokenTTL)); err != nil {
		return "", err
	}

	return token, nil
}

// NormalizeSetupToken makes a typed code comparable: upper case, without
// the spaces and dashes people put in.
func NormalizeSetupToken(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.NewReplacer(" ", "", "-", "", "‑", "").Replace(s)

	return s
}

// AccountForSetupToken is the account a live setup token belongs to.
func (a *Accounts) AccountForSetupToken(token string) (accountID string, ok bool) {
	token = NormalizeSetupToken(token)
	if len(token) != cSetupCodeLen {
		return "", false
	}
	id, found, err := a.dao.AccountForToken(token, cPurposeSetup)
	if err != nil {
		log.Error("error looking up a setup token:", err)
		return "", false
	}

	return id, found
}

// ---------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------

func validEmail(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || len(s) > cEmailMax {
		return "", false
	}
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s {
		return "", false
	}

	return s, true
}

func validName(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > cNameMax || strings.ContainsAny(s, "<>\n\r") {
		return "", false
	}

	return s, true
}

func validCountry(s string) (string, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	_, ok := Countries[s]

	return s, ok
}

// ---------------------------------------------------------------------
// Login throttling, per address (the admin panel does the same)
// ---------------------------------------------------------------------

func (a *Accounts) loginAllowed(addr string, now time.Time) bool {
	a.limiterMu.Lock()
	defer a.limiterMu.Unlock()
	recent := a.failures[addr][:0]
	for _, t := range a.failures[addr] {
		if now.Sub(t) < cLoginWindow {
			recent = append(recent, t)
		}
	}
	a.failures[addr] = recent

	return len(recent) < cLoginFailures
}

func (a *Accounts) loginFailed(addr string, now time.Time) {
	a.limiterMu.Lock()
	defer a.limiterMu.Unlock()
	a.failures[addr] = append(a.failures[addr], now)
	if len(a.failures) > 10000 {
		a.failures = map[string][]time.Time{}
	}
}

func (a *Accounts) loginSucceeded(addr string) {
	a.limiterMu.Lock()
	defer a.limiterMu.Unlock()
	delete(a.failures, addr)
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return host
}

// ---------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func accountJSON(acc *dao.Account) map[string]any {
	return map[string]any{
		"email": acc.Email, "name": acc.Name, "surname": acc.Surname, "country": acc.Country,
		"created": acc.Created, "free_until": acc.FreeUntil,
		"profile_complete": acc.Name != "" && acc.Surname != "" && acc.Country != "",
		"has_password":     acc.PasswordHash != "",
	}
}

// signedIn answers a successful sign-up or sign-in: the account, and -
// with ?for=setup, the wizard's way - a setup token to claim a name with.
func (a *Accounts) signedIn(w http.ResponseWriter, r *http.Request, acc *dao.Account, status int) {
	a.setSession(w, r, acc.ID)
	_ = a.dao.TouchAccount(acc.ID)
	out := map[string]any{"account": accountJSON(acc)}
	if r.URL.Query().Get("for") == "setup" {
		tok, err := a.IssueSetupToken(acc.ID)
		if err != nil {
			log.Error("error issuing a setup token:", err)
			writeError(w, http.StatusInternalServerError, "could not start the setup")
			return
		}
		out["setup_token"] = tok
	}
	writeJSON(w, status, out)
}

// Signup creates an email+password account. POST /api/account/signup
// {email, password, name, surname, country, accept_terms}.
func (a *Accounts) Signup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email, Password, Name, Surname, Country string
		AcceptTerms                             bool `json:"accept_terms"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	email, ok := validEmail(body.Email)
	if !ok {
		writeError(w, http.StatusBadRequest, "that email address doesn't look right")
		return
	}
	if len(body.Password) < cMinPassword {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("the password needs at least %d characters", cMinPassword))
		return
	}
	name, ok1 := validName(body.Name)
	surname, ok2 := validName(body.Surname)
	country, ok3 := validCountry(body.Country)
	if !ok1 || !ok2 || !ok3 {
		writeError(w, http.StatusBadRequest, "name, surname and country of residence are required")
		return
	}
	if !body.AcceptTerms {
		writeError(w, http.StatusBadRequest, "please accept the terms")
		return
	}
	if existing, err := a.dao.GetAccountByEmail(email); err != nil {
		log.Error("error checking an email at sign-up:", err)
		writeError(w, http.StatusInternalServerError, "could not sign up right now")
		return
	} else if existing != nil {
		writeError(w, http.StatusConflict, "there is already an account with that email - sign in instead")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not sign up right now")
		return
	}
	now := time.Now()
	acc := dao.Account{
		ID: uuid.New().String(), Email: email, Name: name, Surname: surname, Country: country,
		PasswordHash: string(hash), Created: now, LastSeen: now, FreeUntil: now.AddDate(FreeYears, 0, 0),
	}
	if err := a.dao.CreateAccount(acc); err != nil {
		log.Error("error creating an account:", err)
		writeError(w, http.StatusConflict, "there is already an account with that email - sign in instead")
		return
	}
	log.Info("account created:", email, "from", clientIP(r))
	a.signedIn(w, r, &acc, http.StatusCreated)
}

// Login checks an email and password. POST /api/account/login.
func (a *Accounts) Login(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	ip := clientIP(r)
	if !a.loginAllowed(ip, now) {
		writeError(w, http.StatusTooManyRequests, "too many failed attempts, try again later")
		return
	}
	var body struct{ Email, Password string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	email, _ := validEmail(body.Email)
	acc, err := a.dao.GetAccountByEmail(email)
	if err != nil {
		log.Error("error looking up an account:", err)
		writeError(w, http.StatusInternalServerError, "could not sign in right now")
		return
	}
	// bcrypt runs either way, so the timing says nothing about whether the
	// email exists.
	hash := "$2a$10$invalidinvalidinvaliduinvalidinvalidinvalidinvalidin"
	if acc != nil && acc.PasswordHash != "" {
		hash = acc.PasswordHash
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(body.Password)) != nil || acc == nil || acc.PasswordHash == "" {
		a.loginFailed(ip, now)
		if acc != nil && acc.PasswordHash == "" {
			writeError(w, http.StatusUnauthorized, "that account signs in with Google or Apple - use the account page, then a setup code")
			return
		}
		writeError(w, http.StatusUnauthorized, "wrong email or password")
		return
	}
	a.loginSucceeded(ip)
	a.signedIn(w, r, acc, http.StatusOK)
}

// Logout drops the session cookie. POST /api/account/logout.
func (a *Accounts) Logout(w http.ResponseWriter, r *http.Request) {
	a.clearSession(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Me is the signed-in account. GET /api/account/me.
func (a *Accounts) Me(w http.ResponseWriter, r *http.Request, accountID string) {
	acc, err := a.dao.GetAccount(accountID)
	if err != nil || acc == nil {
		a.clearSession(w, r)
		writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	_ = a.dao.TouchAccount(accountID)
	writeJSON(w, http.StatusOK, map[string]any{"account": accountJSON(acc), "terms": Terms, "providers": a.ProviderList()})
}

// UpdateProfile completes or changes name, surname and country. PUT
// /api/account/me. With ?for=setup it also returns a setup token, for the
// "complete your profile, then back to the wizard" step of a provider
// sign-in.
func (a *Accounts) UpdateProfile(w http.ResponseWriter, r *http.Request, accountID string) {
	var body struct{ Name, Surname, Country string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name, ok1 := validName(body.Name)
	surname, ok2 := validName(body.Surname)
	country, ok3 := validCountry(body.Country)
	if !ok1 || !ok2 || !ok3 {
		writeError(w, http.StatusBadRequest, "name, surname and country of residence are required")
		return
	}
	if err := a.dao.UpdateAccountProfile(accountID, name, surname, country); err != nil {
		log.Error("error updating a profile:", err)
		writeError(w, http.StatusInternalServerError, "could not save right now")
		return
	}
	acc, err := a.dao.GetAccount(accountID)
	if err != nil || acc == nil {
		writeError(w, http.StatusInternalServerError, "could not save right now")
		return
	}
	a.signedIn(w, r, acc, http.StatusOK)
}

// SetPassword sets or changes the password of the signed-in account (a
// provider account gains one this way). PUT /api/account/password.
func (a *Accounts) SetPassword(w http.ResponseWriter, r *http.Request, accountID string) {
	var body struct{ Password string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Password) < cMinPassword {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("the password needs at least %d characters", cMinPassword))
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not save right now")
		return
	}
	if err := a.dao.SetAccountPassword(accountID, string(hash)); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save right now")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// SetupToken hands the signed-in account a setup code for the wizard.
// GET /api/account/setup-token.
func (a *Accounts) SetupToken(w http.ResponseWriter, r *http.Request, accountID string) {
	tok, err := a.IssueSetupToken(accountID)
	if err != nil {
		log.Error("error issuing a setup token:", err)
		writeError(w, http.StatusInternalServerError, "could not make a setup code right now")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"setup_token": tok, "expires_in_seconds": int(cSetupTokenTTL.Seconds())})
}

// SetupTokenInfo tells a wizard whose code it was handed. GET
// /api/account/setup-token-info?token=. Public: the code is the secret.
func (a *Accounts) SetupTokenInfo(w http.ResponseWriter, r *http.Request) {
	id, ok := a.AccountForSetupToken(r.URL.Query().Get("token"))
	if !ok {
		writeError(w, http.StatusNotFound, "that setup code is not valid or has expired")
		return
	}
	acc, err := a.dao.GetAccount(id)
	if err != nil || acc == nil {
		writeError(w, http.StatusNotFound, "that setup code is not valid or has expired")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"email": acc.Email, "name": acc.Name})
}

// Countries lists the country picker. GET /api/account/countries.
func (a *Accounts) CountryList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, Countries)
}

// Providers says which sign-in providers are configured. GET
// /api/account/providers.
func (a *Accounts) Providers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"providers": a.ProviderList(), "terms": Terms})
}

// ProviderList is the configured providers' names.
func (a *Accounts) ProviderList() []string {
	out := []string{}
	for _, name := range []string{"google", "apple"} {
		if _, ok := a.providers[name]; ok {
			out = append(out, name)
		}
	}

	return out
}

// validReturnURL is where a provider sign-in started from the wizard may
// send the browser back to: the wizard runs on the device's own address
// (its hotspot, then the LAN), so a private address or a .local name, and
// nothing else - never an arbitrary site, which would make the callback
// an open redirect handing out setup tokens.
func validReturnURL(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", false
	}
	host := u.Hostname()
	if host == "otc" || host == "otc.local" || strings.HasSuffix(host, ".local") {
		return raw, true
	}
	ip := net.ParseIP(host)
	if ip != nil && (ip.IsPrivate() || ip.IsLoopback()) {
		return raw, true
	}

	return "", false
}

// ErrInvalidReturn is what a sign-in start with a bad return URL fails with.
var ErrInvalidReturn = errors.New("that return address is not allowed")
