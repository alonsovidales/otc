// SPDX-License-Identifier: AGPL-3.0-or-later

package settings

import (
	"sync"

	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/log"
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

	return &Settings{
		dao:                    dao,
		imageTaggingEnabled:    imageTaggingEnabled,
		domain:                 domain,
		deviceUuid:             deviceUuid,
		bridgeSecret:           bridgeSecret,
		faceRecognitionEnabled: faceRecognitionEnabled,
	}, nil
}

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
