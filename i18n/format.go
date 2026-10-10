// SPDX-License-Identifier: AGPL-3.0-or-later

package i18n

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/feature/plural"
	"golang.org/x/text/number"
)

// FormatInt writes n as lang writes numbers: 1,234 in English, 1.234 in
// Spanish, 1 234 in French (with a no-break space).
func FormatInt(lang string, n int64) string { return std.formatInt(std.lang(lang), n) }

// FormatBytes writes a size in decimal units, as the apps do: "820 MB",
// "4.6 GB", "984 GB" (French: "4,6 Go").
func FormatBytes(lang string, n int64) string { return std.formatBytes(std.lang(lang), n) }

// FormatTime writes a date and time, in t's location: "11 Oct 2026, 14:05",
// "11.10.2026, 14:05" in German.
func FormatTime(lang string, t time.Time) string { return std.formatTime(std.lang(lang), t) }

func (b *bundle) formatInt(i int, n int64) string {
	return b.printers[i].Sprint(number.Decimal(n))
}

// isOne reports whether n takes the plural form "one" in language i, by
// its canonical tag (French 0 is "one", Portuguese of Portugal 0 is not).
// Every other category the rules may give (CLDR 32 in x/text has no
// "many" for these languages) uses "other".
func (b *bundle) isOne(i int, n int64) bool {
	if n < 0 {
		if n == math.MinInt64 {
			return false
		}
		n = -n
	}
	// Keep the operand inside int on every platform; for these rules a
	// billion and more is "other" anyway, and the last digits stay.
	if n >= 1_000_000_000 {
		n = 1_000_000_000 + n%1_000_000
	}
	return plural.Cardinal.MatchPlural(b.tags[i], int(n), 0, 0, 0, 0) == plural.One
}

// sizeUnits are the decimal size units of one language.
type sizeUnits struct {
	units [6]string // B, kB, MB, GB, TB, PB
	sep   string    // between the number and the unit
}

var (
	englishSizes = sizeUnits{[6]string{"B", "KB", "MB", "GB", "TB", "PB"}, " "}
	// Most languages write the same units; a no-break space keeps the
	// number on the unit's line.
	europeanSizes = sizeUnits{[6]string{"B", "KB", "MB", "GB", "TB", "PB"}, " "}
	// French counts octets.
	frenchSizes = sizeUnits{[6]string{"o", "ko", "Mo", "Go", "To", "Po"}, " "}
)

func (b *bundle) sizes(i int) sizeUnits {
	switch base, _ := b.tags[i].Base(); base.String() {
	case "en":
		return englishSizes
	case "fr":
		return frenchSizes
	}
	return europeanSizes
}

func (b *bundle) formatBytes(i int, n int64) string {
	u := b.sizes(i)
	if n < 1000 {
		return b.formatInt(i, n) + u.sep + u.units[0]
	}
	v, k := float64(n), 0
	for v >= 1000 && k < len(u.units)-1 {
		v /= 1000
		k++
	}
	// One decimal under 100 ("4.6 GB"), none from there ("984 GB"); a
	// value that rounds up to 1000 moves to the next unit.
	if math.Round(v*10)/10 < 100 {
		return b.printers[i].Sprint(number.Decimal(v, number.MinFractionDigits(1), number.MaxFractionDigits(1))) + u.sep + u.units[k]
	}
	if math.Round(v) >= 1000 && k < len(u.units)-1 {
		return b.printers[i].Sprint(number.Decimal(v/1000, number.MinFractionDigits(1), number.MaxFractionDigits(1))) + u.sep + u.units[k+1]
	}
	return b.printers[i].Sprint(number.Decimal(math.Round(v))) + u.sep + u.units[k]
}

// dateStyle is how a language writes a date: day, abbreviated month and
// year ("11 oct 2026"), or numbers in its order ("11.10.2026"). x/text
// has no date formats; these follow CLDR's medium dates, with the time in
// 24 hours as the device writes it everywhere ("2 Jan 15:04").
type dateStyle struct {
	months [12]string // abbreviated names; empty for a numeric date
	sep    string     // between the numbers of a numeric date
}

var dateStyles = map[string]dateStyle{
	"en": {months: [12]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}},
	"es": {months: [12]string{"ene", "feb", "mar", "abr", "may", "jun", "jul", "ago", "sept", "oct", "nov", "dic"}},
	"fr": {months: [12]string{"janv.", "févr.", "mars", "avr.", "mai", "juin", "juil.", "août", "sept.", "oct.", "nov.", "déc."}},
	"it": {months: [12]string{"gen", "feb", "mar", "apr", "mag", "giu", "lug", "ago", "set", "ott", "nov", "dic"}},
	"nl": {months: [12]string{"jan", "feb", "mrt", "apr", "mei", "jun", "jul", "aug", "sep", "okt", "nov", "dec"}},
	"de": {sep: "."},
	"pt": {sep: "/"},
}

func (b *bundle) formatTime(i int, t time.Time) string {
	base, _ := b.tags[i].Base()
	st, ok := dateStyles[base.String()]
	if !ok {
		return t.Format("2006-01-02 15:04")
	}
	var s strings.Builder
	if st.sep != "" {
		s.WriteString(t.Format("02" + st.sep + "01" + st.sep + "2006"))
	} else {
		s.WriteString(strconv.Itoa(t.Day()))
		s.WriteString(" ")
		s.WriteString(st.months[t.Month()-1])
		s.WriteString(" ")
		s.WriteString(strconv.Itoa(t.Year()))
	}
	s.WriteString(", ")
	s.WriteString(t.Format("15:04"))
	return s.String()
}

// unixTime is a datetime argument (unix seconds) in the machine's zone.
func unixTime(sec int64) time.Time { return time.Unix(sec, 0) }

// toInt reads an integer argument: any Go integer that fits an int64, a
// float64 or json.Number holding a whole number (stored arguments decoded
// as JSON), or a time.Time (a datetime, as unix seconds).
func toInt(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int8:
		return int64(n), true
	case int16:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case uint:
		return int64(n), uint64(n) <= math.MaxInt64
	case uint8:
		return int64(n), true
	case uint16:
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		return int64(n), n <= math.MaxInt64
	case float64:
		if n != math.Trunc(n) || n < math.MinInt64 || n >= math.MaxInt64 {
			return 0, false
		}
		return int64(n), true
	case json.Number:
		i, err := strconv.ParseInt(string(n), 10, 64)
		return i, err == nil
	case time.Time:
		return n.Unix(), true
	}
	return 0, false
}
