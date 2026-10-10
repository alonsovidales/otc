// SPDX-License-Identifier: AGPL-3.0-or-later

// Package model is the in-memory form of Off The Cloud's translation
// catalog (docs/i18n.md). It reads i18n/languages.json, the English
// sources in i18n/strings/en, the translations in i18n/strings/<code> and
// the glossaries, checks every rule the design sets, writes the sources
// back in their canonical form, and hands the platform emitters of
// i18n/cmd/i18ngen the merged text they generate files from.
//
// The words used throughout:
//
//   - key: "app.photos.deleted_by" (see KeyPattern).
//   - root: a key's first segment ("app"). languages.json routes every
//     root to the consumers (the programs) that show its keys.
//   - prefix: the source file a key lives in, without ".json": the root
//     itself ("common", "push"), or the root and one more segment
//     ("app.photos") for a root split over several files. Generated
//     files are split by prefix too, so units working on different
//     prefixes never touch the same file.
//   - form: one of a text's plural forms, "one" or "other". A text that
//     isn't plural has the single form "other".
package model

import (
	"regexp"
	"strings"
	"sync"

	"golang.org/x/text/feature/plural"
	"golang.org/x/text/language"
)

// SourceCode is the language every key is written in first, and the
// fallback for anything not translated.
const SourceCode = "en"

// Directories and files, relative to the repository root.
const (
	LanguagesFile = "i18n/languages.json"
	StringsDir    = "i18n/strings"
	GlossaryDir   = "i18n/glossary"
	NeverFile     = "i18n/glossary/never.json"
	StoredFile    = "i18n/stored.json"
)

// KeyPattern is the shape of every key: lowercase segments joined by dots,
// at least two, underscores only inside a segment after its first part.
var KeyPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z0-9]+(_[a-z0-9]+)*)+$`)

// MaxKeyLen is the longest key accepted: notifications.msg_key, where the
// device stores alert keys, is a VARCHAR(96).
const MaxKeyLen = 96

var (
	argNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	tagNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*$`)
	rootPattern    = regexp.MustCompile(`^[a-z][a-z0-9]*$`)
	codePattern    = regexp.MustCompile(`^[a-z]{2,3}$`)
	prefixPattern  = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z0-9]+(_[a-z0-9]+)*)?$`)
)

// Consumers are the programs a root can be routed to. "go" is everything
// built from package i18n's embedded catalog on the device and the bridge
// (replies, alerts, pushes, emails, API errors, public pages); otc-sync
// reads the same catalog but is a consumer of its own because it shows
// different roots.
const (
	ConsumerWeb     = "web"
	ConsumerIOS     = "ios"
	ConsumerAndroid = "android"
	ConsumerMacOS   = "macos"
	ConsumerOtcSync = "otcsync"
	ConsumerGo      = "go"
	ConsumerWizard  = "wizard"
)

// Consumers lists every consumer, in a fixed order.
var Consumers = []string{ConsumerWeb, ConsumerIOS, ConsumerAndroid, ConsumerMacOS, ConsumerOtcSync, ConsumerGo, ConsumerWizard}

// Status says whether a language goes into the generated files.
type Status string

const (
	Shipping Status = "shipping"
	Draft    Status = "draft"
)

// Language is one entry of languages.json.
type Language struct {
	// Code is what is stored, sent on the wire and used in file names.
	Code string
	// Tag is the BCP 47 tag behind plural rules and number and date
	// formats ("pt-PT": "pt" alone would mean Brazilian rules).
	Tag string
	// Apple is the .lproj name.
	Apple string
	// Android is the resource qualifier after "values-" ("" for the
	// default resources, which are English).
	Android string
	// Name is the language's own name for itself, shown in the pickers.
	Name   string
	Status Status
	// Pseudo marks the pseudo-locale that -draft adds (PseudoLanguage);
	// it never comes from languages.json.
	Pseudo bool
}

// IsSource reports whether this is the English source language.
func (l Language) IsSource() bool { return l.Code == SourceCode }

// LanguageTag parses Tag; languages.json is checked to hold valid tags.
func (l Language) LanguageTag() language.Tag {
	t, err := language.Parse(l.Tag)
	if err != nil {
		return language.Und
	}
	return t
}

// OneMeansOne reports whether the language uses its plural form "one" for
// the number 1 alone, so that a "one" text may say "a photo" without
// {count}. It is false for French, where 0 is "one" too.
func (l Language) OneMeansOne() bool {
	if v, ok := oneMeansOne.Load(l.Tag); ok {
		return v.(bool)
	}
	t := l.LanguageTag()
	only := true
	for n := 0; n <= 200 && only; n++ {
		only = n == 1 || plural.Cardinal.MatchPlural(t, n, 0, 0, 0, 0) != plural.One
	}
	oneMeansOne.Store(l.Tag, only)
	return only
}

// oneMeansOne caches OneMeansOne by tag.
var oneMeansOne sync.Map

// ArgType is the declared type of an argument.
type ArgType string

const (
	// ArgText is text the app supplies.
	ArgText ArgType = "text"
	// ArgUser is user-controlled text, wrapped in FSI/PDI isolation marks
	// when rendered.
	ArgUser ArgType = "user"
	// ArgCount is the integer that picks the plural form; at most one per
	// key, and a key has one exactly when its text is plural.
	ArgCount ArgType = "count"
	// ArgInt is any other integer, formatted for the language.
	ArgInt ArgType = "int"
	// ArgBytes, ArgDatetime (unix seconds) and ArgMsg (a sub-key from an
	// allowlist in code) exist only for keys rendered by Go from stored
	// data: roots routed to "go" alone.
	ArgBytes    ArgType = "bytes"
	ArgDatetime ArgType = "datetime"
	ArgMsg      ArgType = "msg"
)

var argTypes = map[ArgType]bool{ArgText: true, ArgUser: true, ArgCount: true, ArgInt: true, ArgBytes: true, ArgDatetime: true, ArgMsg: true}

// goOnlyArgTypes need a consumer that renders from stored data.
var goOnlyArgTypes = map[ArgType]bool{ArgBytes: true, ArgDatetime: true, ArgMsg: true}

// Arg is one declared argument. Its position N (1-based) in Entry.Args is
// its position in every language and every generated accessor.
type Arg struct {
	Name string
	Type ArgType
}

// Plural form names.
const (
	FormOne   = "one"
	FormOther = "other"
)

// Text is an entry's text: one string, or the plural forms one and other
// (exactly those two; a special text for zero is a key of its own).
type Text struct {
	Plural bool
	// One is the "one" form; empty unless Plural.
	One string
	// Other is the "other" form, or the whole text when not Plural.
	Other string
}

// Form is one form of a text.
type Form struct {
	Name string // FormOne or FormOther
	Text string
}

// PlainText returns a text that isn't plural.
func PlainText(s string) Text { return Text{Other: s} }

// PluralText returns a plural text.
func PluralText(one, other string) Text { return Text{Plural: true, One: one, Other: other} }

// Forms returns the forms in order: one then other, or just other.
func (t Text) Forms() []Form {
	if t.Plural {
		return []Form{{FormOne, t.One}, {FormOther, t.Other}}
	}
	return []Form{{FormOther, t.Other}}
}

// Form returns the named form ("" when the text has no such form).
func (t Text) Form(name string) string {
	switch {
	case name == FormOther:
		return t.Other
	case name == FormOne && t.Plural:
		return t.One
	}
	return ""
}

// Equal reports whether two texts are identical, plural shape included.
func (t Text) Equal(u Text) bool { return t == u }

// Map applies f to every form.
func (t Text) Map(f func(string) string) Text {
	if t.Plural {
		return PluralText(f(t.One), f(t.Other))
	}
	return PlainText(f(t.Other))
}

// Entry is one English source entry.
type Entry struct {
	Key    string
	Root   string
	Prefix string
	Text   Text
	Args   []Arg
	// Note tells translators where and how the text is shown.
	Note string
	// Max is the most characters a form may have (0: no limit), counted
	// as code points with tags left out and each placeholder as written.
	Max int
	// Rich lists the tags the text may use; a key with any is rich and
	// gets the platforms' rich accessors.
	Rich []string
	// Stored marks keys written to a database: such a key can never be
	// deleted or have its arguments changed (StoredFile keeps the record).
	Stored bool
	// Review is "required" for consent, legal, security and destructive
	// text, which then needs Reviewed in English and in every translation.
	Review string
	// Reviewed is "<who> <YYYY-MM-DD> #<fingerprint>" (see Fingerprint).
	Reviewed string
	// Translate is false for text that stays English in every language.
	Translate bool

	File string // repository-relative source file
	Line int
}

// ReviewRequired is the one value of Entry.Review.
const ReviewRequired = "required"

// IsRich reports whether the entry may carry tags.
func (e *Entry) IsRich() bool { return len(e.Rich) > 0 }

// ArgIndex returns the 0-based position of the named argument, or -1.
func (e *Entry) ArgIndex(name string) int {
	for i, a := range e.Args {
		if a.Name == name {
			return i
		}
	}
	return -1
}

// CountArg returns the argument that picks the plural form, if any.
func (e *Entry) CountArg() (Arg, int, bool) {
	for i, a := range e.Args {
		if a.Type == ArgCount {
			return a, i, true
		}
	}
	return Arg{}, -1, false
}

// Translation is one entry of a translation file: the text and the
// English it was made from.
type Translation struct {
	Key  string
	Lang string
	Text Text
	// EN is the English text this translation was made from. When it no
	// longer equals the entry's Text, the translation is stale.
	EN Text
	// Reviewed is set when someone checked this translation of a
	// review-required key, in the same format as Entry.Reviewed.
	Reviewed string

	File string
	Line int
}

// rootOf returns a key's first segment.
func rootOf(key string) string {
	if i := strings.IndexByte(key, '.'); i >= 0 {
		return key[:i]
	}
	return key
}
