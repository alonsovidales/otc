// SPDX-License-Identifier: AGPL-3.0-or-later

package settings

import "github.com/alonsovidales/otc/dao"

type Settings struct {
	dao          *dao.Dao
	Domain       string
	DeviceUuid   string
	BridgeSecret string
	// FaceRecognitionEnabled (issue #52) - off by default. See
	// SetFaceRecognitionEnabled and db.sql's settings.face_recognition_
	// enabled doc comment for why turning it on never retroactively
	// processes anything already uploaded.
	FaceRecognitionEnabled bool
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

	return &Settings{
		dao:                    dao,
		Domain:                 domain,
		DeviceUuid:             deviceUuid,
		BridgeSecret:           bridgeSecret,
		FaceRecognitionEnabled: faceRecognitionEnabled,
	}, nil
}

// SetFaceRecognitionEnabled toggles issue #52's feature. Purely a switch
// for *future* uploads - see its own doc comment in db.sql/the proto
// message for why this never triggers (or needs) a backfill of whatever
// was already in the library.
func (st *Settings) SetFaceRecognitionEnabled(enabled bool) (err error) {
	err = st.dao.SetFaceRecognitionEnabled(enabled)
	if err == nil {
		st.FaceRecognitionEnabled = enabled
	}

	return err
}

func (st *Settings) SetSettings(domain string) (err error) {
	err = st.dao.UpdateSettings(domain)
	if err == nil {
		st.Domain = domain
	}

	return err
}

// SetBridgeSecret updates the shared secret this device registers with the
// bridge relay (issue #40). Kept in memory too so the *next* bridge
// (re)connection attempt (see websocket.OpenBridge, which reads
// mg.settings.BridgeSecret fresh on every dial) picks it up without
// needing a service restart.
func (st *Settings) SetBridgeSecret(secret string) (err error) {
	err = st.dao.UpdateBridgeSecret(secret)
	if err == nil {
		st.BridgeSecret = secret
	}

	return err
}
