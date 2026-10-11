// SPDX-License-Identifier: AGPL-3.0-or-later

// Package oslang is the language otc-sync shows its own text in, and the
// one it sends the device as ReqEnvelope.lang (docs/i18n.md, "The stored
// choice"). The choice is the user's, kept on the device per user and in
// config.json as a local copy; Automatic ("") follows the computer: the
// user's preferred UI languages on Windows (GetUserPreferredUILanguages),
// the gettext environment elsewhere (LANGUAGE, LC_ALL, LC_MESSAGES, LANG),
// matched against the languages this build carries by i18n.Match - by
// base language, as the Mac app's L10n.systemLanguage does.
package oslang

import (
	"strings"

	"github.com/alonsovidales/otc/i18n"
)

// pseudoCode is the pseudo-locale a make i18n DRAFT=1 build carries
// (i18n/model.PseudoLanguage): it can be chosen, never matched to the
// computer's language.
const pseudoCode = "qps"

// Preferred is the user's preferred languages, most preferred first, as
// BCP 47 tags ("pt-BR"); empty when the computer doesn't say.
func Preferred() []string { return preferred() }

// System is the language Automatic shows: the first preferred language
// this build carries, matched by base language (es-MX gives es, pt-BR pt,
// [ca-ES, es-ES] es), else English. Never the pseudo-locale.
func System() string { return match(Preferred()) }

// match is System for a given list of preferences.
func match(prefs []string) string {
	if code := i18n.Match(prefs...); code != pseudoCode {
		return code
	}

	return English()
}

// English is the source language's code, the fallback for anything this
// build can't show.
func English() string { return i18n.Languages()[0].Code }

// Effective is the language otc-sync shows for choice, the local copy of
// the user's choice: the computer's language for "" (Automatic), the
// choice itself when this build carries it, else English - a language
// added after this build was released is kept as the choice and shown in
// English until an update brings its text.
func Effective(choice string) string {
	if choice == "" {
		return System()
	}
	if Carried(choice) {
		return choice
	}

	return English()
}

// Carried says whether this build can show code's text.
func Carried(code string) bool {
	for _, l := range i18n.Languages() {
		if l.Code == code {
			return true
		}
	}

	return false
}

// Name is the language's own name ("Español"), never translated; the code
// itself for one this build doesn't carry.
func Name(code string) string {
	for _, l := range i18n.Languages() {
		if l.Code == code {
			return l.Name
		}
	}

	return code
}

// Choosable says whether the user can pick a language at all: only with
// more than one in this build (the pickers stay hidden while English is
// the only language that ships).
func Choosable() bool { return len(i18n.Languages()) > 1 }

// fromEnv reads the gettext environment, in gettext's order: the messages
// locale is the first of LC_ALL, LC_MESSAGES and LANG that is set, and
// LANGUAGE - a colon-separated list - comes before it, unless that locale
// is C or POSIX (gettext ignores LANGUAGE then, and so does the rest of
// the desktop).
func fromEnv(getenv func(string) string) []string {
	locale := ""
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := getenv(name); v != "" {
			locale = v

			break
		}
	}
	main := posixTag(locale)
	var tags []string
	if list := getenv("LANGUAGE"); list != "" && main != "" {
		for _, name := range strings.Split(list, ":") {
			if t := posixTag(name); t != "" {
				tags = append(tags, t)
			}
		}
	}
	if main != "" {
		tags = append(tags, main)
	}

	return tags
}

// posixTag turns a POSIX locale name into a BCP 47 tag: "pt_BR.UTF-8"
// gives "pt-BR", "sr_RS@latin" "sr-RS", "de" "de". C, POSIX (with any
// codeset) and anything that isn't a locale name give "".
func posixTag(name string) string {
	if i := strings.IndexAny(name, ".@"); i >= 0 {
		name = name[:i]
	}
	if name == "" || name == "C" || name == "POSIX" || len(name) > 35 {
		return ""
	}
	for i := 0; i < len(name); i++ {
		switch c := name[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return ""
		}
	}

	return strings.ReplaceAll(name, "_", "-")
}
