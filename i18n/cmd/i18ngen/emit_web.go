// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/alonsovidales/otc/i18n/model"
)

func init() { register(webEmitter{}) }

// webEmitter writes the web app's files (docs/i18n.md, "Generated files"):
//
//   - web/src/i18n/locales/<code>/<prefix>.json for every language and
//     every prefix routed to the web: key -> text, or key -> PluralMessage
//     ({"one", "other", "arg"}) for a key with a count. The runtime loads
//     them with import.meta.glob("./locales/*/*.json").
//   - web/src/i18n/messages.ts: the language table and the types that check
//     every lookup - MessageKey (keys without tags, for t()), RichKey (keys
//     with tags, for <Trans>), interface MessageArgs (each key's arguments)
//     and interface RichTags (each rich key's tags). Types only, apart from
//     the small language table, and no enums (erasableSyntaxOnly).
//
// Texts are data: they only ever go into the JSON files, written by
// encoding/json with its HTML escaping, and never into TypeScript source.
// Each form is rebuilt from model.Tokenize's tokens, so the runtime parses
// exactly this grammar:
//
//   - {fileName}: an argument, named as in MessageArgs (model.ArgName). A
//     user argument is written between U+2068 and U+2069 (FSI ... PDI), so
//     the runtime inserts every argument the same way, as plain text.
//   - <link> and </link>: a span of a rich key; tags don't nest.
//   - Anything else is literal text with no "{", "}" or "<" in it (a ">"
//     may appear).
type webEmitter struct{}

const (
	webMessagesFile = "web/src/i18n/messages.ts"
	webLocalesDir   = "web/src/i18n/locales"
)

// webDraftKey holds DraftMarker in the JSON files of a -draft run. It can't
// be a message key (those start with a letter), and lookups never ask for it.
const webDraftKey = "//"

// FSI and PDI, around every user argument.
const (
	webFSI = "\u2068"
	webPDI = "\u2069"
)

var (
	webCodePattern    = regexp.MustCompile(`^[a-z]{2,3}$`)
	webArgNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	webTagNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*$`)
)

func (webEmitter) Name() string        { return "web" }
func (webEmitter) Consumers() []string { return []string{model.ConsumerWeb} }
func (webEmitter) Owns() []string      { return []string{webLocalesDir + "/*/*.json", webMessagesFile} }

func (webEmitter) Emit(c *model.Catalog, opts Options) (map[string][]byte, error) {
	if len(opts.Languages) == 0 || !opts.Languages[0].IsSource() {
		return nil, fmt.Errorf("the languages to write must start with English")
	}
	entries := c.EntriesFor(model.ConsumerWeb)
	args := make(map[string]string, len(entries)) // key -> its MessageArgs type
	for _, e := range entries {
		a, err := webArgsType(e)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Key, err)
		}
		args[e.Key] = a
	}
	out := map[string][]byte{}
	for _, l := range opts.Languages {
		if !webCodePattern.MatchString(l.Code) {
			return nil, fmt.Errorf("language code %q can't name a directory", l.Code)
		}
		for _, p := range c.PrefixesFor(model.ConsumerWeb) {
			data, err := webLocale(c.Messages(l, p), opts.Draft)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", l.Code, err)
			}
			out[webLocalesDir+"/"+l.Code+"/"+p+".json"] = data
		}
	}
	ts, err := webMessages(entries, args, opts)
	if err != nil {
		return nil, err
	}
	out[webMessagesFile] = ts
	return out, nil
}

// webPlural is a plural text in a locale file. Arg names the argument that
// picks the form (the key's count), as MessageArgs names it.
type webPlural struct {
	One   string `json:"one"`
	Other string `json:"other"`
	Arg   string `json:"arg"`
}

// webLocale writes one locale file: every key of a prefix in one language.
func webLocale(msgs []model.Resolved, draft bool) ([]byte, error) {
	m := make(map[string]any, len(msgs)+1)
	if draft {
		m[webDraftKey] = DraftMarker + ": written by make i18n DRAFT=1 - run make i18n before committing"
	}
	for _, r := range msgs {
		if !model.KeyPattern.MatchString(r.Key) {
			return nil, fmt.Errorf("%q is not a key", r.Key)
		}
		if !r.Text.Plural {
			s, err := webTemplate(r.Entry, r.Text.Other)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", r.Key, err)
			}
			m[r.Key] = s
			continue
		}
		count, _, ok := r.CountArg()
		if !ok {
			return nil, fmt.Errorf("%s: a plural text needs a count argument", r.Key)
		}
		one, err := webTemplate(r.Entry, r.Text.One)
		if err != nil {
			return nil, fmt.Errorf("%s (one): %w", r.Key, err)
		}
		other, err := webTemplate(r.Entry, r.Text.Other)
		if err != nil {
			return nil, fmt.Errorf("%s (other): %w", r.Key, err)
		}
		m[r.Key] = webPlural{One: one, Other: other, Arg: model.ArgName(count.Name)}
	}
	// Map keys come out sorted. Marshal escapes "<", ">" and "&" (so no
	// "</script>" survives anywhere this file is pasted), U+2028 and
	// U+2029; the isolation marks are escaped too, so that a review of the
	// file sees them.
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	s := strings.NewReplacer(webFSI, `\u2068`, webPDI, `\u2069`).Replace(string(data))
	return []byte(s + "\n"), nil
}

// webTemplate rebuilds one form for the runtime from its tokens: arguments
// renamed to their MessageArgs names, user arguments isolated, tags kept.
// Anything the grammar or the entry doesn't allow is an error.
func webTemplate(e *model.Entry, form string) (string, error) {
	toks, err := model.Tokenize(form)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, t := range toks {
		switch t.Kind {
		case model.Literal:
			b.WriteString(t.Value)
		case model.Placeholder:
			i := e.ArgIndex(t.Value)
			if i < 0 {
				return "", fmt.Errorf("{%s} is not a declared argument", t.Value)
			}
			ph := "{" + model.ArgName(t.Value) + "}"
			if e.Args[i].Type == model.ArgUser {
				ph = webFSI + ph + webPDI
			}
			b.WriteString(ph)
		case model.OpenTag, model.CloseTag:
			if !slices.Contains(e.Rich, t.Value) {
				return "", fmt.Errorf("<%s> is not one of the key's tags", t.Value)
			}
			if t.Kind == model.OpenTag {
				b.WriteString("<" + t.Value + ">")
			} else {
				b.WriteString("</" + t.Value + ">")
			}
		default:
			return "", fmt.Errorf("unknown token kind %d", t.Kind)
		}
	}
	return b.String(), nil
}

// webArgsType is a key's member type in MessageArgs: undefined when it
// takes no arguments, else an object type naming each one.
func webArgsType(e *model.Entry) (string, error) {
	if !model.KeyPattern.MatchString(e.Key) {
		return "", fmt.Errorf("not a key")
	}
	if len(e.Args) == 0 {
		return "undefined", nil
	}
	seen := map[string]bool{}
	fields := make([]string, 0, len(e.Args))
	for _, a := range e.Args {
		if !webArgNamePattern.MatchString(a.Name) {
			return "", fmt.Errorf("argument name %q", a.Name)
		}
		id := model.ArgName(a.Name)
		if seen[id] {
			return "", fmt.Errorf("two arguments are named %s in TypeScript", id)
		}
		seen[id] = true
		var ts string
		switch a.Type {
		case model.ArgText, model.ArgUser:
			ts = "string"
		case model.ArgCount, model.ArgInt:
			ts = "number"
		default:
			return "", fmt.Errorf("argument %q: the web can't show type %q", a.Name, a.Type)
		}
		fields = append(fields, id+": "+ts)
	}
	return "{ " + strings.Join(fields, "; ") + " }", nil
}

// webMessages writes messages.ts.
func webMessages(entries []*model.Entry, args map[string]string, opts Options) ([]byte, error) {
	var plain, rich []*model.Entry
	for _, e := range entries {
		if e.IsRich() {
			rich = append(rich, e)
		} else {
			plain = append(plain, e)
		}
	}
	var b strings.Builder
	w := func(lines ...string) {
		for _, l := range lines {
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}

	w("// SPDX-License-Identifier: AGPL-3.0-or-later",
		"// Code generated by i18ngen from i18n/languages.json and i18n/strings. DO NOT EDIT.")
	if opts.Draft {
		w("// " + DraftMarker + ": written by make i18n DRAFT=1 - run make i18n before committing.")
	}
	w("//",
		"// Change the catalog and run make i18n (i18n/README.md). The texts are in",
		"// locales/<code>/<prefix>.json, one file per language and prefix (type",
		"// Messages), for import.meta.glob. In a text:",
		"//",
		"//   {fileName}       an argument, by its name in MessageArgs: inserted as plain",
		"//                    text, a number formatted with Intl first. A user argument",
		"//                    is already between U+2068 and U+2069 (FSI ... PDI).",
		"//   <link>...</link> a span of a RichKey, rendered by code; tags don't nest.",
		"//",
		"// No other \"{\", \"}\" or \"<\" appears. A PluralMessage's arg names the count:",
		"// use forms[new Intl.PluralRules(tag).select(n)] ?? forms.other.",
		"")

	w("/** A language the generated locales hold (i18n/languages.json). */",
		"export interface Language {",
		"  /** Stored, sent on the wire and used in file names (\"pt\"). */",
		"  readonly code: LanguageCode;",
		"  /** The BCP 47 tag behind plural rules and formatters (\"pt-PT\"). */",
		"  readonly tag: string;",
		"  /** The language's own name, for the picker. */",
		"  readonly name: string;",
		"  readonly status: \"shipping\" | \"draft\";",
		"}",
		"")
	codes := make([]string, 0, len(opts.Languages))
	rows := make([]string, 0, len(opts.Languages))
	for _, l := range opts.Languages {
		if l.Status != model.Shipping && l.Status != model.Draft {
			return nil, fmt.Errorf("%s: status %q", l.Code, l.Status)
		}
		codes = append(codes, tsString(l.Code))
		rows = append(rows, fmt.Sprintf("  { code: %s, tag: %s, name: %s, status: %s },",
			tsString(l.Code), tsString(l.Tag), tsString(l.Name), tsString(string(l.Status))))
	}
	w("export type LanguageCode ="+tsUnion(codes),
		"",
		"/** English: the source, and the text of anything not translated. */",
		"export const sourceLanguage: LanguageCode = "+tsString(model.SourceCode)+";",
		"",
		"/** The languages, English first. */",
		"export const languages: readonly Language[] = [")
	w(rows...)
	w("];",
		"")

	w("/** A text with a count: one form per plural category, other as the fallback. */",
		"export interface PluralMessage {",
		"  readonly one: string;",
		"  readonly other: string;",
		"  /** The argument that picks the form. */",
		"  readonly arg: string;",
		"}",
		"",
		"/** A locale file: key -> text. */",
		"export type Messages = Readonly<Record<string, string | PluralMessage>>;",
		"")

	keyNames := func(es []*model.Entry) []string {
		out := make([]string, len(es))
		for i, e := range es {
			out[i] = tsString(e.Key)
		}
		return out
	}
	w("/** The keys t() looks up: every web key without tags. */",
		"export type MessageKey ="+tsUnion(keyNames(plain)),
		"",
		"/** The keys <Trans> renders: every web key with tags. */",
		"export type RichKey ="+tsUnion(keyNames(rich)),
		"")

	w("/** Each key's arguments by name; undefined when it takes none. */")
	if len(entries) == 0 {
		w("// eslint-disable-next-line @typescript-eslint/no-empty-object-type")
	}
	members := make([]string, 0, len(entries))
	for _, e := range entries {
		members = append(members, tsString(e.Key)+": "+args[e.Key]+";")
	}
	w("export interface MessageArgs "+tsBlock(members),
		"")

	w("/** Each rich key's tags. */")
	if len(rich) == 0 {
		w("// eslint-disable-next-line @typescript-eslint/no-empty-object-type")
	}
	members = members[:0]
	for _, e := range rich {
		tags := slices.Compact(slices.Sorted(slices.Values(e.Rich)))
		for i, t := range tags {
			if !webTagNamePattern.MatchString(t) {
				return nil, fmt.Errorf("%s: tag name %q", e.Key, t)
			}
			tags[i] = tsString(t)
		}
		members = append(members, tsString(e.Key)+": "+strings.Join(tags, " | ")+";")
	}
	w("export interface RichTags "+tsBlock(members),
		"")

	w("/**",
		" * What follows the key in a lookup of K: nothing, or its arguments, as in",
		" * t<K extends MessageKey>(key: K, ...args: ArgsOf<K>).",
		" */",
		"export type ArgsOf<K extends keyof MessageArgs> = MessageArgs[K] extends undefined ? [] : [args: MessageArgs[K]];")
	return []byte(b.String()), nil
}

// tsString is s as a TypeScript string literal. JSON's string syntax is
// JavaScript's, and encoding/json also escapes "<", ">", "&", U+2028 and
// U+2029.
func tsString(s string) string {
	b, _ := json.Marshal(s) // a string always marshals
	return string(b)
}

// tsBlock is the body of an interface, one member per line.
func tsBlock(members []string) string {
	if len(members) == 0 {
		return "{}"
	}
	return "{\n  " + strings.Join(members, "\n  ") + "\n}"
}

// tsUnion is the right-hand side of a union type alias, one member per
// line, ending the statement: never when there are none.
func tsUnion(members []string) string {
	if len(members) == 0 {
		return " never;"
	}
	return "\n  | " + strings.Join(members, "\n  | ") + ";"
}
