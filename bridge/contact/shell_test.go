// SPDX-License-Identifier: AGPL-3.0-or-later

package contact

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/bridge/mailer"
)

var contactCols = []string{"id", "name", "email", "reason", "message", "created", "is_read"}

// testShell is a Shell over the real dao.Dao on sqlmock, with a mailer
// that records what it is given (or fails with sendErr).
type testShell struct {
	*Shell
	mock    sqlmock.Sqlmock
	out     *bytes.Buffer
	sent    []mailer.Mail
	sendErr error
}

func newTestShell(t *testing.T, answer string) *testShell {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		db.Close()
	})
	ts := &testShell{mock: mock, out: &bytes.Buffer{}}
	ts.Shell = &Shell{Store: dao.NewWithDB(db), Self: "info@off-the.cloud", In: strings.NewReader(answer), Out: ts.out, Err: &bytes.Buffer{},
		Send: func(m mailer.Mail) error {
			ts.sent = append(ts.sent, m)
			return ts.sendErr
		}}
	return ts
}

func (ts *testShell) expectMessage(id int, read bool) {
	ts.mock.ExpectQuery("select `id`, `name`, `email`, `reason`, `message`, `created`, `is_read` from `contact_requests` where `id` = \\?").
		WithArgs(id).WillReturnRows(sqlmock.NewRows(contactCols).AddRow(id, "Alice Smith", "alice@example.com", "help", "How do I set up the bridge?", alice.Created, read))
}

func (ts *testShell) expectNoMessage(id int) {
	ts.mock.ExpectQuery("from `contact_requests` where `id` = \\?").WithArgs(id).WillReturnRows(sqlmock.NewRows(contactCols))
}

func (ts *testShell) expectMarkRead(id int) {
	ts.mock.ExpectExec("update `contact_requests` set `is_read` = \\? where `id` = \\?").WithArgs(true, id).WillReturnResult(sqlmock.NewResult(0, 1))
}

func run(t *testing.T, ts *testShell, args ...string) error {
	t.Helper()
	cmd, err := Parse(args[0], args[1:])
	if err != nil {
		t.Fatalf("%q: %v", args, err)
	}
	return ts.Run(cmd)
}

func TestParse(t *testing.T) {
	for _, c := range []struct {
		args []string
		want Command
	}{
		{[]string{"messages"}, Command{Name: "messages"}},
		{[]string{"messages", "--all"}, Command{Name: "messages", All: true}},
		{[]string{"reply", "42"}, Command{Name: "reply", ID: 42}},
		{[]string{"reply", "--again", "42"}, Command{Name: "reply", ID: 42, Again: true}},
		{[]string{"reply", "42", "--again"}, Command{Name: "reply", ID: 42, Again: true}},
		{[]string{"mark-read", "7"}, Command{Name: "mark-read", ID: 7}},
	} {
		if got, err := Parse(c.args[0], c.args[1:]); err != nil || got != c.want {
			t.Errorf("%q: %+v, %v", c.args, got, err)
		}
	}
	for _, args := range [][]string{
		{"messages", "x"}, {"messages", "--all", "--all"}, {"messages", "--again"},
		{"reply"}, {"reply", "x"}, {"reply", "0"}, {"reply", "-3"}, {"reply", "+3"}, {"reply", "1", "2"}, {"reply", "1", "--all"},
		{"mark-read"}, {"mark-read", "1", "--again"}, {"mark-read", "99999999999999999999999"}, {"nonsense"},
	} {
		var u *UsageError
		if _, err := Parse(args[0], args[1:]); !errors.As(err, &u) || !strings.HasPrefix(err.Error(), "usage: otc_bridge <env> ") {
			t.Errorf("%q: %v", args, err)
		}
	}
	if !IsCommand("reply") || IsCommand("set-admin-password") || IsCommand("prod") {
		t.Error("IsCommand")
	}
}

// messages prints the unread ones oldest first, a block each, and nothing a
// stranger typed reaches the terminal as it is.
func TestMessages(t *testing.T) {
	ts := newTestShell(t, "")
	ts.mock.ExpectQuery("select `id`, `name`, `email`, `reason`, `message`, `created`, `is_read` from `contact_requests` where not `is_read` order by `created`, `id`").
		WillReturnRows(sqlmock.NewRows(contactCols).
			AddRow(3, "Alice Smith", "alice@example.com", "help", "Line one\n\nmessage 9 (unread)", alice.Created, false).
			AddRow(5, "Evil\x1b]0;owned\x07", "e@example.net", "general", "\x1b[2Jcleared?\rover", alice.Created.Add(time.Hour), false))
	if err := run(t, ts, "messages"); err != nil {
		t.Fatal(err)
	}
	want := "message 3 (unread)\n" +
		"Date:   2026-11-13 18:04:05 UTC\n" +
		"Name:   Alice Smith\n" +
		"Email:  alice@example.com\n" +
		"Reason: help\n\n" +
		"    Line one\n\n" +
		"    message 9 (unread)\n\n" +
		"message 5 (unread)\n" +
		"Date:   2026-11-13 19:04:05 UTC\n" +
		"Name:   Evil\uFFFD]0;owned\uFFFD\n" +
		"Email:  e@example.net\n" +
		"Reason: general\n\n" +
		"    \uFFFD[2Jcleared?\n" +
		"    over\n\n" +
		"2 unread messages.\n"
	if got := ts.out.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestMessagesAll(t *testing.T) {
	ts := newTestShell(t, "")
	ts.mock.ExpectQuery("from `contact_requests` order by `created`, `id`$").
		WillReturnRows(sqlmock.NewRows(contactCols).
			AddRow(1, "A", "a@example.com", "help", "hi", alice.Created, true).
			AddRow(2, "B", "b@example.com", "press", "hello", alice.Created, false))
	if err := run(t, ts, "messages", "--all"); err != nil {
		t.Fatal(err)
	}
	if got := ts.out.String(); !strings.HasPrefix(got, "message 1 (read)\n") || !strings.Contains(got, "\nmessage 2 (unread)\n") || !strings.HasSuffix(got, "\n2 messages, 1 unread.\n") {
		t.Errorf("%s", got)
	}

	ts = newTestShell(t, "")
	ts.mock.ExpectQuery("where not `is_read`").WillReturnRows(sqlmock.NewRows(contactCols))
	if err := run(t, ts, "messages"); err != nil || ts.out.String() != "No unread messages.\n" {
		t.Errorf("%v %q", err, ts.out.String())
	}
}

// The answer goes to the sender from the project's address, with a blind
// copy to it and its replies to it; then the message is read.
func TestReply(t *testing.T) {
	ts := newTestShell(t, "\nHello Alice,\n\nThe bridge needs an account first.\n\n")
	ts.expectMessage(42, false)
	ts.expectMarkRead(42)
	if err := run(t, ts, "reply", "42"); err != nil {
		t.Fatal(err)
	}
	if len(ts.sent) != 1 {
		t.Fatalf("%d sent", len(ts.sent))
	}
	m := ts.sent[0]
	want := mailer.Mail{To: "alice@example.com", Bcc: "info@off-the.cloud", ReplyTo: "info@off-the.cloud", Subject: "Re: your message to Off The Cloud",
		Body: "Hello Alice,\n\nThe bridge needs an account first.\n", Personal: true}
	if m != want {
		t.Errorf("sent %+v\nwant %+v", m, want)
	}
	if got := ts.out.String(); got != "The answer went to alice@example.com, with a copy to info@off-the.cloud. Message 42 is marked read.\n" {
		t.Errorf("%q", got)
	}
}

func TestReplyRefusals(t *testing.T) {
	for name, c := range map[string]struct {
		answer string
		setup  func(*testShell)
		args   []string
		want   error
	}{
		"an unknown id":                {"Hi", func(ts *testShell) { ts.expectNoMessage(9) }, []string{"reply", "9"}, ErrNoSuchMessage},
		"an empty answer":              {" \n\t\n", func(ts *testShell) { ts.expectMessage(42, false) }, []string{"reply", "42"}, ErrEmptyAnswer},
		"no answer at all":             {"", func(ts *testShell) { ts.expectMessage(42, false) }, []string{"reply", "42"}, ErrEmptyAnswer},
		"a message read already":       {"Hi", func(ts *testShell) { ts.expectMessage(42, true) }, []string{"reply", "42"}, ErrAlreadyRead},
		"an answer too long":           {strings.Repeat("a", cMaxAnswer+1), func(ts *testShell) { ts.expectMessage(42, false) }, []string{"reply", "42"}, ErrLongAnswer},
		"an answer that isn't text":    {"Hi\x00there", func(ts *testShell) { ts.expectMessage(42, false) }, []string{"reply", "42"}, ErrNotText},
		"an answer that isn't UTF-8":   {"Hi \xff", func(ts *testShell) { ts.expectMessage(42, false) }, []string{"reply", "42"}, ErrNotText},
		"no mailer on this bridge":     {"Hi", func(ts *testShell) { ts.expectMessage(42, false); ts.Send = nil }, []string{"reply", "42"}, mailer.ErrNotConfigured},
		"an address that can't be one": {"Hi", func(ts *testShell) { ts.expectStored(7, "a@example.com, evil@example.net") }, []string{"reply", "7"}, ErrUnmailable},
	} {
		t.Run(name, func(t *testing.T) {
			ts := newTestShell(t, c.answer)
			c.setup(ts)
			if err := run(t, ts, c.args...); !errors.Is(err, c.want) {
				t.Errorf("%v, want %v", err, c.want)
			}
			if len(ts.sent) != 0 || ts.out.Len() != 0 {
				t.Errorf("sent %d, printed %q", len(ts.sent), ts.out.String())
			}
		})
	}
	// The longest answer allowed goes.
	ts := newTestShell(t, strings.Repeat("a", cMaxAnswer))
	ts.expectMessage(42, false)
	ts.expectMarkRead(42)
	if err := run(t, ts, "reply", "42"); err != nil || len(ts.sent) != 1 {
		t.Errorf("%v, %d sent", err, len(ts.sent))
	}
}

func (ts *testShell) expectStored(id int, email string) {
	ts.mock.ExpectQuery("from `contact_requests` where `id` = \\?").WithArgs(id).
		WillReturnRows(sqlmock.NewRows(contactCols).AddRow(id, "A", email, "help", "hi", alice.Created, false))
}

// --again answers a message read already, and it stays read.
func TestReplyAgain(t *testing.T) {
	ts := newTestShell(t, "One more thing.")
	ts.expectMessage(42, true)
	ts.expectMarkRead(42)
	if err := run(t, ts, "reply", "42", "--again"); err != nil {
		t.Fatal(err)
	}
	if len(ts.sent) != 1 || ts.sent[0].To != "alice@example.com" || ts.sent[0].Bcc != "info@off-the.cloud" {
		t.Errorf("%+v", ts.sent)
	}
}

// An answer that didn't go leaves the message unread; one that went but
// couldn't be marked says so.
func TestReplyFailures(t *testing.T) {
	ts := newTestShell(t, "Hi")
	ts.sendErr = errors.New("smtp: 451 try again later")
	ts.expectMessage(42, false)
	if err := run(t, ts, "reply", "42"); err == nil || !strings.Contains(err.Error(), "the answer was not sent") {
		t.Errorf("%v", err)
	}

	ts = newTestShell(t, "Hi")
	ts.expectMessage(42, false)
	ts.mock.ExpectExec("update `contact_requests`").WillReturnError(errors.New("driver: bad connection"))
	err := run(t, ts, "reply", "42")
	if err == nil || !strings.Contains(err.Error(), "the answer went to alice@example.com, but message 42 could not be marked read (mark-read 42)") || len(ts.sent) != 1 {
		t.Errorf("%v, %d sent", err, len(ts.sent))
	}
}

// At a terminal the message comes first, then how to end the answer - on
// stderr, out of what stdout prints.
func TestReplyAtATerminal(t *testing.T) {
	ts := newTestShell(t, "Hi")
	ts.Interactive = true
	ts.expectMessage(42, false)
	ts.expectMarkRead(42)
	if err := run(t, ts, "reply", "42"); err != nil {
		t.Fatal(err)
	}
	said := ts.Err.(*bytes.Buffer).String()
	if !strings.HasPrefix(said, "message 42 (unread)\n") || !strings.HasSuffix(said, "Type the answer to alice@example.com, then Ctrl-D on a line of its own:\n") {
		t.Errorf("%q", said)
	}
}

func TestMarkRead(t *testing.T) {
	ts := newTestShell(t, "")
	ts.expectMessage(42, false)
	ts.expectMarkRead(42)
	if err := run(t, ts, "mark-read", "42"); err != nil || ts.out.String() != "Message 42 is marked read.\n" {
		t.Errorf("%v %q", err, ts.out.String())
	}

	ts = newTestShell(t, "")
	ts.expectMessage(42, true)
	if err := run(t, ts, "mark-read", "42"); err != nil || ts.out.String() != "Message 42 was read already.\n" {
		t.Errorf("%v %q", err, ts.out.String())
	}

	ts = newTestShell(t, "")
	ts.expectNoMessage(9)
	if err := run(t, ts, "mark-read", "9"); !errors.Is(err, ErrNoSuchMessage) || err.Error() != "message 9: no such message" {
		t.Errorf("%v", err)
	}
	if len(ts.sent) != 0 {
		t.Error("mark-read sent mail")
	}
}
