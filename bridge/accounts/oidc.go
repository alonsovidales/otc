// SPDX-License-Identifier: AGPL-3.0-or-later

package accounts

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
)

// Sign in with Google and with Apple, both as OpenID Connect
// authorization-code flows: the browser goes to the provider, comes back
// to /account/auth/<provider>/callback with a code, the bridge exchanges
// it for an id_token and checks that token's signature against the
// provider's published keys. No SDKs: a handful of URLs and one JWT.

type provider struct {
	name         string
	authURL      string
	tokenURL     string
	jwksURL      string
	issuers      []string
	scope        string
	clientID     string
	clientSecret string // Google: the secret; Apple: made per request, see appleClientSecret
	// Apple's "client secret" is a JWT signed with the developer's key.
	appleTeamID string
	appleKeyID  string
	appleKey    *ecdsa.PrivateKey
	// Apple answers with a form POST (response_mode=form_post) because the
	// name scope requires it; Google with a plain GET redirect.
	formPost bool
}

func (a *Accounts) loadProviders() {
	// A bridge without an [accounts] section runs with accounts on (no
	// open registration) and no providers - the section is optional.
	if !cfg.HasSection("accounts") {
		return
	}
	if id := cfg.GetStr("accounts", "google-client-id"); id != "" {
		a.providers["google"] = &provider{
			name: "google", authURL: "https://accounts.google.com/o/oauth2/v2/auth", tokenURL: "https://oauth2.googleapis.com/token",
			jwksURL: "https://www.googleapis.com/oauth2/v3/certs", issuers: []string{"https://accounts.google.com", "accounts.google.com"},
			scope: "openid email profile", clientID: id, clientSecret: cfg.GetStr("accounts", "google-client-secret"),
		}
		log.Info("sign in with Google configured")
	}
	if id := cfg.GetStr("accounts", "apple-client-id"); id != "" {
		p := &provider{
			name: "apple", authURL: "https://appleid.apple.com/auth/authorize", tokenURL: "https://appleid.apple.com/auth/token",
			jwksURL: "https://appleid.apple.com/auth/keys", issuers: []string{"https://appleid.apple.com"},
			scope: "name email", clientID: id, appleTeamID: cfg.GetStr("accounts", "apple-team-id"), appleKeyID: cfg.GetStr("accounts", "apple-key-id"),
			formPost: true,
		}
		pem, err := os.ReadFile(cfg.GetStr("accounts", "apple-private-key"))
		if err != nil {
			log.Error("sign in with Apple not configured: cannot read apple-private-key:", err)
			return
		}
		p.appleKey, err = jwt.ParseECPrivateKeyFromPEM(pem)
		if err != nil {
			log.Error("sign in with Apple not configured: apple-private-key is not an EC key:", err)
			return
		}
		a.providers["apple"] = p
		log.Info("sign in with Apple configured")
	}
}

func (a *Accounts) redirectURI(p *provider) string {
	return "https://" + a.tldHost() + "/account/auth/" + p.name + "/callback"
}

// tldHost is the bridge's own host for absolute URLs (no port: production
// runs on 443).
func (a *Accounts) tldHost() string { return a.tld }

// appleClientSecret is the JWT Apple takes as the client secret: signed
// with the developer's key, five minutes of life is plenty for one
// exchange.
func (p *provider) appleClientSecret() (string, error) {
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": p.appleTeamID, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"aud": "https://appleid.apple.com", "sub": p.clientID,
	})
	tok.Header["kid"] = p.appleKeyID

	return tok.SignedString(p.appleKey)
}

// OAuthStart sends the browser to the provider. GET
// /account/auth/{provider}/start?return=<wizard url, optional>.
func (a *Accounts) OAuthStart(w http.ResponseWriter, r *http.Request) {
	p, ok := a.providers[r.PathValue("provider")]
	if !ok {
		http.Error(w, "that sign-in provider is not configured on this bridge", http.StatusNotFound)
		return
	}
	returnURL, ok := validReturnURL(r.URL.Query().Get("return"))
	if !ok {
		http.Error(w, ErrInvalidReturn.Error(), http.StatusBadRequest)
		return
	}
	// An app's sign-in (appsignin.go): its address, always with a PKCE
	// challenge.
	if u, err := url.Parse(returnURL); err == nil && isAppReturn(u) {
		if returnURL, ok = appReturnWithChallenge(r.URL.Query().Get("challenge")); !ok {
			http.Error(w, "an app sign-in needs a valid challenge", http.StatusBadRequest)
			return
		}
	}
	if !pageNavigation(r) {
		http.Error(w, "a sign-in starts from a link, not from within another page", http.StatusBadRequest)
		return
	}
	// Counted only for a start that writes a state: a bad provider or
	// return address costs nothing.
	if a.oauthStarts != nil && !a.oauthStarts.Allow(clientIP(r)) {
		http.Error(w, "too many sign-in attempts from this address, try again in a minute", http.StatusTooManyRequests)
		return
	}
	buf := make([]byte, cOAuthStateBytes)
	if _, err := rand.Read(buf); err != nil {
		http.Error(w, "could not start the sign-in", http.StatusInternalServerError)
		return
	}
	state := hex.EncodeToString(buf)
	if err := a.dao.SaveOAuthState(state, returnURL); err != nil {
		log.Error("error saving an oauth state:", err)
		http.Error(w, "could not start the sign-in", http.StatusInternalServerError)
		return
	}
	setOAuthCookies(w, state, int(cOAuthStateTTL.Seconds()))
	q := url.Values{
		"client_id": {p.clientID}, "redirect_uri": {a.redirectURI(p)}, "response_type": {"code"},
		"scope": {p.scope}, "state": {state},
	}
	if p.formPost {
		q.Set("response_mode", "form_post")
	}
	http.Redirect(w, r, p.authURL+"?"+q.Encode(), http.StatusFound)
}

// A sign-in's state also goes in a cookie, and the callback must come back
// with both: otherwise anyone could start a sign-in to their own account,
// stop at the callback URL and send it to someone else, whose browser
// would finish it - signed in to the attacker's account.
const (
	// __Host-: a device's page is a same-site subdomain and could otherwise
	// set one carrying the attacker's own state.
	cOAuthCookie = "__Host-otc_oauth_"
	// cOAuthStateTTL is as long as ConsumeOAuthState takes a state.
	cOAuthStateTTL = 15 * time.Minute
	// cOAuthStateBytes is a state's randomness, 64 hex characters: that
	// length is what tells it from one made before the cookie, which had
	// 24 bytes (isPreCookieState).
	cOAuthStateBytes   = 32
	cPreCookieStateLen = 48
)

// pageNavigation is whether the browser is opening r as a page, as every
// real sign-in start is: a link, a location change, an app's sign-in
// sheet or Custom Tab. Not an image, frame or fetch that another site's
// page could fire at the start over and over: each start sets a state
// cookie of its own name, and a browser that takes cookies from such
// requests would fill up and drop the bridge's others, the sessions among
// them. Browsers that send no Sec-Fetch headers (older Safari) go through
// as before.
func pageNavigation(r *http.Request) bool {
	if m := r.Header.Get("Sec-Fetch-Mode"); m != "" && m != "navigate" {
		return false
	}
	if d := r.Header.Get("Sec-Fetch-Dest"); d != "" && d != "document" {
		return false
	}
	return true
}

// oauthCookieNames are the two cookies that carry a state; the names carry
// part of it, so two sign-ins at once (two tabs, an app's sheet and the
// browser) keep their own.
func oauthCookieNames(state string) (string, string) {
	k := state[:16]
	return cOAuthCookie + k, cOAuthCookie + k + "_l"
}

// isOAuthState is whether s has the shape of a state OAuthStart makes.
func isOAuthState(s string) bool {
	_, err := hex.DecodeString(s)
	return len(s) == 2*cOAuthStateBytes && err == nil
}

// isPreCookieState is whether s has the shape of a state made by a release
// from before the state cookie, which set none. The cluster is deployed one
// node at a time, so a sign-in can start on a node still on that release
// and come back to one on this (or start before a restart and finish
// after): such a state goes through without a cookie, as it did there.
// Only those releases store that shape, so once no node runs one, the last
// of them is gone within cOAuthStateTTL and every sign-in needs its cookie.
func isPreCookieState(s string) bool {
	_, err := hex.DecodeString(s)
	return len(s) == cPreCookieStateLen && err == nil
}

// setOAuthCookies sets (maxAge > 0) or clears (< 0) a state's cookies. One
// is SameSite=None: Apple comes back with a cross-site POST, which a Lax
// cookie misses. The other has no SameSite at all, for Safari 12, which
// takes None for Strict and would send neither back.
func setOAuthCookies(w http.ResponseWriter, state string, maxAge int) {
	name, legacy := oauthCookieNames(state)
	value := state
	if maxAge < 0 {
		value = ""
	}
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: true, SameSite: http.SameSiteNoneMode})
	http.SetCookie(w, &http.Cookie{Name: legacy, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: true})
}

// oauthStartedHere is whether this browser started the sign-in with state.
func oauthStartedHere(r *http.Request, state string) bool {
	if !isOAuthState(state) {
		return false
	}
	name, legacy := oauthCookieNames(state)
	for _, n := range []string{name, legacy} {
		if c, err := r.Cookie(n); err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) == 1 {
			return true
		}
	}
	return false
}

// OAuthCallback finishes a sign-in: exchanges the code, verifies the
// id_token, finds or creates the account, sets the session, and sends the
// browser on: to the wizard with a setup token when the sign-in started
// there, to /account otherwise (or to complete the profile first).
func (a *Accounts) OAuthCallback(w http.ResponseWriter, r *http.Request) {
	p, ok := a.providers[r.PathValue("provider")]
	if !ok {
		http.Error(w, "that sign-in provider is not configured on this bridge", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad callback", http.StatusBadRequest)
		return
	}
	state, code := r.Form.Get("state"), r.Form.Get("code")
	// Whatever the outcome, this sign-in's cookies are done with.
	if isOAuthState(state) {
		setOAuthCookies(w, state, -1)
	}
	if errCode := r.Form.Get("error"); errCode != "" || code == "" {
		http.Redirect(w, r, "/account?error="+url.QueryEscape("the sign-in was cancelled"), http.StatusFound)
		return
	}
	// Checked before the state is consumed: a planted link can neither use
	// it nor burn it. A state from a node on an older release never had a
	// cookie (isPreCookieState).
	startedHere := oauthStartedHere(r, state)
	if !startedHere && !isPreCookieState(state) {
		http.Redirect(w, r, "/account?error="+url.QueryEscape("that sign-in has expired, please try again"), http.StatusFound)
		return
	}
	returnURL, found, err := a.dao.ConsumeOAuthState(state)
	if err != nil || !found {
		http.Redirect(w, r, "/account?error="+url.QueryEscape("that sign-in has expired, please try again"), http.StatusFound)
		return
	}
	if !startedHere {
		log.Info("a sign-in started on a node without the state cookie was let through")
	}

	claims, err := a.exchange(p, code)
	if err != nil {
		log.Error("sign in with", p.name, "failed:", err)
		http.Redirect(w, r, "/account?error="+url.QueryEscape("the sign-in could not be completed"), http.StatusFound)
		return
	}
	subject, _ := claims["sub"].(string)
	email, _ := claims["email"].(string)
	email, emailOK := validEmail(email)
	// Linking goes by email, so it must be one the provider verified:
	// Google says so in email_verified (Apple's are always verified, and
	// it sends the claim as a string).
	if v, present := claims["email_verified"]; present && v != true && v != "true" {
		emailOK = false
	}
	if subject == "" || !emailOK {
		http.Redirect(w, r, "/account?error="+url.QueryEscape("the sign-in did not include an email address"), http.StatusFound)
		return
	}
	given, _ := claims["given_name"].(string)
	family, _ := claims["family_name"].(string)
	// Apple only ever sends the name once, as a JSON "user" form field on
	// the first sign-in.
	if p.formPost {
		var user struct {
			Name struct{ FirstName, LastName string }
		}
		if raw := r.Form.Get("user"); raw != "" && json.Unmarshal([]byte(raw), &user) == nil {
			given, family = user.Name.FirstName, user.Name.LastName
		}
	}

	acc, err := a.dao.GetAccountByLogin(p.name, subject)
	if err != nil {
		http.Error(w, "could not sign in right now", http.StatusInternalServerError)
		return
	}
	if acc == nil {
		// The same email signed up with a password (or the other
		// provider) earlier: same person, link rather than duplicate.
		acc, err = a.dao.GetAccountByEmail(email)
		if err != nil {
			http.Error(w, "could not sign in right now", http.StatusInternalServerError)
			return
		}
		if acc != nil && acc.PasswordHash != "" {
			// A password account for this email: whoever signed up with
			// it may not be the person now proving they own the address.
			// The provider sign-in wins - the password is cleared (a new
			// one can be set from the account page).
			// Not linked unless cleared: the old password would go on
			// working on the linked account. Nothing is changed yet, so
			// the user can simply try again.
			if err := a.dao.SetAccountPassword(acc.ID, ""); err != nil {
				log.Error("could not clear the password of an account being linked:", err)
				http.Error(w, "could not sign in right now", http.StatusInternalServerError)
				return
			}
			acc.PasswordHash = ""
		}
		if acc == nil {
			now := time.Now()
			// Google and Apple verify the email themselves.
			created := dao.Account{ID: uuid.New().String(), Email: email, Name: strings.TrimSpace(given), Surname: strings.TrimSpace(family), Created: now, LastSeen: now, FreeUntil: now.AddDate(FreeYears, 0, 0), EmailVerified: true}
			if err := a.dao.CreateAccount(created); err != nil {
				log.Error("error creating an account from", p.name, ":", err)
				http.Error(w, "could not sign in right now", http.StatusInternalServerError)
				return
			}
			acc = &created
			log.Info("account created with", p.name, ":", created.ID) // not the email (issue #162)
			a.notifyNewAccount(created.Email)
		}
		if err := a.dao.LinkAccountLogin(p.name, subject, acc.ID); err != nil {
			log.Error("error linking a", p.name, "login:", err)
		}
		// The provider proved the address: an email sign-up still waiting
		// for its link is verified now.
		if !acc.EmailVerified {
			if err := a.dao.SetEmailVerified(acc.ID); err != nil {
				log.Error("could not mark an email verified:", err)
			}
			acc.EmailVerified = true
			// Whoever signed up with this unproven address may still hold
			// a session: end it, as Reset does. The provider user gets the
			// fresh cookie set just below.
			if _, err := a.dao.BumpAccountSessionEpoch(acc.ID); err != nil {
				log.Error("error ending an account's other sessions:", err)
			}
		}
	}
	a.setSession(w, r, acc.ID)
	_ = a.dao.TouchAccount(acc.ID)

	// Issue #175: a new Google/Apple account accepts the terms on the
	// same page where it completes its profile.
	complete := acc.Name != "" && acc.Surname != "" && acc.Country != "" && TermsAccepted(acc)
	if !complete {
		q := url.Values{"complete": {"1"}}
		if returnURL != "" {
			q.Set("return", returnURL)
		}
		http.Redirect(w, r, "/account?"+q.Encode(), http.StatusFound)
		return
	}
	http.Redirect(w, r, a.afterSignIn(acc.ID, returnURL), http.StatusFound)
}

// afterSignIn is where a finished sign-in lands: back in the wizard with a
// setup token when it started there, else the account page.
func (a *Accounts) afterSignIn(accountID, returnURL string) string {
	if returnURL == "" {
		return "/account"
	}
	if u, err := url.Parse(returnURL); err == nil && isAppReturn(u) {
		return a.appRedirect(accountID, returnURL)
	}
	tok, err := a.IssueSetupToken(accountID)
	if err != nil {
		log.Error("error issuing a setup token after sign-in:", err)
		return "/account"
	}
	sep := "?"
	if strings.Contains(returnURL, "?") {
		sep = "&"
	}

	return returnURL + sep + "setup_token=" + url.QueryEscape(tok)
}

// ContinueSetup sends a signed-in browser back to the wizard with a fresh
// setup token: the last step of "complete your profile" after a provider
// sign-in that started in the wizard. GET /api/account/continue?return=.
//
// Only the account page itself may send the browser here: the Lax cookie
// rides on any site's link, and that site would pick where the token goes
// (an address on the same LAN) or the app flow's PKCE challenge.
func (a *Accounts) ContinueSetup(w http.ResponseWriter, r *http.Request, accountID string) {
	if !a.sameOriginNavigation(r) {
		log.Info("refused a cross-site /api/account/continue")
		http.Error(w, "open this from your account page", http.StatusForbidden)
		return
	}
	returnURL, ok := validReturnURL(r.URL.Query().Get("return"))
	if !ok || returnURL == "" {
		http.Error(w, ErrInvalidReturn.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, a.afterSignIn(accountID, returnURL), http.StatusFound)
}

// sameOriginNavigation is whether a request was started by one of the
// bridge's own pages: Sec-Fetch-Site where the browser sends it, else the
// Referer (older Safari).
func (a *Accounts) sameOriginNavigation(r *http.Request) bool {
	if s := r.Header.Get("Sec-Fetch-Site"); s != "" {
		return s == "same-origin"
	}
	ref, err := url.Parse(r.Header.Get("Referer"))
	return err == nil && ref.Scheme == "https" && strings.EqualFold(ref.Host, a.tldHost())
}

// exchange turns the code into verified id_token claims.
func (a *Accounts) exchange(p *provider, code string) (jwt.MapClaims, error) {
	secret := p.clientSecret
	if p.appleKey != nil {
		var err error
		if secret, err = p.appleClientSecret(); err != nil {
			return nil, err
		}
	}
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {a.redirectURI(p)},
		"client_id": {p.clientID}, "client_secret": {secret},
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.PostForm(p.tokenURL, form)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint answered %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var tok struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || tok.IDToken == "" {
		return nil, errors.New("no id_token in the token response")
	}

	return a.verifyIDToken(p, tok.IDToken)
}

// verifyIDToken checks the signature against the provider's JWKS (fetched
// and cached, refetched once on an unknown key id), the issuer, the
// audience and the expiry.
func (a *Accounts) verifyIDToken(p *provider, raw string) (jwt.MapClaims, error) {
	claims := jwt.MapClaims{}
	keyfunc := func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		key, err := a.jwks.key(p.jwksURL, kid)
		if err != nil {
			return nil, err
		}

		return key, nil
	}
	_, err := jwt.ParseWithClaims(raw, claims, keyfunc, jwt.WithValidMethods([]string{"RS256"}), jwt.WithAudience(p.clientID), jwt.WithExpirationRequired())
	if err != nil {
		return nil, err
	}
	iss, _ := claims["iss"].(string)
	issOK := false
	for _, allowed := range p.issuers {
		if iss == allowed {
			issOK = true
		}
	}
	if !issOK {
		return nil, fmt.Errorf("unexpected issuer %q", iss)
	}

	return claims, nil
}

// jwksCache holds each provider's public keys by key id.
type jwksCache struct {
	mu      sync.Mutex
	keys    map[string]map[string]*rsa.PublicKey // url -> kid -> key
	fetched map[string]time.Time
}

func (c *jwksCache) key(jwksURL, kid string) (*rsa.PublicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.keys == nil {
		c.keys = map[string]map[string]*rsa.PublicKey{}
		c.fetched = map[string]time.Time{}
	}
	if k, ok := c.keys[jwksURL][kid]; ok && time.Since(c.fetched[jwksURL]) < time.Hour {
		return k, nil
	}
	// Unknown (or stale) key id: fetch, at most once a minute.
	if time.Since(c.fetched[jwksURL]) > time.Minute || c.keys[jwksURL] == nil {
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get(jwksURL)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		var set struct {
			Keys []struct{ Kty, Kid, N, E string }
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set); err != nil {
			return nil, err
		}
		keys := map[string]*rsa.PublicKey{}
		for _, k := range set.Keys {
			if k.Kty != "RSA" {
				continue
			}
			n, err1 := base64.RawURLEncoding.DecodeString(k.N)
			e, err2 := base64.RawURLEncoding.DecodeString(k.E)
			if err1 != nil || err2 != nil {
				continue
			}
			keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		}
		c.keys[jwksURL] = keys
		c.fetched[jwksURL] = time.Now()
	}
	if k, ok := c.keys[jwksURL][kid]; ok {
		return k, nil
	}

	return nil, fmt.Errorf("no key %q at %s", kid, jwksURL)
}
