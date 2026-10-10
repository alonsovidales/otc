// SPDX-License-Identifier: AGPL-3.0-or-later

package scan

import (
	"regexp"
	"strings"
	"unicode"
)

// IgnoreMarker is the escape hatch: a line whose trailing comment says
// "i18n-ignore: <reason>" keeps its literal without being counted. The
// reason is required; a marker without one is not honoured.
//
//	Text(verbatim: name) ... Text("OTC-\(id)") // i18n-ignore: device id, never translated
//	<span>Off The Cloud</span>                  {/* i18n-ignore: product name */}
//	<p>OTC</p>                                  <!-- i18n-ignore: product name -->
//	"error": "..."                              # i18n-ignore: protocol value
const IgnoreMarker = "i18n-ignore:"

var ignoreRe = regexp.MustCompile(`(?://|/\*|<!--|#)\s*i18n-ignore:(.*)$`)

// hasIgnore reports whether a source line carries the marker with a reason.
func hasIgnore(line string) bool {
	m := ignoreRe.FindStringSubmatch(line)
	if m == nil {
		return false
	}
	reason := m[1]
	for _, end := range []string{"*/", "-->", "}"} {
		if i := strings.Index(reason, end); i >= 0 {
			reason = reason[:i]
		}
	}
	return strings.ContainsFunc(reason, unicode.IsLetter)
}

// Generated blocks inside hand-written files (the wizard's dictionary, an
// injected one in a page) are the catalog's output, not hard-coded text.
const (
	beginGenerated = "BEGIN GENERATED I18N"
	endGenerated   = "END GENERATED I18N"
)

// generatedLines marks the lines from a begin marker to its end marker,
// both included.
func generatedLines(lines []string) []bool {
	out := make([]bool, len(lines))
	inside := false
	for i, l := range lines {
		if strings.Contains(l, beginGenerated) {
			inside = true
		}
		out[i] = inside
		if strings.Contains(l, endGenerated) {
			inside = false
		}
	}
	return out
}
