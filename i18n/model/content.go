// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// What a form may contain, whatever the language. Translated text ends up
// in web pages on the bridge's apex, in the wizard that runs as root and
// in Android resources, so anything that could turn into markup, a link
// or a reordering of the line is refused before an emitter sees it.

var (
	// "on…=", as in onerror=: an event-handler attribute.
	eventAttrPattern = regexp.MustCompile(`(?i)\bon[a-z]+\s*=`)
	// javascript:alert(1), vbscript:…, data:text/html,… (a colon followed
	// by a space is ordinary punctuation: "needs JavaScript: turn it on").
	scriptURLPattern = regexp.MustCompile(`(?i)\b(java\s*script|vb\s*script|data)\s*:[^\s]+`)
	// "&amp;", "&#60;", "&#x3c;": the catalog holds characters, not entities.
	entityPattern = regexp.MustCompile(`&(#[0-9]+|#[xX][0-9a-fA-F]+|[a-zA-Z][a-zA-Z0-9]*);`)
	// URLs with a scheme, mailto:, emails and bare domains.
	urlPattern    = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*:(//|[^\s/]*@)[^\s<>"'{}]*`)
	emailPattern  = regexp.MustCompile(`(?i)[a-z0-9._%+-]+@[a-z0-9-]+(\.[a-z0-9-]+)*\.[a-z]{2,}`)
	domainPattern = regexp.MustCompile(`(?i)\b([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}\b`)
)

// contentProblems lists what is wrong with one form on its own.
func contentProblems(s string) []string {
	var out []string
	if s == "" {
		return []string{"empty text"}
	}
	seen := map[string]bool{}
	add := func(msg string) {
		if !seen[msg] {
			seen[msg] = true
			out = append(out, msg)
		}
	}
	for _, r := range s {
		switch {
		case r == '\t':
			add("tab character (aapt2 collapses whitespace; say it in words or use a line break)")
		case r == '\n':
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			add(fmt.Sprintf("control character U+%04X", r))
		case (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069):
			add(fmt.Sprintf("bidi control character U+%04X (isolation is added at render time)", r))
		case r == 0x2028 || r == 0x2029 || r == 0xfeff || r == 0xfffe || r == 0xffff:
			add(fmt.Sprintf("invisible character U+%04X", r))
		case r >= 0xe000 && r <= 0xf8ff:
			add(fmt.Sprintf("private-use character U+%04X (reserved for the rich-text renderers)", r))
		case r == utf8.RuneError:
			add("invalid UTF-8")
		}
	}
	if first, _ := utf8.DecodeRuneInString(s); unicode.IsSpace(first) {
		add("leading whitespace")
	}
	if last, _ := utf8.DecodeLastRuneInString(s); unicode.IsSpace(last) {
		add("trailing whitespace")
	}
	if strings.Contains(s, "  ") {
		add("double space")
	}
	if strings.Contains(s, " \n") || strings.Contains(s, "\n ") {
		add("space next to a line break")
	}
	lower := strings.ToLower(s)
	if strings.Contains(lower, "<script") {
		add("<script")
	}
	if eventAttrPattern.MatchString(s) {
		add("event-handler attribute (on…=)")
	}
	if m := scriptURLPattern.FindString(s); m != "" {
		add(fmt.Sprintf("script URL %q", m))
	}
	if m := entityPattern.FindString(s); m != "" {
		add(fmt.Sprintf("HTML entity %s (write the character itself)", m))
	}
	return out
}

// linksIn returns the URLs, email addresses and domains in s.
func linksIn(s string) []string {
	var out []string
	for _, re := range []*regexp.Regexp{urlPattern, emailPattern, domainPattern} {
		out = append(out, re.FindAllString(s, -1)...)
	}
	return out
}
