// SPDX-License-Identifier: AGPL-3.0-or-later

package profile

import (
	"sync"

	"github.com/alonsovidales/otc/dao"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// Profile is the device owner's profile, or a friend's as it signed in.
// Issue #171: the owner's is shared by every connection and edited from
// Settings while feeds and friend requests read it, so its fields are
// behind mu and read through getters. SetProfile swaps in a new image
// slice rather than writing into the old one, so an Image already handed
// out never changes under its reader.
type Profile struct {
	dao *dao.Dao

	mu     sync.RWMutex
	name   string
	domain string
	image  []byte
	text   string
}

func InitFromPb(dao *dao.Dao, pro *pb.Profile) *Profile {
	return &Profile{
		dao:    dao,
		name:   pro.Name,
		image:  pro.Image,
		text:   pro.Text,
		domain: pro.Domain,
	}
}

func Init(dao *dao.Dao, domain string) (*Profile, error) {
	name, text, image, err := dao.GetProfile()
	if err != nil {
		return nil, err
	}

	return &Profile{
		dao:    dao,
		name:   name,
		image:  image,
		text:   text,
		domain: domain,
	}, nil
}

func (pr *Profile) Name() string {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	return pr.name
}

func (pr *Profile) Domain() string {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	return pr.domain
}

func (pr *Profile) Image() []byte {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	return pr.image
}

func (pr *Profile) Text() string {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	return pr.text
}

// Snapshot is name, text and image read together - one version of the
// profile, never a new name with the old picture.
func (pr *Profile) Snapshot() (name, text string, image []byte) {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	return pr.name, pr.text, pr.image
}

func (pr *Profile) SetProfile(name string, image []byte, text string) (err error) {
	err = pr.dao.UpdateProfile(name, text, image)
	if err == nil {
		pr.mu.Lock()
		pr.image = image
		pr.text = text
		pr.name = name
		pr.mu.Unlock()
	}

	return err
}
