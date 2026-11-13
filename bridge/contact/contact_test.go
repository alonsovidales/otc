// SPDX-License-Identifier: AGPL-3.0-or-later

package contact

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/bridge/mailer"
)

var alice = dao.ContactRequest{Id: 42, Name: "Alice Smith", Email: "alice@example.com", Reason: "help",
	Message: "How do I set up the bridge?\n\nIt says no account.", Created: time.Date(2026, 11, 13, 18, 4, 5, 0, time.UTC)}

func TestNotification(t *testing.T) {
	m := Notification("info@off-the.cloud", "off-the.cloud", "prod", alice)
	if m.To != "info@off-the.cloud" || m.ReplyTo != "alice@example.com" || m.Bcc != "" || m.Personal || m.Attachment != nil {
		t.Errorf("%+v", m)
	}
	if m.Subject != "[OTC contact] help: Alice Smith" {
		t.Errorf("subject %q", m.Subject)
	}
	for _, want := range []string{
		"Message 42 from the contact form\n",
		"Name:   Alice Smith\n", "Email:  alice@example.com\n", "Reason: help\n",
		"\nHow do I set up the bridge?\n\nIt says no account.\n",
		"Reply to this email to answer Alice Smith at alice@example.com.",
		"https://off-the.cloud/admin#messages",
		"sudo otc_bridge prod reply 42 (the answer on stdin), or mark-read 42",
	} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("the body lacks %q:\n%s", want, m.Body)
		}
	}
}

func noControl(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' }) < 0
}

// Nothing a stranger types reaches a header as it is, nor starts a line of
// the notification's own.
func TestNotificationFlattensWhatAStrangerTyped(t *testing.T) {
	c := alice
	c.Name = "Bob\r\nBcc: evil@example.net\x1b[2J"
	c.Reason = "help\nX-Spam: no"
	c.Message = "line one\r\nline two\rline three\x00\u202e"
	m := Notification("info@off-the.cloud", "off-the.cloud", "prod", c)
	if !noControl(m.Subject) || m.Subject != "[OTC contact] help X-Spam: no: Bob Bcc: evil@example.net\uFFFD[2J" {
		t.Errorf("subject %q", m.Subject)
	}
	for _, want := range []string{"Name:   Bob Bcc: evil@example.net\uFFFD[2J\n", "Reason: help X-Spam: no\n", "\nline one\nline two\nline three\uFFFD\uFFFD\n"} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("the body lacks %q:\n%q", want, m.Body)
		}
	}
	if strings.ContainsAny(m.Body, "\r\x00\x1b") {
		t.Errorf("the body %q", m.Body)
	}
}

// A message stored before the form refused such an address gets no
// Reply-To, which the mailer would refuse along with the whole email.
func TestNotificationWithoutAReplyTo(t *testing.T) {
	c := alice
	c.Email = "alice@example.com, evil@example.net"
	m := Notification("info@off-the.cloud", "", "prod", c)
	if m.ReplyTo != "" || !strings.Contains(m.Body, "no Reply-To") || strings.Contains(m.Body, "https://") {
		t.Errorf("%+v", m)
	}
}

func TestNotifierSendsInTheBackground(t *testing.T) {
	sent := make(chan mailer.Mail, 1)
	release := make(chan struct{})
	n := &Notifier{To: "info@off-the.cloud", Site: "off-the.cloud", Env: "prod", Send: func(m mailer.Mail) error {
		<-release
		sent <- m
		return errors.New("smtp: no answer")
	}}
	done := make(chan struct{})
	go func() { n.Notify(alice); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Notify waited for the SMTP server")
	}
	close(release)
	select {
	case m := <-sent:
		if m.ReplyTo != "alice@example.com" {
			t.Errorf("%+v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was sent")
	}
}

// Past the email budget's share for notes, nothing is sent; without a
// mailer, nothing either.
func TestNotifierKeepsToTheBudget(t *testing.T) {
	var sends atomic.Int32
	n := &Notifier{To: "info@off-the.cloud", Allow: func() bool { return false }, Send: func(mailer.Mail) error { sends.Add(1); return nil }}
	n.Notify(alice)
	(*Notifier)(nil).Notify(alice)
	(&Notifier{To: "info@off-the.cloud"}).Notify(alice)
	time.Sleep(50 * time.Millisecond)
	if sends.Load() != 0 {
		t.Errorf("%d sent", sends.Load())
	}
}
