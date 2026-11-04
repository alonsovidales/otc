// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"runtime"
	"strings"
)

// A device path whose name Windows can't hold as it is - "a\b.txt" from a
// Mac or Linux computer, which Windows writes as a folder "a" holding
// "b.txt"; "report." or "report ", whose last dot or space Win32 drops;
// "aux.txt", a device name - came back under another name. The next pass
// took that one for new here (uploaded) and the device's for deleted here
// (deleted on the device): the file moved there, its versions lost, and
// the other computers moved it too. On Windows such device entries are
// left out of the pass, either way, as the ":" names always were, and
// counted on the folder.

// windowsNames: the names Windows can hold are the rule (a test sets it
// elsewhere).
var windowsNames = runtime.GOOS == "windows"

// unusableName: this computer's file system can't hold the name of the
// device path rel (slash-separated) as it is.
func unusableName(rel string) bool {
	return windowsNames && windowsUnusable(rel)
}

// windowsUnusable: a component of rel has a character Windows doesn't
// allow in a name (\ < > : " | ? * or a control character), ends in a dot
// or a space, or is a device name (CON, NUL, COM1, LPT1..., with any
// extension).
func windowsUnusable(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			continue // safeRelative's to refuse
		}
		if strings.ContainsAny(part, `\<>:"|?*`) || strings.ContainsFunc(part, func(r rune) bool { return r < 0x20 }) {
			return true
		}
		if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return true
		}
		stem, _, _ := strings.Cut(part, ".")
		if windowsDeviceNames[strings.ToUpper(strings.TrimRight(stem, " "))] {
			return true
		}
	}

	return false
}

var windowsDeviceNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true, "CONIN$": true, "CONOUT$": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"COM¹": true, "COM²": true, "COM³": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
	"LPT¹": true, "LPT²": true, "LPT³": true,
}
