// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"errors"

	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/log"
)

// Image Tagging on a device's memory (release 158; owner, 2026-11-01:
// "perhaps we can disable tagging to run in small devices", then "the 4
// gigabytes is going to don't have tagging"). RAM++ takes most of what a
// 4 GB device has to spare: ~1-2 GB once loaded and while it runs, where
// everything else - faces, Smart search, On this day, MariaDB - stayed
// under 1 GB in a container held to 3.5 GB that processed the demo
// library with 48 MP photos and a 4K video. And Smart search finds photos
// by what they show better than the tags do. So a low-memory device has no
// Image Tagging: it is off at every start, Settings says it isn't
// available (Settings.image_tagging_available) and turning it on is
// refused (release 160). Elsewhere it is on until the user turns it off
// (settings.image_tagging_chosen), so a library moved to a bigger device
// tags again. Outside low-memory mode the model loads only once a photo
// needs tags (Init), so a device whose users all have it off never holds
// it.
func (mg *Manager) applyTaggingDefault(p MemoryProfile) {
	if !p.TaggingAllowed() {
		changed, err := mg.dao.TurnOffImageTagging()
		if err != nil {
			log.Error("image tagging: could not turn it off on this low-memory device:", err)
		} else if changed {
			log.Info("image tagging: off - a low-memory device has none")
		}
		return
	}
	changed, err := mg.dao.ApplyImageTaggingDefault(true)
	switch {
	case errors.Is(err, dao.ErrSchemaBehind):
		log.Info("image tagging: the database has no place for the user's choice yet (the release's migration), the switch stays as it is")
	case err != nil:
		log.Error("image tagging: could not apply the device's default:", err)
	case changed:
		log.Info("image tagging: on, this device's default (its user hasn't chosen)")
	}
}

// TaggingAllowed says whether this device can have Image Tagging on (see
// above).
func (mg *Manager) TaggingAllowed() bool {
	return mg.profile.TaggingAllowed()
}
