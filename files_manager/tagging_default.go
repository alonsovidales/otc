// SPDX-License-Identifier: AGPL-3.0-or-later

package filesmanager

import (
	"errors"

	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/log"
)

// Image Tagging on a device's memory (release 158; owner, 2026-11-01:
// "perhaps we can disable tagging to run in small devices", then "the 4
// gigabytes is going to don't have tagging"; 2026-11-03, issue #209: "Opt-in
// on 4 GB"). RAM++ takes most of what a 4 GB device has to spare: ~1-2 GB
// once loaded and while it runs, where everything else - faces, Smart
// search, On this day, MariaDB - stayed under 1 GB in a container held to
// 3.5 GB that processed the demo library with 48 MP photos and a 4K video;
// with tagging on it peaked at 2.55 GB, nothing killed, photos about four
// times slower. And Smart search finds photos by what they show better
// than the tags do. So, at every start of every instance:
//
//   - under 3 GB (a 2 GB board) there is no Image Tagging: it is turned
//     off, chosen or not, Settings says it isn't available
//     (Settings.image_tagging_available) and turning it on is refused;
//   - a low-memory device with more (a 4 GB board, or [otc] low-memory =
//     on) may have it, off until its user turns it on
//     (settings.image_tagging_chosen), and Settings says what it costs
//     there (Settings.image_tagging_heavy). Release 160 turned it off on
//     these at every start without touching the choice, so a library that
//     ran it stays off - its user chose on before 160 and was overruled, or
//     never chose - until its user turns it on again, which marks the
//     choice and keeps it on through restarts;
//   - elsewhere it is on until the user turns it off, so a library moved to
//     a bigger device tags again.
//
// Outside low-memory mode the model loads only once a photo needs tags
// (Init), so a device whose users all have it off never holds it; in
// low-memory mode it loads at first use and is released when idle
// (idleModels).
func (mg *Manager) applyTaggingDefault(p MemoryProfile) {
	if !p.TaggingAllowed() {
		changed, err := mg.dao.TurnOffImageTagging()
		if err != nil {
			log.Error("image tagging: could not turn it off on this device:", err)
		} else if changed {
			log.Info("image tagging: off - a device with this little memory has none")
		}
		return
	}
	on := !p.TaggingHeavy()
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

// TaggingAllowed says whether this device can have Image Tagging on (see
// above).
func (mg *Manager) TaggingAllowed() bool {
	return mg.profile.TaggingAllowed()
}
