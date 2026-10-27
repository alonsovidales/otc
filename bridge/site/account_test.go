// SPDX-License-Identifier: AGPL-3.0-or-later

package site

import (
	"bytes"
	"encoding/json"
	"errors"
	"html/template"
	"regexp"
	"strings"
	"testing"

	"github.com/alonsovidales/otc/i18n"
)

// accountScriptText is the account page's #account-text in a language.
func accountScriptText(t *testing.T, lang string) map[string]any {
	t.Helper()
	out, err := Render(Account, lang, testOptions)
	if err != nil {
		t.Fatal(err)
	}
	var texts map[string]any
	if err := json.Unmarshal([]byte(jsonBlock(t, out, "account-text")), &texts); err != nil {
		t.Fatal(err)
	}
	return texts
}

// asMarkup writes a script text as the page's script makes it: a string
// as it is, spans with the element each tag stands for (accountTags) -
// the markup the old page's template literals held.
func asMarkup(t *testing.T, name string, v any) string {
	t.Helper()
	if s, ok := v.(string); ok {
		return s
	}
	spans, ok := v.([]any)
	if !ok {
		t.Fatalf("%s: neither a string nor spans: %#v", name, v)
	}
	var b strings.Builder
	for _, sp := range spans {
		pair, ok := sp.([]any)
		if !ok || len(pair) != 2 {
			t.Fatalf("%s: a span that isn't [tag, text]: %#v", name, sp)
		}
		tag, _ := pair[0].(string)
		text, _ := pair[1].(string)
		if tag == "" {
			b.WriteString(text)
			continue
		}
		open, ok := accountTags[tag]
		if name == "dataHint" && tag == "privacy" {
			open = `<a href="/privacy">` // the same tab, unlike the terms' links
		}
		if !ok {
			t.Errorf("%s: tag <%s> the script doesn't make", name, tag)
		}
		closing := "</" + tag + ">"
		if strings.HasPrefix(open, "<a ") {
			closing = "</a>"
		}
		b.WriteString(open + text + closing)
	}
	return b.String()
}

// accountTags are the elements the account page's script makes of each
// tag (rich() and its tags arguments in pages/account.html).
var accountTags = map[string]string{
	"b":       "<b>",
	"i":       "<i>",
	"code":    "<code>",
	"small":   "<small>",
	"terms":   `<a href="/terms" target="_blank">`,
	"privacy": `<a href="/privacy" target="_blank">`,
	"mail":    `<a href="mailto:{contact}">`,
	"details": `<a href="/account?complete=1">`,
}

// What the account page's script wrote before its text moved into the
// catalog, copied from bridge/static/account.html: each ${...} the old
// template literals filled in at run time is the {placeholder} the script
// fills in now, and what they held as literals (the 8 of the password's
// length, "delete", the home address, the identity file) is filled in
// here with those same values. The differences: the account holder's
// name in "Hello, {name}" comes between isolation marks, the dashes are
// the site's (" — "), and the setup code's hint names every way of
// setting up (2026-10-27).
var accountScriptEnglish = map[string]string{
	"signOut":             "Sign out",
	"emailLabel":          "Email",
	"nameLabel":           "Name",
	"surnameLabel":        "Surname",
	"countryLabel":        "Country of residence",
	"countryChoose":       "Choose…",
	"newPasswordLabel":    "New password <small>(8 characters or more)</small>",
	"repeatPasswordLabel": "Repeat the new password",
	"passwordMismatch":    "The two passwords don't match",
	"resetTitle":          "Choose a new password",
	"resetCard":           "New password",
	"resetSet":            "Set the password",
	"resetSignsOut":       "Setting it signs your account out everywhere else.",
	"resetFailed":         "Could not set it",
	"resetDone":           "Your new password is set, and you are signed in.",
	"verifyDone":          "Your email is confirmed. You can register your devices now.",
	"verifyFailed":        "Could not confirm the email",
	"verifyPromptTitle":   "Confirm your email",
	"verifyPrompt":        "We sent a link to <b>{email}</b>. Open it to confirm the address is yours — until then this account can't register device names or give setup codes.",
	"verifyResend":        "Send the link again",
	"verifyResent":        "Sent — check your inbox (and the spam folder).",
	"sendFailed":          "Could not send it right now",
	"signedOutTitle":      "Your Off The Cloud account",
	"signedOutLead":       "One account holds the domains of your devices: sign in here to see them, replace a lost device, or get a code for setting up a new one.",
	"signInTitle":         "Sign in",
	"passwordLabel":       "Password",
	"signInButton":        "Sign in",
	"signInFailed":        "Could not sign in",
	"continueWith":        "Continue with {provider}",
	"forgotOpen":          "Forgot your password?",
	"forgotEmail":         "Your account's email",
	"forgotSend":          "Email me a reset link",
	"forgotSent":          "If there is an account for that email, a link to reset its password is on its way. It works for an hour.",
	"signUpTitle":         "Create an account",
	"signUpPasswordLabel": "Password <small>(8 characters or more)</small>",
	"signUpButton":        "Create account",
	"signUpFailed":        "Could not create the account",
	"signUpProviders":     "Or create it with a sign-in provider (you accept the terms on the next page):",
	"termsIntro":          "Creating an account is only necessary to use our bridge: you will get up to <b>{max_domains} domains</b> like <i>name</i>.{host} to connect to your device and also connect with the devices of your friends and family. If you only want to use your device in your local network as a NAS or with Tailscale Funnel, you do not need an account.",
	"termsPricing":        `An account for up to {max_domains} domains is <b>free for the first {free_years} years</b>, and after this period will cost {price} euros + VAT a year (we will e-mail you before the period ends). If you need more domains, each extra one costs {extra_domain_eur} euros a year: please contact us at <a href="mailto:{contact}">{contact}</a>. An account unused for {inactive_months} months (no sign-in here, and no app or friend reaching your devices through the bridge) is removed and its domains released; we email you a month before. Read the full <a href="/terms" target="_blank">terms of use</a> and the <a href="/privacy" target="_blank">privacy notice</a>.`,
	"termsAccept":         `I accept the <a href="/terms" target="_blank">terms of use</a> and have read the <a href="/privacy" target="_blank">privacy notice</a>.`,
	"termsPromptTitle":    "Our terms of use",
	"termsPrompt":         `We have published <a href="/terms" target="_blank">terms of use</a> for accounts and the bridge. Please read and accept them: until then this account can't register device names or give setup codes.`,
	"termsAcceptButton":   "Accept",
	"termsTick":           "Tick the box to accept them.",
	"termsSaveFailed":     "Could not save right now",
	"profileNewTitle":     "Almost there",
	"profileEditTitle":    "Your details",
	"profileNewLead":      "We need a name and your country of residence to finish creating the account.",
	"profileEditLead":     "Update the details on your account.",
	"save":                "Save",
	"saveContinue":        "Save and continue the setup",
	"back":                "Back",
	"profileSaveFailed":   "Could not save",
	"hello":               "Hello, \u2068{name}\u2069",
	"summary":             `{registered} of {max} domains registered · free until <b>{date}</b> · <a href="/account?complete=1">your details</a>`,
	"setupTitle":          "Setting up a device?",
	"setupHint":           "Prepare the card with the computer app (or flash the image), power the device on and finish the setup over Bluetooth from the iPhone or Android app, or the Mac app. At the account step choose <b>I have a setup code</b> and type this code — it links the new device's name to this account. Good for 15 minutes.",
	"setupGet":            "Get a setup code",
	"setupLost":           "Lost a device? Set up its replacement the same way and give it the <b>same name</b>: the name moves to the new device and the old one is locked out.",
	"setupFailed":         "Could not make a code",
	"setupExpires":        "Expires in {minutes} minutes.",
	"manualTitle":         "Installing by hand?",
	"manualHint":          "For a device installed with the install script rather than the image: register its name here and put the identity you get into <code>/etc/otc/otc-install.env</code> before running the script (it is shown once).",
	"manualNameLabel":     "Name",
	"manualPlaceholder":   "e.g. casa",
	"manualRegister":      "Register the name",
	"manualFailed":        "Could not register that name",
	"identityYours":       "<b>{domain}</b> is yours. Copy this now — it is not shown again:",
	"domainsTitle":        "Your domains",
	"connected":           "connected",
	"notConnected":        "not connected",
	"domainDisabled":      "disabled",
	"since":               "since {date}",
	"newIdentity":         "New identity",
	"release":             "Release",
	"domainsEmpty":        "No domains yet. Set up a device with a setup code, or register a name for a manual install.",
	"identityConfirm":     "Give {domain} a new identity? The device using it now will be locked out of the bridge until it is set up with the new one.",
	"identityFailed":      "Could not re-issue that domain",
	"releaseConfirm":      "Release {domain}? Its device goes on working at home only (http://otc.local:8080), and the name stays reserved for you for 30 days.",
	"releaseFailed":       "Could not release that domain",
	"passwordTitle":       "Password and sign-in",
	"passwordCurrent":     "Current password",
	"passwordProvider":    "You sign in with Google or Apple. You can add a password within 15 minutes of signing in that way.",
	"passwordChange":      "Change password",
	"passwordAdd":         "Add a password",
	"passwordSignsOut":    "Changing the password signs you out everywhere else. Left signed in on a computer you don't use any more?",
	"signOutAll":          "Sign out everywhere",
	"signOutAllConfirm":   "Sign out of your account on every browser and device, this one included?",
	"passwordSaveFailed":  "Could not save the password",
	"passwordSaved":       "Password saved. Other sessions are signed out.",
	"dataTitle":           "Your data and your account",
	"dataHint":            `Download everything this bridge keeps about you: your details, how you sign in, your domains and their traffic counts. What <a href="/privacy">we keep, and why</a>.`,
	"dataDownload":        "Download my data",
	"deleteHint":          "<b>Deleting your account</b> releases all your domains (each name stays reserved for you for 30 days, so no one can pose as your device to your friends) and removes your details, sign-ins and setup codes. Your devices keep working at home, at <code>http://otc.local:8080</code>, as if they had been set up without an account — with everything on them — but are no longer reachable from outside nor by your friends. This can't be undone.",
	"deletePassword":      "Your password",
	"deleteProvider":      "You sign in with Google or Apple: sign in again just before deleting (within 15 minutes).",
	"deleteType":          "Type <b>delete</b> to confirm",
	"deleteTypeError":     "Type delete to confirm",
	"deleteWord":          "delete", // the old page's constant, now a key
	"deleteButton":        "Delete my account",
	"deleteFailed":        "Could not delete the account",
	"deletedTitle":        "Your account is deleted",
	"deletedLead":         "Your devices go on working at home, at <code>http://otc.local:8080</code>.",
	"deletedReleased":     "Released: {domains}. Your devices go on working at home, at <code>http://otc.local:8080</code>.",
	"deletedHome":         "Back to the home page",
}

// The literals the old script held where the new one passes a value.
var accountLiterals = strings.NewReplacer(
	"{min_chars}", "8",
	"{word}", "delete",
	"{address}", "http://otc.local:8080",
	"{path}", "/etc/otc/otc-install.env",
)

// What the account page's script writes in English is what it wrote
// before: TestEnglishUnchanged covers the page around it.
func TestAccountScriptTextsUnchanged(t *testing.T) {
	texts := accountScriptText(t, English())
	for name, want := range accountScriptEnglish {
		v, ok := texts[name]
		if !ok {
			t.Errorf("#account-text has no %s", name)
			continue
		}
		if got := accountLiterals.Replace(asMarkup(t, name, v)); got != want {
			t.Errorf("%s:\n  got  %q\n  want %q", name, got, want)
		}
	}
	for name := range texts {
		if _, ok := accountScriptEnglish[name]; !ok {
			t.Errorf("#account-text has %s, which the old page didn't say", name)
		}
	}
}

// headErrorTable is account.html's #error-text before the page moved into
// the binary, as it was.
const headErrorTable = `{
    "api": {
      "invalid_body": "invalid request body",
      "not_signed_in": "not signed in",
      "signin_check_unavailable": "could not check your sign-in right now - try again",
      "cross_origin": "not allowed from this page",
      "invalid_credentials": "wrong email or password - if you signed up with Google or Apple, use that instead (on the account page, then a setup code)",
      "too_many_attempts": "too many failed attempts, try again later",
      "signin_unavailable": "could not sign in right now",
      "too_many_signups": "too many sign-ups from this address, try again later",
      "invalid_email": "that email address doesn't look right",
      "password_too_short": "the password needs at least {min_password} characters",
      "profile_incomplete": "name, surname and country of residence are required",
      "terms_required": "please accept the terms of use",
      "signup_unavailable": "could not sign up right now",
      "email_taken": "there is already an account with that email - sign in instead",
      "save_unavailable": "could not save right now",
      "wrong_password": "the password is not right",
      "recent_signin_required": "sign in again (with Google or Apple), then try again within 15 minutes",
      "account_check_unavailable": "could not check your account right now",
      "confirm_required": "confirm with {\"confirm\": \"delete\"}",
      "delete_unavailable": "could not delete the account right now",
      "too_many_emails": "too many emails asked for from here, try again later",
      "email_send_unavailable": "could not send the email right now",
      "verify_unavailable": "could not confirm right now",
      "verify_link_invalid": "that link is not valid any more - sign in and ask for a new one",
      "reset_unavailable": "could not reset it right now",
      "reset_link_invalid": "that link is not valid any more - ask for a new one",
      "email_not_verified": "confirm your email first - open the link we sent you, or ask for a new one above",
      "terms_not_accepted": "accept the terms of use above first",
      "setup_code_unavailable": "could not make a setup code right now",
      "register_unavailable": "could not register that name right now",
      "invalid_name": "a name is lower-case letters, digits and hyphens, up to 63 characters",
      "name_taken": "that name is already taken",
      "name_held": "that name was released recently and is held for its previous owner for 30 days",
      "domain_limit": "an account can register up to {max_domains} domains - for more ({extra_domain_eur} euros a year each), write to {contact}",
      "reissue_unavailable": "could not re-issue that domain right now",
      "not_your_domain": "that domain is not yours",
      "release_unavailable": "could not release that domain right now"
    },
    "signin": {
      "cancelled": "the sign-in was cancelled",
      "expired": "that sign-in has expired, please try again",
      "failed": "the sign-in could not be completed",
      "no_email": "the sign-in did not include an email address"
    }
  }`

// The error table reads as before in English: the same text for every
// code, the placeholders named as the API's keys name them (the script
// fills them in from the same figures), and the deletion's confirmation
// body passed in rather than written in the text. invalid_language is new
// (PUT /api/account/language), with the API's own words.
func TestAccountErrorTableUnchanged(t *testing.T) {
	out, err := Render(Account, English(), testOptions)
	if err != nil {
		t.Fatal(err)
	}
	var got, want struct {
		API    map[string]string `json:"api"`
		SignIn map[string]string `json:"signin"`
	}
	if err := json.Unmarshal([]byte(jsonBlock(t, out, "error-text")), &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(headErrorTable), &want); err != nil {
		t.Fatal(err)
	}
	if got.API["invalid_language"] != i18n.ApiErrorInvalidLanguage().Render("en") {
		t.Errorf("invalid_language: %q", got.API["invalid_language"])
	}
	delete(got.API, "invalid_language")
	old := strings.NewReplacer("{min_chars}", "{min_password}", "{price_eur}", "{extra_domain_eur}", "{body}", `{"confirm": "delete"}`)
	for code, text := range got.API {
		if old.Replace(text) != want.API[code] {
			t.Errorf("api %s: %q, was %q", code, text, want.API[code])
		}
	}
	for code, text := range got.SignIn {
		if text != want.SignIn[code] {
			t.Errorf("signin %s: %q, was %q", code, text, want.SignIn[code])
		}
	}
	if len(got.API) != len(want.API) || len(got.SignIn) != len(want.SignIn) {
		t.Errorf("%d api and %d signin codes, were %d and %d", len(got.API), len(got.SignIn), len(want.API), len(want.SignIn))
	}
}

// The account page names its address in every carried language and its
// canonical one, /account; its links home go to the landing page in its
// language.
func TestAccountAlternates(t *testing.T) {
	langs := i18n.Languages()
	for _, l := range langs {
		out, err := Render(Account, l.Code, testOptions)
		if err != nil {
			t.Fatal(err)
		}
		s := string(out)
		if want := `<link rel="canonical" href="https://off-the.cloud` + AccountPath(l.Code) + `">`; !strings.Contains(s, want) {
			t.Errorf("%s: no %s", l.Code, want)
		}
		for _, other := range langs {
			want := `<link rel="alternate" hreflang="` + other.Tag + `" href="https://off-the.cloud` + AccountPath(other.Code) + `">`
			if !strings.Contains(s, want) {
				t.Errorf("%s: missing %s", l.Code, want)
			}
		}
		if !strings.Contains(s, `<link rel="alternate" hreflang="x-default" href="https://off-the.cloud/account">`) {
			t.Errorf("%s: no x-default alternate", l.Code)
		}
		if !strings.Contains(s, `<a class="brand" href="`+Path(l.Code)+`">`) {
			t.Errorf("%s: the brand doesn't link to %s", l.Code, Path(l.Code))
		}
		var page struct{ Lang, Tag, Home string }
		if err := json.Unmarshal([]byte(jsonBlock(t, out, "account-page")), &page); err != nil ||
			page.Lang != l.Code || page.Tag != l.Tag || page.Home != Path(l.Code) {
			t.Errorf("%s: #account-page %+v, %v", l.Code, page, err)
		}
		if got := strings.Contains(s, `class="lang-switch"`); got != (len(langs) > 1) {
			t.Errorf("%s: language switcher shown %v with %d languages", l.Code, got, len(langs))
		}
		if len(langs) > 1 && !strings.Contains(s, `<a href="/`+l.Code+`/account" lang="`+l.Tag+`" hreflang="`+l.Tag+`" aria-current="page">`) {
			t.Errorf("%s: the switcher doesn't mark this page", l.Code)
		}
	}
	if AccountPath(English()) != "/account" || AccountPath("es") != "/es/account" {
		t.Errorf("AccountPath: %q %q", AccountPath(English()), AccountPath("es"))
	}
}

// The page's policy allows its own <style> and <script> by nonce and
// nothing inline besides: no style attributes, no event-handler
// attributes; and its script builds elements from text, never markup.
func TestAccountPageFitsThePolicy(t *testing.T) {
	out, err := Render(Account, English(), testOptions)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, bad := range []*regexp.Regexp{
		regexp.MustCompile(`\sstyle\s*=`),
		regexp.MustCompile(`\son[a-z]+\s*=\s*["']`),
		regexp.MustCompile(`innerHTML|outerHTML|insertAdjacentHTML|document\.write|\.html\(|javascript:`),
		regexp.MustCompile(`\bstyle:\s`), // h(tag, {style: ...}) would be a style attribute
		regexp.MustCompile(`setAttribute\(\s*['"](style|on)`),
		regexp.MustCompile(`\beval\(|new Function\(`),
	} {
		if m := bad.FindString(s); m != "" {
			t.Errorf("the page has %q", m)
		}
	}
}

// scriptForm gives a key's text back with its placeholders, refuses
// arguments that aren't the key's, and leaves a user argument isolated.
func TestScriptForm(t *testing.T) {
	f, err := scriptForm(English(), "site.account.home.hello", []string{"name", "user"})
	if err != nil || f.text != "Hello, \u2068{name}\u2069" {
		t.Errorf("hello: %q, %v", f.text, err)
	}
	f, err = scriptForm(English(), "site.account.domains.release_confirm", []string{"domain", "text", "address", "text"})
	if err != nil || f.text != "Release {domain}? Its device goes on working at home only ({address}), and the name stays reserved for you for 30 days." {
		t.Errorf("release_confirm: %q, %v", f.text, err)
	}
	f, err = scriptForm(English(), "site.account.home.summary", []string{"registered", "int", "max", "int", "date", "text"})
	if spans, ok := f.text.([][2]string); err != nil || !ok || len(spans) != 4 || spans[0][1] != "{registered} of {max} domains registered · free until " ||
		spans[1] != [2]string{"b", "{date}"} || spans[3] != [2]string{"details", "your details"} {
		t.Errorf("summary: %#v, %v", f.text, err)
	}
	for _, c := range []struct {
		key  string
		args []string
	}{
		{"site.account.home.hello", nil},                                                             // its argument missing
		{"site.account.home.hello", []string{"name", "int"}},                                         // a number where it takes text
		{"site.account.home.hello", []string{"name", "user", "x", "text"}},                           // one it doesn't take
		{"site.account.home.hello", []string{"name"}},                                                // no type
		{"site.account.home.summary", []string{"registered", "count", "max", "int", "date", "text"}}, // a type a script can't fill
		{"site.account.no_such_key", nil},
	} {
		if _, err := scriptForm(English(), c.key, c.args); err == nil {
			t.Errorf("%s %v: no error", c.key, c.args)
		}
	}
	if _, err := scriptForm(English(), "site.account.no_such_key", nil); !errors.Is(err, i18n.ErrUnknownKey) {
		t.Errorf("an unknown key: %v", err)
	}
	// Every language this build carries renders every script text back
	// with its placeholders (a missing translation is English, with
	// English numbers).
	for _, l := range i18n.Languages() {
		texts := accountScriptText(t, l.Code)
		for name, v := range texts {
			s := asMarkup(t, name, v)
			for _, ph := range regexp.MustCompile(`\{[a-z_]+\}`).FindAllString(accountScriptEnglish[name], -1) {
				if ph == "{contact}" && name == "termsPricing" {
					continue
				}
				if !strings.Contains(accountLiterals.Replace(s), ph) {
					t.Errorf("%s in %s: no %s", name, l.Code, ph)
				}
			}
		}
	}
}

// A script's text is data in its JSON block: whatever a translation holds
// reads back as written and never closes the block or becomes markup.
func TestScriptTextIsInert(t *testing.T) {
	const hostile = `</script><img src=x onerror=alert(1)>{{.}}"'&<b>` + " "
	tmpl, err := template.New("page").Funcs(funcs(English())).Parse(
		`<script type="application/json" id="x">{{jstexts "plain" .Plain "rich" .Rich}}</script>`)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, map[string]any{
		"Plain": scriptText{hostile},
		"Rich":  scriptText{[][2]string{{"", hostile}, {"b", hostile}}},
	}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, bad := range []string{"</script><", "<img", "<b>"} {
		if strings.Contains(out, bad) {
			t.Errorf("%q came out as markup: %s", bad, out)
		}
	}
	var got struct {
		Plain string      `json:"plain"`
		Rich  [][2]string `json:"rich"`
	}
	if err := json.Unmarshal([]byte(jsonBlock(t, b.Bytes(), "x")), &got); err != nil {
		t.Fatal(err)
	}
	if got.Plain != hostile || len(got.Rich) != 2 || got.Rich[1] != [2]string{"b", hostile} {
		t.Errorf("the text doesn't read back: %#v", got)
	}
}
