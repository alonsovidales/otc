// SPDX-License-Identifier: AGPL-3.0-or-later

package settings

import (
	"errors"
	"sync"

	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
	"github.com/alonsovidales/otc/push"
)

// Settings is the device's identity and switches. Issue #171: requests
// change them (Settings, the bridge switch-on, Regenerate secret) while
// the bridge connection, push relay, friend sync and share links read them
// - so every field is behind mu and read through its getter; a torn string
// read could send a half-written secret.
type Settings struct {
	dao *dao.Dao

	mu           sync.RWMutex
	domain       string
	deviceUuid   string
	bridgeSecret string
	// faceRecognitionEnabled (issue #52) - off by default. See
	// SetFaceRecognitionEnabled and db.sql's settings.face_recognition_
	// enabled doc comment for why turning it on never retroactively
	// processes anything already uploaded.
	faceRecognitionEnabled bool
	// imageTaggingEnabled (issue #181) - on by default.
	imageTaggingEnabled bool
	// language is the user's language for every app (docs/i18n.md): ""
	// for Automatic, else a code ValidLanguage accepts - possibly one this
	// build has no text for, kept for the apps that do. lastUILanguage is
	// the language of the app that last registered for pushes, a code
	// this build carries or "". Both "" on a database without the columns.
	language       string
	lastUILanguage string
	// langMu serialises the language writes, each a compare and a write:
	// one process owns each database (issue #82), so compare-and-set under
	// it is atomic without holding mu across a database round trip.
	langMu sync.Mutex
}

func Init(dao *dao.Dao) (*Settings, error) {
	domain, deviceUuid, bridgeSecret, err := dao.GetSettings()
	if err != nil {
		return nil, err
	}
	faceRecognitionEnabled, err := dao.GetFaceRecognitionEnabled()
	if err != nil {
		return nil, err
	}

	imageTaggingEnabled, err := dao.GetImageTaggingEnabled()
	if err != nil {
		// A database the release script hasn't reached yet: tag, as before.
		log.Error("could not read image_tagging_enabled, tagging stays on:", err)
		imageTaggingEnabled = true
	}

	language, lastUILanguage, err := dao.GetLanguageSettings()
	if errors.Is(err, daoSchemaBehind) {
		// A database release 118's script hasn't reached: Automatic, and
		// pushes in English, as before.
		log.Error("settings.language is missing, the language stays Automatic:", err)
		err = nil
	}
	if err != nil {
		return nil, err
	}

	return &Settings{
		dao:                    dao,
		imageTaggingEnabled:    imageTaggingEnabled,
		domain:                 domain,
		deviceUuid:             deviceUuid,
		bridgeSecret:           bridgeSecret,
		faceRecognitionEnabled: faceRecognitionEnabled,
		language:               language,
		lastUILanguage:         lastUILanguage,
	}, nil
}

// daoSchemaBehind is dao.ErrSchemaBehind, reachable where a parameter
// named dao hides the package.
var daoSchemaBehind = dao.ErrSchemaBehind

// Domain is this device's own domain ("cala.off-the.cloud").
func (st *Settings) Domain() string {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.domain
}

// DeviceUuid is this device's id on the bridge.
func (st *Settings) DeviceUuid() string {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.deviceUuid
}

// BridgeSecret is what this device authenticates to the bridge with.
func (st *Settings) BridgeSecret() string {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.bridgeSecret
}

// FaceRecognitionEnabled is issue #52's switch.
func (st *Settings) FaceRecognitionEnabled() bool {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.faceRecognitionEnabled
}

// Identity is DeviceUuid, Domain and BridgeSecret read together, so a
// bridge dial never pairs one identity's secret with another's domain.
func (st *Settings) Identity() (deviceUuid, domain, bridgeSecret string) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.deviceUuid, st.domain, st.bridgeSecret
}

// SetIdentity replaces the in-memory identity at once - after the database
// already holds it (the bridge switch-on).
func (st *Settings) SetIdentity(deviceUuid, domain, bridgeSecret string) {
	st.mu.Lock()
	st.deviceUuid, st.domain, st.bridgeSecret = deviceUuid, domain, bridgeSecret
	st.mu.Unlock()
}

// SetFaceRecognitionEnabled toggles issue #52's feature. Purely a switch
// for *future* uploads - see its own doc comment in db.sql/the proto
// message for why this never triggers (or needs) a backfill of whatever
// was already in the library.
func (st *Settings) SetFaceRecognitionEnabled(enabled bool) (err error) {
	err = st.dao.SetFaceRecognitionEnabled(enabled)
	if err == nil {
		st.mu.Lock()
		st.faceRecognitionEnabled = enabled
		st.mu.Unlock()
	}

	return err
}

// ImageTaggingEnabled is issue #181's switch.
func (st *Settings) ImageTaggingEnabled() bool {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.imageTaggingEnabled
}

// SetImageTaggingEnabled toggles the tagging model for what is processed
// from now on.
func (st *Settings) SetImageTaggingEnabled(enabled bool) (err error) {
	err = st.dao.SetImageTaggingEnabled(enabled)
	if err == nil {
		st.mu.Lock()
		st.imageTaggingEnabled = enabled
		st.mu.Unlock()
	}
	return err
}

func (st *Settings) SetSettings(domain string) (err error) {
	err = st.dao.UpdateSettings(domain)
	if err == nil {
		st.mu.Lock()
		st.domain = domain
		st.mu.Unlock()
	}

	return err
}

// SetBridgeSecret updates the shared secret this device registers with the
// bridge relay (issue #40). Kept in memory too so the *next* bridge
// (re)connection attempt (see websocket.OpenBridge, which reads it fresh
// on every dial) picks it up without needing a service restart.
func (st *Settings) SetBridgeSecret(secret string) (err error) {
	err = st.dao.UpdateBridgeSecret(secret)
	if err == nil {
		st.mu.Lock()
		st.bridgeSecret = secret
		st.mu.Unlock()
	}

	return err
}

// ErrInvalidLanguage is a language SetLanguage doesn't store: neither ""
// nor two or three lowercase letters.
var ErrInvalidLanguage = errors.New("invalid language code") // i18n-ignore: a sentinel, answered as an Ack.code

// ErrLanguageChanged is a SetLanguage whose expected value is no longer
// the stored one: another app changed the language since this one last
// saw it, and nothing was written.
var ErrLanguageChanged = errors.New("the language was changed meanwhile") // i18n-ignore: a sentinel, answered as an Ack.code

// ValidLanguage reports whether language may be stored: "" (Automatic) or
// ^[a-z]{2,3}$, so a device keeps a language added after its release.
func ValidLanguage(language string) bool {
	return language == "" || i18n.ValidCode(language)
}

// Language is the user's language for every app, "" for Automatic.
func (st *Settings) Language() string {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.language
}

// LastUILanguage is the language of the app that last registered for
// pushes, "" when none has said.
func (st *Settings) LastUILanguage() string {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.lastUILanguage
}

// PushLanguage is the language this instance's pushes are written in:
// push.ResolveLanguage of the two above, read together.
func (st *Settings) PushLanguage() string {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return push.ResolveLanguage(st.language, st.lastUILanguage)
}

// SetLanguage stores the user's language (SetLanguage, owner only). With
// expected set it writes only while the stored value still equals it, and
// answers ErrLanguageChanged otherwise; the compare and the write are one
// step under langMu. changed says whether the value is a new one. On a
// database without the column the choice is kept in memory until a
// restart - Status and Settings carry it meanwhile - rather than failing.
func (st *Settings) SetLanguage(language string, expected *string) (changed bool, err error) {
	if !ValidLanguage(language) {
		return false, ErrInvalidLanguage
	}
	st.langMu.Lock()
	defer st.langMu.Unlock()
	cur := st.Language()
	if expected != nil && *expected != cur {
		return false, ErrLanguageChanged
	}
	if language == cur {
		return false, nil
	}
	if err := st.dao.SetLanguage(language); err != nil {
		if !errors.Is(err, dao.ErrSchemaBehind) {
			return false, err
		}
		log.Error("settings.language is missing, the language is kept until a restart:", err)
	}
	st.mu.Lock()
	st.language = language
	st.mu.Unlock()
	return true, nil
}

// NoteUILanguage records the language of an owner's app that registered
// for pushes, when i18n.Normalize accepts it and it changed; changed says
// whether it did. Like SetLanguage, a database without the column keeps
// it in memory only.
func (st *Settings) NoteUILanguage(lang string) (changed bool, err error) {
	lang = i18n.Normalize(lang)
	if lang == "" {
		return false, nil
	}
	st.langMu.Lock()
	defer st.langMu.Unlock()
	if lang == st.LastUILanguage() {
		return false, nil
	}
	if err := st.dao.SetLastUILanguage(lang); err != nil {
		if !errors.Is(err, dao.ErrSchemaBehind) {
			return false, err
		}
		log.Error("settings.last_ui_language is missing, the language is kept until a restart:", err)
	}
	st.mu.Lock()
	st.lastUILanguage = lang
	st.mu.Unlock()
	return true, nil
}
