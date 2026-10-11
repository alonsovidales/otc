// SPDX-License-Identifier: AGPL-3.0-or-later

package social

import (
	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
)

// The errors this package returns to the owner's requests are devtext
// errors (docs/i18n.md, "Text the device writes"): Error() is the English
// as it always read - the log and a request without a language get it -
// and the websocket layer shows the same text in a request's language.

// causeErr is a sentence of this package's own around another error, the
// cause: Error() is english, the text as it always read, and Localized
// renders the sentence in a request's language with the cause's own text
// in that language too (devtext.Text) where it has one - a files_manager
// error, say - rather than only in English.
type causeErr struct {
	english string
	msg     func(detail string) i18n.Msg
	cause   error
}

func wrapCause(english string, cause error, msg func(detail string) i18n.Msg) error {
	return &causeErr{english: english, msg: msg, cause: cause}
}

func (e *causeErr) Error() string { return e.english }

func (e *causeErr) Unwrap() error { return e.cause }

// Localized is the text in lang (devtext.Text).
func (e *causeErr) Localized(lang string) string {
	if devtext.English(lang) {
		return e.english
	}
	detail := "<nil>" // what %v writes for a missing cause, as the English does
	if e.cause != nil {
		detail = devtext.Text(lang, e.cause)
	}
	return e.msg(detail).Render(lang)
}
