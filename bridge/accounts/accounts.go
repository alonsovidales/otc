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
	"github.com/alonsovidales/otc/bridge/clientaddr"
	"github.com/alonsovidales/otc/bridge/limits"
	"golang.org/x/net/publicsuffix"
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
	"github.com/alonsovidales/otc/bridge/mailer"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
)

const (
	// __Host-: only this exact host can set it - a device's page on a
	// subdomain (same site) used to be able to plant its own account
	// cookie for the whole domain. Always Secure (a __Host- requirement).
	cSessionCookie = "__Host-otc_account"
	cSessionTTL    = 30 * 24 * time.Hour
	// cFreshSignIn: how recent a sign-in must be to set a first password
	// on a Google/Apple account (issue #164).
	cFreshSignIn = 15 * time.Minute
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

// TermsVersion is the version of the terms of use (/terms) in force; an
// account records the one it accepted (issue #175). Bump it whenever
// static/terms.html changes in substance: every account is then asked to
// accept the new text before it gets another setup code.
const TermsVersion = "2026-10-04"

// TermsAccepted: the account accepted the terms in force.
func TermsAccepted(acc *dao.Account) bool {
	return acc != nil && acc.TermsVersion == TermsVersion
}

// Terms is what the account page and the wizard show at sign-up.
var Terms = map[string]any{
	"terms_version":      TermsVersion,
	"terms_url":          "/terms",
	"free_years":         FreeYears,
	"price_per_year_eur": 9.99,
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

	// mailer sends verification and reset emails; nil without [smtp].
	mailer *mailer.Mailer
	// emailsPerAddr limits the "send me an email" requests per address.
	emailsPerAddr *limits.Rate

	limiterMu sync.Mutex
	failures  map[string][]time.Time
	// signups limits account creation per address (issue #163): each is
	// a bcrypt, and each answer says whether an email has an account.
	signups *limits.Rate
	// oauthStarts limits sign-in starts per address: each one inserts an
	// oauth_states row.
	oauthStarts *limits.Rate
	jwks        jwksCache
}

// Init reads [accounts] from the config: open-registration, and the
// provider credentials (google-client-id / google-client-secret,
// apple-client-id / apple-team-id / apple-key-id / apple-private-key, a
// path to the .p8) - a provider without credentials is simply not offered.
// deriveKey is admin.DeriveKey - repeated here rather than imported, so
// the two packages stay independent.
func deriveKey(secret []byte, purpose string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(purpose))
	return mac.Sum(nil)
}

func Init(d *dao.Dao, sessionSecret []byte, tld string) *Accounts {
	a := &Accounts{
		dao: d,
		// Its own key (see admin.DeriveKey): never the admin one.
		secret:           deriveKey(sessionSecret, "account-session"),
		tld:              tld,
		openRegistration: cfg.HasSection("accounts") && cfg.GetStr("accounts", "open-registration") == "true",
		providers:        map[string]*provider{},
		failures:         map[string][]time.Time{},
		signups:          limits.NewRate(cSignupsPerHour/3600.0, cSignupsPerHour),
		emailsPerAddr:    limits.NewRate(cEmailsPerHour/3600.0, cEmailsPerHour),
		oauthStarts:      limits.NewRate(cOAuthStartsPerMinute/60.0, cOAuthStartsPerMinute),
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
// Sessions: HMAC-signed "accountID|epoch|expiry" cookies, no session
// table. Issue #164: the epoch is the account's session_epoch when the
// cookie was issued; bumping it (a password change, "sign out everywhere")
// ends every session issued before.
// ---------------------------------------------------------------------

func sign(secret []byte, payload string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))

	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *Accounts) sessionToken(accountID string, epoch int, now time.Time) string {
	payload := fmt.Sprintf("%s|%d|%d", accountID, epoch, now.Add(cSessionTTL).Unix())

	return payload + "." + sign(a.secret, payload)
}

// verifySession checks the signature and expiry; the epoch it returns
// still has to match the account's (session).
func (a *Accounts) verifySession(token string, now time.Time) (accountID string, epoch int, issued time.Time, ok bool) {
	payload, sig, found := strings.Cut(token, ".")
	if !found || subtle.ConstantTimeCompare([]byte(sig), []byte(sign(a.secret, payload))) != 1 {
		return "", 0, time.Time{}, false
	}
	parts := strings.Split(payload, "|")
	if len(parts) != 3 {
		return "", 0, time.Time{}, false // cookies from before issue #164 included
	}
	epoch, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", 0, time.Time{}, false
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || now.After(time.Unix(exp, 0)) {
		return "", 0, time.Time{}, false
	}

	return parts[0], epoch, time.Unix(exp, 0).Add(-cSessionTTL), true
}

// session is the account a cookie signs in, provided the account's
// sessions haven't been ended since it was issued.
func (a *Accounts) session(token string, now time.Time) (accountID string, issued time.Time, ok bool) {
	id, epoch, issued, ok := a.verifySession(token, now)
	if !ok {
		return "", time.Time{}, false
	}
	current, found, err := a.dao.AccountSessionEpoch(id)
	if err != nil || !found || current != epoch {
		return "", time.Time{}, false
	}
	return id, issued, true
}

func (a *Accounts) setSession(w http.ResponseWriter, r *http.Request, accountID string) {
	now := time.Now()
	epoch, _, err := a.dao.AccountSessionEpoch(accountID)
	if err != nil {
		log.Error("error reading an account's session epoch:", err)
	}
	http.SetCookie(w, &http.Cookie{
		Name: cSessionCookie, Value: a.sessionToken(accountID, epoch, now), Path: "/",
		Expires: now.Add(cSessionTTL), HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
}

func (a *Accounts) clearSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cSessionCookie, Value: "", Path: "/", Expires: time.Unix(0, 0), HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
}

// AccountFromRequest is the signed-in account, if the request carries a
// live session cookie.
func (a *Accounts) AccountFromRequest(r *http.Request) (accountID string, ok bool) {
	c, err := r.Cookie(cSessionCookie)
	if err != nil {
		return "", false
	}

	id, _, ok := a.session(c.Value, time.Now())
	return id, ok
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
	// A device name is only ever registered for a proven email.
	acc, err := a.dao.GetAccount(accountID)
	if err != nil || acc == nil || !acc.EmailVerified {
		return "", ErrEmailNotVerified
	}
	// Issue #175: and only once the terms in force are accepted.
	if !TermsAccepted(acc) {
		return "", ErrTermsNotAccepted
	}
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
	if found && !a.Verified(id) {
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
	// The connecting address, never X-Forwarded-For: nothing sits in front
	// of the bridge, so that header is whatever the client wrote - a
	// fresh one per attempt used to escape the login limit.
	return clientaddr.Of(r)
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
		"email_verified":   acc.EmailVerified,
		"terms_accepted":   TermsAccepted(acc),
	}
}

// signedIn answers a successful sign-up or sign-in: the account, and -
// with ?for=setup, the wizard's way - a setup token to claim a name with.
func (a *Accounts) signedIn(w http.ResponseWriter, r *http.Request, acc *dao.Account, status int) {
	a.setSession(w, r, acc.ID)
	_ = a.dao.TouchAccount(acc.ID)
	out := map[string]any{"account": accountJSON(acc)}
	if r.URL.Query().Get("for") == "setup" {
		if !acc.EmailVerified {
			// No setup code until the email is proven. "error" is what a
			// wizard from an older image shows; a current one checks
			// verify_email and waits for the link instead.
			out["verify_email"] = true
			out["error"] = fmt.Sprintf("Confirm your email first: we sent a link to %s. Open it, then sign in here again to continue.", acc.Email)
			if status == http.StatusOK {
				// A sign-in, not the sign-up that just sent one: send the
				// link again in case the first one got lost.
				status = http.StatusForbidden
				if err := a.sendVerification(acc); err != nil {
					log.Error("could not send a verification email for", acc.ID, ":", err)
				}
			}
			writeJSON(w, status, out)
			return
		}
		tok, err := a.IssueSetupToken(acc.ID)
		if errors.Is(err, ErrTermsNotAccepted) {
			writeError(w, http.StatusForbidden, cTermsMessage)
			return
		}
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
	if a.signups != nil && !a.signups.Allow(clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many sign-ups from this address, try again later")
		return
	}
	var body struct {
		Email, Password, Name, Surname, Country string
		AcceptTerms                             bool `json:"accept_terms"`
	}
	if err := limits.DecodeJSON(w, r, &body, limits.MaxJSONBody); err != nil {
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
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), limits.BcryptCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not sign up right now")
		return
	}
	now := time.Now()
	acc := dao.Account{
		ID: uuid.New().String(), Email: email, Name: name, Surname: surname, Country: country,
		PasswordHash: string(hash), Created: now, LastSeen: now, FreeUntil: now.AddDate(FreeYears, 0, 0),
		TermsVersion: TermsVersion, TermsAcceptedAt: now,
	}
	if err := a.dao.CreateAccount(acc); err != nil {
		log.Error("error creating an account:", err)
		writeError(w, http.StatusConflict, "there is already an account with that email - sign in instead")
		return
	}
	// The account's id, never its email or the address it came from
	// (issue #162: personal data kept in logs with no retention).
	log.Info("account created:", acc.ID)
	if err := a.sendVerification(&acc); err != nil {
		log.Error("could not send the verification email for", acc.ID, ":", err)
	}
	a.signedIn(w, r, &acc, http.StatusCreated)
}

// dummyHash is compared against when the email has no password, at the
// same cost as a real one so the timing doesn't tell them apart.
var dummyHash = func() string {
	h, err := bcrypt.GenerateFromPassword([]byte("not a password"), limits.BcryptCost)
	if err != nil {
		panic(err)
	}
	return string(h)
}()

// cSignupsPerHour bounds account creation per address.
const cSignupsPerHour = 5

// cOAuthStartsPerMinute bounds Google/Apple sign-in starts per address: a
// person starts one per attempt, a NAT or an office a few more.
const cOAuthStartsPerMinute = 30

// Login checks an email and password. POST /api/account/login.
func (a *Accounts) Login(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	ip := clientIP(r)
	if !a.loginAllowed(ip, now) {
		writeError(w, http.StatusTooManyRequests, "too many failed attempts, try again later")
		return
	}
	var body struct{ Email, Password string }
	if err := limits.DecodeJSON(w, r, &body, limits.MaxJSONBody); err != nil {
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
	hash := dummyHash
	if acc != nil && acc.PasswordHash != "" {
		hash = acc.PasswordHash
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(body.Password)) != nil || acc == nil || acc.PasswordHash == "" {
		a.loginFailed(ip, now)
		// The same answer whether or not the email has an account, or
		// one without a password (issue #163).
		writeError(w, http.StatusUnauthorized, "wrong email or password - if you signed up with Google or Apple, use that instead (on the account page, then a setup code)")
		return
	}
	a.loginSucceeded(ip)
	// Older hashes move to the current cost while the password is at hand.
	if cost, err := bcrypt.Cost([]byte(acc.PasswordHash)); err == nil && cost < limits.BcryptCost {
		if h, err := bcrypt.GenerateFromPassword([]byte(body.Password), limits.BcryptCost); err == nil {
			if err := a.dao.SetAccountPassword(acc.ID, string(h)); err != nil {
				log.Error("error rehashing a password:", err)
			}
		}
	}
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
	var body struct {
		Name, Surname, Country string
		AcceptTerms            bool `json:"accept_terms"`
	}
	if err := limits.DecodeJSON(w, r, &body, limits.MaxJSONBody); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// A Google/Apple account accepts the terms here, completing its
	// profile (issue #175): nothing else is saved without it.
	current, err := a.dao.GetAccount(accountID)
	if err != nil || current == nil {
		writeError(w, http.StatusInternalServerError, "could not save right now")
		return
	}
	if !TermsAccepted(current) {
		if !body.AcceptTerms {
			writeError(w, http.StatusBadRequest, "please accept the terms of use")
			return
		}
		if err := a.dao.AcceptTerms(accountID, TermsVersion, time.Now()); err != nil {
			log.Error("error recording the terms acceptance:", err)
			writeError(w, http.StatusInternalServerError, "could not save right now")
			return
		}
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

// AcceptTerms records that the signed-in account accepts the terms of use
// in force - for accounts from before they were recorded, or after a new
// version. POST /api/account/accept-terms.
func (a *Accounts) AcceptTerms(w http.ResponseWriter, r *http.Request, accountID string) {
	if err := a.dao.AcceptTerms(accountID, TermsVersion, time.Now()); err != nil {
		log.Error("error recording the terms acceptance:", err)
		writeError(w, http.StatusInternalServerError, "could not save right now")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// SetPassword sets or changes the password of the signed-in account (a
// provider account gains one this way). PUT /api/account/password.
//
// Issue #164: a change needs the current password - a session cookie
// alone (a borrowed laptop, a stolen cookie) must not be enough to lock
// the owner out - and an account with none yet (Google/Apple) needs a
// sign-in from the last cFreshSignIn. Every other session ends; this one
// gets a new cookie and carries on.
func (a *Accounts) SetPassword(w http.ResponseWriter, r *http.Request, accountID string) {
	var body struct{ Password, Current string }
	if err := limits.DecodeJSON(w, r, &body, limits.MaxJSONBody); err != nil || len(body.Password) < cMinPassword {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("the password needs at least %d characters", cMinPassword))
		return
	}
	acc, err := a.dao.GetAccount(accountID)
	if err != nil || acc == nil {
		writeError(w, http.StatusInternalServerError, "could not save right now")
		return
	}
	now := time.Now()
	if acc.PasswordHash != "" {
		ip := clientIP(r)
		if !a.loginAllowed(ip, now) {
			writeError(w, http.StatusTooManyRequests, "too many attempts - try again in a few minutes")
			return
		}
		if bcrypt.CompareHashAndPassword([]byte(acc.PasswordHash), []byte(body.Current)) != nil {
			a.loginFailed(ip, now)
			writeError(w, http.StatusUnauthorized, "the current password is not right")
			return
		}
		a.loginSucceeded(ip)
	} else if c, err := r.Cookie(cSessionCookie); err != nil {
		writeError(w, http.StatusUnauthorized, "sign in again to set a password")
		return
	} else if _, issued, ok := a.session(c.Value, now); !ok || now.Sub(issued) > cFreshSignIn {
		writeError(w, http.StatusUnauthorized, "sign in again (with Google or Apple) to set a password")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), limits.BcryptCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not save right now")
		return
	}
	if err := a.dao.SetAccountPassword(accountID, string(hash)); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save right now")
		return
	}
	if _, err := a.dao.BumpAccountSessionEpoch(accountID); err != nil {
		log.Error("error ending an account's other sessions:", err)
	}
	a.setSession(w, r, accountID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ConfirmOwner checks, before something that can't be undone (deleting
// the account, issue #182), that whoever holds this session is the
// account's owner right now: its password, or for an account that signs
// in only with Google or Apple, a sign-in within cFreshSignIn. It answers
// the request itself when the check fails.
func (a *Accounts) ConfirmOwner(w http.ResponseWriter, r *http.Request, accountID, password string) bool {
	acc, err := a.dao.GetAccount(accountID)
	if err != nil || acc == nil {
		writeError(w, http.StatusInternalServerError, "could not check your account right now")
		return false
	}
	now := time.Now()
	if acc.PasswordHash != "" {
		ip := clientIP(r)
		if !a.loginAllowed(ip, now) {
			writeError(w, http.StatusTooManyRequests, "too many attempts - try again in a few minutes")
			return false
		}
		if bcrypt.CompareHashAndPassword([]byte(acc.PasswordHash), []byte(password)) != nil {
			a.loginFailed(ip, now)
			writeError(w, http.StatusUnauthorized, "the password is not right")
			return false
		}
		a.loginSucceeded(ip)
		return true
	}
	c, err := r.Cookie(cSessionCookie)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "sign in again (with Google or Apple), then delete the account within 15 minutes")
		return false
	}
	if _, issued, ok := a.session(c.Value, now); !ok || now.Sub(issued) > cFreshSignIn {
		writeError(w, http.StatusUnauthorized, "sign in again (with Google or Apple), then delete the account within 15 minutes")
		return false
	}
	return true
}

// EndSession drops this browser's session cookie (after the account is
// gone).
func (a *Accounts) EndSession(w http.ResponseWriter, r *http.Request) { a.clearSession(w, r) }

// Export answers everything kept for the account as a JSON download
// (issue #176, GDPR Art. 15/20). GET /api/account/export.
func (a *Accounts) Export(w http.ResponseWriter, r *http.Request, accountID string) {
	out, err := a.dao.ExportAccount(accountID)
	if err != nil || out == nil {
		writeError(w, http.StatusInternalServerError, "could not export your data right now")
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="off-the-cloud-account.json"`)
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

// LogoutEverywhere ends every session of the account, this one included
// (issue #164). POST /api/account/logout-everywhere.
func (a *Accounts) LogoutEverywhere(w http.ResponseWriter, r *http.Request, accountID string) {
	if _, err := a.dao.BumpAccountSessionEpoch(accountID); err != nil {
		writeError(w, http.StatusInternalServerError, "could not sign out right now")
		return
	}
	a.clearSession(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// SetupToken hands the signed-in account a setup code for the wizard.
// GET /api/account/setup-token.
func (a *Accounts) SetupToken(w http.ResponseWriter, r *http.Request, accountID string) {
	tok, err := a.IssueSetupToken(accountID)
	if errors.Is(err, ErrEmailNotVerified) {
		writeError(w, http.StatusForbidden, "confirm your email first - open the link we sent you, or ask for a new one above")
		return
	}
	if errors.Is(err, ErrTermsNotAccepted) {
		writeError(w, http.StatusForbidden, cTermsMessage)
		return
	}
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
	if err == nil && isAppReturn(u) {
		// The apps' own address (appsignin.go) - only ever with a PKCE
		// challenge, which OAuthStart adds and checks.
		if _, ok := appReturnWithChallenge(u.Query().Get("challenge")); ok || u.RawQuery == "" {
			return raw, true
		}
		return "", false
	}
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", false
	}
	// One trailing dot is the same name, fully qualified ("pit.otc."):
	// classify what is left, or "evil.com." passes as an unknown TLD.
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	// net.ParseIP only takes canonical addresses, which a browser reads the
	// same way: the address checked is the one it connects to.
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsPrivate() || ip.IsLoopback() {
			return raw, true
		}
		return "", false
	}
	if host == "otc" || host == "otc.local" || strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".home.arpa") || strings.HasSuffix(host, ".internal") {
		return raw, true
	}
	// A name under a top-level domain the internet doesn't have (a home
	// router's "pit.otc", "nas.lan", "box.home"): only a local resolver
	// answers for it, so no one else can be behind it - as safe as .local.
	// A private suffix in the public list (github.io) has a dot, and an
	// ICANN one is a real TLD: neither counts.
	if !plainLocalName(host) {
		return "", false
	}
	if suffix, icann := publicsuffix.PublicSuffix(host); !icann && !strings.Contains(suffix, ".") {
		return raw, true
	}

	return "", false
}

// plainLocalName is whether host is a plain ASCII name a browser takes as
// it is. Anything else may become a public host in the browser: Unicode
// that IDNA maps to one ("evil。com" is evil.com), or a number it reads as
// an IPv4 address ("1572395042", "0x7f.1", "010.0.0.5").
func plainLocalName(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	labels := strings.Split(host, ".")
	for _, l := range labels {
		if l == "" {
			return false
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
	}
	last := labels[len(labels)-1]
	if strings.HasPrefix(last, "0x") || strings.Trim(last, "0123456789") == "" {
		return false
	}
	return true
}

// ReturnAllowed answers whether a provider sign-in may come back to the
// given address (GET /api/account/return-allowed?return=...), so the
// device's web app shows the Google and Apple buttons only where they can
// work - with validReturnURL itself as the one rule. Public and
// cookie-less: it only says yes or no about an address.
func (a *Accounts) ReturnAllowed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_, ok := validReturnURL(r.URL.Query().Get("return"))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": ok && r.URL.Query().Get("return") != ""})
}

// ErrInvalidReturn is what a sign-in start with a bad return URL fails with.
var ErrInvalidReturn = errors.New("that return address is not allowed")

// ---------------------------------------------------------------------
// Email verification and password reset
// ---------------------------------------------------------------------

// ErrEmailNotVerified: the account hasn't proven its email, so it gets no
// setup code and can't register a device name.
var ErrEmailNotVerified = errors.New("the account's email is not verified")

// ErrTermsNotAccepted: the account hasn't accepted the terms of use in
// force (issue #175), so it gets no setup code.
var ErrTermsNotAccepted = errors.New("the account has not accepted the terms of use")

const cTermsMessage = "accept the terms of use first: sign in at off-the.cloud/account and accept them there"

const (
	cPurposeVerify   = "verify"
	cPurposeReset    = "reset"
	cVerifyTTL       = 48 * time.Hour
	cResetTTL        = time.Hour
	cEmailEvery      = 2 * time.Minute // one email per account and purpose
	cEmailsPerHour   = 10              // per address, all kinds
	cResendOnSetupIn = 10 * time.Minute
)

// SetMailer gives the account emails a way out (main, after [smtp]).
func (a *Accounts) SetMailer(m *mailer.Mailer) { a.mailer = m }

// Verified is whether accountID proved its email.
// HasAcceptedTerms: the account accepted the terms of use in force.
func (a *Accounts) HasAcceptedTerms(accountID string) bool {
	acc, err := a.dao.GetAccount(accountID)
	return err == nil && TermsAccepted(acc)
}

func (a *Accounts) Verified(accountID string) bool {
	acc, err := a.dao.GetAccount(accountID)
	return err == nil && acc != nil && acc.EmailVerified
}

func hashEmailToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return fmt.Sprintf("%x", sum[:])
}

// emailLink makes a link for purpose, stores its hash and mails it. The
// token rides in the URL's fragment, which a browser never sends to a
// server: the account page reads it and posts it back.
func (a *Accounts) emailLink(acc *dao.Account, purpose string, ttl time.Duration, subject, body func(link string) string) error {
	if a.mailer == nil {
		return mailer.ErrNotConfigured
	}
	if last, err := a.dao.LastEmailTokenSent(acc.ID, purpose); err == nil && time.Since(last) < cEmailEvery {
		return nil // one is on its way already
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	if err := a.dao.SaveEmailToken(hashEmailToken(token), acc.ID, purpose, ttl); err != nil {
		return err
	}
	link := fmt.Sprintf("https://%s/account#%s=%s", a.tld, purpose, token)
	return a.mailer.Send(acc.Email, subject(link), body(link))
}

func greeting(acc *dao.Account) string {
	if acc.Name != "" {
		return "Hello " + acc.Name + ","
	}
	return "Hello,"
}

func (a *Accounts) sendVerification(acc *dao.Account) error {
	return a.emailLink(acc, cPurposeVerify, cVerifyTTL,
		func(string) string { return "Confirm your email for Off The Cloud" },
		func(link string) string {
			return greeting(acc) + "\n\nPlease confirm that this is your email address by opening this link:\n\n" + link +
				"\n\nThe link works for 48 hours. Once your email is confirmed you can register your devices on the bridge." +
				"\n\nIf you didn't create an Off The Cloud account, ignore this email: nothing is registered for this address without the confirmation." +
				"\n\n- Off The Cloud\nhttps://" + a.tld + "\n"
		})
}

func (a *Accounts) sendReset(acc *dao.Account) error {
	return a.emailLink(acc, cPurposeReset, cResetTTL,
		func(string) string { return "Reset your Off The Cloud password" },
		func(link string) string {
			return greeting(acc) + "\n\nSomeone - hopefully you - asked to reset the password of the Off The Cloud account for this email. To choose a new one, open this link:\n\n" + link +
				"\n\nThe link works for one hour, once. Setting a new password signs your account out everywhere else." +
				"\n\nIf you didn't ask for this, ignore this email: your password stays as it is." +
				"\n\n- Off The Cloud\nhttps://" + a.tld + "\n"
		})
}

// Verify confirms an email with the link's token. POST /api/account/verify
// {token}. Public: the token is the proof.
func (a *Accounts) Verify(w http.ResponseWriter, r *http.Request) {
	var body struct{ Token string }
	if err := limits.DecodeJSON(w, r, &body, limits.MaxJSONBody); err != nil || body.Token == "" {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	id, ok, err := a.dao.ConsumeEmailToken(hashEmailToken(body.Token), cPurposeVerify)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not confirm right now")
		return
	}
	if !ok {
		writeError(w, http.StatusBadRequest, "that link is not valid any more - sign in and ask for a new one")
		return
	}
	if err := a.dao.SetEmailVerified(id); err != nil {
		writeError(w, http.StatusInternalServerError, "could not confirm right now")
		return
	}
	log.Info("email verified for account", id)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ResendVerification mails a new verification link to the signed-in
// account. POST /api/account/resend-verification.
func (a *Accounts) ResendVerification(w http.ResponseWriter, r *http.Request, accountID string) {
	if a.emailsPerAddr != nil && !a.emailsPerAddr.Allow(clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many emails asked for from here, try again later")
		return
	}
	acc, err := a.dao.GetAccount(accountID)
	if err != nil || acc == nil {
		writeError(w, http.StatusInternalServerError, "could not send it right now")
		return
	}
	if acc.EmailVerified {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if err := a.sendVerification(acc); err != nil {
		log.Error("could not send a verification email for", acc.ID, ":", err)
		writeError(w, http.StatusInternalServerError, "could not send the email right now")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Forgot mails a password-reset link. POST /api/account/forgot {email}.
// The answer is the same whether or not the email has an account.
func (a *Accounts) Forgot(w http.ResponseWriter, r *http.Request) {
	if a.emailsPerAddr != nil && !a.emailsPerAddr.Allow(clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many emails asked for from here, try again later")
		return
	}
	var body struct{ Email string }
	if err := limits.DecodeJSON(w, r, &body, limits.MaxJSONBody); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if email, ok := validEmail(body.Email); ok {
		if acc, err := a.dao.GetAccountByEmail(email); err == nil && acc != nil {
			if err := a.sendReset(acc); err != nil {
				log.Error("could not send a reset email for", acc.ID, ":", err)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Reset sets a new password with a reset link's token, which also proves
// the email; every other session ends. POST /api/account/reset {token,
// password}.
func (a *Accounts) Reset(w http.ResponseWriter, r *http.Request) {
	var body struct{ Token, Password string }
	if err := limits.DecodeJSON(w, r, &body, limits.MaxJSONBody); err != nil || body.Token == "" {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(body.Password) < cMinPassword {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("the password needs at least %d characters", cMinPassword))
		return
	}
	id, ok, err := a.dao.ConsumeEmailToken(hashEmailToken(body.Token), cPurposeReset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not reset it right now")
		return
	}
	if !ok {
		writeError(w, http.StatusBadRequest, "that link is not valid any more - ask for a new one")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), limits.BcryptCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not reset it right now")
		return
	}
	if err := a.dao.SetAccountPassword(id, string(hash)); err != nil {
		writeError(w, http.StatusInternalServerError, "could not reset it right now")
		return
	}
	_ = a.dao.SetEmailVerified(id)
	if _, err := a.dao.BumpAccountSessionEpoch(id); err != nil {
		log.Error("error ending an account's other sessions:", err)
	}
	a.setSession(w, r, id)
	log.Info("password reset for account", id)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
