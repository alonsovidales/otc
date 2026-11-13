// SPDX-License-Identifier: AGPL-3.0-or-later

package mailer

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/big"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/textproto"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestAddressRefused(t *testing.T) {
	rcpt := func(code int, msg string) error { return &RecipientError{Err: &textproto.Error{Code: code, Msg: msg}} }
	for _, c := range []struct {
		err  error
		want bool
	}{
		{rcpt(550, "5.1.1 <x@y.z>: Recipient address rejected: User unknown"), true},
		{rcpt(550, "5.1.2 Bad destination system address"), true},
		{rcpt(553, "5.1.3 Invalid recipient address"), true},
		{rcpt(556, "5.1.10 Recipient address has null MX"), true},
		{fmt.Errorf("sending: %w", rcpt(550, "5.1.1 no such user")), true},
		// May clear up, or not about the recipient's address.
		{rcpt(450, "4.1.1 Recipient address rejected: try again"), false},
		{rcpt(452, "4.5.3 Too many recipients"), false},
		{rcpt(550, "5.7.1 Daily user sending limit exceeded"), false},
		{rcpt(554, "5.7.1 <info@off-the.cloud>: Sender address rejected: not owned by user"), false},
		{rcpt(553, "5.1.8 <info@off-the.cloud>: Sender address rejected: Domain not found"), false},
		{rcpt(550, "Requested action not taken: mailbox unavailable"), false},
		{rcpt(550, ""), false},
		{&RecipientError{Err: errors.New("connection reset by peer")}, false},
		{&textproto.Error{Code: 535, Msg: "5.7.8 Authentication credentials invalid"}, false},
		{&textproto.Error{Code: 550, Msg: "5.1.1 not at RCPT TO"}, false},
		{ErrNotConfigured, false},
		{nil, false},
	} {
		if got := AddressRefused(c.err); got != c.want {
			t.Errorf("AddressRefused(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestValidAddress(t *testing.T) {
	for _, s := range []string{"a@b.c", "alice@example.com", "user+tag@example.co.uk", "jos\u00e9@b\u00fccher.example", "a@[192.0.2.1]"} {
		if !ValidAddress(s) {
			t.Errorf("ValidAddress(%q) = false", s)
		}
	}
	for _, s := range []string{
		"", "a", "a@", "@b.c", "a@b.c,c@d.e", "a@b.c, c@d.e", "Bob <a@b.c>", "<a@b.c>", "a@b.c (x)", `"a b"@c.d`,
		"a@b.c\r\nBcc: x@y.z", "a@b.c\nBcc: x@y.z", "a@b.c\r", "a\x00@b.c", "a@b.c\x0b", "a\x7f@b.c", "a@b.c\u0085",
		"a\u2028@b.c", "a\u202e@b.c", "a@b.c\u00a0", "a\u200d@b.c", "a b@c.d", "a@b@c.d", "a..b@c.d", "\xff@b.c",
	} {
		if ValidAddress(s) {
			t.Errorf("ValidAddress(%q) = true", s)
		}
	}
}

// Nothing a header can't carry gets to the server: the checks come before
// the dial, which would fail otherwise (no server on port 1).
func TestSendMailRefusesWhatAHeaderCantCarry(t *testing.T) {
	m := &Mailer{host: "127.0.0.1", port: "1", user: "info@x.example", pass: "p", from: "info@x.example"}
	ok := Mail{To: "a@b.example", Subject: "Hello", Body: "hi"}
	for name, c := range map[string]Mail{
		"Reply-To with a header after it": {ReplyTo: "a@b.example\r\nBcc: evil@c.example"},
		"Reply-To with a line feed":       {ReplyTo: "a@b.example\nX: y"},
		"two Reply-To addresses":          {ReplyTo: "a@b.example, evil@c.example"},
		"a Reply-To with a name":          {ReplyTo: "Evil <evil@c.example>"},
		"Bcc with a line feed":            {Bcc: "info@x.example\nX: y"},
		"subject with a header after it":  {Subject: "Hi\r\nBcc: evil@c.example"},
		"subject with a line feed":        {Subject: "Hi\nthere"},
		"subject with a NUL":              {Subject: "Hi\x00"},
		"subject with an escape":          {Subject: "Hi \x1b[2J"},
		"subject with a line separator":   {Subject: "Hi\u2028there"},
		"subject that isn't UTF-8":        {Subject: "Hi \xff"},
	} {
		msg := ok
		if c.ReplyTo != "" {
			msg.ReplyTo = c.ReplyTo
		}
		if c.Bcc != "" {
			msg.Bcc = c.Bcc
		}
		if c.Subject != "" {
			msg.Subject = c.Subject
		}
		if err := m.SendMail(msg); !errors.Is(err, ErrInvalidHeader) {
			t.Errorf("%s: %v, want ErrInvalidHeader", name, err)
		}
	}
	// A recipient no header can carry is refused for good, like a 5.1.1.
	for _, to := range []string{"a@b.example\r\nBcc: evil@c.example", "a@b.example, evil@c.example", "a\u2028@b.example", ""} {
		err := m.SendMail(Mail{To: to, Subject: "Hello", Body: "hi"})
		if !AddressRefused(err) {
			t.Errorf("To %q: %v, want an address refused for good", to, err)
		}
	}
	if err := (*Mailer)(nil).SendMail(ok); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("no mailer: %v", err)
	}
}

func TestMessageHeaders(t *testing.T) {
	m := &Mailer{from: "info@x.example", fromName: "Off The Cloud"}
	auto := string(m.message(Mail{To: "info@x.example", ReplyTo: "alice@b.example", Subject: "[OTC contact] help: Jos\u00e9", Body: "one\ntwo"}))
	head, body, _ := strings.Cut(auto, "\r\n\r\n")
	for _, want := range []string{"\r\nTo: info@x.example\r\n", "\r\nReply-To: alice@b.example\r\n", "\r\nAuto-Submitted: auto-generated\r\n",
		"\r\nSubject: =?utf-8?q?[OTC_contact]_help:_Jos=C3=A9?=\r\n", "\r\nContent-Transfer-Encoding: 8bit"} {
		if !strings.Contains(head+"\r\n", want) {
			t.Errorf("headers lack %q:\n%s", want, head)
		}
	}
	if body != "one\r\ntwo" {
		t.Errorf("body %q", body)
	}
	answer := string(m.message(Mail{To: "alice@b.example", Bcc: "info@x.example", ReplyTo: "info@x.example", Subject: "Re: your message", Body: "hi", Personal: true}))
	head, _, _ = strings.Cut(answer, "\r\n\r\n")
	if strings.Contains(strings.ToLower(head), "bcc") || strings.Contains(head, "Auto-Submitted") {
		t.Errorf("a person's answer with a blind copy:\n%s", head)
	}
	if !strings.Contains(head, "\r\nTo: alice@b.example\r\n") || !strings.Contains(head, "\r\nReply-To: info@x.example\r\n") {
		t.Errorf("a person's answer:\n%s", head)
	}
}

func TestTextPart(t *testing.T) {
	if enc, text := textPart("a\r\nb\nc\rd\x00e"); enc != "8bit" || text != "a\r\nb\r\nc\r\nd\uFFFDe" {
		t.Errorf("line breaks and NUL: %s %q", enc, text)
	}
	long := strings.Repeat("word ", 300) + "= the end"
	enc, text := textPart("Hi\n" + long)
	if enc != "quoted-printable" {
		t.Fatalf("a %d-byte line went %s", len(long), enc)
	}
	for line := range strings.SplitSeq(text, "\r\n") {
		if len(line) > 76 {
			t.Errorf("a line of %d bytes: %q", len(line), line)
		}
	}
	back, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(text)))
	if err != nil || string(back) != "Hi\r\n"+long {
		t.Errorf("decoded back: %v %q", err, back)
	}
}

// smtpSession is what the fake server took in one connection.
type smtpSession struct {
	rcpt []string
	data string
}

// fakeSMTP is a submission server on loopback that requires STARTTLS, as
// Proton's does, and refuses the recipients in refuse with a 5.1.1.
func fakeSMTP(t *testing.T, refuse ...string) (*Mailer, <-chan smtpSession) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	serverTLS := &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	sessions := make(chan smtpSession, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var s smtpSession
				defer func() { sessions <- s }()
				tp := textproto.NewConn(conn)
				tp.PrintfLine("220 fake ESMTP")
				secure := false
				for {
					line, err := tp.ReadLine()
					if err != nil {
						return
					}
					verb, arg, _ := strings.Cut(line, " ")
					switch strings.ToUpper(verb) {
					case "EHLO":
						if secure {
							tp.PrintfLine("250-fake\r\n250 AUTH PLAIN")
						} else {
							tp.PrintfLine("250-fake\r\n250-STARTTLS\r\n250 AUTH PLAIN")
						}
					case "STARTTLS":
						tp.PrintfLine("220 go ahead")
						tc := tls.Server(conn, serverTLS)
						if tc.Handshake() != nil {
							return
						}
						conn, tp, secure = tc, textproto.NewConn(tc), true
					case "AUTH":
						tp.PrintfLine("235 2.7.0 ok")
					case "MAIL":
						tp.PrintfLine("250 2.1.0 ok")
					case "RCPT":
						addr := strings.TrimSuffix(strings.TrimPrefix(arg, "TO:<"), ">")
						s.rcpt = append(s.rcpt, addr)
						if slices.Contains(refuse, addr) {
							tp.PrintfLine("550 5.1.1 <%s>: no such user", addr)
						} else {
							tp.PrintfLine("250 2.1.5 ok")
						}
					case "DATA":
						tp.PrintfLine("354 go ahead")
						b, err := tp.ReadDotBytes()
						if err != nil {
							return
						}
						s.data = string(b)
						tp.PrintfLine("250 2.0.0 queued")
					case "QUIT":
						tp.PrintfLine("221 bye")
						return
					default:
						tp.PrintfLine("250 ok")
					}
				}
			}()
		}
	}()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	return &Mailer{host: host, port: port, user: "info@x.example", pass: "secret", from: "info@x.example", roots: roots}, sessions
}

func session(t *testing.T, sessions <-chan smtpSession) smtpSession {
	t.Helper()
	select {
	case s := <-sessions:
		return s
	case <-time.After(10 * time.Second):
		t.Fatal("the server saw no session")
		return smtpSession{}
	}
}

// The blind copy is a second recipient of the same transaction, named in
// no header.
func TestSendMailSendsTheBlindCopy(t *testing.T) {
	m, sessions := fakeSMTP(t)
	err := m.SendMail(Mail{To: "alice@b.example", Bcc: "info@x.example", ReplyTo: "info@x.example", Subject: "Re: your message",
		Body: "Hello Alice,\n.\nA line with a dot alone.", Personal: true})
	if err != nil {
		t.Fatal(err)
	}
	s := session(t, sessions)
	if !slices.Equal(s.rcpt, []string{"alice@b.example", "info@x.example"}) {
		t.Errorf("recipients %q", s.rcpt)
	}
	head, body, _ := strings.Cut(s.data, "\n\n")
	if strings.Contains(strings.ToLower(head), "bcc") || strings.Contains(head, "Auto-Submitted") {
		t.Errorf("headers:\n%s", head)
	}
	if !strings.Contains(head, "To: alice@b.example\n") || !strings.Contains(head, "Reply-To: info@x.example\n") {
		t.Errorf("headers:\n%s", head)
	}
	if body != "Hello Alice,\n.\nA line with a dot alone.\n" {
		t.Errorf("body %q", body)
	}

	// No copy of a message to the project's own address.
	if err := m.SendMail(Mail{To: "info@x.example", Bcc: "INFO@x.example", Subject: "s", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	if s := session(t, sessions); !slices.Equal(s.rcpt, []string{"info@x.example"}) {
		t.Errorf("recipients %q", s.rcpt)
	}
}

// A refused copy sends nothing to anyone, and isn't taken for the
// recipient's address refused.
func TestARefusedBlindCopySendsNothing(t *testing.T) {
	m, sessions := fakeSMTP(t, "copy@x.example")
	err := m.SendMail(Mail{To: "alice@b.example", Bcc: "copy@x.example", Subject: "s", Body: "b"})
	if err == nil || AddressRefused(err) {
		t.Fatalf("a refused copy: %v", err)
	}
	if s := session(t, sessions); s.data != "" {
		t.Errorf("the message went: %q", s.data)
	}
	m2, sessions2 := fakeSMTP(t, "alice@b.example")
	if err := m2.SendMail(Mail{To: "alice@b.example", Bcc: "info@x.example", Subject: "s", Body: "b"}); !AddressRefused(err) {
		t.Errorf("a refused recipient: %v", err)
	}
	if s := session(t, sessions2); s.data != "" || len(s.rcpt) != 1 {
		t.Errorf("after the recipient's refusal: %+v", s)
	}
}

// A long subject - a name of control characters, each shown as U+FFFD, is
// nine encoded bytes a character - is folded into lines SMTP carries, and
// reads the same unfolded.
func TestLongSubjectsAreFolded(t *testing.T) {
	for _, subject := range []string{
		"[OTC contact] help: " + strings.Repeat("\uFFFD", 150),
		"[OTC fleet] 2 problems: " + strings.Repeat("bridge1 disk almost full, ", 7),
		"Short one",
		"A  double space",
	} {
		line := folded("Subject", mime.QEncoding.Encode("utf-8", subject))
		for part := range strings.SplitSeq(strings.TrimSuffix(line, "\r\n"), "\r\n") {
			if len(part) > cFoldAt || strings.TrimSpace(part) == "" {
				t.Errorf("%q: a line %q", subject, part)
			}
		}
		value := strings.TrimPrefix(strings.ReplaceAll(strings.TrimSuffix(line, "\r\n"), "\r\n", ""), "Subject: ")
		back, err := new(mime.WordDecoder).DecodeHeader(value)
		if err != nil || back != subject {
			t.Errorf("%q came back %q (%v)", subject, back, err)
		}
	}
	if got := folded("Subject", "Hello"); got != "Subject: Hello\r\n" {
		t.Errorf("%q", got)
	}
}
