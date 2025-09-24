package profile

import "github.com/alonsovidales/otc/dao"

type Profile struct {
	dao   *dao.Dao
	Name  string
	Image []byte
	Text  string
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
