// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"errors"

	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/log"
)

// Image Tagging's default (release 158; owner, 2026-11-01: "perhaps we can
// disable tagging to run in small devices"). RAM++ takes most of what a
// 4 GB device has to spare: ~1-2 GB once loaded and while it runs, where
// everything else - faces, Smart search, On this day, MariaDB - stayed
// under 1 GB in a container held to 3.5 GB that processed the demo
// library with 48 MP photos and a 4K video. And Smart search finds photos
// by what they show better than the tags do. So on a low-memory device it
// is off, and on elsewhere, until the user turns it on or off in Settings
// (settings.image_tagging_chosen): the default follows the device, so a
// library moved to a bigger one tags again. Outside low-memory mode the
// model loads only once a photo needs tags (Init), so a device whose users
// all have it off never holds it.
func (mg *Manager) applyTaggingDefault(p MemoryProfile) {
	on := !p.Low
	changed, err := mg.dao.ApplyImageTaggingDefault(on)
	switch {
	case errors.Is(err, dao.ErrSchemaBehind):
		log.Info("image tagging: the database has no place for the user's choice yet (the release's migration), the switch stays as it is")
	case err != nil:
		log.Error("image tagging: could not apply the device's default:", err)
	case changed && on:
		log.Info("image tagging: on, this device's default (its user hasn't chosen)")
	case changed:
		log.Info("image tagging: off, the default on a low-memory device (its user hasn't chosen; Settings turns it on)")
	}
}
