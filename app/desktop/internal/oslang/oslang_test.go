// SPDX-License-Identifier: AGPL-3.0-or-later

package oslang

import (
	"slices"
	"testing"

	"github.com/alonsovidales/otc/i18n"
)

func env(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

// The gettext order: LANGUAGE first, then the messages locale (LC_ALL,
// LC_MESSAGES, LANG - the first set), in POSIX form or not; nothing from
// LANGUAGE when the locale is C.
func TestFromEnv(t *testing.T) {
	cases := []struct {
		name string
		vars map[string]string
		want []string
	}{
		{"nothing set", nil, nil},
		{"LANG", map[string]string{"LANG": "pt_BR.UTF-8"}, []string{"pt-BR"}},
		{"LC_MESSAGES before LANG", map[string]string{"LC_MESSAGES": "es_MX.utf8", "LANG": "en_US.UTF-8"}, []string{"es-MX"}},
		{"LC_ALL before all", map[string]string{"LC_ALL": "nl_BE@euro", "LC_MESSAGES": "es_MX", "LANG": "en_US"}, []string{"nl-BE"}},
		{"LANGUAGE list first", map[string]string{"LANGUAGE": "ca_ES:es_ES:en", "LANG": "ca_ES.UTF-8"}, []string{"ca-ES", "es-ES", "en", "ca-ES"}},
		{"LANGUAGE ignored with C", map[string]string{"LANGUAGE": "es", "LANG": "C.UTF-8"}, nil},
		{"LANGUAGE ignored with no locale", map[string]string{"LANGUAGE": "es"}, nil},
		{"POSIX", map[string]string{"LC_ALL": "POSIX"}, nil},
		{"a tag as it is", map[string]string{"LANG": "de-AT"}, []string{"de-AT"}},
		{"junk skipped", map[string]string{"LANGUAGE": "es:<x>::fr_FR", "LANG": "fr_FR.UTF-8"}, []string{"es", "fr-FR", "fr-FR"}},
		{"junk locale", map[string]string{"LANG": "../../etc"}, nil},
	}
	for _, c := range cases {
		if got := fromEnv(env(c.vars)); !slices.Equal(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestPosixTag(t *testing.T) {
	for in, want := range map[string]string{
		"pt_BR.UTF-8": "pt-BR", "sr_RS@latin": "sr-RS", "de": "de", "en_US.ISO-8859-1": "en-US",
		"C": "", "C.UTF-8": "", "POSIX": "", "": "", "es ES": "", "a\x00b": "",
	} {
		if got := posixTag(in); got != want {
			t.Errorf("posixTag(%q) = %q, want %q", in, got, want)
		}
	}
}

// carriedOr is want when this build carries it, else English: a shipping
// build carries English alone, a make i18n DRAFT=1 build every language.
func carriedOr(want string) string {
	if Carried(want) {
		return want
	}

	return English()
}

// By base language, the first preferred one this build carries; never a
// related language (Galician is not Spanish), never the pseudo-locale.
func TestMatch(t *testing.T) {
	cases := []struct {
		prefs []string
		want  string
	}{
		{nil, "en"},
		{[]string{"es-MX"}, carriedOr("es")},
		{[]string{"pt-BR"}, carriedOr("pt")},
		{[]string{"nl-BE"}, carriedOr("nl")},
		{[]string{"ca-ES", "es-ES"}, carriedOr("es")},
		{[]string{"gl-ES"}, "en"},
		{[]string{"ja-JP", "de-DE"}, carriedOr("de")},
		{[]string{"en-US", "fr-FR"}, "en"},
		{[]string{"en-XA"}, "en"},
	}
	for _, c := range cases {
		if got := match(c.prefs); got != c.want {
			t.Errorf("match(%q) = %q, want %q", c.prefs, got, c.want)
		}
	}
	// The whole path, from the environment.
	if got := match(fromEnv(env(map[string]string{"LANGUAGE": "ca_ES:es_ES", "LANG": "ca_ES.UTF-8"}))); got != carriedOr("es") {
		t.Errorf("[ca-ES, es-ES] from the environment: %q", got)
	}
}

// The choice wins when this build carries it; a code it doesn't (added
// later) shows English but stays the choice; Automatic is the computer's.
func TestEffective(t *testing.T) {
	if got := Effective("en"); got != "en" {
		t.Errorf("en: %q", got)
	}
	if got := Effective("es"); got != carriedOr("es") {
		t.Errorf("es: %q", got)
	}
	if got := Effective("sv"); got != "en" {
		t.Errorf("a language this build doesn't have: %q", got)
	}
	if got, want := Effective(""), System(); got != want {
		t.Errorf("Automatic: %q, the computer's is %q", got, want)
	}
	if Name("en") != "English" || Name("sv") != "sv" {
		t.Errorf("names: %q %q", Name("en"), Name("sv"))
	}
	if Choosable() != (len(languagesForTest()) > 1) {
		t.Error("Choosable")
	}
}

func languagesForTest() []string {
	var codes []string
	for _, l := range i18n.Languages() {
		codes = append(codes, l.Code)
	}

	return codes
}
