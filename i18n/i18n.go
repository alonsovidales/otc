// SPDX-License-Identifier: AGPL-3.0-or-later

// Package i18n renders Off The Cloud's text in the user's language for the
// Go programs: the device (replies, alerts, pushes), the bridge (emails,
// API errors, public pages) and otc-sync (its tray). The text is the
// catalog `make i18n` writes into catalog/ from i18n/strings (docs/i18n.md,
// i18n/README.md), embedded in the binary: every language the build ships,
// every key the Go programs show.
//
// Code builds a message with the constructor generated for its key and
// renders it once it knows the language:
//
//	m := i18n.DevPhotosDeleted(name, n) // i18n.Msg{Key, Args}
//	reply := m.Render(env.Lang)
//
// The rendering contract (docs/i18n.md) holds throughout: a template is
// split into literal text and placeholders once, and every argument goes
// in as plain text in one pass, never scanned again. User-controlled
// arguments are wrapped in FSI/PDI isolation marks. A key a language
// lacks falls back to English, and a key English lacks renders as the key
// itself. Plural forms follow the language's canonical tag (pt-PT, never
// pt), and numbers its formats.
//
// Nothing here logs an argument or a rendered text: a missing key or
// arguments that don't fit their key are logged by key, once.
package i18n

import (
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/alonsovidales/otc/log"
)

// Msg is a message to render later: a key and its arguments by name. The
// generated constructors (msg_*_gen.go) build it with the names and types
// the key declares: a string for text, user and msg arguments, an int64
// for count, int, bytes and datetime (unix seconds) ones.
type Msg struct {
	Key  string
	Args map[string]any
}

// Render returns the message in lang: a language code as a client sends
// it (ReqEnvelope.lang, a stored choice); anything Normalize doesn't know
// renders English.
func (m Msg) Render(lang string) string { return std.render(lang, m.Key, m.Args) }

// Rich is a message whose text marks spans with tags ("Read the
// <link>privacy policy</link>"), built by the constructors of rich keys.
// It has no Render: the caller decides what each tag becomes (Parts), so
// rendering a rich key as plain text doesn't compile.
type Rich struct {
	Key  string
	Args map[string]any
}

// Parts returns the message in lang split at its tags.
func (r Rich) Parts(lang string) []Part { return std.parts(lang, r.Key, r.Args) }

// Part is one span of a rich text: plain text when Tag is "", else the
// text between <Tag> and </Tag> (tags don't nest). Text has its arguments
// in place and is plain text: escape it for whatever it is written into.
type Part struct {
	Tag  string
	Text string
}

// T renders key in lang with args, like Msg{key, args}.Render(lang). A
// rich key's tags are left out.
func T(lang, key string, args map[string]any) string { return std.render(lang, key, args) }

// Isolation marks around a user-controlled argument, so that a name in
// another script can't reorder the sentence around it.
const (
	fsi = "⁨" // FIRST STRONG ISOLATE
	pdi = "⁩" // POP DIRECTIONAL ISOLATE
)

// isolate wraps a user-controlled argument in FSI/PDI. Bidi embedding,
// override and isolate controls inside it are dropped first: a PDI there
// would end the isolation early, and the others have no business in a
// name.
func isolate(s string) string {
	if strings.ContainsFunc(s, isBidiControl) {
		s = strings.Map(func(r rune) rune {
			if isBidiControl(r) {
				return -1
			}
			return r
		}, s)
	}
	return fsi + s + pdi
}

// isBidiControl reports the explicit directional formatting characters:
// LRE, RLE, PDF, LRO, RLO (U+202A-202E) and LRI, RLI, FSI, PDI
// (U+2066-2069).
func isBidiControl(r rune) bool {
	return (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069)
}

// logged remembers what was logged, so a missing key in a busy reply path
// is one line, not one per request. It stops growing at maxLogged.
var (
	logged  sync.Map
	nLogged atomic.Int32
)

const maxLogged = 256

// logOnce logs a problem with a key, by key only: never its arguments.
func logOnce(problem, key string) {
	if key == "" || nLogged.Load() >= maxLogged {
		return
	}
	if _, seen := logged.LoadOrStore(problem+"\x00"+key, true); seen {
		return
	}
	nLogged.Add(1)
	if len(key) > maxKeyLen {
		key = key[:maxKeyLen]
	}
	log.Error("i18n:", problem, strconv.Quote(key))
}

// maxKeyLen is the longest key (notifications.msg_key is a VARCHAR(96)).
const maxKeyLen = 96
