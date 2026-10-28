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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/bridge/mailer"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
	"golang.org/x/text/language"
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
	// ExtraDomainEUR is the yearly price of each domain beyond MaxDomains,
	// arranged by writing to ContactEmail (the landing page, the account
	// page and README.md state it).
	ExtraDomainEUR = 2
	// FreeYears is how long a new account uses the bridge for free.
	FreeYears = 2
	// ContactEmail is where to ask for more domains.
	ContactEmail = "info@off-the.cloud"

	// Login throttling: this many failed passwords from one address in
	// the window lock that address out for the window.
	cLoginFailures = 8
	cLoginWindow   = 10 * time.Minute
	// cFailuresSweepAt: the failures map is swept of stale addresses once
	// it holds this many, then whenever it has doubled since (loginFailed).
	cFailuresSweepAt = 10000

	// cSetupCodeAlphabet has no 0/O/1/I, so a code read out loud or typed
	// from a screen has no lookalikes.
	cSetupCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	cSetupCodeLen      = 8
)

// NamePattern is a valid device name: one DNS label, lower case, no
// leading/trailing hyphen - the same shape scripts/install.sh accepts.
var NamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ReservedNames can never be device names: they are (or may one day be)
// the bridge's own hosts.
var ReservedNames = map[string]bool{
	"www": true, "api": true, "admin": true, "mail": true, "static": true,
	"downloads": true, "bridge": true, "ns1": true, "ns2": true,
}

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
	"extra_domain_eur":   ExtraDomainEUR,
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
	// failuresKept is the failures map's size after its last sweep.
	failuresKept int
	// signups limits account creation per address (issue #163): each is
	// a bcrypt, and each answer says whether an email has an account.
	signups *limits.Rate
	// oauthStarts limits sign-in starts per address: each one inserts an
	// oauth_states row.
	oauthStarts *limits.Rate
	jwks        jwksCache

	// deviceOnline reports whether a device is connected to the bridge
	// now, on any node (websocket.Manager.IsOnline): the inactivity job
	// never warns or removes an account while one of its devices is. Nil
	// until StartInactivityJob.
	deviceOnline func(domain string) bool
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
		a.limiterMu.Lock()
		a.pruneFailuresLocked(time.Now())
		a.limiterMu.Unlock()
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
// err is a database failure reading the account's session epoch: "try
// again", never "signed out" (issue #189) - a signed-in account told to
// sign in, or a claim made without its account.
func (a *Accounts) session(token string, now time.Time) (accountID string, issued time.Time, ok bool, err error) {
	id, epoch, issued, ok := a.verifySession(token, now)
	if !ok {
		return "", time.Time{}, false, nil
	}
	current, found, err := a.dao.AccountSessionEpoch(id)
	if err != nil {
		log.Error("error reading an account's session epoch:", err)
		return "", time.Time{}, false, err
	}
	if !found || current != epoch {
		return "", time.Time{}, false, nil
	}
	return id, issued, true, nil
}

// setSession issues a new session cookie. Only ever right after a real
// authentication (a password, a provider, a reset link): the cookie's age
// is what the cFreshSignIn checks in SetPassword and ConfirmOwner trust.
func (a *Accounts) setSession(w http.ResponseWriter, r *http.Request, accountID string) {
	epoch, _, err := a.dao.AccountSessionEpoch(accountID)
	if err != nil {
		log.Error("error reading an account's session epoch:", err)
	}
	a.setSessionAt(w, accountID, epoch)
}

// setSessionAt is setSession for a caller that already has the account's
// epoch (a password change, which moved it); the same rule applies.
func (a *Accounts) setSessionAt(w http.ResponseWriter, accountID string, epoch int) {
	now := time.Now()
	http.SetCookie(w, &http.Cookie{
		Name: cSessionCookie, Value: a.sessionToken(accountID, epoch, now), Path: "/",
		Expires: now.Add(cSessionTTL), HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
}

func (a *Accounts) clearSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cSessionCookie, Value: "", Path: "/", Expires: time.Unix(0, 0), HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
}

// AccountFromRequest is the signed-in account, if the request carries a
// live session cookie; err is a database failure checking it.
func (a *Accounts) AccountFromRequest(r *http.Request) (accountID string, ok bool, err error) {
	c, cerr := r.Cookie(cSessionCookie)
	if cerr != nil {
		return "", false, nil
	}

	id, _, ok, err := a.session(c.Value, time.Now())
	return id, ok, err
}

// RequireAuth wraps a handler so it only runs for a signed-in account.
func (a *Accounts) RequireAuth(next func(w http.ResponseWriter, r *http.Request, accountID string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok, err := a.AccountFromRequest(r)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "signin_check_unavailable", errorText(i18n.ApiErrorSigninCheckUnavailable()))
			return
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, "not_signed_in", errorText(i18n.ApiErrorNotSignedIn()))
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
	// A device name is only ever registered for a proven email. A database
	// error is not "unverified": that sends a verified user looking for an
	// email instead of trying again.
	acc, err := a.dao.GetAccount(accountID)
	if err != nil {
		return "", err
	}
	if acc == nil || !acc.EmailVerified {
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

// LookupSetupToken is the account a live setup token belongs to: ok is
// false for an unknown or expired code, or an account that hasn't proven
// its email; err is a database failure, worth "try again", not "sign in".
func (a *Accounts) LookupSetupToken(token string) (accountID string, ok bool, err error) {
	token = NormalizeSetupToken(token)
	if len(token) != cSetupCodeLen {
		return "", false, nil
	}
	id, found, err := a.dao.AccountForToken(token, cPurposeSetup)
	if err != nil {
		log.Error("error looking up a setup token:", err)
		return "", false, err
	}
	if !found {
		return "", false, nil
	}
	verified, err := a.verified(id)
	if err != nil {
		log.Error("error looking up a setup token's account:", err)
		return "", false, err
	}
	if !verified {
		return "", false, nil
	}

	return id, true, nil
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

// profileFieldCodes are the codes a profile_incomplete answer gives each
// field at fault (its "fields"): missing, or there but not usable (a name
// over cNameMax or with an angle bracket, a country code not in
// Countries). Like every code, never translated or renamed.
var profileFieldCodes = map[string]struct{ required, invalid string }{
	"name":    {"name_required", "name_invalid"},
	"surname": {"surname_required", "surname_invalid"},
	"country": {"country_required", "country_invalid"},
}

// profileProblems is what is wrong with a profile's name, surname and
// country, by field, each with its code (profileFieldCodes); empty when
// nothing is.
func profileProblems(name, surname, country string) map[string]string {
	out := map[string]string{}
	for field, v := range map[string]string{"name": name, "surname": surname, "country": country} {
		valid := false
		if field == "country" {
			_, valid = validCountry(v)
		} else {
			_, valid = validName(v)
		}
		if valid {
			continue
		}
		if strings.TrimSpace(v) == "" {
			out[field] = profileFieldCodes[field].required
		} else {
			out[field] = profileFieldCodes[field].invalid
		}
	}
	return out
}

// ---------------------------------------------------------------------
// Login throttling, per address, as the admin panel does - except that a
// sign-in that succeeds clears nothing here: failures only age out of the
// window. Sign-up is open, so clearing them on success let anyone sign in
// to an account of their own between guesses at someone else's password
// and never meet the lockout. A right password is never counted, so
// someone who mistyped and then got it right signs in again as often as
// they like; only more mistakes add up. (No one can make an admin
// account, so the panel still clears an address on success.)
// ---------------------------------------------------------------------

// loginAllowed is whether addr may try a password now, and if not, how
// long until it may (the 429's Retry-After).
func (a *Accounts) loginAllowed(addr string, now time.Time) (ok bool, retryAfter time.Duration) {
	a.limiterMu.Lock()
	defer a.limiterMu.Unlock()
	recent := a.failures[addr][:0]
	for _, t := range a.failures[addr] {
		if now.Sub(t) < cLoginWindow {
			recent = append(recent, t)
		}
	}
	// No entry for an address without recent failures: every caller
	// passes here, failing or not (a junk body, a database error).
	if len(recent) == 0 {
		delete(a.failures, addr)
	} else {
		a.failures[addr] = recent
	}
	if len(recent) < cLoginFailures {
		return true, 0
	}
	// Free again once fewer than cLoginFailures are in the window: when
	// the oldest of the newest cLoginFailures leaves it.
	return false, recent[len(recent)-cLoginFailures].Add(cLoginWindow).Sub(now)
}

// loginFailed counts a wrong password from addr.
//
// An address with failures in the window is never dropped to make room:
// that would lift its lockout, which is what a guesser spraying from
// many addresses wants (the map used to be emptied, every lockout with
// it, past 10,000). Only stale addresses are swept, once the map holds
// cFailuresSweepAt and then each time it has doubled since the last
// sweep, so a spray costs O(1) per failure, not a scan. Nor is there a
// cap on new addresses: past one, a new address would go uncounted - the
// limit off for everyone new, the mirror image of the lockout a capped
// limits.Rate put on them (a0ce30c). The map is bounded already: every
// failure follows a bcrypt, so one window holds at most what the node
// can hash in 10 minutes - thousands of addresses per core, a few
// hundred bytes each.
func (a *Accounts) loginFailed(addr string, now time.Time) {
	a.limiterMu.Lock()
	defer a.limiterMu.Unlock()
	if _, tracked := a.failures[addr]; !tracked && len(a.failures) >= max(cFailuresSweepAt, 2*a.failuresKept) {
		a.pruneFailuresLocked(now)
	}
	a.failures[addr] = append(a.failures[addr], now)
}

// pruneFailuresLocked drops the addresses whose failures are all older
// than the window, which loginAllowed would ignore anyway. The caller
// holds limiterMu.
func (a *Accounts) pruneFailuresLocked(now time.Time) {
	for addr, ts := range a.failures {
		// Appended in time order: the last one is the newest.
		if len(ts) == 0 || now.Sub(ts[len(ts)-1]) >= cLoginWindow {
			delete(a.failures, addr)
		}
	}
	a.failuresKept = len(a.failures)
}

// clientIP is the key the per-address limits here count a request
// against: the connecting address, never X-Forwarded-For (nothing sits in
// front of the bridge, so that header is whatever the client wrote - a
// fresh one per attempt used to escape the login limit), and an IPv6 one
// by its /64 (clientaddr.Bucket), which one host can rotate through.
func clientIP(r *http.Request) string {
	return clientaddr.BucketOf(r)
}

// ---------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError answers {"code", "error"}: code is a stable snake_case word
// the clients act on and show in their own words (the account page,
// later in the user's language - docs/i18n.md); error is the English
// text every client showed before codes, kept word for word for the ones
// that still show it. One code per reason: a refusal never gets a code
// that tells more than its text (a wrong password and an unknown email
// are both invalid_credentials). Codes are never translated or renamed.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"code": code, "error": msg})
}

// writeFieldsError is writeError for a request some of whose fields won't
// do: its code and English, what every client from before goes by (the
// setup wizard words profile_incomplete itself), and fields, each field at
// fault with a code of its own, for a client that marks them (the account
// page).
func writeFieldsError(w http.ResponseWriter, status int, code, msg string, fields map[string]string) {
	writeJSON(w, status, map[string]any{"code": code, "error": msg, "fields": fields})
}

// errorText is the "error" of an answer: its text in the catalog
// (api.error.*), in English for every request (docs/i18n.md) - the
// clients from before codes show it as it is, and the account page and
// the apps word an answer by its code, in the reader's language.
func errorText(m i18n.Msg) string { return m.Render("en") }

func accountJSON(acc *dao.Account) map[string]any {
	return map[string]any{
		"email": acc.Email, "name": acc.Name, "surname": acc.Surname, "country": acc.Country,
		"created": acc.Created, "free_until": acc.FreeUntil,
		"profile_complete": acc.Name != "" && acc.Surname != "" && acc.Country != "",
		"has_password":     acc.PasswordHash != "",
		"email_verified":   acc.EmailVerified,
		"terms_accepted":   TermsAccepted(acc),
		// The language its emails are written in, "" for not known.
		"lang": acc.Lang,
	}
}

// signedIn answers a successful sign-up or sign-in: a session, the
// account, and - with ?for=setup, the wizard's way - a setup token to
// claim a name with.
func (a *Accounts) signedIn(w http.ResponseWriter, r *http.Request, acc *dao.Account, status int) {
	a.setSession(w, r, acc.ID)
	a.answerAccount(w, r, acc, status)
}

// answerAccount is signedIn without a new session cookie, for a request
// the current session already authenticated.
func (a *Accounts) answerAccount(w http.ResponseWriter, r *http.Request, acc *dao.Account, status int) {
	_ = a.dao.TouchAccount(acc.ID)
	out := map[string]any{"account": accountJSON(acc)}
	if r.URL.Query().Get("for") == "setup" {
		if !acc.EmailVerified {
			// No setup code until the email is proven. "error" is what a
			// wizard from an older image shows; a current one checks
			// verify_email and waits for the link instead.
			out["verify_email"] = true
			out["code"] = "email_not_verified"
			out["error"] = errorText(i18n.ApiErrorEmailNotVerifiedSetup(acc.Email))
			if status == http.StatusOK {
				// A sign-in, not the sign-up that just sent one: send the
				// link again in case the first one got lost.
				status = http.StatusForbidden
				if err := a.sendVerification(acc, acc.Lang); err != nil {
					log.Error("could not send a verification email for", acc.ID, ":", err)
				}
			}
			writeJSON(w, status, out)
			return
		}
		tok, err := a.IssueSetupToken(acc.ID)
		if errors.Is(err, ErrTermsNotAccepted) {
			writeError(w, http.StatusForbidden, "terms_not_accepted", errorText(i18n.ApiErrorTermsNotAcceptedSetup()))
			return
		}
		if err != nil {
			log.Error("error issuing a setup token:", err)
			writeError(w, http.StatusInternalServerError, "setup_unavailable", errorText(i18n.ApiErrorSetupUnavailable()))
			return
		}
		out["setup_token"] = tok
	}
	writeJSON(w, status, out)
}

// Signup creates an email+password account. POST /api/account/signup
// {email, password, name, surname, country, accept_terms, lang}: lang,
// optional, is the language of the page signing up (requestLanguage).
func (a *Accounts) Signup(w http.ResponseWriter, r *http.Request) {
	if a.signups != nil {
		if ok, wait := a.signups.Take(clientIP(r)); !ok {
			limits.RetryAfter(w, wait)
			writeError(w, http.StatusTooManyRequests, "too_many_signups", errorText(i18n.ApiErrorTooManySignups()))
			return
		}
	}
	var body struct {
		Email, Password, Name, Surname, Country string
		AcceptTerms                             bool   `json:"accept_terms"`
		Lang                                    string `json:"lang"`
	}
	if err := limits.DecodeJSON(w, r, &body, limits.MaxJSONBody); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", errorText(i18n.ApiErrorInvalidBody()))
		return
	}
	email, ok := validEmail(body.Email)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_email", errorText(i18n.ApiErrorInvalidEmail()))
		return
	}
	if len(body.Password) < cMinPassword {
		writeError(w, http.StatusBadRequest, "password_too_short", errorText(i18n.ApiErrorPasswordTooShort(cMinPassword)))
		return
	}
	name, ok1 := validName(body.Name)
	surname, ok2 := validName(body.Surname)
	country, ok3 := validCountry(body.Country)
	if !ok1 || !ok2 || !ok3 {
		writeFieldsError(w, http.StatusBadRequest, "profile_incomplete", errorText(i18n.ApiErrorProfileIncomplete()), profileProblems(body.Name, body.Surname, body.Country))
		return
	}
	if !body.AcceptTerms {
		writeError(w, http.StatusBadRequest, "terms_required", errorText(i18n.ApiErrorTermsRequired()))
		return
	}
	if existing, err := a.dao.GetAccountByEmail(email); err != nil {
		log.Error("error checking an email at sign-up:", err)
		writeError(w, http.StatusInternalServerError, "signup_unavailable", errorText(i18n.ApiErrorSignupUnavailable()))
		return
	} else if existing != nil {
		writeError(w, http.StatusConflict, "email_taken", errorText(i18n.ApiErrorEmailTaken()))
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), limits.BcryptCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "signup_unavailable", errorText(i18n.ApiErrorSignupUnavailable()))
		return
	}
	now := time.Now()
	// The language of its emails. Kept apart for the first one: the
	// account can lose it (a database without the column yet).
	lang := requestLanguage(body.Lang, r)
	acc := dao.Account{
		ID: uuid.New().String(), Email: email, Name: name, Surname: surname, Country: country,
		PasswordHash: string(hash), Created: now, LastSeen: now, FreeUntil: now.AddDate(FreeYears, 0, 0),
		TermsVersion: TermsVersion, TermsAcceptedAt: now, Lang: lang,
	}
	if err := a.dao.CreateAccount(&acc); err != nil {
		log.Error("error creating an account:", err)
		writeError(w, http.StatusConflict, "email_taken", errorText(i18n.ApiErrorEmailTaken()))
		return
	}
	// The account's id, never its email or the address it came from
	// (issue #162: personal data kept in logs with no retention).
	log.Info("account created:", acc.ID)
	a.notifyNewAccount(acc.Email)
	if err := a.sendVerification(&acc, lang); err != nil {
		log.Error("could not send the verification email for", acc.ID, ":", err)
	}
	a.signedIn(w, r, &acc, http.StatusCreated)
}

// dummyHash is compared against when the email has no password, at the
// same cost as a real one so the timing doesn't tell them apart.
var dummyHash = func() string {
	h, err := bcrypt.GenerateFromPassword([]byte("not a password"), limits.BcryptCost) // i18n-ignore: a dummy bcrypt input, never shown
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
	if ok, wait := a.loginAllowed(ip, now); !ok {
		limits.RetryAfter(w, wait)
		writeError(w, http.StatusTooManyRequests, "too_many_attempts", errorText(i18n.ApiErrorTooManyAttempts()))
		return
	}
	var body struct{ Email, Password string }
	if err := limits.DecodeJSON(w, r, &body, limits.MaxJSONBody); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", errorText(i18n.ApiErrorInvalidBody()))
		return
	}
	email, _ := validEmail(body.Email)
	acc, err := a.dao.GetAccountByEmail(email)
	if err != nil {
		log.Error("error looking up an account:", err)
		writeError(w, http.StatusInternalServerError, "signin_unavailable", errorText(i18n.ApiErrorSigninUnavailable()))
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
		writeError(w, http.StatusUnauthorized, "invalid_credentials", errorText(i18n.ApiErrorInvalidCredentials()))
		return
	}
	// The address's failures stay, to age out (see "Login throttling").
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
	if err != nil {
		// "Try again" with the session kept (issue #189), as RequireAuth
		// answers when the session check itself fails.
		log.Error("error loading the signed-in account:", err)
		writeError(w, http.StatusInternalServerError, "signin_check_unavailable", errorText(i18n.ApiErrorSigninCheckUnavailable()))
		return
	}
	if acc == nil {
		a.clearSession(w, r)
		writeError(w, http.StatusUnauthorized, "not_signed_in", errorText(i18n.ApiErrorNotSignedIn()))
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
		writeError(w, http.StatusBadRequest, "invalid_body", errorText(i18n.ApiErrorInvalidBody()))
		return
	}
	// A Google/Apple account accepts the terms here, completing its
	// profile (issue #175): nothing else is saved without it.
	current, err := a.dao.GetAccount(accountID)
	if err != nil || current == nil {
		writeError(w, http.StatusInternalServerError, "save_unavailable", errorText(i18n.ApiErrorSaveUnavailable()))
		return
	}
	if !TermsAccepted(current) {
		if !body.AcceptTerms {
			writeError(w, http.StatusBadRequest, "terms_required", errorText(i18n.ApiErrorTermsRequiredProfile()))
			return
		}
		if err := a.dao.AcceptTerms(accountID, TermsVersion, time.Now()); err != nil {
			log.Error("error recording the terms acceptance:", err)
			writeError(w, http.StatusInternalServerError, "save_unavailable", errorText(i18n.ApiErrorSaveUnavailable()))
			return
		}
	}
	name, ok1 := validName(body.Name)
	surname, ok2 := validName(body.Surname)
	country, ok3 := validCountry(body.Country)
	if !ok1 || !ok2 || !ok3 {
		writeFieldsError(w, http.StatusBadRequest, "profile_incomplete", errorText(i18n.ApiErrorProfileIncomplete()), profileProblems(body.Name, body.Surname, body.Country))
		return
	}
	if err := a.dao.UpdateAccountProfile(accountID, name, surname, country); err != nil {
		log.Error("error updating a profile:", err)
		writeError(w, http.StatusInternalServerError, "save_unavailable", errorText(i18n.ApiErrorSaveUnavailable()))
		return
	}
	acc, err := a.dao.GetAccount(accountID)
	if err != nil || acc == nil {
		writeError(w, http.StatusInternalServerError, "save_unavailable", errorText(i18n.ApiErrorSaveUnavailable()))
		return
	}
	// No new cookie: saving a profile is no sign-in, and a fresh cookie
	// would make any old session pass the recent-sign-in checks.
	a.answerAccount(w, r, acc, http.StatusOK)
}

// AcceptTerms records that the signed-in account accepts the terms of use
// in force - for accounts from before they were recorded, or after a new
// version. POST /api/account/accept-terms.
func (a *Accounts) AcceptTerms(w http.ResponseWriter, r *http.Request, accountID string) {
	if err := a.dao.AcceptTerms(accountID, TermsVersion, time.Now()); err != nil {
		log.Error("error recording the terms acceptance:", err)
		writeError(w, http.StatusInternalServerError, "save_unavailable", errorText(i18n.ApiErrorSaveUnavailable()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// SetLanguage sets the language the signed-in account's emails are
// written in: PUT /api/account/language {"lang": "es"}, or "" for not
// known (English). Only a language this build carries; a regional tag
// ("es-ES") keeps its language. Answers {"ok": true, "lang": <stored>}.
// Only the owner, signed in, changes it (docs/i18n.md); never logged.
func (a *Accounts) SetLanguage(w http.ResponseWriter, r *http.Request, accountID string) {
	var body struct {
		Lang *string `json:"lang"`
	}
	if err := limits.DecodeJSON(w, r, &body, limits.MaxJSONBody); err != nil || body.Lang == nil {
		writeError(w, http.StatusBadRequest, "invalid_body", errorText(i18n.ApiErrorInvalidBody()))
		return
	}
	lang := i18n.Normalize(*body.Lang)
	if lang == "" && *body.Lang != "" {
		writeError(w, http.StatusBadRequest, "invalid_language", errorText(i18n.ApiErrorInvalidLanguage()))
		return
	}
	if err := a.dao.SetAccountLang(accountID, lang); err != nil {
		log.Error("error saving an account's language:", err)
		writeError(w, http.StatusInternalServerError, "save_unavailable", errorText(i18n.ApiErrorSaveUnavailable()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "lang": lang})
}

// requestLanguage is the language a new account's emails are written in
// (accounts.lang, docs/i18n.md): the signing-up page's own when it says
// (explicit, the sign-up's "lang"), else the first language of the
// browser's Accept-Language that this build carries. "" when neither
// names one: emails are then English, and the owner can pick one on the
// account page. Never a language this build doesn't carry.
func requestLanguage(explicit string, r *http.Request) string {
	if lang := i18n.Normalize(explicit); lang != "" {
		return lang
	}
	return acceptLanguage(r.Header.Get("Accept-Language"))
}

// cMaxAcceptLanguage bounds the header acceptLanguage parses, as
// i18n.Match bounds a preference.
const cMaxAcceptLanguage = 1024

// acceptLanguage is the language of an Accept-Language header that this
// build carries, or "". i18n.Match picks it (by base language: es-MX is
// es, pt-BR pt), but answers English when nothing matches; a browser that
// asks only for languages we don't carry yet has told us none, and gets
// "", not English, so that a later bridge carrying its language can tell.
func acceptLanguage(header string) string {
	if header == "" || len(header) > cMaxAcceptLanguage {
		return ""
	}
	tags, _, err := language.ParseAcceptLanguage(header)
	if err != nil {
		return ""
	}
	for _, t := range tags {
		base, _ := t.Base()
		if code := i18n.Match(t.String()); code == base.String() {
			return code
		}
	}
	return ""
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
		writeError(w, http.StatusBadRequest, "password_too_short", errorText(i18n.ApiErrorPasswordTooShort(cMinPassword)))
		return
	}
	acc, err := a.dao.GetAccount(accountID)
	if err != nil || acc == nil {
		writeError(w, http.StatusInternalServerError, "save_unavailable", errorText(i18n.ApiErrorSaveUnavailable()))
		return
	}
	now := time.Now()
	if acc.PasswordHash != "" {
		ip := clientIP(r)
		if ok, wait := a.loginAllowed(ip, now); !ok {
			limits.RetryAfter(w, wait)
			writeError(w, http.StatusTooManyRequests, "too_many_attempts", errorText(i18n.ApiErrorTooManyAttemptsMinutes()))
			return
		}
		if bcrypt.CompareHashAndPassword([]byte(acc.PasswordHash), []byte(body.Current)) != nil {
			a.loginFailed(ip, now)
			writeError(w, http.StatusUnauthorized, "wrong_password", errorText(i18n.ApiErrorWrongPasswordCurrent()))
			return
		}
	} else if c, err := r.Cookie(cSessionCookie); err != nil {
		writeError(w, http.StatusUnauthorized, "recent_signin_required", errorText(i18n.ApiErrorRecentSigninRequired()))
		return
	} else if _, issued, ok, err := a.session(c.Value, now); err != nil {
		writeError(w, http.StatusInternalServerError, "save_unavailable", errorText(i18n.ApiErrorSaveUnavailable()))
		return
	} else if !ok || now.Sub(issued) > cFreshSignIn {
		writeError(w, http.StatusUnauthorized, "recent_signin_required", errorText(i18n.ApiErrorRecentSigninRequiredProvider()))
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), limits.BcryptCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "save_unavailable", errorText(i18n.ApiErrorSaveUnavailable()))
		return
	}
	// One statement: never a new password with the old sessions still on.
	epoch, err := a.dao.SetAccountPasswordEndingSessions(accountID, string(hash))
	if err != nil {
		log.Error("error setting a password:", err)
		writeError(w, http.StatusInternalServerError, "save_unavailable", errorText(i18n.ApiErrorSaveUnavailable()))
		return
	}
	a.setSessionAt(w, accountID, epoch)
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
		writeError(w, http.StatusInternalServerError, "account_check_unavailable", errorText(i18n.ApiErrorAccountCheckUnavailable()))
		return false
	}
	now := time.Now()
	if acc.PasswordHash != "" {
		ip := clientIP(r)
		if ok, wait := a.loginAllowed(ip, now); !ok {
			limits.RetryAfter(w, wait)
			writeError(w, http.StatusTooManyRequests, "too_many_attempts", errorText(i18n.ApiErrorTooManyAttemptsMinutes()))
			return false
		}
		if bcrypt.CompareHashAndPassword([]byte(acc.PasswordHash), []byte(password)) != nil {
			a.loginFailed(ip, now)
			writeError(w, http.StatusUnauthorized, "wrong_password", errorText(i18n.ApiErrorWrongPassword()))
			return false
		}
		return true
	}
	c, err := r.Cookie(cSessionCookie)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "recent_signin_required", errorText(i18n.ApiErrorRecentSigninRequiredDelete()))
		return false
	}
	_, issued, ok, err := a.session(c.Value, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "signin_check_unavailable", errorText(i18n.ApiErrorSigninCheckUnavailable()))
		return false
	}
	if !ok || now.Sub(issued) > cFreshSignIn {
		writeError(w, http.StatusUnauthorized, "recent_signin_required", errorText(i18n.ApiErrorRecentSigninRequiredDelete()))
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
		writeError(w, http.StatusInternalServerError, "export_unavailable", errorText(i18n.ApiErrorExportUnavailable()))
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
		writeError(w, http.StatusInternalServerError, "signout_unavailable", errorText(i18n.ApiErrorSignoutUnavailable()))
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
		writeError(w, http.StatusForbidden, "email_not_verified", errorText(i18n.ApiErrorEmailNotVerified()))
		return
	}
	if errors.Is(err, ErrTermsNotAccepted) {
		writeError(w, http.StatusForbidden, "terms_not_accepted", errorText(i18n.ApiErrorTermsNotAcceptedSetup()))
		return
	}
	if err != nil {
		log.Error("error issuing a setup token:", err)
		writeError(w, http.StatusInternalServerError, "setup_code_unavailable", errorText(i18n.ApiErrorSetupCodeUnavailable()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"setup_token": tok, "expires_in_seconds": int(cSetupTokenTTL.Seconds())})
}

// SetupTokenInfo tells a wizard whose code it was handed. GET
// /api/account/setup-token-info?token=. Public: the code is the secret.
func (a *Accounts) SetupTokenInfo(w http.ResponseWriter, r *http.Request) {
	id, ok, err := a.LookupSetupToken(r.URL.Query().Get("token"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "setup_code_check_unavailable", errorText(i18n.ApiErrorSetupCodeCheckUnavailable()))
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "invalid_setup_code", errorText(i18n.ApiErrorInvalidSetupCode()))
		return
	}
	acc, err := a.dao.GetAccount(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "setup_code_check_unavailable", errorText(i18n.ApiErrorSetupCodeCheckUnavailable()))
		return
	}
	if acc == nil {
		writeError(w, http.StatusNotFound, "invalid_setup_code", errorText(i18n.ApiErrorInvalidSetupCode()))
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

// ---------------------------------------------------------------------
// Email verification and password reset
// ---------------------------------------------------------------------

// ErrEmailNotVerified: the account hasn't proven its email, so it gets no
// setup code and can't register a device name.
var ErrEmailNotVerified = errors.New("the account's email is not verified") // i18n-ignore: compared with errors.Is, never sent

// ErrTermsNotAccepted: the account hasn't accepted the terms of use in
// force (issue #175), so it gets no setup code.
var ErrTermsNotAccepted = errors.New("the account has not accepted the terms of use") // i18n-ignore: compared with errors.Is, never sent

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

// notifyNewAccount tells the project that an account was created, at its
// own address (info@off-the.cloud, the mailer's): the new account's email
// and nothing else. In the background, so a slow SMTP server never holds
// up a sign-up; a failure is only logged, by account-free text (#162).
func (a *Accounts) notifyNewAccount(email string) {
	m := a.mailer
	if m == nil || m.Self() == "" {
		return
	}
	go func() {
		if err := m.Send(m.Self(), "New account", email); err != nil { // i18n-ignore: an email to the project itself, read by its operators

			log.Error("could not send the new-account email:", err)
		}
	}()
}

func (a *Accounts) verified(accountID string) (bool, error) {
	acc, err := a.dao.GetAccount(accountID)
	if err != nil {
		return false, err
	}
	return acc != nil && acc.EmailVerified, nil
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
	return a.sendEmailLink(a.mailer.Send, acc, purpose, ttl, subject, body)
}

// sendEmailLink is emailLink with the sending passed in. The new link is
// stored, sent, and only then replaces the older ones: when the send
// fails, the link already in the inbox keeps working, and the throttle
// doesn't answer the next "send it again" with an email that never left.
func (a *Accounts) sendEmailLink(send func(to, subject, body string) error, acc *dao.Account, purpose string, ttl time.Duration, subject, body func(link string) string) error {
	if last, err := a.dao.LastEmailTokenSent(acc.ID, purpose); err == nil && time.Since(last) < cEmailEvery {
		return nil // one is on its way already
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	h := hashEmailToken(token)
	created, err := a.dao.AddEmailToken(h, acc.ID, purpose, ttl)
	if err != nil {
		return err
	}
	link := fmt.Sprintf("https://%s/account#%s=%s", a.tld, purpose, token)
	if err := send(acc.Email, subject(link), body(link)); err != nil {
		if derr := a.dao.DropEmailToken(h); derr != nil {
			log.Error("could not drop an unsent email link for", acc.ID, ":", derr)
		}
		return err
	}
	// Only logged: the email is out, and the older links expire anyway.
	if err := a.dao.KeepNewestEmailToken(h, acc.ID, purpose, created); err != nil {
		log.Error("could not retire the older email links for", acc.ID, ":", err)
	}
	return nil
}

// sendVerification mails acc a link that confirms its email, in lang:
// the account's language, or at sign-up the one chosen for it.
func (a *Accounts) sendVerification(acc *dao.Account, lang string) error {
	return a.emailLink(acc, cPurposeVerify, cVerifyTTL,
		func(string) string { return verificationSubject(lang) },
		func(link string) string { return a.verificationBody(lang, acc.Name, link) })
}

// sendReset mails acc a password-reset link, in the account's language -
// never that of whoever asked for it (any address can be asked for).
func (a *Accounts) sendReset(acc *dao.Account) error {
	return a.emailLink(acc, cPurposeReset, cResetTTL,
		func(string) string { return resetSubject(acc.Lang) },
		func(link string) string { return a.resetBody(acc.Lang, acc.Name, link) })
}

// Verify confirms an email with the link's token. POST /api/account/verify
// {token}. Public: the token is the proof.
func (a *Accounts) Verify(w http.ResponseWriter, r *http.Request) {
	var body struct{ Token string }
	if err := limits.DecodeJSON(w, r, &body, limits.MaxJSONBody); err != nil || body.Token == "" {
		writeError(w, http.StatusBadRequest, "invalid_body", errorText(i18n.ApiErrorInvalidBody()))
		return
	}
	id, ok, err := a.dao.ConsumeEmailToken(hashEmailToken(body.Token), cPurposeVerify)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "verify_unavailable", errorText(i18n.ApiErrorVerifyUnavailable()))
		return
	}
	if !ok {
		writeError(w, http.StatusBadRequest, "verify_link_invalid", errorText(i18n.ApiErrorVerifyLinkInvalid()))
		return
	}
	if err := a.dao.SetEmailVerified(id); err != nil {
		writeError(w, http.StatusInternalServerError, "verify_unavailable", errorText(i18n.ApiErrorVerifyUnavailable()))
		return
	}
	log.Info("email verified for account", id)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// emailAllowed applies emailsPerAddr to a "send me an email" request,
// answering it with a 429 and its Retry-After when the address has asked
// for too many.
func (a *Accounts) emailAllowed(w http.ResponseWriter, r *http.Request) bool {
	if a.emailsPerAddr == nil {
		return true
	}
	ok, wait := a.emailsPerAddr.Take(clientIP(r))
	if !ok {
		limits.RetryAfter(w, wait)
		writeError(w, http.StatusTooManyRequests, "too_many_emails", errorText(i18n.ApiErrorTooManyEmails()))
	}
	return ok
}

// ResendVerification mails a new verification link to the signed-in
// account. POST /api/account/resend-verification.
func (a *Accounts) ResendVerification(w http.ResponseWriter, r *http.Request, accountID string) {
	if !a.emailAllowed(w, r) {
		return
	}
	acc, err := a.dao.GetAccount(accountID)
	if err != nil || acc == nil {
		writeError(w, http.StatusInternalServerError, "email_send_unavailable", errorText(i18n.ApiErrorEmailSendUnavailable()))
		return
	}
	if acc.EmailVerified {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if err := a.sendVerification(acc, acc.Lang); err != nil {
		log.Error("could not send a verification email for", acc.ID, ":", err)
		writeError(w, http.StatusInternalServerError, "email_send_unavailable", errorText(i18n.ApiErrorEmailSendUnavailableEmail()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Forgot mails a password-reset link. POST /api/account/forgot {email}.
// The answer is the same whether or not the email has an account.
func (a *Accounts) Forgot(w http.ResponseWriter, r *http.Request) {
	if !a.emailAllowed(w, r) {
		return
	}
	var body struct{ Email string }
	if err := limits.DecodeJSON(w, r, &body, limits.MaxJSONBody); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", errorText(i18n.ApiErrorInvalidBody()))
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
		writeError(w, http.StatusBadRequest, "invalid_body", errorText(i18n.ApiErrorInvalidBody()))
		return
	}
	if len(body.Password) < cMinPassword {
		writeError(w, http.StatusBadRequest, "password_too_short", errorText(i18n.ApiErrorPasswordTooShort(cMinPassword)))
		return
	}
	id, ok, err := a.dao.ConsumeEmailToken(hashEmailToken(body.Token), cPurposeReset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "reset_unavailable", errorText(i18n.ApiErrorResetUnavailable()))
		return
	}
	if !ok {
		writeError(w, http.StatusBadRequest, "reset_link_invalid", errorText(i18n.ApiErrorResetLinkInvalid()))
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), limits.BcryptCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "reset_unavailable", errorText(i18n.ApiErrorResetUnavailable()))
		return
	}
	// One statement: a reset (often for a stolen session) never leaves
	// the old sessions on.
	epoch, err := a.dao.SetAccountPasswordEndingSessions(id, string(hash))
	if err != nil {
		log.Error("error resetting a password:", err)
		writeError(w, http.StatusInternalServerError, "reset_unavailable", errorText(i18n.ApiErrorResetUnavailable()))
		return
	}
	_ = a.dao.SetEmailVerified(id)
	a.setSessionAt(w, id, epoch)
	log.Info("password reset for account", id)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
