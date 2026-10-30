// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"errors"

	filesmanager "github.com/alonsovidales/otc/files_manager"
	"github.com/alonsovidales/otc/i18n"
)

// Smart search's error codes: a text search (SearchPhotos.text) while it is
// off, or while its model is still downloading (release 152), and turning
// it on or searching on a device that can't get its model. The apps hide
// the free-text search on the first two and say why on the last.
const (
	cCodeSmartSearchOff         = "smart_search_off"
	cCodeSmartSearchDownloading = "smart_search_downloading"
	cCodeSmartSearchUnavailable = "smart_search_unavailable"
)

// smartSearchRefusal is err's code and words in lang when it is one of
// Smart search's refusals; ok false otherwise.
func smartSearchRefusal(lang string, err error) (code, message string, ok bool) {
	switch {
	case errors.Is(err, filesmanager.ErrSmartSearchOff):
		return cCodeSmartSearchOff, i18n.DevFilesSmartSearchOff().Render(lang), true
	case errors.Is(err, filesmanager.ErrSmartSearchDownloading):
		percent := filesmanager.SmartSearchDownloadPercent(err)
		return cCodeSmartSearchDownloading, i18n.DevFilesSmartSearchDownloading(percent).Render(lang), true
	case errors.Is(err, filesmanager.ErrSmartSearchUnavailable):
		return cCodeSmartSearchUnavailable, i18n.DevFilesSmartSearchUnavailable().Render(lang), true
	}
	return "", "", false
}
