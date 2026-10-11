// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"github.com/alonsovidales/otc/devtext"
	"github.com/alonsovidales/otc/i18n"
)

// Replies are written in the request's language: lang, which requestLang
// works out once per request and the three process* handlers pass down
// (docs/i18n.md, "Text the device writes"). A request without one - a
// released app, a friend's device, the bridge - or in English gets the
// English it always got, byte for byte: the dev.* keys hold that English,
// and a message whose arguments are all text renders it exactly. The few
// that would differ go through sayOr.

// sayOr is m in lang, or english - the reply as it always read - for a
// request in English. For messages with an argument the catalog renders
// differently from today's English: a user-controlled one (isolated with
// FSI/PDI marks) or a number (grouped: 4,194,304).
func sayOr(lang, english string, m i18n.Msg) string {
	if devtext.English(lang) {
		return english
	}
	return m.Render(lang)
}

// errText is a whole reply that is only err, with no sentence of the
// handler's own around it: err's words in lang when it is one of the
// device's translated errors (devtext.Text), and any other error - a
// database's or the system's own words - as it is, in English, which in
// another language comes after a sentence saying the request failed.
func errText(lang string, err error) string {
	if devtext.English(lang) {
		return err.Error()
	}
	if l, ok := err.(interface{ Localized(string) string }); ok {
		return l.Localized(lang)
	}
	return i18n.DevErrActionFailed(err.Error()).Render(lang)
}

// notAvailableHere refuses a request only the primary instance serves
// (ch.mg.sup == nil: an additional user's instance, issue #82).
func notAvailableHere(lang string) string {
	return i18n.DevErrNotOnThisInstance().Render(lang)
}
