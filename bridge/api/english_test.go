// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/alonsovidales/otc/i18n"
)

// legacyErrors is every API error's English as the bridge wrote it before
// the catalog (HEAD before the api.* keys), by the key that now holds it.
// The "error" of every answer must stay exactly this for every request:
// older account pages, wizards and apps show it as it is.
var legacyErrors = map[string]string{
	"api.error.account_check_unavailable":       "could not check your account right now",
	"api.error.claim_unavailable":               "could not reserve that name right now",
	"api.error.cross_origin":                    "not allowed from this page",
	"api.error.delete_unavailable":              "could not delete the account right now",
	"api.error.domains_unavailable":             "could not list your domains right now",
	"api.error.email_not_verified":              "confirm your email first - open the link we sent you, or ask for a new one above",
	"api.error.email_send_unavailable":          "could not send it right now",
	"api.error.email_send_unavailable_email":    "could not send the email right now",
	"api.error.email_taken":                     "there is already an account with that email - sign in instead",
	"api.error.export_unavailable":              "could not export your data right now",
	"api.error.field_too_long":                  "one of the fields is too long",
	"api.error.internal_error":                  "internal error, please try again",
	"api.error.invalid_body":                    "invalid request body",
	"api.error.invalid_credentials":             "wrong email or password - if you signed up with Google or Apple, use that instead (on the account page, then a setup code)",
	"api.error.invalid_email":                   "that email address doesn't look right",
	"api.error.invalid_email_contact":           "that doesn't look like a valid email address",
	"api.error.invalid_language":                "that language is not available",
	"api.error.invalid_name":                    "a name is lower-case letters, digits and hyphens, up to 63 characters",
	"api.error.invalid_name_claim":              "that name can't be used",
	"api.error.invalid_name_short":              "invalid name",
	"api.error.invalid_setup_code":              "that setup code is not valid or has expired",
	"api.error.login_required":                  "sign in to register a name",
	"api.error.missing_fields":                  "name, email and message are required",
	"api.error.name_check_unavailable":          "could not check that name right now",
	"api.error.name_held":                       "that name was released recently and is held for its previous owner for 30 days",
	"api.error.name_taken":                      "that name is already taken",
	"api.error.not_signed_in":                   "not signed in",
	"api.error.not_your_domain":                 "that domain is not yours",
	"api.error.profile_incomplete":              "name, surname and country of residence are required",
	"api.error.recent_signin_required":          "sign in again to set a password",
	"api.error.recent_signin_required_delete":   "sign in again (with Google or Apple), then delete the account within 15 minutes",
	"api.error.recent_signin_required_provider": "sign in again (with Google or Apple) to set a password",
	"api.error.register_unavailable":            "could not register that name right now",
	"api.error.reissue_unavailable":             "could not re-issue that domain right now",
	"api.error.release_unavailable":             "could not release that domain right now",
	"api.error.reset_link_invalid":              "that link is not valid any more - ask for a new one",
	"api.error.reset_unavailable":               "could not reset it right now",
	"api.error.save_unavailable":                "could not save right now",
	"api.error.setup_code_check_unavailable":    "could not check that setup code right now",
	"api.error.setup_code_unavailable":          "could not make a setup code right now",
	"api.error.setup_unavailable":               "could not start the setup",
	"api.error.signin_check_unavailable":        "could not check your sign-in right now - try again",
	"api.error.signin_expired":                  "that sign-in has expired - try again",
	"api.error.signin_unavailable":              "could not sign in right now",
	"api.error.signin_unavailable_finish":       "could not finish the sign-in",
	"api.error.signout_unavailable":             "could not sign out right now",
	"api.error.signup_unavailable":              "could not sign up right now",
	"api.error.terms_not_accepted":              "accept the terms of use above first",
	"api.error.terms_not_accepted_setup":        "accept the terms of use first: sign in at off-the.cloud/account and accept them there",
	"api.error.terms_required":                  "please accept the terms",
	"api.error.terms_required_profile":          "please accept the terms of use",
	"api.error.too_many_attempts":               "too many failed attempts, try again later",
	"api.error.too_many_attempts_minutes":       "too many attempts - try again in a few minutes",
	"api.error.too_many_claims":                 "please wait a moment before trying another name",
	"api.error.too_many_emails":                 "too many emails asked for from here, try again later",
	"api.error.too_many_messages":               "please wait a moment before sending another message",
	"api.error.too_many_signups":                "too many sign-ups from this address, try again later",
	"api.error.verify_link_invalid":             "that link is not valid any more - sign in and ask for a new one",
	"api.error.verify_unavailable":              "could not confirm right now",
	"api.error.wrong_password":                  "the password is not right",
	"api.error.wrong_password_current":          "the current password is not right",
	// Plain-text pages of the Google and Apple sign-in (http.Error).
	"api.page.bad_callback":       "bad callback",
	"api.page.challenge_required": "an app sign-in needs a valid challenge",
	"api.page.invalid_return":     "that return address is not allowed",
	"api.page.not_navigation":     "a sign-in starts from a link, not from within another page",
	"api.page.open_from_account":  "open this from your account page",
	"api.page.provider_unknown":   "that sign-in provider is not configured on this bridge",
	"api.page.start_unavailable":  "could not start the sign-in",
	"api.page.too_many_starts":    "too many sign-in attempts from this address, try again in a minute",
	// New with the question before a setup code goes to a device's page
	// (audit F4, 2026-10-27): no text before it.
	"api.page.setup_expired": "this step has expired - start the sign-in again from your device's page",
	// Websocket replies (bridge/websocket checks them as relayText too).
	"api.relay.account_disabled":    "This account has been disabled.",
	"api.relay.device_unreachable":  "This device isn't reachable right now. It may be switched off or offline - please try again later.",
	"api.relay.domain_check_failed": "error checking domain",
	"api.relay.internal_error":      "The bridge could not do this right now, try again later",
	"api.relay.invalid_secret":      "Invalid Secret",
	"api.relay.logs_no_email":       "the bridge can't send email right now",
	"api.relay.logs_send_failed":    "the logs could not be sent right now",
	"api.relay.logs_too_big":        "the logs are too big to send",
	"api.relay.logs_too_often":      "logs were sent from this device a few times already - try again in an hour",
}

// Every api.* key renders today's English, in the "error" of an answer
// and for any language a request could name: an answer's text is English
// for every request (the account page and the apps word it by its code).
func TestErrorEnglishIsUnchanged(t *testing.T) {
	b, err := os.ReadFile("../../i18n/strings/en/api.json")
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]struct {
		Args []json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(b, &keys); err != nil {
		t.Fatal(err)
	}
	for key, e := range keys {
		if len(e.Args) > 0 {
			continue // below, with their arguments
		}
		want, ok := legacyErrors[key]
		if !ok {
			t.Errorf("%s: no English from before to compare with", key)
			continue
		}
		if got := errorText(i18n.Msg{Key: key}); got != want {
			t.Errorf("%s: %q, want %q", key, got, want)
		}
	}
	for key := range legacyErrors {
		if _, ok := keys[key]; !ok {
			t.Errorf("%s: not in api.json", key)
		}
	}

	// The texts with arguments, against the old fmt.Sprintf.
	for _, c := range []struct {
		m    i18n.Msg
		want string
	}{
		{i18n.ApiErrorDomainLimit(5, 2, "info@off-the.cloud"), "an account can register up to 5 domains - for more (2 euros a year each), write to info@off-the.cloud"},
		{i18n.ApiErrorDomainLimit(1234, 10, "x@y.z"), "an account can register up to 1,234 domains - for more (10 euros a year each), write to x@y.z"},
		{i18n.ApiErrorPasswordTooShort(8), "the password needs at least 8 characters"},
		{i18n.ApiErrorEmailNotVerifiedSetup("a@b.c"), "Confirm your email first: we sent a link to a@b.c. Open it, then sign in here again to continue."},
		{i18n.ApiErrorEmailNotVerifiedSetup("{email} %s"), "Confirm your email first: we sent a link to {email} %s. Open it, then sign in here again to continue."},
	} {
		if got := errorText(c.m); got != c.want {
			t.Errorf("%s: %q, want %q", c.m.Key, got, c.want)
		}
	}
}
