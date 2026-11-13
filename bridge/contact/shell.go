// SPDX-License-Identifier: AGPL-3.0-or-later

package contact

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/bridge/mailer"
)

// The shell's commands, as `otc_bridge <env> <command>` runs them, and how
// each is written.
var usage = map[string]string{
	"messages":  "otc_bridge <env> messages [--all]",
	"reply":     "otc_bridge <env> reply <id> [--again]   (the answer is read from stdin)",
	"mark-read": "otc_bridge <env> mark-read <id>",
}

// IsCommand is whether name is one of the shell's commands.
func IsCommand(name string) bool {
	_, ok := usage[name]
	return ok
}

// Usage is how the three commands are written, a line each.
func Usage() string {
	return usage["messages"] + "\n" + usage["reply"] + "\n" + usage["mark-read"]
}

// ReplySubject is an answer's subject.
const ReplySubject = "Re: your message to Off The Cloud" // i18n-ignore: the sender's language isn't stored with the message, and the operator writes the answer in it

// cMaxAnswer bounds an answer read from stdin: a contact message is at most
// 4,000 bytes, and many times that is a file piped in by mistake.
const cMaxAnswer = 32 << 10

// What reply and mark-read refuse, wrapped with the message's id where
// there is one.
var (
	ErrNoSuchMessage = errors.New("no such message")
	ErrAlreadyRead   = errors.New("it is read already (answered, or marked read)")
	ErrUnmailable    = errors.New("its address can't be mailed")
	ErrEmptyAnswer   = errors.New("the answer is empty: nothing was sent")
	ErrLongAnswer    = fmt.Errorf("the answer is over %d KB: nothing was sent", cMaxAnswer>>10)
	ErrNotText       = errors.New("the answer isn't UTF-8 text: nothing was sent")
)

// UsageError is a command written wrong; its text is how to write it.
type UsageError struct{ Usage string }

func (e *UsageError) Error() string { return "usage: " + e.Usage }

// Command is one of the shell's, read from its arguments.
type Command struct {
	// Name is "messages", "reply" or "mark-read".
	Name string
	// ID is reply's and mark-read's message.
	ID int
	// All is messages --all, Again reply --again.
	All, Again bool
}

// Parse reads the command name and its arguments (os.Args after it).
func Parse(name string, args []string) (Command, error) {
	u, ok := usage[name]
	if !ok {
		return Command{}, &UsageError{Usage()}
	}
	cmd := Command{Name: name}
	var rest []string
	for _, a := range args {
		switch {
		case a == "--all" && name == "messages" && !cmd.All:
			cmd.All = true
		case a == "--again" && name == "reply" && !cmd.Again:
			cmd.Again = true
		default:
			rest = append(rest, a)
		}
	}
	if name == "messages" {
		if len(rest) != 0 {
			return Command{}, &UsageError{u}
		}
		return cmd, nil
	}
	if len(rest) != 1 {
		return Command{}, &UsageError{u}
	}
	id, err := strconv.Atoi(rest[0])
	if err != nil || id <= 0 || rest[0][0] == '+' {
		return Command{}, &UsageError{u}
	}
	cmd.ID = id
	return cmd, nil
}

// Store is what the commands read and write: dao.Dao's contact messages.
type Store interface {
	ContactRequest(id int) (*dao.ContactRequest, error)
	ContactRequestsOldestFirst(all bool) ([]dao.ContactRequest, error)
	SetContactRequestRead(id int, isRead bool) error
}

// Shell runs the operator's commands on the contact messages - from the
// bridge's shell only: nothing on the web reaches them. They read and
// write contact_requests, and an answer sends one email.
type Shell struct {
	Store Store
	// Send mails an answer (the mailer's SendMail) from Self, the
	// project's own address; nil when the bridge has no email ([smtp]).
	Send func(mailer.Mail) error
	Self string
	// In is an answer's text; Out is what a command prints, Err what it
	// says along the way.
	In       io.Reader
	Out, Err io.Writer
	// Interactive is whether In is a terminal: an answer shows the
	// message first, and how to end the text.
	Interactive bool
}

// Run runs cmd; its error is what to tell the operator.
func (s *Shell) Run(cmd Command) error {
	switch cmd.Name {
	case "messages":
		return s.messages(cmd.All)
	case "reply":
		return s.reply(cmd.ID, cmd.Again)
	case "mark-read":
		return s.markRead(cmd.ID)
	}
	return &UsageError{Usage()}
}

// messages prints the unread messages, or all of them, oldest first, and
// how many there were.
func (s *Shell) messages(all bool) error {
	list, err := s.Store.ContactRequestsOldestFirst(all)
	if err != nil {
		return fmt.Errorf("reading the messages: %w", err)
	}
	unread := 0
	for _, c := range list {
		writeMessage(s.Out, c)
		if !c.IsRead {
			unread++
		}
	}
	switch {
	case all && len(list) == 0:
		fmt.Fprintln(s.Out, "No messages.")
	case all:
		fmt.Fprintf(s.Out, "%s, %d unread.\n", plural(len(list), "message"), unread)
	case len(list) == 0:
		fmt.Fprintln(s.Out, "No unread messages.")
	default:
		fmt.Fprintf(s.Out, "%s.\n", plural(len(list), "unread message"))
	}
	return nil
}

// reply mails an answer read from In to message id's sender, from the
// project's address, which gets a blind copy and the replies, then marks
// it read. A message read already is answered only again: answered here
// once, or in the mailbox and marked read, it may need no second answer.
func (s *Shell) reply(id int, again bool) error {
	c, err := s.message(id)
	if err != nil {
		return err
	}
	if c.IsRead && !again {
		return fmt.Errorf("message %d: %w - add --again to answer it anyway", id, ErrAlreadyRead)
	}
	if !mailer.ValidAddress(c.Email) {
		return fmt.Errorf("message %d: %w (%s): answer it from the mailbox, then mark-read %d", id, ErrUnmailable, oneLine(c.Email), id)
	}
	if s.Send == nil || s.Self == "" {
		return mailer.ErrNotConfigured
	}
	if s.Interactive {
		writeMessage(s.Err, *c)
		fmt.Fprintf(s.Err, "Type the answer to %s, then Ctrl-D on a line of its own:\n", c.Email)
	}
	answer, err := readAnswer(s.In)
	if err != nil {
		return err
	}
	mail := mailer.Mail{To: c.Email, Bcc: s.Self, ReplyTo: s.Self, Subject: ReplySubject, Body: answer, Personal: true}
	if err := s.Send(mail); err != nil {
		return fmt.Errorf("the answer was not sent: %w", err)
	}
	if err := s.Store.SetContactRequestRead(id, true); err != nil {
		return fmt.Errorf("the answer went to %s, but message %d could not be marked read (mark-read %d): %w", c.Email, id, id, err)
	}
	fmt.Fprintf(s.Out, "The answer went to %s, with a copy to %s. Message %d is marked read.\n", c.Email, s.Self, id)
	return nil
}

// markRead marks message id read: spam, or a message answered from the
// mailbox.
func (s *Shell) markRead(id int) error {
	c, err := s.message(id)
	if err != nil {
		return err
	}
	if c.IsRead {
		fmt.Fprintf(s.Out, "Message %d was read already.\n", id)
		return nil
	}
	if err := s.Store.SetContactRequestRead(id, true); err != nil {
		return fmt.Errorf("marking message %d read: %w", id, err)
	}
	fmt.Fprintf(s.Out, "Message %d is marked read.\n", id)
	return nil
}

func (s *Shell) message(id int) (*dao.ContactRequest, error) {
	c, err := s.Store.ContactRequest(id)
	if err != nil {
		return nil, fmt.Errorf("reading message %d: %w", id, err)
	}
	if c == nil {
		return nil, fmt.Errorf("message %d: %w", id, ErrNoSuchMessage)
	}
	return c, nil
}

// readAnswer reads an answer: UTF-8 text without a NUL, at most cMaxAnswer
// bytes, with something in it besides white space; the line breaks before
// it and the white space after it go.
func readAnswer(in io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(in, cMaxAnswer+1))
	if err != nil {
		return "", fmt.Errorf("reading the answer: %w", err)
	}
	if len(b) > cMaxAnswer {
		return "", ErrLongAnswer
	}
	if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
		return "", ErrNotText
	}
	answer := strings.TrimRight(strings.TrimLeft(string(b), "\r\n"), " \t\r\n")
	if strings.TrimSpace(answer) == "" {
		return "", ErrEmptyAnswer
	}
	return answer + "\n", nil
}

// writeMessage prints c as one block: its id and whether it was read, when
// (UTC), who, the reason, then the message indented as git log indents a
// commit's, so no line of it passes for a block's head. What the sender
// typed reaches the terminal without control characters (oneLine, text).
func writeMessage(w io.Writer, c dao.ContactRequest) {
	state := "unread"
	if c.IsRead {
		state = "read"
	}
	fmt.Fprintf(w, "message %d (%s)\n", c.Id, state)
	fmt.Fprintf(w, "Date:   %s\n", c.Created.UTC().Format("2006-01-02 15:04:05 UTC"))
	fmt.Fprintf(w, "Name:   %s\n", oneLine(c.Name))
	fmt.Fprintf(w, "Email:  %s\n", oneLine(c.Email))
	fmt.Fprintf(w, "Reason: %s\n\n", oneLine(c.Reason))
	for line := range strings.SplitSeq(strings.TrimRight(text(c.Message), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			fmt.Fprintln(w)
			continue
		}
		fmt.Fprintf(w, "    %s\n", line)
	}
	fmt.Fprintln(w)
}

// plural is n and what, "s" added when n isn't 1.
func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return strconv.Itoa(n) + " " + what + "s"
}
