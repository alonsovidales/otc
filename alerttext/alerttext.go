// SPDX-License-Identifier: AGPL-3.0-or-later

// Package alerttext is the text of the device's alerts - the rows of
// Notifications that the device writes itself (an error, a disk that
// stopped, an update) - written twice (docs/i18n.md, "Text the device
// writes"):
//
//   - in English, as notifications.title and details have always held it:
//     what a request without a language gets, and every app from before
//     the localized ones;
//   - as catalog keys with their arguments: notifications.msg_key and
//     msg_args for the title, msg_lines for the details, which
//     ListNotifications renders in each request's language (Render).
//
// The English is the catalog's, rendered once when the alert is made, so
// the two always say the same thing. A key written to the database is
// marked "stored" in the catalog: rows written long ago must still render.
package alerttext

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
)

// Alert is one alert's text: its English and, unless it has only its
// English, the same as catalog messages. It is also what wifiwatch keeps
// for an alert the database refused, so its JSON form is kept stable.
type Alert struct {
	// Title and Detail are the English: notifications.title and the
	// detail of its details line.
	Title  string `json:"title"`
	Detail string `json:"detail"`
	// Key and Args are the title as a catalog message (Args as msg_args
	// holds them). No Key: the alert has only its English, which every
	// language then shows.
	Key  string          `json:"key,omitempty"`
	Args json.RawMessage `json:"args,omitempty"`
	// DetailKey and DetailArgs are the detail as a catalog message. A
	// keyed alert without a DetailKey has a raw Detail (an error's own
	// words, which no catalog has), the same in every language, or none.
	DetailKey  string          `json:"detail_key,omitempty"`
	DetailArgs json.RawMessage `json:"detail_args,omitempty"`
}

// New is the alert titled title with detail as its detail; a zero detail
// (no Key) is an alert without one.
func New(title, detail i18n.Msg) Alert {
	a := Alert{Title: EnglishOf(title)}
	if detail.Key != "" {
		a.Detail = EnglishOf(detail)
	}
	args, ok := storedArgs(title)
	if !ok {
		return English(a.Title, a.Detail)
	}
	a.Key, a.Args = title.Key, args
	if detail.Key != "" {
		if a.DetailArgs, ok = storedArgs(detail); !ok {
			return English(a.Title, a.Detail)
		}
		a.DetailKey = detail.Key
	}
	return a
}

// WithRaw is the alert titled title whose detail is raw: text no catalog
// has (an error's own words), shown as it is in every language.
func WithRaw(title i18n.Msg, raw string) Alert {
	a := New(title, i18n.Msg{})
	a.Detail = raw
	return a
}

// English is an alert that has only its English: every language shows it
// as it is. For text no key can say (wifiwatch's alert kept by a binary
// from before keys, several missing disks).
func English(title, detail string) Alert {
	return Alert{Title: title, Detail: detail}
}

// Keyed reports whether the alert has its catalog form.
func (a Alert) Keyed() bool { return a.Key != "" }

// storedArgs is m's arguments as msg_args stores them, once m is checked
// against the catalog: a key it lacks, or arguments that don't fit, is a
// mistake in the code, logged by key, and the alert keeps only its
// English.
func storedArgs(m i18n.Msg) (json.RawMessage, bool) {
	if err := m.Check(); err != nil {
		log.Error("alert text: a message that doesn't fit its key, kept in English only:", m.Key)
		return nil, false
	}
	raw, err := m.MarshalArgs()
	if err != nil {
		log.Error("alert text: arguments that can't be stored, kept in English only:", m.Key)
		return nil, false
	}
	return raw, true
}

// EnglishOf is m in English as the device has always written it: the
// catalog's English, without the isolation marks the runtime puts around
// user-controlled arguments (a file name). The English in the title and
// details columns never had them; inside the marks the runtime has
// already taken out any directional control characters of the name.
func EnglishOf(m i18n.Msg) string {
	return strings.Map(func(r rune) rune {
		if r == fsi || r == pdi {
			return -1
		}
		return r
	}, m.Render("en"))
}

// The isolation marks i18n puts around a user argument.
const (
	fsi = '⁨'
	pdi = '⁩'
)

// Line is one line of an alert's details as msg_lines holds them: when it
// happened (At, UTC RFC 3339) and what happened (Key/Args, the title it
// had) with its detail - a catalog message (DetailKey/DetailArgs) or raw
// text (Detail). A row's details without times (a storage or an update
// alert) are one line with only its detail.
type Line struct {
	At         string          `json:"at,omitempty"`
	Key        string          `json:"key,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`
	Detail     string          `json:"detail,omitempty"`
	DetailKey  string          `json:"detail_key,omitempty"`
	DetailArgs json.RawMessage `json:"detail_args,omitempty"`
}

// EnglishLine is a's line in an error row's English details, at t: "15:04
// <title>: <detail>", the time in the device's own zone.
func (a Alert) EnglishLine(t time.Time) string {
	line := t.Format(cClock) + " " + a.Title
	if a.Detail != "" {
		line += ": " + a.Detail
	}
	return line
}

// cClock is how a details line writes its time.
const cClock = "15:04"

// Line is a's line at t, as msg_lines holds it (EnglishLine's twin).
func (a Alert) Line(t time.Time) Line {
	l := a.DetailLine()
	l.At, l.Key, l.Args = t.UTC().Format(time.RFC3339), a.Key, a.Args
	return l
}

// DetailLine is a's detail alone: the details of a row whose English is
// the detail itself, without a time or a title (AddStorageNotification,
// AddUpdateNotification).
func (a Alert) DetailLine() Line {
	if a.DetailKey != "" {
		return Line{DetailKey: a.DetailKey, DetailArgs: a.DetailArgs}
	}
	return Line{Detail: a.Detail}
}

// What msg_lines holds at most, like details' own cap: later lines are
// left out (the English details stop growing at their own cap, and the
// row keeps counting).
const (
	MaxLines      = 500
	MaxLinesBytes = 60000
)

// Lines encodes lines as msg_lines holds them; ok is false when they
// don't fit its caps.
func Lines(lines ...Line) ([]byte, bool) {
	if len(lines) > MaxLines {
		return nil, false
	}
	raw, err := json.Marshal(lines)
	if err != nil || len(raw) > MaxLinesBytes {
		return nil, false
	}
	return raw, true
}

// AppendLine is stored (msg_lines) with l added at its end; ok is false
// when that would pass the caps, or stored isn't a list of lines (then
// left as it is: Render shows that row's English).
func AppendLine(stored []byte, l Line) ([]byte, bool) {
	var items []json.RawMessage
	if !bytes.HasPrefix(bytes.TrimSpace(stored), []byte("[")) || json.Unmarshal(stored, &items) != nil || len(items) >= MaxLines {
		return nil, false
	}
	raw, err := json.Marshal(l)
	if err != nil {
		return nil, false
	}
	items = append(items, raw)
	var b bytes.Buffer
	b.WriteByte('[')
	for i, it := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(it)
	}
	b.WriteByte(']')
	if b.Len() > MaxLinesBytes {
		return nil, false
	}
	return b.Bytes(), true
}

// Render is a stored alert in lang: its title from key and args, its
// details from lines (one per line), or title and details - the English
// stored with them - as they are: when lang renders as English (a request
// without a language gets exactly what it always did), when the row has
// no key, or when anything in it doesn't match the catalog (a key this
// build doesn't have, arguments that don't fit their key, a line that
// doesn't parse). Never a mix of the two.
func Render(lang, key string, args, lines []byte, title, details string) (string, string) {
	if key == "" || devtext.English(lang) {
		return title, details
	}
	m, err := i18n.ParseStored(key, args)
	if err != nil {
		return title, details
	}
	var parsed []Line
	if len(bytes.TrimSpace(lines)) > 0 && json.Unmarshal(lines, &parsed) != nil {
		return title, details
	}
	if len(parsed) == 0 && details != "" {
		return title, details
	}
	out := make([]string, 0, len(parsed))
	for _, l := range parsed {
		s, ok := renderLine(lang, l)
		if !ok {
			return title, details
		}
		out = append(out, s)
	}
	return m.Render(lang), strings.Join(out, "\n")
}

// renderLine is one line of the details in lang; ok is false for a line
// that doesn't match the catalog, or of a shape this build doesn't write.
func renderLine(lang string, l Line) (string, bool) {
	var title, detail string
	if l.Key != "" {
		m, err := i18n.ParseStored(l.Key, l.Args)
		if err != nil {
			return "", false
		}
		title = m.Render(lang)
	}
	if l.DetailKey != "" {
		m, err := i18n.ParseStored(l.DetailKey, l.DetailArgs)
		if err != nil {
			return "", false
		}
		detail = m.Render(lang)
	} else {
		detail = l.Detail
	}
	switch {
	case l.At == "" && l.Key == "":
		return detail, true
	case l.At == "" || l.Key == "":
		return "", false
	}
	at, err := time.Parse(time.RFC3339, l.At)
	if err != nil {
		return "", false
	}
	clock := at.Local().Format(cClock)
	if detail == "" {
		return i18n.DevAlertLinePlain(clock, title).Render(lang), true
	}
	return i18n.DevAlertLineDetail(clock, title, detail).Render(lang), true
}
