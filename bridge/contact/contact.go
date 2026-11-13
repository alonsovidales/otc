// SPDX-License-Identifier: AGPL-3.0-or-later

// Package contact is what happens to a message from the public site's
// contact form once the bridge has stored it (issue #57, dao's
// contact_requests): it is mailed to the project's own address with the
// sender's as Reply-To (Notifier), and from the bridge's shell it is listed,
// answered and marked read (Shell: `otc_bridge <env> messages`, `reply` and
// `mark-read`), as the admin panel's Messages tab does.
package contact

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/bridge/mailer"
	"github.com/alonsovidales/otc/log"
)

// Notifier mails every stored contact message to the project
// (Notification).
type Notifier struct {
	// Send sends a mail: the mailer's SendMail.
	Send func(mailer.Mail) error
	// To is the project's own address, the mailer's (Self).
	To string
	// Site is the bridge's own host ([otc-api] tld), for the admin panel's
	// link, and Env the environment its shell commands take (os.Args[1]).
	Site, Env string
	// Allow takes one email from the node's budget for notes to the
	// project (accounts.AllowNote); nil allows every one.
	Allow func() bool
}

// Notify mails c in the background, so the form's answer never waits for
// the SMTP server. A failure is only logged, by the message's id: c is
// stored already, in the admin panel's Messages tab. It is an email
// strangers can have the bridge send, so it comes out of the node's email
// budget as the new-account note does - the first to go when they send
// many - behind the form's own limits. A nil Notifier mails nothing.
func (n *Notifier) Notify(c dao.ContactRequest) {
	if n == nil || n.Send == nil || n.To == "" {
		return
	}
	if n.Allow != nil && !n.Allow() {
		log.Info("contact message", c.Id, "was not mailed: the email budget is running low")
		return
	}
	mail := Notification(n.To, n.Site, n.Env, c)
	send := n.Send
	go func() {
		if err := send(mail); err != nil {
			log.Error("could not mail contact message", c.Id, "to the project:", err)
			return
		}
		log.Info("contact message", c.Id, "mailed to the project")
	}()
}

// Notification is the email that tells the project about the stored
// message c, sent to self (the project's own address): the sender's
// address as Reply-To, so that answering it in a mail client answers them,
// and a subject naming the reason and the sender. What the sender typed
// goes in without control characters - the name, address and reason on
// one line each (oneLine), the message on its own lines (text). site is
// the bridge's host, for the admin panel's link ("" for none), and env the
// environment of the shell commands it names.
func Notification(self, site, env string, c dao.ContactRequest) mailer.Mail {
	name, email, reason := oneLine(c.Name), oneLine(c.Email), oneLine(c.Reason)
	m := mailer.Mail{
		To:      self,
		Subject: fmt.Sprintf("[OTC contact] %s: %s", reason, name), // i18n-ignore: an email to the project itself, read by its operators
	}
	var b strings.Builder
	if c.Id > 0 {
		fmt.Fprintf(&b, "Message %d from the contact form\n\n", c.Id)
	} else {
		fmt.Fprintf(&b, "A message from the contact form\n\n")
	}
	fmt.Fprintf(&b, "Name:   %s\nEmail:  %s\nReason: %s\n\n%s\n\n-- \n", name, email, reason, text(c.Message))
	if mailer.ValidAddress(c.Email) {
		m.ReplyTo = c.Email
		fmt.Fprintf(&b, "Reply to this email to answer %s at %s.\n", name, email)
	} else {
		fmt.Fprintf(&b, "This address can't be mailed as it is, so there is no Reply-To.\n")
	}
	if site != "" {
		fmt.Fprintf(&b, "It is also in the admin panel's Messages tab: https://%s/admin#messages\n", site)
	} else {
		fmt.Fprintf(&b, "It is also in the admin panel's Messages tab.\n")
	}
	if c.Id > 0 {
		fmt.Fprintf(&b, "From the bridge's shell: sudo otc_bridge %s reply %d (the answer on stdin), or mark-read %d.\n", env, c.Id, c.Id)
	}
	m.Body = b.String()
	return m
}

// hidden is a character that isn't text to show: a control character, or
// a bidi control, which reorders the text around it.
func hidden(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r)
}

// oneLine is s on one line, as a header or a line of the shell's shows it:
// every run of white space (a line break, a tab) one space, every other
// hidden character U+FFFD, the ends trimmed. Nothing a stranger typed ends
// a header, colours a terminal or moves its cursor.
func oneLine(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToValidUTF8(s, "\uFFFD") {
		switch {
		case unicode.IsSpace(r):
			space = b.Len() > 0
			continue
		case hidden(r):
			r = '\uFFFD'
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// text is a message's own text as it is shown: its lines kept - a CR LF,
// a lone CR, a NEL and a Unicode line or paragraph separator each one line
// break - and its tabs, every other hidden character U+FFFD.
func text(s string) string {
	s = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\u0085", "\n", "\u2028", "\n", "\u2029", "\n").Replace(strings.ToValidUTF8(s, "\uFFFD"))
	return strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && hidden(r) {
			return '\uFFFD'
		}
		return r
	}, s)
}
