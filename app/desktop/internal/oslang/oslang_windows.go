// SPDX-License-Identifier: AGPL-3.0-or-later

package oslang

import "golang.org/x/sys/windows"

// preferred is the user's display languages from Settings > Time &
// language, in their order, as language names ("es-ES"). The environment
// plays no part on Windows.
func preferred() []string {
	langs, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	if err != nil {
		return nil
	}

	return langs
}
