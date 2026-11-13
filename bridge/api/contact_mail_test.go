// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/bridge/accounts"
	"github.com/alonsovidales/otc/bridge/contact"
	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/bridge/mailer"
)

// contactAPI is an API with a stand-in database and the notifier given.
func contactAPI(t *testing.T, notes *contact.Notifier) (*API, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &API{muxHTTPServer: http.NewServeMux(), dao: dao.NewWithDB(db), lastContactByAddr: map[string]time.Time{}, contactNotes: notes}, mock
}

func postContact(api *API, body, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/contact", strings.NewReader(body))
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	api.submitContact(rec, req)
	return rec
}

func waitMail(t *testing.T, sent <-chan mailer.Mail) mailer.Mail {
	t.Helper()
	select {
	case m := <-sent:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was mailed")
		return mailer.Mail{}
	}
}

// A stored message is mailed to the project's own address, the sender's as
// Reply-To.
func TestContactMessageIsMailedToTheProject(t *testing.T) {
	sent := make(chan mailer.Mail, 1)
	api, mock := contactAPI(t, &contact.Notifier{To: "info@off-the.cloud", Site: "off-the.cloud", Env: "prod",
		Send: func(m mailer.Mail) error { sent <- m; return nil }})
	mock.ExpectExec("insert into `contact_requests`").
		WithArgs("Alice Smith", "alice@example.com", "help", "How do I set up the bridge?\nThanks").
		WillReturnResult(sqlmock.NewResult(42, 1))
	rec := postContact(api, `{"name":" Alice Smith ","email":"alice@example.com","reason":"help","message":"How do I set up the bridge?\nThanks"}`, "203.0.113.7:1111")
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	m := waitMail(t, sent)
	if m.To != "info@off-the.cloud" || m.ReplyTo != "alice@example.com" || m.Subject != "[OTC contact] help: Alice Smith" || m.Bcc != "" {
		t.Errorf("%+v", m)
	}
	for _, want := range []string{"Message 42 from the contact form", "Name:   Alice Smith", "Email:  alice@example.com", "Reason: help",
		"How do I set up the bridge?\nThanks", "https://off-the.cloud/admin#messages", "sudo otc_bridge prod reply 42"} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("the body lacks %q:\n%s", want, m.Body)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// The form's answer doesn't wait for the SMTP server, and an email that
// fails loses nothing: the message was stored before it.
func TestContactMailNeverHoldsTheAnswerNorLosesTheMessage(t *testing.T) {
	release := make(chan struct{})
	tried := make(chan struct{}, 1)
	api, mock := contactAPI(t, &contact.Notifier{To: "info@off-the.cloud", Send: func(mailer.Mail) error {
		<-release
		tried <- struct{}{}
		return errors.New("smtp: 421 service not available")
	}})
	defer close(release)
	mock.ExpectExec("insert into `contact_requests`").WillReturnResult(sqlmock.NewResult(7, 1))
	answered := make(chan int, 1)
	go func() {
		answered <- postContact(api, `{"name":"a","email":"a@b.example","message":"hi"}`, "203.0.113.7:1111").Code
	}()
	select {
	case code := <-answered:
		if code != http.StatusCreated {
			t.Fatalf("answered %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the form waited for the email")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("the message wasn't stored first: %v", err)
	}
	release <- struct{}{}
	select {
	case <-tried:
	case <-time.After(5 * time.Second):
		t.Fatal("the email was never tried")
	}
}

// A message the database refused is not mailed (nothing to point at), and
// the form says so as before.
func TestContactNotStoredIsNotMailed(t *testing.T) {
	sent := make(chan mailer.Mail, 1)
	api, mock := contactAPI(t, &contact.Notifier{To: "info@off-the.cloud", Send: func(m mailer.Mail) error { sent <- m; return nil }})
	mock.ExpectExec("insert into `contact_requests`").WillReturnError(errors.New("driver: bad connection"))
	if rec := postContact(api, `{"name":"a","email":"a@b.example","message":"hi"}`, "203.0.113.7:1111"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("%d", rec.Code)
	}
	select {
	case m := <-sent:
		t.Errorf("mailed %+v", m)
	case <-time.After(100 * time.Millisecond):
	}
}

// An address the email's Reply-To couldn't carry as it is is refused before
// anything is stored or mailed. (White space at its ends is trimmed, as
// before: what is stored and mailed is the address without it.)
func TestContactRefusesAnAddressWithAHeaderInIt(t *testing.T) {
	sent := make(chan mailer.Mail, 1)
	api, mock := contactAPI(t, &contact.Notifier{To: "info@off-the.cloud", Send: func(m mailer.Mail) error { sent <- m; return nil }})
	for _, email := range []string{
		`a@b.example\r\nBcc: evil@c.example`, `a@b.example\nBcc: evil@c.example`, `a@b.ex\rample`, `a\u0000@b.example`,
		`a@b.ex\u000bample`, `a@b.ex\u0085ample`, `a\u2028b@c.example`, `a\u202e@b.example`, `a\u00a0b@c.example`,
		`a@b.example, evil@c.example`, `a@b.example,evil@c.example`, `Evil <evil@c.example>`, `<a@b.example>`, `a@b.example (x)`,
	} {
		// Refused before the cooldown: one address sends them all.
		rec := postContact(api, `{"name":"a","email":"`+email+`","message":"hi"}`, "203.0.113.7:1")
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"invalid_email"`) {
			t.Errorf("%s: %d %s", email, rec.Code, rec.Body.String())
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("something was stored: %v", err)
	}
	select {
	case m := <-sent:
		t.Errorf("mailed %+v", m)
	default:
	}
}

// What a stranger types into the name and reason reaches the email's
// subject on one line, with no control character.
func TestContactSubjectCarriesNoHeaderOfTheSenders(t *testing.T) {
	sent := make(chan mailer.Mail, 1)
	api, mock := contactAPI(t, &contact.Notifier{To: "info@off-the.cloud", Send: func(m mailer.Mail) error { sent <- m; return nil }})
	mock.ExpectExec("insert into `contact_requests`").WillReturnResult(sqlmock.NewResult(8, 1))
	rec := postContact(api, `{"name":"Bob\r\nBcc: evil@c.example","email":"bob@b.example","reason":"help\u0000\u001b[2J","message":"hi"}`, "203.0.113.9:1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	m := waitMail(t, sent)
	if strings.ContainsAny(m.Subject, "\r\n\x00\x1b") || m.Subject != "[OTC contact] help\uFFFD\uFFFD[2J: Bob Bcc: evil@c.example" || m.ReplyTo != "bob@b.example" {
		t.Errorf("%q, Reply-To %q", m.Subject, m.ReplyTo)
	}
}

// The email to the project comes out of the node's email budget like the
// new-account note: with its spare spent, the message is stored and not
// mailed.
func TestContactMailKeepsToTheEmailBudget(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	acc := accounts.Init(dao.NewWithDB(db), []byte("k"), "off-the.cloud")
	for acc.AllowNote() {
	}
	sent := make(chan mailer.Mail, 1)
	api, mock := contactAPI(t, &contact.Notifier{To: "info@off-the.cloud", Allow: acc.AllowNote, Send: func(m mailer.Mail) error { sent <- m; return nil }})
	mock.ExpectExec("insert into `contact_requests`").WillReturnResult(sqlmock.NewResult(9, 1))
	if rec := postContact(api, `{"name":"a","email":"a@b.example","message":"hi"}`, "203.0.113.7:1"); rec.Code != http.StatusCreated {
		t.Fatalf("%d", rec.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
	select {
	case m := <-sent:
		t.Errorf("mailed past the budget: %+v", m)
	case <-time.After(100 * time.Millisecond):
	}
}
