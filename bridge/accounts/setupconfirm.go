// SPDX-License-Identifier: AGPL-3.0-or-later

package accounts

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/alonsovidales/otc/bridge/site"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
)

// Audit F4: a Google or Apple sign-in started on a device's page on the
// home network (the web app's Settings › Bridge, BridgePanel.tsx) ends by
// sending that page a setup code, in its address (afterSignIn). Any site's
// link can start a sign-in, and the provider may answer without asking
// anything (someone who has signed in with it before), so a link naming
// another address on the same network - an attacker's, or one they can
// watch, over plain http - would hand them the code: for 15 minutes it
// registers devices on the account and moves the account's names to them.
//
// So the code goes only once the person says so, on a page of the bridge's
// own that names the address (site.ContinueSetup, never in a frame): the
// provider's callback answers with that page itself - or, when the profile
// is completed first, ContinueSetup does after it - and only the page's
// Continue sends the code (ConfirmSetup); Cancel opens the account page.
// Continue works only in the browser that came back from the provider, for
// the account it signed in: it names a grant the callback put in a cookie
// of its own (__Host-otc_setup_<id>), signed, for that account and address,
// good for cSetupGrantTTL and used once. A setup is one click longer. The
// apps' sign-in (otcsetup://) asks nothing: its code is worth nothing
// without the app's verifier (appsignin.go).
const (
	cSetupGrantCookie = "__Host-otc_setup_"
	// cSetupGrantTTL is as long as the sign-in's state was good for: the
	// question waits that long, and so does a profile being completed.
	cSetupGrantTTL = cOAuthStateTTL
	// cSetupGrantIDBytes is a grant's id: 32 hex characters, in its
	// cookie's name, so two sign-ins at once keep their own.
	cSetupGrantIDBytes = 16
	// ContinuePath is the question page's Continue (ConfirmSetup).
	ContinuePath = "/account/auth/continue"
)

// setupGrant lets accountID's browser send a setup code to returnURL.
type setupGrant struct {
	id, accountID, returnURL string
}

// grantKey signs the grants: derived from the session key, so a session
// cookie is never a grant nor a grant a session.
func (a *Accounts) grantKey() []byte { return deriveKey(a.secret, "setup-grant") }

// needsConfirm is whether a sign-in coming back to returnURL asks before
// the setup code goes there: any address but the account page ("") and
// the apps' (whose code needs their verifier).
func needsConfirm(returnURL string) bool {
	if returnURL == "" {
		return false
	}
	u, err := url.Parse(returnURL)
	return err != nil || !isAppReturn(u)
}

// grantSetup gives this browser a grant for accountID to send a setup code
// to returnURL, in a cookie of its own, and returns its id.
func (a *Accounts) grantSetup(w http.ResponseWriter, accountID, returnURL string, now time.Time) (string, error) {
	buf := make([]byte, cSetupGrantIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	id := hex.EncodeToString(buf)
	// The address goes last: it may hold a |, the others never do.
	payload := id + "|" + accountID + "|" + strconv.FormatInt(now.Add(cSetupGrantTTL).Unix(), 10) + "|" + returnURL
	http.SetCookie(w, &http.Cookie{
		Name: cSetupGrantCookie + id, Value: base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sign(a.grantKey(), payload),
		Path: "/", MaxAge: int(cSetupGrantTTL.Seconds()), HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	return id, nil
}

// clearSetupGrant drops the grant id's cookie: it was used.
func clearSetupGrant(w http.ResponseWriter, id string) {
	http.SetCookie(w, &http.Cookie{Name: cSetupGrantCookie + id, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
}

// isGrantID is whether s has the shape of a grant's id.
func isGrantID(s string) bool {
	_, err := hex.DecodeString(s)
	return len(s) == 2*cSetupGrantIDBytes && err == nil
}

// readGrant is the grant a cookie named name holds, if this bridge signed
// it, under that name, and it hasn't expired.
func (a *Accounts) readGrant(name, value string, now time.Time) (setupGrant, bool) {
	enc, sig, ok := strings.Cut(value, ".")
	if !ok {
		return setupGrant{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return setupGrant{}, false
	}
	payload := string(raw)
	if subtle.ConstantTimeCompare([]byte(sig), []byte(sign(a.grantKey(), payload))) != 1 {
		return setupGrant{}, false
	}
	parts := strings.SplitN(payload, "|", 4)
	if len(parts) != 4 || name != cSetupGrantCookie+parts[0] || parts[1] == "" {
		return setupGrant{}, false
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || !now.Before(time.Unix(exp, 0)) {
		return setupGrant{}, false
	}
	return setupGrant{id: parts[0], accountID: parts[1], returnURL: parts[3]}, true
}

// setupGrantByID is this browser's grant id.
func (a *Accounts) setupGrantByID(r *http.Request, id string, now time.Time) (setupGrant, bool) {
	if !isGrantID(id) {
		return setupGrant{}, false
	}
	c, err := r.Cookie(cSetupGrantCookie + id)
	if err != nil {
		return setupGrant{}, false
	}
	return a.readGrant(c.Name, c.Value, now)
}

// setupGrantFor is this browser's grant for accountID to send a setup code
// to returnURL: the one the callback gave before the profile was completed.
func (a *Accounts) setupGrantFor(r *http.Request, accountID, returnURL string, now time.Time) (setupGrant, bool) {
	for _, c := range r.Cookies() {
		if !strings.HasPrefix(c.Name, cSetupGrantCookie) {
			continue
		}
		if g, ok := a.readGrant(c.Name, c.Value, now); ok && g.accountID == accountID && g.returnURL == returnURL {
			return g, true
		}
	}
	return setupGrant{}, false
}

// askBeforeSetup answers with the question page for grant g: Continue
// setting up the device at its address?
func (a *Accounts) askBeforeSetup(w http.ResponseWriter, r *http.Request, g setupGrant) {
	u, err := url.Parse(g.returnURL)
	if err != nil || u.Host == "" {
		a.pageError(w, r, i18n.ApiPageInvalidReturn(), http.StatusBadRequest)
		return
	}
	o := site.Options{SetupHost: u.Host, SetupContinue: ContinuePath + "?id=" + g.id}
	if err := a.writePage(w, site.ContinueSetup, pageLanguage(r), http.StatusOK, o); err != nil {
		log.Error("could not render the setup's question page:", err)
		a.pageError(w, r, i18n.ApiErrorSigninUnavailable(), http.StatusInternalServerError)
	}
}

// prefetch is whether r is a browser fetching a page in case it is opened
// next, rather than opening it.
func prefetch(r *http.Request) bool {
	for _, h := range []string{"Sec-Purpose", "Purpose"} {
		if strings.Contains(strings.ToLower(r.Header.Get(h)), "prefetch") {
			return true
		}
	}
	return false
}

// ConfirmSetup is the question page's Continue: the browser goes back to
// the device's page with a setup token. GET /account/auth/continue?id=.
// Only a link followed on the bridge's own page gets there (never another
// site's, a frame or a prefetch), in the browser holding the grant, signed
// in to its account; the grant is used up either way.
func (a *Accounts) ConfirmSetup(w http.ResponseWriter, r *http.Request) {
	if !a.sameOriginNavigation(r) || !pageNavigation(r) || prefetch(r) {
		log.Info("refused a setup's Continue that wasn't followed on the bridge's own page")
		a.pageError(w, r, i18n.ApiPageSetupExpired(), http.StatusForbidden)
		return
	}
	g, ok := a.setupGrantByID(r, r.URL.Query().Get("id"), time.Now())
	if !ok {
		a.pageError(w, r, i18n.ApiPageSetupExpired(), http.StatusBadRequest)
		return
	}
	accountID, signedIn, err := a.AccountFromRequest(r)
	if err != nil {
		a.pageError(w, r, i18n.ApiErrorSigninCheckUnavailable(), http.StatusInternalServerError)
		return
	}
	clearSetupGrant(w, g.id)
	if !signedIn || accountID != g.accountID {
		a.pageError(w, r, i18n.ApiPageSetupExpired(), http.StatusBadRequest)
		return
	}
	if returnURL, ok := validReturnURL(g.returnURL); !ok || !needsConfirm(returnURL) {
		a.pageError(w, r, i18n.ApiPageInvalidReturn(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, a.afterSignIn(accountID, g.returnURL), http.StatusFound)
}
