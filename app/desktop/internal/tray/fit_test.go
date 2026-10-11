// SPDX-License-Identifier: AGPL-3.0-or-later

package tray

import (
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/alonsovidales/otc/app/desktop/internal/flasher"
	"github.com/alonsovidales/otc/i18n"
)

// A folder's progress writes its percentage as the language does: English
// as before ("42% · …"), a no-break space before the sign where the
// language puts one.
func TestProgressTextFollowsTheLanguage(t *testing.T) {
	if got := progressText("en", 0.427, "12/340 · photo.jpg"); got != "42% · 12/340 · photo.jpg" {
		t.Errorf("English: %q", got)
	}
	for lang, want := range map[string]string{"fr": "42 % · 3/9 · a.jpg", "de": "42 % · 3/9 · a.jpg", "it": "42% · 3/9 · a.jpg", "nl": "42% · 3/9 · a.jpg"} {
		if i18n.Normalize(lang) != lang {
			continue // a build that doesn't carry it
		}
		if got := progressText(lang, 0.42, "3/9 · a.jpg"); got != want {
			t.Errorf("%s: %q, want %q", lang, got, want)
		}
	}
}

// Linux keeps the whole explanation; Windows' one-line prompt is short in
// every language.
func TestSettingsHintFitsWindows(t *testing.T) {
	for _, l := range i18n.Languages() {
		long, short := i18n.DeskMainSettingsHint().Render(l.Code), i18n.DeskMainSettingsHintWindows().Render(l.Code)
		if n := utf8.RuneCountInString(short); n > 36 || n == 0 {
			t.Errorf("%s: Windows prompt %q has %d characters", l.Code, short, n)
		}
		want := long
		if runtime.GOOS == "windows" {
			want = short
		}
		if got := settingsHint(l.Code); got != want {
			t.Errorf("%s: %q, want %q", l.Code, got, want)
		}
	}
}

// Windows' list of cards: the size and the disk's number always whole and
// first, the name shortened to fit, and every row its own.
func TestWindowsCardRowFits(t *testing.T) {
	names := []string{"", "SDHC Card", "Generic STORAGE DEVICE", "Generic- SD/MMC/MS PRO USB Device", "Ünïcødé 名前 ディスク カード"}
	sizes := []int64{8_000_000_000, 31_914_983_424, 512_110_190_592, 1_024_209_543_168}
	for _, l := range i18n.Languages() {
		for _, number := range []string{"2", "12"} {
			for _, name := range names {
				for _, size := range sizes {
					d := flasher.Disk{ID: `\\.\PhysicalDrive` + number, Name: name, Size: size}
					row := windowsCardRow(l.Code, d)
					head := flasher.HumanSizeIn(l.Code, size) + " · " + d.ShortIDIn(l.Code) + " · "
					if !strings.HasPrefix(row, head) {
						t.Errorf("%s: %q doesn't start with %q", l.Code, row, head)
					}
					if n := utf8.RuneCountInString(row); n > cardRowRunes {
						t.Errorf("%s: %q has %d characters", l.Code, row, n)
					}
					shown := strings.TrimSuffix(strings.TrimPrefix(row, head), "…")
					if name != "" && !strings.HasPrefix(name, shown) {
						t.Errorf("%s: %q isn't the start of %q", l.Code, shown, name)
					}
					if name == "" && !strings.HasPrefix(i18n.DeskFoldersFlashUnnamedDisk().Render(l.Code), shown) {
						t.Errorf("%s: unnamed disk as %q", l.Code, row)
					}
				}
			}
		}
		// Two disks with the same name and size differ by their number.
		a := windowsCardRow(l.Code, flasher.Disk{ID: `\\.\PhysicalDrive2`, Name: "Generic STORAGE DEVICE", Size: 31_914_983_424})
		b := windowsCardRow(l.Code, flasher.Disk{ID: `\\.\PhysicalDrive3`, Name: "Generic STORAGE DEVICE", Size: 31_914_983_424})
		if a == b {
			t.Errorf("%s: two disks share the row %q", l.Code, a)
		}
	}
	// A name that fits is whole.
	if got := windowsCardRow("en", flasher.Disk{ID: `\\.\PhysicalDrive2`, Name: "SDHC Card", Size: 31_914_983_424}); got != "31.9 GB · disk 2 · SDHC Card" {
		t.Errorf("English: %q", got)
	}
	if got := windowsCardRow("en", flasher.Disk{ID: `\\.\PhysicalDrive2`, Name: "Generic STORAGE DEVICE", Size: 31_914_983_424}); got != "31.9 GB · disk 2 · Generic STORAG…" {
		t.Errorf("English, shortened: %q", got)
	}
}

func TestShorten(t *testing.T) {
	for _, c := range []struct {
		s    string
		n    int
		want string
	}{
		{"abc", 3, "abc"},
		{"abcd", 3, "ab…"},
		{"ab cd", 4, "ab…"},
		{"abcdef", 0, "a…"},
		{"ab", 0, "ab"},
		{"名前ディスク", 4, "名前デ…"},
	} {
		if got := shorten(c.s, c.n); got != c.want {
			t.Errorf("shorten(%q, %d) = %q, want %q", c.s, c.n, got, c.want)
		}
	}
}
