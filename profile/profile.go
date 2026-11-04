// SPDX-License-Identifier: AGPL-3.0-or-later

package profile

import (
	"bytes"
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

// SetProfile stores and keeps a new profile. Under mu, the write too: the
// stored profile and the one kept are always the same version, whatever
// ReplaceImage does meanwhile.
func (pr *Profile) SetProfile(name string, image []byte, text string) (err error) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	if err = pr.dao.UpdateProfile(name, text, image); err == nil {
		pr.image = image
		pr.text = text
		pr.name = name
	}
	return err
}

// ReplaceImage stores image as the profile's picture in place of old, only
// while the picture is still old - not one set meanwhile. replaced says
// whether it was.
func (pr *Profile) ReplaceImage(old, image []byte) (replaced bool, err error) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	if !bytes.Equal(pr.image, old) {
		return false, nil
	}
	if err := pr.dao.UpdateProfile(pr.name, pr.text, image); err != nil {
		return false, err
	}
	pr.image = image
	return true, nil
}
