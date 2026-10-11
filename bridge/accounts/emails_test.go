// SPDX-License-Identifier: AGPL-3.0-or-later

package accounts

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/alonsovidales/otc/i18n"
)

// The emails as the bridge wrote them before the catalog, word for word
// (HEAD before the mail.* keys): what every email must still be in
// English.
func legacyGreeting(name string) string {
	if name != "" {
		return "Hello " + name + ","
	}
	return "Hello,"
}

func legacyVerification(name, tld, link string) (string, string) {
	return "Confirm your email for Off The Cloud",
		legacyGreeting(name) + "\n\nPlease confirm that this is your email address by opening this link:\n\n" + link +
			"\n\nThe link works for 48 hours. Once your email is confirmed you can register your devices on the bridge." +
			"\n\nIf you didn't create an Off The Cloud account, ignore this email: nothing is registered for this address without the confirmation." +
			"\n\n- Off The Cloud\nhttps://" + tld + "\n"
}

func legacyReset(name, tld, link string) (string, string) {
	return "Reset your Off The Cloud password",
		legacyGreeting(name) + "\n\nSomeone - hopefully you - asked to reset the password of the Off The Cloud account for this email. To choose a new one, open this link:\n\n" + link +
			"\n\nThe link works for one hour, once. Setting a new password signs your account out everywhere else." +
			"\n\nIf you didn't ask for this, ignore this email: your password stays as it is." +
			"\n\n- Off The Cloud\nhttps://" + tld + "\n"
}

func legacyInactivity(name, email, tld string, now time.Time) (string, string) {
	when := now.Add(cWarnAhead).Format("2 January 2006")
	greet := "Hello,"
	if name != "" {
		greet = "Hello " + name + ","
	}
	body := fmt.Sprintf("%s\n\nYour Off The Cloud bridge account (%s) hasn't been used for five months: no one has signed in to it, and no app or friend has reached your devices through the bridge.\n\n"+
		"As our terms say, an account unused for six months is removed. If nothing changes, this one and its device names will be removed on %s. Your devices keep working at home.\n\n"+
		"To keep it, sign in at https://%s/account or use your device through the bridge before then.\n\n"+
		"Off The Cloud\n", greet, email, when, tld)
	return "Your Off The Cloud account will be removed on " + when, body
}

// Names as people type them: none, accents, another script (right to
// left), an emoji, text that looks like a placeholder or a format verb,
// and a bidi control (passed on as it always was: the emails take names
// as text).
var emailNames = []string{"", "Ann", "José María", "محمد", "Zoë 🙂", "{name} %s %d $& {{.}}", "Ann\u202eevil"}

// The languages an English email goes out in: none (not known), English,
// a regional tag, languages this build doesn't carry, and junk.
var englishLangs = []string{"", "en", "EN-gb", "es", "pt-BR", "zz", "en; drop table"}

func englishLang(lang string) bool {
	code := i18n.Normalize(lang)
	return code == "" || code == "en"
}

// Every email kind, in English, is byte for byte what it was.
func TestEmailsInEnglishAreUnchanged(t *testing.T) {
	for _, tld := range []string{"off-the.cloud", "bridge.test:8081"} {
		a := &Accounts{tld: tld}
		for _, lang := range englishLangs {
			if !englishLang(lang) {
				continue // a draft build carrying it: TestEmailsFollowTheLanguage
			}
			for _, name := range emailNames {
				link := "https://" + tld + "/account#verify=Qx-_9aZ"
				ws, wb := legacyVerification(name, tld, link)
				if s, b := verificationSubject(lang), a.verificationBody(lang, name, link); s != ws || b != wb {
					t.Errorf("verification %q %q %q:\n%q %q\nwant\n%q %q", tld, lang, name, s, b, ws, wb)
				}
				link = "https://" + tld + "/account#reset=Qx-_9aZ"
				ws, wb = legacyReset(name, tld, link)
				if s, b := resetSubject(lang), a.resetBody(lang, name, link); s != ws || b != wb {
					t.Errorf("reset %q %q %q:\n%q %q\nwant\n%q %q", tld, lang, name, s, b, ws, wb)
				}
				for _, email := range []string{"a@b.c", "x.y+tag@example.com"} {
					now := time.Date(2026, 10, 11, 3, 4, 5, 0, time.UTC)
					ws, wb = legacyInactivity(name, email, tld, now)
					if s, b := a.inactivityEmail(lang, name, email, now.Add(cWarnAhead)); s != ws || b != wb {
						t.Errorf("inactivity %q %q %q:\n%q %q\nwant\n%q %q", tld, lang, name, s, b, ws, wb)
					}
				}
			}
		}
	}
}

// The removal date reads as Go's "2 January 2006" for every day of two
// years, in the date's own location.
func TestLongDateInEnglishIsUnchanged(t *testing.T) {
	for _, loc := range []*time.Location{time.UTC, time.FixedZone("UTC+14", 14*3600), time.FixedZone("UTC-11", -11*3600)} {
		for d := time.Date(2026, 1, 1, 23, 30, 0, 0, loc); d.Year() < 2028; d = d.AddDate(0, 0, 1) {
			if got, want := longDate("en", d), d.Format("2 January 2006"); got != want {
				t.Fatalf("%v: %q, want %q", d, got, want)
			}
		}
	}
	// A year never gets a thousands separator.
	if got := longDate("en", time.Date(12026, 3, 9, 0, 0, 0, 0, time.UTC)); got != "9 March 12026" {
		t.Errorf("year 12026: %q", got)
	}
}

// The daily inactivity pass sends the warning as it always did: to the
// account's address, with the same subject and text, the account's
// language ("" here) picking English.
func TestInactivityPassSendsTheSameWarning(t *testing.T) {
	for _, c := range []struct{ name, lang string }{{"Ann", ""}, {"", "en"}, {"محمد", "zz"}} {
		a, mock := testAccounts(t)
		a.tld = "off-the.cloud"
		mock.ExpectExec("update `accounts` a set a.`inactivity_warned_at` = null").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery("select a.`id`, a.`email`, a.`name`, a.`inactivity_warned_at`, a.`lang` from `accounts` a").
			WillReturnRows(sqlmock.NewRows([]string{"id", "email", "name", "inactivity_warned_at", "lang"}).AddRow("acc1", "a@b.c", c.name, nil, c.lang))
		mock.ExpectExec("update `accounts` set `inactivity_warned_at` = \\? where `id` = \\? and `inactivity_warned_at` is null").
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery("select a.`id`, a.`email`, a.`name`, a.`inactivity_warned_at`, a.`lang` from `accounts` a").
			WillReturnRows(sqlmock.NewRows([]string{"id", "email", "name", "inactivity_warned_at", "lang"}))

		now := time.Date(2026, 12, 3, 10, 0, 0, 0, time.UTC)
		var to, subject, body string
		a.inactivityPass(now, func(t, s, b string) error { to, subject, body = t, s, b; return nil }, nil)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		ws, wb := legacyInactivity(c.name, "a@b.c", a.tld, now)
		if to != "a@b.c" || subject != ws || body != wb {
			t.Errorf("%+v: sent %q %q %q, want %q %q", c, to, subject, body, ws, wb)
		}
		if !strings.Contains(subject, "2 January 2027") {
			t.Errorf("subject %q: a month after 3 December 2026 is 2 January 2027", subject)
		}
	}
}

// With a second language carried (a draft build: make i18n DRAFT=1 adds
// the pseudo-locale qps), every email follows the language it is given
// and keeps the links, the address and the name it carries. Skipped in a
// build that ships English only.
func TestEmailsFollowTheLanguage(t *testing.T) {
	const lang = "qps"
	if i18n.Normalize(lang) != lang {
		t.Skip("this build carries no pseudo-locale (make i18n DRAFT=1)")
	}
	a := &Accounts{tld: "off-the.cloud"}
	link := "https://off-the.cloud/account#verify=Qx-_9aZ"
	now := time.Date(2026, 10, 11, 3, 4, 5, 0, time.UTC)
	ins, inb := a.inactivityEmail(lang, "Ann", "a@b.c", now.Add(cWarnAhead))
	ens, enb := a.inactivityEmail("en", "Ann", "a@b.c", now.Add(cWarnAhead))
	for _, c := range []struct {
		kind, got, english string
		keep               []string
	}{
		{"verification subject", verificationSubject(lang), verificationSubject("en"), nil},
		{"verification body", a.verificationBody(lang, "Ann", link), a.verificationBody("en", "Ann", link), []string{"Ann", link, "- Off The Cloud\nhttps://off-the.cloud\n"}},
		{"reset subject", resetSubject(lang), resetSubject("en"), nil},
		{"reset body", a.resetBody(lang, "", link), a.resetBody("en", "", link), []string{link}},
		{"inactivity subject", ins, ens, []string{"2026"}},
		{"inactivity body", inb, enb, []string{"Ann", "a@b.c", "https://off-the.cloud/account", "2026", "\n\nOff The Cloud\n"}},
	} {
		if c.got == c.english || strings.Contains(c.got, "Hello") || strings.Contains(c.got, "November") {
			t.Errorf("%s is still English: %q", c.kind, c.got)
		}
		for _, k := range c.keep {
			if !strings.Contains(c.got, k) {
				t.Errorf("%s lost %q: %q", c.kind, k, c.got)
			}
		}
	}
}
