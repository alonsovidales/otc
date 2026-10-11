// SPDX-License-Identifier: AGPL-3.0-or-later

package accounts

import (
	"strconv"
	"time"

	"github.com/alonsovidales/otc/i18n"
)

// The emails an account holder gets: the email confirmation, the password
// reset and the inactivity warning, written from the catalog (mail.*) in
// the account's language - accounts.lang, set at sign-up and changed only
// by its owner; "" (not known) or a language this build doesn't carry is
// English (docs/i18n.md). Plain text, as they always were: the catalog's
// text is data, and every link comes from here.
//
// Names and addresses go in as they are (the mail.* keys declare them as
// text, not user): the isolation marks a user argument gets are invisible
// in a browser, but a plain-text email reader may show them, spam filters
// score bidi controls, and the English stays byte for byte what it was.

// emailGreeting opens every account email on a line of its own: "Hello
// Ann," or, without a name (an Apple account that shared none), "Hello,".
func emailGreeting(lang, name string) string {
	if name != "" {
		return i18n.MailGreetingNamed(name).Render(lang)
	}
	return i18n.MailGreetingPlain().Render(lang)
}

// signature ends the verification and reset emails: the product's name
// and its website, in every language.
func (a *Accounts) signature() string {
	return "- Off The Cloud\nhttps://" + a.tld + "\n" // i18n-ignore: the product's name and address
}

func verificationSubject(lang string) string { return i18n.MailVerifySubject().Render(lang) }

// verificationBody is the verification email's text, carrying link.
func (a *Accounts) verificationBody(lang, name, link string) string {
	return emailGreeting(lang, name) + "\n\n" + i18n.MailVerifyBody(link).Render(lang) + "\n\n" + a.signature()
}

func resetSubject(lang string) string { return i18n.MailResetSubject().Render(lang) }

// resetBody is the password-reset email's text, carrying link.
func (a *Accounts) resetBody(lang, name, link string) string {
	return emailGreeting(lang, name) + "\n\n" + i18n.MailResetBody(link).Render(lang) + "\n\n" + a.signature()
}

// inactivityEmail is the warning that an account unused for five months
// (email, its address) is removed on removeOn unless it is used.
func (a *Accounts) inactivityEmail(lang, name, email string, removeOn time.Time) (subject, body string) {
	date := longDate(lang, removeOn)
	subject = i18n.MailInactivitySubject(date).Render(lang)
	body = emailGreeting(lang, name) + "\n\n" +
		i18n.MailInactivityBody(email, date, "https://"+a.tld+"/account").Render(lang) +
		"\n\nOff The Cloud\n" // i18n-ignore: the product's name, the signature
	return subject, body
}

// monthNames are mail.date.long's month names, January first.
var monthNames = [12]func() i18n.Msg{
	i18n.MailDateJanuary, i18n.MailDateFebruary, i18n.MailDateMarch, i18n.MailDateApril,
	i18n.MailDateMay, i18n.MailDateJune, i18n.MailDateJuly, i18n.MailDateAugust,
	i18n.MailDateSeptember, i18n.MailDateOctober, i18n.MailDateNovember, i18n.MailDateDecember,
}

// longDate writes t's date as the emails do, in t's location: "2 January
// 2027" in English (Go's "2 January 2006"), each language's own order and
// month names in the others. The year goes in as text: as a number the
// language's format would group it ("2,027").
func longDate(lang string, t time.Time) string {
	return i18n.MailDateLong(t.Day(), monthNames[t.Month()-1](), strconv.Itoa(t.Year())).Render(lang)
}
