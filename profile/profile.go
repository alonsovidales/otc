package profile

import (
	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
)

type Profile struct {
	dao    *dao.Dao
	Name   string
	Domain string
	Image  []byte
	Uuid   string
	Text   string
}

func InitFromPb(dao *dao.Dao, pro *pb.Profile) *Profile {
	return &Profile{
		dao:    dao,
		Name:   pro.Name,
		Image:  pro.Image,
		Text:   pro.Text,
		Domain: pro.Domain,
	}
}

func Init(dao *dao.Dao) (*Profile, error) {
	name, text, image, err := dao.GetProfile()
	if err != nil {
		return nil, err
	}

	return &Profile{
		dao:   dao,
		Name:  name,
		Image: image,
		Text:  text,
	}, nil
}

func (pr *Profile) SetProfile(name string, image []byte, text string) (err error) {
	err = pr.dao.UpdateProfile(name, text, image)
	if err == nil {
		pr.Image = image
		pr.Text = text
		pr.Name = name
	}

	return err
}
