// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"github.com/alonsovidales/otc/dao"
	filesmanager "github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/log"
)

// fitStoredPictures keeps what was stored before profile pictures were
// capped (dao.MaxProfilePictureBytes) within the cap, once at each start,
// in the background:
//   - the owner's own, set from an app before 2026-09-29 as the photo was
//     picked (several MB): made small (filesmanager.FitProfilePicture), so
//     what this device sends its friends - and GetProfile, to anyone - is
//     small. One it can't make small is left as it is;
//   - the friends' stored larger: dropped. An accepted friend's is asked
//     for again at the next sync, and kept made small then.
func (mg *Manager) fitStoredPictures() {
	if mg.profile != nil {
		if old := mg.profile.Image(); len(old) > dao.MaxProfilePictureBytes {
			if small := filesmanager.FitProfilePicture(old, filesmanager.PictureFromOwner); small != nil {
				if replaced, err := mg.profile.ReplaceImage(old, small); err != nil {
					log.Error("could not store the profile picture made small:", err)
				} else if replaced {
					log.Info("profile picture of", len(old), "bytes made small:", len(small), "bytes")
				}
			} else {
				log.Info("the profile picture of", len(old), "bytes could not be made small; left as it is")
			}
		}
	}
	if mg.dao == nil {
		return
	}
	n, err := mg.dao.DropOversizedFriendPictures()
	if err != nil {
		log.Error("could not drop the friends' oversized profile pictures:", err)
		return
	}
	if n > 0 {
		log.Info("dropped", n, "friends' profile pictures over", dao.MaxProfilePictureBytes, "bytes; asked for again at the next sync")
	}
}
