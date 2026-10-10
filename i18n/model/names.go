// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"slices"
	"strings"
)

// The names keys and prefixes get on each platform. Emitters must use
// these functions (not their own renaming), because Check verifies that
// they never collide.

// AndroidName is the resource name: "app.photos.deleted_by" ->
// "app_photos_deleted_by".
func AndroidName(key string) string { return strings.ReplaceAll(key, ".", "_") }

// CamelName is the Swift and Kotlin accessor name: "app.photos.deleted_by"
// -> "appPhotosDeletedBy". Every segment and underscore-separated part
// after the first starts upper-case.
func CamelName(key string) string {
	var b strings.Builder
	for i, part := range strings.FieldsFunc(key, func(r rune) bool { return r == '.' || r == '_' }) {
		if i == 0 {
			b.WriteString(part)
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]))
		b.WriteString(part[1:])
	}
	return b.String()
}

// GoName is the exported Go constructor name: "AppPhotosDeletedBy".
func GoName(key string) string {
	c := CamelName(key)
	if c == "" {
		return c
	}
	return strings.ToUpper(c[:1]) + c[1:]
}

// PrefixFileName is a prefix as part of a file name: "app.photos" ->
// "app_photos" (Android's strings_app_photos.xml).
func PrefixFileName(prefix string) string { return AndroidName(prefix) }

// PrefixTypeName is a prefix as a type or table name: "app.photos" ->
// "AppPhotos" (Apple's AppPhotos.xcstrings and S+AppPhotos.swift).
func PrefixTypeName(prefix string) string { return GoName(prefix) }

// ArgName is an argument's name in Swift, Kotlin, TypeScript and Go code:
// "file_name" -> "fileName".
func ArgName(name string) string { return CamelName(name) }

// Words an argument name may not be, per language accessors are generated
// in: an argument becomes a parameter on its own, so it can't be a keyword
// (or, in Go, a predeclared type or constant). A key's own identifiers join
// two or more segments, so they can never be one of these.
var reservedWords = map[string]map[string]bool{
	"Swift":  words("associatedtype class deinit enum extension fileprivate func import init inout internal let open operator private precedencegroup protocol public rethrows static struct subscript typealias var break case catch continue default defer do else fallthrough for guard if in repeat return throw switch where while as await false is nil self super throws true try any some Any Self Type"),
	"Kotlin": words("as break class continue do else false for fun if in interface is null object package return super this throw true try typealias typeof val var when while"),
	"Go": words("break case chan const continue default defer else fallthrough for func go goto if import interface map package range return select struct switch type var " +
		"any bool byte comparable complex64 complex128 error float32 float64 int int8 int16 int32 int64 rune string uint uint8 uint16 uint32 uint64 uintptr true false iota nil"),
	"TypeScript": words("break case catch class const continue debugger default delete do else enum export extends false finally for function if import in instanceof new null return super switch this throw true try typeof var void while with yield let static implements interface package private protected public await arguments eval undefined"),
}

// consumerLanguages are the languages each consumer's accessors are
// generated in. The wizard looks keys up by name with its arguments in a
// JSON object, so it has no parameter names.
var consumerLanguages = map[string][]string{
	ConsumerWeb:     {"TypeScript"},
	ConsumerIOS:     {"Swift"},
	ConsumerMacOS:   {"Swift"},
	ConsumerAndroid: {"Kotlin"},
	ConsumerOtcSync: {"Go"},
	ConsumerGo:      {"Go"},
}

func words(list string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(list) {
		m[w] = true
	}
	return m
}

// ReservedIn returns the generated languages, among those of consumers, in
// which name (as ArgName renders it) can't be a parameter name.
func ReservedIn(name string, consumers []string) []string {
	var out []string
	id := ArgName(name)
	for _, lang := range []string{"Go", "Kotlin", "Swift", "TypeScript"} {
		for _, c := range consumers {
			if slices.Contains(consumerLanguages[c], lang) && reservedWords[lang][id] {
				out = append(out, lang)
				break
			}
		}
	}
	return out
}
