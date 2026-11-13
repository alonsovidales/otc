// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mailer sends the bridge's emails - the account emails
// (verification, password reset), its notes to the project and an
// operator's answer to a contact message - through an SMTP submission
// server: Proton's, as info@off-the.cloud.
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
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/alonsovidales/otc/cfg"
)

var ErrNotConfigured = errors.New("email is not configured on this bridge ([smtp])")

// ErrInvalidHeader is a message refused before anything is sent: its
// Reply-To or Bcc isn't one address a header can carry as it is
// (ValidAddress), or its subject holds a control character. Its text
// never names the address: errors are logged.
var ErrInvalidHeader = errors.New("invalid header")

// errUnmailable is a recipient's address no header can carry as it is;
// sent as a RecipientError, it is refused for good (AddressRefused).
var errUnmailable = errors.New("not an address that can be mailed")

// RecipientError is a send that failed at the recipient (RCPT TO); its
// text is the underlying error's.
type RecipientError struct{ Err error }

func (e *RecipientError) Error() string { return e.Err.Error() }
func (e *RecipientError) Unwrap() error { return e.Err }

// badRecipient are the RFC 3463 (and 7505) statuses that say the
// recipient's address itself is refused for good: no such mailbox, no
// such domain, a malformed address, a mailbox that moved, a domain that
// takes no mail.
var badRecipient = map[string]bool{"5.1.1": true, "5.1.2": true, "5.1.3": true, "5.1.6": true, "5.1.10": true}

// AddressRefused is whether err is the recipient's address refused for
// good: one no header can carry (ValidAddress), or a 5xx reply to RCPT TO
// whose enhanced status code is one of badRecipient. Sending to that
// address again fails the same way. Anything else may clear up, or isn't
// about the address: no reply, a 4xx, a refused login or sender (some
// servers refuse a sender only at RCPT TO), a sending limit (5.7.X), or a
// 5xx without a status code to tell which.
func AddressRefused(err error) bool {
	var re *RecipientError
	if !errors.As(err, &re) {
		return false
	}
	if errors.Is(re.Err, errUnmailable) {
		return true
	}
	var te *textproto.Error
	if !errors.As(re.Err, &te) || te.Code < 500 || te.Code > 599 {
		return false
	}
	f := strings.Fields(te.Msg)
	return len(f) > 0 && badRecipient[f[0]]
}

// ValidAddress is whether s is one address a header can carry as it is:
// what net/mail reads as exactly s - no display name, comment, quoting or
// second address - in visible characters only. net/mail lets through any
// character past ASCII, a Unicode line separator, a bidi override or a
// no-break space among them; none belongs in an address.
func ValidAddress(s string) bool {
	if !utf8.ValidString(s) || strings.IndexFunc(s, func(r rune) bool {
		return !unicode.IsGraphic(r) || unicode.IsSpace(r)
	}) >= 0 {
		return false
	}
	a, err := mail.ParseAddress(s)
	return err == nil && a.Name == "" && a.Address == s
}

// noneOrValid is whether s is no address, or a ValidAddress.
func noneOrValid(s string) bool { return s == "" || ValidAddress(s) }

// headerText is whether s can be a header's text: valid UTF-8 without a
// control character - CR and LF above all, which end a header and start
// another - or a Unicode line or paragraph separator.
func headerText(s string) bool {
	return utf8.ValidString(s) && strings.IndexFunc(s, func(r rune) bool {
		return unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
	}) < 0
}

type Mailer struct {
	host, port, user, pass, from, fromName string
	// roots verifies the server's certificate: nil is the system's
	// (tests trust a server of their own).
	roots *x509.CertPool
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
// also where messages for the project (a device's logs, a contact
// message) go.
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

// Mail is one message, in plain text. To receives it, and Bcc (empty for
// none) a copy the message doesn't name; replies go to ReplyTo (empty: to
// the sender).
type Mail struct {
	To, Bcc, ReplyTo, Subject, Body string
	Attachment                      *Attachment
	// Personal is a message a person wrote, an operator's answer: it
	// isn't marked Auto-Submitted as the bridge's own emails are, which
	// helpdesks and list servers set aside as automatic.
	Personal bool
}

// Send mails a plain-text message to one address: STARTTLS (required), then
// AUTH PLAIN, within 30 seconds.
func (m *Mailer) Send(to, subject, body string) error {
	return m.SendMail(Mail{To: to, Subject: subject, Body: body})
}

// SendWith is Send with a Reply-To (empty for none) and an attachment.
func (m *Mailer) SendWith(to, replyTo, subject, body string, att *Attachment) error {
	return m.SendMail(Mail{To: to, ReplyTo: replyTo, Subject: subject, Body: body, Attachment: att})
}

// SendMail sends msg as Send does, to To and Bcc in one transaction: the
// server takes it for both or for neither. Nothing is sent when a header
// can't carry what msg puts in it.
func (m *Mailer) SendMail(msg Mail) error {
	if m == nil {
		return ErrNotConfigured
	}
	if !ValidAddress(msg.To) {
		return &RecipientError{errUnmailable}
	}
	if !noneOrValid(msg.Bcc) || !noneOrValid(msg.ReplyTo) || !headerText(msg.Subject) {
		return ErrInvalidHeader
	}
	if strings.EqualFold(msg.Bcc, msg.To) {
		msg.Bcc = "" // the copy would be the message itself
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
	if err := c.StartTLS(&tls.Config{ServerName: m.host, MinVersion: tls.VersionTLS12, RootCAs: m.roots}); err != nil {
		return err
	}
	if err := c.Auth(smtp.PlainAuth("", m.user, m.pass, m.host)); err != nil {
		return err
	}
	if err := c.Mail(m.from); err != nil {
		return err
	}
	if err := c.Rcpt(msg.To); err != nil {
		return &RecipientError{err}
	}
	if msg.Bcc != "" {
		// Never a RecipientError: AddressRefused is about To.
		if err := c.Rcpt(msg.Bcc); err != nil {
			return fmt.Errorf("the blind copy was refused: %w", err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(m.message(msg)); err != nil {
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

// message is msg as SendMail writes it after DATA. Bcc is in no header:
// the copy is the envelope's alone.
func (m *Mailer) message(msg Mail) []byte {
	idBytes := make([]byte, 12)
	_, _ = rand.Read(idBytes)
	domain := m.from[strings.LastIndex(m.from, "@")+1:]
	from := m.from
	if m.fromName != "" {
		from = mime.QEncoding.Encode("utf-8", m.fromName) + " <" + m.from + ">"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", msg.To)
	b.WriteString(folded("Subject", mime.QEncoding.Encode("utf-8", msg.Subject)))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s@%s>\r\n", hex.EncodeToString(idBytes), domain)
	if msg.ReplyTo != "" {
		fmt.Fprintf(&b, "Reply-To: %s\r\n", msg.ReplyTo)
	}
	b.WriteString("MIME-Version: 1.0\r\n")
	if !msg.Personal {
		b.WriteString("Auto-Submitted: auto-generated\r\n")
	}
	encoding, text := textPart(msg.Body)
	att := msg.Attachment
	if att == nil {
		fmt.Fprintf(&b, "Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: %s\r\n\r\n", encoding)
		b.WriteString(text)
		return []byte(b.String())
	}
	boundary := "otc-" + hex.EncodeToString(idBytes)
	fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", boundary)
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: %s\r\n\r\n%s\r\n", boundary, encoding, text)
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

// cFoldAt is where a header line is folded: RFC 5322's 78 characters.
const cFoldAt = 78

// folded is the header line "name: value", folded at value's spaces where
// it would pass cFoldAt (RFC 5322, 2.2.3): unfolding takes each CRLF out
// and keeps the space after it, so the value reads the same. Never before
// the first word, nor into a line of white space alone. A long subject's
// encoded words are 75 characters at most, each with its own space.
func folded(name, value string) string {
	var b strings.Builder
	b.WriteString(name + ":")
	n, first := len(name)+1, true
	for _, word := range strings.Split(value, " ") {
		if !first && word != "" && n+1+len(word) > cFoldAt {
			b.WriteString("\r\n")
			n = 0
		}
		b.WriteString(" " + word)
		n += 1 + len(word)
		first = false
	}
	return b.String() + "\r\n"
}

// cMaxLine is the longest line SMTP carries, in bytes, without its CRLF
// (RFC 5321, 4.5.3.1.6).
const cMaxLine = 998

// textPart is body ready for a text part, with its Content-Transfer-Encoding:
// every line break a CRLF - a lone CR too, which servers read in different
// ways - and a NUL, which no server takes, U+FFFD; 8bit as it is, or
// quoted-printable when a line is longer than SMTP carries (a message
// typed without line breaks).
func textPart(body string) (encoding, text string) {
	body = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\x00", "\uFFFD").Replace(body)
	for line := range strings.SplitSeq(body, "\n") {
		if len(line) > cMaxLine {
			var b strings.Builder
			w := quotedprintable.NewWriter(&b)
			_, _ = w.Write([]byte(body))
			_ = w.Close()
			return "quoted-printable", b.String()
		}
	}
	return "8bit", strings.ReplaceAll(body, "\n", "\r\n")
}
