package settings

import "github.com/alonsovidales/otc/dao"

type Settings struct {
	Domain string
}

func SetSettings(dao *dao.Dao, domain string) (err error) {
	return dao.UpdateSettings(domain)
}

func GetSettings(dao *dao.Dao) (set *Settings, err error) {
	domain, _, _, err := dao.GetSettings()
	if err != nil {
		return nil, err
	}

	return &Settings{
		Domain: domain,
	}, nil
}
