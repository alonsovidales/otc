package settings

import "github.com/alonsovidales/otc/dao"

type Settings struct {
	dao          *dao.Dao
	Domain       string
	DeviceUuid   string
	BridgeSecret string
}

func Init(dao *dao.Dao) (*Settings, error) {
	domain, deviceUuid, bridgeSecret, err := dao.GetSettings()
	if err != nil {
		return nil, err
	}

	return &Settings{
		dao:          dao,
		Domain:       domain,
		DeviceUuid:   deviceUuid,
		BridgeSecret: bridgeSecret,
	}, nil
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
