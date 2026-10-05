// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mailer sends the bridge's account emails (verification, password
// reset) through an SMTP submission server - Proton's, as info@off-the.cloud.
//
// Config, [smtp] in the bridge's ini:
//
//	host=smtp.protonmail.ch
//	port=587
//	username=info@off-the.cloud
//	password-file=/etc/otc/smtp-token   (0600, the service's user; never in the ini)
//	from=info@off-the.cloud
//	from-name=Off The Cloud
//
// Without it the bridge runs, and every send fails with ErrNotConfigured.
package mailer

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"os"
	"strings"
	"time"

	"github.com/alonsovidales/otc/cfg"
)

var ErrNotConfigured = errors.New("email is not configured on this bridge ([smtp])")

type Mailer struct {
	host, port, user, pass, from, fromName string
}

// Init reads [smtp]; nil when the section or its password file is missing.
func Init() (*Mailer, error) {
	if !cfg.HasSection("smtp") {
		return nil, nil
	}
	m := &Mailer{
		host:     cfg.GetStr("smtp", "host"),
		port:     cfg.GetStr("smtp", "port"),
		user:     cfg.GetStr("smtp", "username"),
		from:     cfg.GetStr("smtp", "from"),
		fromName: cfg.GetStr("smtp", "from-name"),
	}
	if m.port == "" {
		m.port = "587"
	}
	if m.from == "" {
		m.from = m.user
	}
	raw, err := os.ReadFile(cfg.GetStr("smtp", "password-file"))
	if err != nil {
		return nil, fmt.Errorf("reading the SMTP password file: %w", err)
	}
	m.pass = strings.TrimSpace(string(raw))
	if m.host == "" || m.user == "" || m.pass == "" {
		return nil, errors.New("[smtp] needs host, username and a password-file")
	}
	return m, nil
}

// Self is the address mail is sent from - the project's own, which is
// also where messages for the project (a device's logs) go.
func (m *Mailer) Self() string {
	if m == nil {
		return ""
	}
	return m.from
}

// Attachment is a file sent with a message.
type Attachment struct {
	Name, ContentType string
	Data              []byte
}

// Send mails a plain-text message to one address: STARTTLS (required), then
// AUTH PLAIN, within 30 seconds.
func (m *Mailer) Send(to, subject, body string) error {
	return m.SendWith(to, "", subject, body, nil)
}

// SendWith is Send with a Reply-To (empty for none) and an attachment.
func (m *Mailer) SendWith(to, replyTo, subject, body string, att *Attachment) error {
	if m == nil {
		return ErrNotConfigured
	}
	if strings.ContainsAny(to+replyTo, "\r\n") || strings.ContainsAny(subject, "\r\n") {
		return errors.New("invalid header")
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(m.host, m.port), 15*time.Second)
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	c, err := smtp.NewClient(conn, m.host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); !ok {
		return errors.New("the SMTP server offers no STARTTLS")
	}
	if err := c.StartTLS(&tls.Config{ServerName: m.host, MinVersion: tls.VersionTLS12}); err != nil {
		return err
	}
	if err := c.Auth(smtp.PlainAuth("", m.user, m.pass, m.host)); err != nil {
		return err
	}
	if err := c.Mail(m.from); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(m.message(to, replyTo, subject, body, att)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	// Close read the server's answer to the message: it is accepted. A
	// failed goodbye after that is no failed send - a caller would send
	// it again, or drop the link it carries.
	_ = c.Quit()
	return nil
}

func (m *Mailer) message(to, replyTo, subject, body string, att *Attachment) []byte {
	idBytes := make([]byte, 12)
	_, _ = rand.Read(idBytes)
	domain := m.from[strings.LastIndex(m.from, "@")+1:]
	from := m.from
	if m.fromName != "" {
		from = mime.QEncoding.Encode("utf-8", m.fromName) + " <" + m.from + ">"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s@%s>\r\n", hex.EncodeToString(idBytes), domain)
	if replyTo != "" {
		fmt.Fprintf(&b, "Reply-To: %s\r\n", replyTo)
	}
	b.WriteString("MIME-Version: 1.0\r\nAuto-Submitted: auto-generated\r\n")
	text := strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n")
	if att == nil {
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
		b.WriteString(text)
		return []byte(b.String())
	}
	boundary := "otc-" + hex.EncodeToString(idBytes)
	fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", boundary)
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n", boundary, text)
	fmt.Fprintf(&b, "--%s\r\nContent-Type: %s\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename=%q\r\n\r\n", boundary, att.ContentType, att.Name)
	enc := base64.StdEncoding.EncodeToString(att.Data)
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return []byte(b.String())
}
