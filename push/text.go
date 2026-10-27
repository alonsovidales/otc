// SPDX-License-Identifier: AGPL-3.0-or-later

package push

import (
	"unicode"
	"unicode/utf8"

	"github.com/alonsovidales/otc/i18n"
)

// Text is a push's title and body in a language: rendered in the push
// language (Lang) when the push is delivered, not when it is queued.
//
// What a push may say is the rule in NotifyNewPost's doc comment: Apple
// and Google can read it, so only a friend's name and a generic phrase,
// never what was posted or said. The catalog holds the push.* keys to
// that too: their only arguments are {name}, {version} and {port}
// (docs/i18n.md). The language itself becomes visible to them.
type Text func(lang string) (title, body string)

// Literal is a Text already written, the same in every language.
func Literal(title, body string) Text {
	return func(string) (string, string) { return title, body }
}

// FromFriend is the push for something a friend did: the friend's name as
// the title, as it is (a name is never translated, nor isolated: the
// title is the name alone), and what they did as the body.
func FromFriend(name string, did i18n.Msg) Text {
	return func(lang string) (string, string) { return name, did.Render(lang) }
}

// CriticalUpdate is the push for a critical update to version (its
// label). In English it says what it always did: the release's summary,
// then where to install it. Release summaries stay English (docs/i18n.md,
// "Decisions"), so in another language the body is only the sentence
// saying where to install it.
func CriticalUpdate(version, summary string) Text {
	return func(lang string) (string, string) {
		title := i18n.PushUpdateCriticalTitle(version).Render(lang)
		body := i18n.PushUpdateCriticalBody().Render(lang)
		if english(lang) {
			body = summary + " " + body
		}
		return title, body
	}
}

// CriticalImageUpdate is CriticalUpdate for a device that runs from the
// container image, which is updated by pulling its new image on the server
// rather than from Settings: the body says so.
func CriticalImageUpdate(version, summary string) Text {
	return func(lang string) (string, string) {
		title := i18n.PushUpdateCriticalTitle(version).Render(lang)
		body := i18n.PushUpdateCriticalBodyContainer().Render(lang)
		if english(lang) {
			body = summary + " " + body
		}
		return title, body
	}
}

// The ports raidwatch names a disk by (raidwatch.Port), as strings: push
// is built into the bridge too, which has no business with raidwatch.
const (
	portTop     = "top"
	portBottom  = "bottom"
	portUnknown = "unknown"
)

// StorageDiskStopped is the push for a disk of the mirror that stopped
// working. ports are where each missing disk is plugged in
// (raidwatch.MissingPorts, as strings: "top", "bottom", "unknown") and
// label raidwatch.Missing's English for them ("the disk in the top blue
// USB port"). One disk is named in every language. Several - which a
// two-disk mirror that still runs can't have - are named only in English,
// from label, as before; other languages say "one of the two". working is
// where the disk still working is (raidwatch.StillWorking, "top",
// "bottom" or "unknown"), said when the missing one's port is unknown.
func StorageDiskStopped(ports []string, label, working string) Text {
	return func(lang string) (string, string) {
		title := i18n.PushRaidStoppedTitle().Render(lang)
		if len(ports) != 1 && english(lang) && label != "" {
			return title, capitalize(label) + " failed. Your files are safe on the other one: replace it soon." // i18n-ignore: English only, for several missing disks, which no key names
		}
		body := i18n.PushRaidStoppedUnknown()
		if len(ports) == 1 {
			switch ports[0] {
			case portTop:
				body = i18n.PushRaidStoppedTop()
			case portBottom:
				body = i18n.PushRaidStoppedBottom()
			default:
				switch working {
				case portTop:
					body = i18n.PushRaidStoppedWorkingTop()
				case portBottom:
					body = i18n.PushRaidStoppedWorkingBottom()
				}
			}
		}
		return title, body.Render(lang)
	}
}

// StorageDiskStoppedUnnamed is StorageDiskStopped for the container's
// mirror, whose disks are known by the names their owner gave them in .env
// - a model and a serial number, such as
// ata-WDC_WD40EFRX-68N32N0_WD-WCC7K1111111. That name is none of Apple's
// or Google's business, so the push says "one of the two storage disks"
// in every language; the alert in the apps names the disk.
func StorageDiskStoppedUnnamed() Text {
	return StorageDiskStopped([]string{portUnknown}, "", portUnknown)
}

// StorageDisksWorkingAgain is the push for a mirror that is whole again
// after StorageDiskStopped (or StorageDiskStoppedUnnamed).
func StorageDisksWorkingAgain() Text {
	return func(lang string) (string, string) {
		return i18n.PushRaidWorkingTitle().Render(lang), i18n.PushRaidWorkingBody().Render(lang)
	}
}

// StorageFull is the push for a device whose storage is nearly full
// (storagewatch): no figures, which Apple and Google have no business
// reading - the alert in the apps has them.
func StorageFull() Text {
	return func(lang string) (string, string) {
		return i18n.PushStorageFullTitle().Render(lang), i18n.PushStorageFullBody().Render(lang)
	}
}

// OfflineAlert is the title and body of the bridge's push to a device's
// phones and browsers when the device has gone unreachable (issue #62), in
// lang: the product's name as the title, never translated. name is the
// device's name (casa, of casa.off-the.cloud) when the body must say which
// device - its account has several - and "" when it needn't: the body is
// then the one it always was. A device's name is a {name} Apple and Google
// may read, as a friend's is.
func OfflineAlert(lang, name string) (title, body string) {
	if name != "" {
		return "Off The Cloud", i18n.PushOfflineBodyNamed(name).Render(lang)
	}
	return "Off The Cloud", i18n.PushOfflineBody().Render(lang)
}

// english reports whether lang renders as English: absent, English, or a
// language this build has no text for.
func english(lang string) bool {
	code := i18n.Normalize(lang)
	return code == "" || code == "en"
}

// capitalize upper-cases the first letter, as raidwatch does for its
// titles.
func capitalize(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}
