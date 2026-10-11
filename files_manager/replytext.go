// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
)

// The errors this package returns to a request are devtext errors where a
// person reads them (docs/i18n.md, "Text the device writes"): Error() is
// the English as it always read - what the log, an alert's detail and a
// request without a language get - and the websocket layer shows the same
// text in a request's language. devtext.WithEnglish keeps that English for
// a text the catalog would render differently (a path is isolated with
// FSI/PDI marks, a number grouped: 4,194,304); englishErr does the same
// for one that also wraps a cause errors.Is has to see.

// englishErr is devtext.WithEnglish with a cause.
type englishErr struct {
	english string
	msg     i18n.Msg
	cause   error
}

func (e *englishErr) Error() string { return e.english }

func (e *englishErr) Unwrap() error { return e.cause }

// Localized is the text in lang (devtext.Text).
func (e *englishErr) Localized(lang string) string {
	if devtext.English(lang) {
		return e.english
	}
	return e.msg.Render(lang)
}
