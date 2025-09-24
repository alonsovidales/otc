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
