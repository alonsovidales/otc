// SPDX-License-Identifier: AGPL-3.0-or-later

package accounts

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/i18n"
)

// The sign-in's plain-text pages are what http.Error wrote before the
// catalog, whatever languages the browser asks for, as long as this build
// carries English only.
func TestSignInPagesInEnglishAreUnchanged(t *testing.T) {
	for _, header := range []string{"", "en-GB,en;q=0.9", "es-ES,es;q=0.9", "de, en;q=0.5", "ja", "zz-ZZ;q=x", strings.Repeat("a", 2000)} {
		if code := acceptLanguage(header); code != "" && code != "en" {
			continue // a build carrying that language
		}
		for _, c := range []struct {
			m      i18n.Msg
			status int
			want   string
		}{
			{i18n.ApiPageProviderUnknown(), http.StatusNotFound, "that sign-in provider is not configured on this bridge"},
			{i18n.ApiPageInvalidReturn(), http.StatusBadRequest, "that return address is not allowed"},
			{i18n.ApiPageChallengeRequired(), http.StatusBadRequest, "an app sign-in needs a valid challenge"},
			{i18n.ApiPageNotNavigation(), http.StatusBadRequest, "a sign-in starts from a link, not from within another page"},
			{i18n.ApiPageTooManyStarts(), http.StatusTooManyRequests, "too many sign-in attempts from this address, try again in a minute"},
			{i18n.ApiPageStartUnavailable(), http.StatusInternalServerError, "could not start the sign-in"},
			{i18n.ApiPageBadCallback(), http.StatusBadRequest, "bad callback"},
			{i18n.ApiErrorSigninUnavailable(), http.StatusInternalServerError, "could not sign in right now"},
			{i18n.ApiPageOpenFromAccount(), http.StatusForbidden, "open this from your account page"},
		} {
			r := httptest.NewRequest("GET", "/account/auth/google/start", nil)
			if header != "" {
				r.Header.Set("Accept-Language", header)
			}
			got, want := httptest.NewRecorder(), httptest.NewRecorder()
			pageError(got, r, c.m, c.status)
			http.Error(want, c.want, c.status)
			if got.Code != want.Code || got.Body.String() != want.Body.String() || got.Header().Get("Content-Type") != want.Header().Get("Content-Type") {
				t.Errorf("%s with %q: %d %q %q, want %d %q", c.m.Key, header, got.Code, got.Body.String(), got.Header().Get("Content-Type"), want.Code, want.Body.String())
			}
		}
	}

	// Through a handler: a provider this bridge doesn't have.
	a, _ := testAccounts(t)
	a.providers = map[string]*provider{}
	r := httptest.NewRequest("GET", "/account/auth/github/start", nil)
	r.SetPathValue("provider", "github")
	r.Header.Set("Accept-Language", "ja-JP") // a language no build carries
	w := httptest.NewRecorder()
	a.OAuthStart(w, r)
	if w.Code != http.StatusNotFound || w.Body.String() != "that sign-in provider is not configured on this bridge\n" {
		t.Errorf("unknown provider: %d %q", w.Code, w.Body.String())
	}
}
