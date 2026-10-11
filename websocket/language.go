// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"errors"

	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/settings"
)

// Localization (docs/i18n.md): the user's language lives here, per user
// (settings.language, "" for Automatic), and every app sends its effective
// language with each request (ReqEnvelope.lang).

const (
	// cCodeInvalidLanguage is Ack.code on a SetLanguage whose language is
	// neither "" nor two or three lowercase letters.
	cCodeInvalidLanguage = "invalid_language"
	// cCodeLanguageChanged is Ack.code on a SetLanguage whose expected
	// value is no longer the stored one: the app adopts the device's
	// value (Settings.language, Status.language) instead.
	cCodeLanguageChanged = "changed"
)

// requestLang is the language a request's replies are to be written in:
// its ReqEnvelope.lang normalized to a language this build carries, or ""
// for a request without one (released apps, friends' devices, the bridge)
// and for any language this build has no text for - both get today's
// English, byte for byte. Requests on one connection run concurrently
// (serveConnection), so it is worked out per request in processMessage and
// handed to the handlers as an argument, never kept on the connection.
func requestLang(env *pb.ReqEnvelope) string {
	return i18n.Normalize(env.GetLang())
}

// setLanguage answers SetLanguage (owner sessions only: processAuthRequest).
// The compare with expected and the write are one step
// (settings.SetLanguage); a new value goes to the bridge with the push
// registrations, since pushes follow it.
func (ch *connHandler) setLanguage(req *pb.SetLanguage) (*pb.Ack, error) {
	var expected *string
	if req.Expected != nil {
		e := req.GetExpected()
		expected = &e
	}
	changed, err := ch.mg.settings.SetLanguage(req.GetLanguage(), expected)
	switch {
	case errors.Is(err, settings.ErrInvalidLanguage):
		return &pb.Ack{Ok: false, Code: cCodeInvalidLanguage}, nil
	case errors.Is(err, settings.ErrLanguageChanged):
		return &pb.Ack{Ok: false, Code: cCodeLanguageChanged}, nil
	case err != nil:
		return nil, err
	}
	if changed {
		log.Info("language changed")
		ch.mg.requestPushSync()
	}
	return &pb.Ack{Ok: true}, nil
}

// noteUILanguage records, after an owner's push registration (APNs, FCM,
// Web Push), the language of the app that made it: pushes use it while
// the user has chosen no language. Called before that registration's
// requestPushSync, which then carries it to the bridge. The reply never
// depends on it.
func (ch *connHandler) noteUILanguage(lang string) {
	if _, err := ch.mg.settings.NoteUILanguage(lang); err != nil {
		log.Error("could not store the language of the app registering for pushes:", err)
	}
}
