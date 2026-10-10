// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/alonsovidales/otc/i18n/model"
)

func init() { register(goEmitter{}) }

// goEmitter writes what package i18n (the Go runtime of the device, the
// bridge and otc-sync) embeds and exposes:
//
//   - i18n/catalog/<code>/<prefix>.json: the keys of every prefix routed
//     to "go" or "otcsync". English has every key and declares the
//     arguments:
//     {"key": {"text": "..." or {"one": "...", "other": "..."}, "args": [["name", "type"], ...]}}.
//     Another language holds only its current translations: package i18n
//     falls back to English key by key, and must then use English plural
//     rules and number formats (an English text under French rules would
//     read "Syncing 0 file"), which a copy of the English in the French
//     file would hide. Every language still shows every key. One catalog
//     serves both consumers: otc-sync carries the device's text and the
//     device otc-sync's, which costs a little size and keeps one runtime.
//   - i18n/msg_<prefix>_gen.go: a typed constructor per key returning
//     i18n.Msg (i18n.Rich for a key with tags).
//   - i18n/languages_gen.go: the languages written, for Languages,
//     Normalize and Match.
type goEmitter struct{}

func (goEmitter) Name() string        { return "go" }
func (goEmitter) Consumers() []string { return []string{model.ConsumerGo, model.ConsumerOtcSync} }
func (goEmitter) Owns() []string {
	return []string{goCatalogDir + "/*/*.json", "i18n/msg_*_gen.go", goLanguagesFile}
}

const (
	goCatalogDir    = "i18n/catalog"
	goLanguagesFile = "i18n/languages_gen.go"
)

// goRuntimeNames are the exported names of package i18n's own code. A
// constructor can't take one of them (TestGoRuntimeNames keeps the list
// current). Constructor names join two or more segments of a key, so only
// a root named like a runtime word could ever collide.
var goRuntimeNames = []string{
	"ErrArgs", "ErrUnknownKey", "FormatBytes", "FormatInt", "FormatTime", "Language", "Languages",
	"Match", "Msg", "Normalize", "ParseStored", "Part", "RenderStored", "Rich", "T", "ValidCode",
}

// The shapes model checks, for an emitter handed a catalog Check never
// saw.
var (
	goArgNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	goCodePattern    = regexp.MustCompile(`^[a-z]{2,3}$`)
	goPrefixPattern  = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z0-9]+(_[a-z0-9]+)*)?$`)
)

// goPrefixes are the prefixes the Go runtime carries.
func goPrefixes(c *model.Catalog) []string {
	ps := append(c.PrefixesFor(model.ConsumerGo), c.PrefixesFor(model.ConsumerOtcSync)...)
	slices.Sort(ps)
	return slices.Compact(ps)
}

func (goEmitter) Emit(c *model.Catalog, opts Options) (map[string][]byte, error) {
	out := map[string][]byte{}
	prefixes := goPrefixes(c)
	names := map[string]string{} // constructor -> key
	for _, p := range prefixes {
		if !goPrefixPattern.MatchString(p) {
			return nil, fmt.Errorf("prefix %q is not a prefix", p)
		}
		for _, e := range c.EntriesIn(p) {
			if err := checkGoEntry(e); err != nil {
				return nil, err
			}
			name := model.GoName(e.Key)
			if other, dup := names[name]; dup {
				return nil, fmt.Errorf("%s and %s both become %s", other, e.Key, name)
			}
			names[name] = e.Key
		}
		file := "i18n/msg_" + model.PrefixFileName(p) + "_gen.go"
		if _, dup := out[file]; dup {
			return nil, fmt.Errorf("two prefixes become %s", file)
		}
		src, err := goConstructors(c, p, opts.Draft)
		if err != nil {
			return nil, err
		}
		out[file] = src
	}
	for _, l := range opts.Languages {
		if !goCodePattern.MatchString(l.Code) {
			return nil, fmt.Errorf("language code %q", l.Code)
		}
		for _, p := range prefixes {
			data, err := goCatalogFile(c, l, p, opts.Draft)
			if err != nil {
				return nil, err
			}
			out[path.Join(goCatalogDir, l.Code, p+".json")] = data
		}
	}
	src, err := goLanguages(opts.Languages, opts.Draft)
	if err != nil {
		return nil, err
	}
	out[goLanguagesFile] = src
	return out, nil
}

// checkGoEntry refuses what would make the generated code or catalog
// wrong. Check already refuses all of it; Emit can be handed a catalog
// Check never saw (the hostile-catalog tests), and never guesses.
func checkGoEntry(e *model.Entry) error {
	switch {
	case !model.KeyPattern.MatchString(e.Key) || len(e.Key) > model.MaxKeyLen:
		return fmt.Errorf("%q is not a key", e.Key)
	case slices.Contains(goRuntimeNames, model.GoName(e.Key)):
		return fmt.Errorf("%s: its constructor %s would clash with package i18n's own", e.Key, model.GoName(e.Key))
	}
	params := map[string]bool{}
	for _, a := range e.Args {
		switch {
		case !goArgNamePattern.MatchString(a.Name):
			return fmt.Errorf("%s: %q is not an argument name", e.Key, a.Name)
		case len(model.ReservedIn(a.Name, []string{model.ConsumerGo})) > 0:
			return fmt.Errorf("%s: argument %s is a Go keyword or predeclared name", e.Key, a.Name)
		case params[model.ArgName(a.Name)]:
			return fmt.Errorf("%s: argument %s is declared twice", e.Key, a.Name)
		case goParamType(a.Type) == "":
			return fmt.Errorf("%s: argument %s has unknown type %q", e.Key, a.Name, a.Type)
		}
		params[model.ArgName(a.Name)] = true
	}
	return checkGoForms(e, e.Text)
}

// checkGoForms tokenizes every form of a text in the entry's key: every
// placeholder declared, tags only in a rich key.
func checkGoForms(e *model.Entry, t model.Text) error {
	for _, f := range t.Forms() {
		toks, err := model.Tokenize(f.Text)
		if err != nil {
			return fmt.Errorf("%s: %v", e.Key, err)
		}
		for _, tok := range toks {
			switch {
			case tok.Kind == model.Placeholder && e.ArgIndex(tok.Value) < 0:
				return fmt.Errorf("%s: {%s} is not a declared argument", e.Key, tok.Value)
			case tok.Kind == model.OpenTag && !slices.Contains(e.Rich, tok.Value):
				return fmt.Errorf("%s: <%s> is not one of its tags", e.Key, tok.Value)
			}
		}
	}
	return nil
}

// goCatalogFile writes one language's prefix: a JSON object, one key per
// line, sorted (an empty object for a language with no translation there
// yet). Texts go through encoding/json: data, never code.
func goCatalogFile(c *model.Catalog, l model.Language, prefix string, draft bool) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("{")
	sep := "\n  "
	if draft {
		b.WriteString(sep + goJSONString(goDraftKey) + ": " + goJSONString(DraftMarker))
		sep = ",\n  "
	}
	for _, r := range c.Messages(l, prefix) {
		if !l.IsSource() && (r.Fallback() || r.State == model.StateSource) {
			continue // English, from the English file
		}
		if r.Text.Plural != r.Entry.Text.Plural {
			return nil, fmt.Errorf("%s: the %s text and the English differ in plural forms", r.Key, l.Code)
		}
		if err := checkGoForms(r.Entry, r.Text); err != nil {
			return nil, fmt.Errorf("%s (%s)", err, l.Code)
		}
		b.WriteString(sep + goJSONString(r.Key) + `: {"text": `)
		sep = ",\n  "
		if r.Text.Plural {
			b.WriteString(`{"one": ` + goJSONString(r.Text.One) + `, "other": ` + goJSONString(r.Text.Other) + `}`)
		} else {
			b.WriteString(goJSONString(r.Text.Other))
		}
		if l.IsSource() && len(r.Args) > 0 {
			b.WriteString(`, "args": [`)
			for i, a := range r.Args {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString("[" + goJSONString(a.Name) + ", " + goJSONString(string(a.Type)) + "]")
			}
			b.WriteString("]")
		}
		b.WriteString("}")
	}
	b.WriteString("\n}\n")
	return b.Bytes(), nil
}

// goDraftKey marks a draft catalog file (package i18n skips it).
const goDraftKey = "@draft"

// goJSONString encodes a string as JSON, leaving <, > and & readable (the
// catalog is read by encoding/json only).
func goJSONString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(s) // a string always encodes
	return strings.TrimSuffix(b.String(), "\n")
}

// goParamType is a constructor parameter's Go type for an argument type.
func goParamType(t model.ArgType) string {
	switch t {
	case model.ArgText, model.ArgUser:
		return "string"
	case model.ArgCount, model.ArgInt:
		return "int"
	case model.ArgBytes:
		return "int64"
	case model.ArgDatetime:
		return "time.Time"
	case model.ArgMsg:
		return "Msg"
	}
	return ""
}

// goArgValue is the value a constructor stores for a parameter: what
// msg_args keeps (a string or an int64).
func goArgValue(a model.Arg) string {
	p := model.ArgName(a.Name)
	switch a.Type {
	case model.ArgCount, model.ArgInt:
		return "int64(" + p + ")"
	case model.ArgDatetime:
		return p + ".Unix()"
	case model.ArgMsg:
		return p + ".Key"
	}
	return p
}

// goHeader starts every generated Go file.
func goHeader(b *bytes.Buffer, from string, draft bool) {
	b.WriteString("// SPDX-License-Identifier: AGPL-3.0-or-later\n\n")
	b.WriteString("// Code generated by i18ngen from " + from + "; DO NOT EDIT.\n")
	if draft {
		b.WriteString("// " + DraftMarker + ": make i18n DRAFT=1 output, never commit it.\n")
	}
	b.WriteString("\npackage i18n\n")
}

// goConstructors writes a prefix's constructors:
//
//	// DevPhotosDeleted is dev.photos_deleted: "{name} deleted {count} photos".
//	func DevPhotosDeleted(name string, count int) Msg {
//		return Msg{Key: "dev.photos_deleted", Args: map[string]any{"name": name, "count": int64(count)}}
//	}
//
// Text reaches the code only through strconv.Quote, inside a comment.
func goConstructors(c *model.Catalog, prefix string, draft bool) ([]byte, error) {
	var b bytes.Buffer
	goHeader(&b, model.StringsDir+"/"+model.SourceCode+"/"+prefix+".json", draft)
	entries := c.EntriesIn(prefix)
	for _, e := range entries {
		if slices.ContainsFunc(e.Args, func(a model.Arg) bool { return a.Type == model.ArgDatetime }) {
			b.WriteString("\nimport \"time\"\n")
			break
		}
	}
	for _, e := range entries {
		name, typ := model.GoName(e.Key), "Msg"
		if e.IsRich() {
			typ = "Rich"
		}
		var params, args []string
		for _, a := range e.Args {
			params = append(params, model.ArgName(a.Name)+" "+goParamType(a.Type))
			args = append(args, strconv.Quote(a.Name)+": "+goArgValue(a))
		}
		fields := "Key: " + strconv.Quote(e.Key)
		if len(args) > 0 {
			fields += ", Args: map[string]any{" + strings.Join(args, ", ") + "}"
		}
		fmt.Fprintf(&b, "\n// %s is %s: %s.\n", name, e.Key, goCommentText(e.Text))
		fmt.Fprintf(&b, "func %s(%s) %s {\n\treturn %s{%s}\n}\n", name, strings.Join(params, ", "), typ, typ, fields)
	}
	src, err := format.Source(b.Bytes())
	if err != nil {
		return nil, fmt.Errorf("%s: generated Go doesn't parse: %v", prefix, err)
	}
	return src, nil
}

// goCommentText is an English text for a doc comment: quoted (so a line
// break or anything else can't end the comment), the "other" form of a
// plural, shortened. gofmt turns two single quotes or two backquotes in a
// doc comment into a curly quote, so a doubled quote is written as its
// escape.
func goCommentText(t model.Text) string {
	s := t.Other
	if utf8.RuneCountInString(s) > 80 {
		s = string([]rune(s)[:80]) + "…"
	}
	return strings.NewReplacer("''", `'\x27`, "``", "`\\x60").Replace(strconv.Quote(s))
}

// goLanguages writes the languages the catalog has, English first.
func goLanguages(langs []model.Language, draft bool) ([]byte, error) {
	var b bytes.Buffer
	goHeader(&b, model.LanguagesFile, draft)
	b.WriteString("\n// languages are the languages this build carries, English first: the\n")
	b.WriteString("// shipping ones (make i18n DRAFT=1 adds the drafts and the pseudo-locale).\n")
	b.WriteString("var languages = []Language{\n")
	for _, l := range langs {
		fmt.Fprintf(&b, "\t{Code: %s, Tag: %s, Name: %s},\n", strconv.Quote(l.Code), strconv.Quote(l.Tag), strconv.Quote(l.Name))
	}
	b.WriteString("}\n")
	return format.Source(b.Bytes())
}
