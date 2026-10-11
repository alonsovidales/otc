// SPDX-License-Identifier: AGPL-3.0-or-later

// Package devtext gives the apps the device's own words in the language of
// the request that reads them, for the text its lower layers make rather
// than the reply code in websocket: the errors updater, bridgeaccess and
// tailscalefunnel return to a reply, and what the root-side runners write
// in their status files (docs/i18n.md, "Text the device writes").
//
// English stays byte for byte what it was. A request without a language
// (released apps, friends, the bridge) gets an Error's Error(), the
// catalog's English, and a runner's message exactly as the runner wrote it.
package devtext

import (
	"github.com/alonsovidales/otc/i18n"
)

// English reports whether lang renders as English: absent, English, or a
// language this build carries no text for.
func English(lang string) bool {
	code := i18n.Normalize(lang)
	return code == "" || code == "en"
}

// Error is an error whose text the apps show to a person. Error() is the
// English (what the log and a request without a language get); Localized
// is the same text in a request's language.
type Error struct {
	msg i18n.Msg
	// english, when set, is the English as it came (see WithEnglish);
	// otherwise it is msg rendered in English.
	english string
	// err is the cause, for errors.Is and errors.As. Its text is already
	// in msg, as the detail after a translated sentence.
	err error
}

// New is an error the apps show as m.
func New(m i18n.Msg) *Error { return &Error{msg: m} }

// Wrap is New for an error with a cause: m carries err's text (raw, in
// English: the system's own words) as its detail, and errors.Is and
// errors.As see err.
func Wrap(m i18n.Msg, err error) *Error { return &Error{msg: m, err: err} }

// WithEnglish is an error whose English is given rather than rendered:
// text that comes from elsewhere (the bridge's own words for one of its
// error codes), or that a user argument would change in English (the
// catalog isolates those with FSI/PDI marks, which today's English never
// had). m is the same text for every other language.
func WithEnglish(english string, m i18n.Msg) *Error {
	return &Error{msg: m, english: english}
}

func (e *Error) Error() string {
	if e.english != "" {
		return e.english
	}
	return e.msg.Render("en")
}

func (e *Error) Unwrap() error { return e.err }

// Localized is the error's text in lang (a request's language).
func (e *Error) Localized(lang string) string {
	if English(lang) {
		return e.Error()
	}
	return e.msg.Render(lang)
}

// localized is what Text looks for: an Error, or a runner's failure.
type localized interface {
	Localized(lang string) string
}

// Text is err's text in lang for a reply: Localized when err is one of
// this package's errors itself, else err.Error() as it is. Only err itself
// counts, not an error it wraps: a wrapper's own words would be lost.
func Text(lang string, err error) string {
	if l, ok := err.(localized); ok {
		return l.Localized(lang)
	}
	return err.Error()
}
