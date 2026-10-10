// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/alonsovidales/otc/i18n/model"
)

func init() { register(androidEmitter{}) }

// androidEmitter writes the Android app's files (docs/i18n.md, "Generated
// files"), for every prefix routed to android (common, app.*, android.*):
//
//   - res/values[-<qualifier>]/strings_<prefix>.xml per language: a
//     <string> or <plurals> per key, named model.AndroidName(key). English
//     is the default resources (values/); the hand-written values/strings.xml
//     is never touched, and a key whose name it already uses is an error.
//   - res/xml/locales_config.xml: the languages, for android:localeConfig.
//   - java/cloud/offthe/otc/i18n/S_<Prefix>.kt: one accessor per key, an
//     extension of object S (UiText.kt) returning a UiText, or a RichText
//     for a key with tags: S.appPhotosDeletedBy(name, count).
//   - java/cloud/offthe/otc/i18n/Languages.kt: the language table.
//
// Every argument becomes a positional %N$s (N = its declared position): the
// runtime hands Resources strings only, numbers already formatted by ICU
// for the configuration's locale, and the count also as the quantity. A
// user argument's %N$s sits between U+2068 and U+2069 (FSI ... PDI) in the
// resource itself, so the accessors pass the raw value. Invisible
// formatting characters, those marks included, are written as \uXXXX.
//
// Texts are data. Each form is rebuilt from model.Tokenize's tokens and
// escaped for aapt2, which would otherwise reinterpret it: \ ' " as \\ \'
// \", a line break and a tab as \n and \t, & < > as entities, a leading @
// or ? as \@ \? (a resource reference, an attribute), and a space aapt2
// would trim or collapse as \u0020. A literal % is %% in a key with
// arguments (they go through String.format); a key without arguments is
// never formatted and gets formatted="false" when it has a %. Tags stay
// literal text, &lt;b&gt;: aapt2 would turn real markup into spans that
// getString drops, and the runtime parses them itself (RichText). The
// English texts only reach Kotlin as comments, sanitized.
type androidEmitter struct{}

const (
	androidMain       = "app/android/app/src/main"
	androidRes        = androidMain + "/res"
	androidKotlinDir  = androidMain + "/java/cloud/offthe/otc/i18n"
	androidPackage    = "cloud.offthe.otc.i18n"
	androidRClass     = "cloud.offthe.otc.R"
	androidLocales    = androidRes + "/xml/locales_config.xml"
	androidLanguages  = androidKotlinDir + "/Languages.kt"
	androidStringsPre = "strings_"
	androidKotlinPre  = "S_"
)

// The pseudo-locale's resources on Android. model.PseudoLanguage says
// en-rXA, the qualifier Android's own pseudo-locale uses, but AGP's
// pseudoLocalesEnabled generates values-en-rXA from the default resources
// and aapt2 then refuses the build ("conflicting value for configuration
// (en-rXA)"). English with the private-use script Qaaa clashes with
// nothing, keeps English plural rules and number formats, and - because
// its script is explicit - matches only a configuration that asks for it:
// Android's resource matching (ResTable_config::match, since API 24)
// rejects a resource whose script differs from the request's, so an en-GB
// phone can't fall into it the way it would into an ordinary en-rXX folder.
const (
	androidPseudoQualifier = "b+en+Qaaa"
	androidPseudoTag       = "en-Qaaa"
)

// FSI and PDI, around every user argument, as aapt2 escapes (like every
// invisible formatting character, so that a diff shows them).
const (
	androidFSI = `\u2068`
	androidPDI = `\u2069`
)

var (
	androidArgNamePattern   = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	androidPrefixPattern    = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z0-9]+(_[a-z0-9]+)*)?$`)
	androidQualifierPattern = regexp.MustCompile(`^([a-z]{2,3}(-r[A-Z]{2})?|b\+[a-z]{2,3}(\+[A-Za-z0-9]{2,8})*)$`)
	androidTagPattern       = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)
	androidCodePattern      = regexp.MustCompile(`^[a-z]{2,3}$`)
	// Resource names in the hand-written values files.
	androidResNamePattern = regexp.MustCompile(`<(?:string|plurals|string-array)\b[^>]*\bname\s*=\s*"([^"]+)"`)
)

// androidManyLanguages are the languages whose CLDR 48 rules (Android's
// ICU from API 33, and lint for targetSdk 36) have a "many" category for
// integers - exact millions - while the catalog has one and other only.
// Their <plurals> repeat other as many: Resources would fall back to other
// for a million anyway, but lint warns (MissingQuantity) about every one.
var androidManyLanguages = map[string]bool{"es": true, "fr": true, "it": true, "pt": true}

func (androidEmitter) Name() string        { return "android" }
func (androidEmitter) Consumers() []string { return []string{model.ConsumerAndroid} }

func (androidEmitter) Owns() []string {
	return []string{
		androidRes + "/values/" + androidStringsPre + "*.xml",
		androidRes + "/values-*/" + androidStringsPre + "*.xml",
		androidLocales,
		androidKotlinDir + "/" + androidKotlinPre + "*.kt",
		androidLanguages,
	}
}

func (androidEmitter) Emit(c *model.Catalog, opts Options) (map[string][]byte, error) {
	if len(opts.Languages) == 0 || !opts.Languages[0].IsSource() {
		return nil, fmt.Errorf("the languages to write must start with English")
	}
	taken, err := androidHandWrittenNames(opts.Root)
	if err != nil {
		return nil, err
	}
	for _, e := range c.EntriesFor(model.ConsumerAndroid) {
		if err := androidCheckEntry(e); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Key, err)
		}
		if f, ok := taken[model.AndroidName(e.Key)]; ok {
			return nil, fmt.Errorf("%s: the resource name %s is already used by the hand-written %s", e.Key, model.AndroidName(e.Key), f)
		}
	}

	out := map[string][]byte{}
	prefixes := c.PrefixesFor(model.ConsumerAndroid)
	seenDirs := map[string]string{}
	for _, l := range opts.Languages {
		dir, err := androidValuesDir(l)
		if err != nil {
			return nil, err
		}
		if other, dup := seenDirs[dir]; dup {
			return nil, fmt.Errorf("languages %s and %s both write %s", other, l.Code, dir)
		}
		seenDirs[dir] = l.Code
		for _, p := range prefixes {
			data, err := androidStringsFile(c, l, p, opts.Draft)
			if err != nil {
				return nil, err
			}
			out[path.Join(androidRes, dir, androidStringsPre+model.PrefixFileName(p)+".xml")] = data
		}
	}
	for _, p := range prefixes {
		data, err := androidAccessorFile(c, p, opts.Draft)
		if err != nil {
			return nil, err
		}
		out[path.Join(androidKotlinDir, androidKotlinPre+model.PrefixTypeName(p)+".kt")] = data
	}
	if out[androidLanguages], err = androidLanguagesFile(opts.Languages, opts.Draft); err != nil {
		return nil, err
	}
	if out[androidLocales], err = androidLocalesFile(opts.Languages, opts.Draft); err != nil {
		return nil, err
	}
	return out, nil
}

// androidHandWrittenNames returns the string resource names the app's own
// values/*.xml files define (strings.xml's app_name), by name -> file. A
// generated name equal to one would fail the build with a duplicate
// resource, so Emit refuses it with the key's name instead.
func androidHandWrittenNames(root string) (map[string]string, error) {
	names := map[string]string{}
	if root == "" {
		return names, nil
	}
	dir := filepath.Join(root, filepath.FromSlash(androidRes), "values")
	des, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return names, nil
	} else if err != nil {
		return nil, err
	}
	for _, de := range des {
		n := de.Name()
		if de.IsDir() || !strings.HasSuffix(n, ".xml") || strings.HasPrefix(n, androidStringsPre) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return nil, err
		}
		for _, m := range androidResNamePattern.FindAllStringSubmatch(string(data), -1) {
			names[m[1]] = androidRes + "/values/" + n
		}
	}
	return names, nil
}

// androidCheckEntry refuses what the files below can't express. Check
// already refuses all of it; this keeps a catalog that skipped Check (the
// hostile-catalog tests) from writing broken Kotlin.
func androidCheckEntry(e *model.Entry) error {
	if !model.KeyPattern.MatchString(e.Key) {
		return fmt.Errorf("not a valid key")
	}
	if !androidPrefixPattern.MatchString(e.Prefix) {
		return fmt.Errorf("prefix %q is not valid", e.Prefix)
	}
	for _, a := range e.Args {
		if !androidArgNamePattern.MatchString(a.Name) {
			return fmt.Errorf("argument name %q is not valid", a.Name)
		}
		if rs := model.ReservedIn(a.Name, []string{model.ConsumerAndroid}); len(rs) > 0 {
			return fmt.Errorf("argument name %q is a Kotlin keyword", a.Name)
		}
		if _, err := androidKotlinType(a.Type); err != nil {
			return err
		}
	}
	_, _, hasCount := e.CountArg()
	switch {
	case e.Text.Plural && !hasCount:
		return fmt.Errorf("a plural text needs a count argument")
	case !e.Text.Plural && hasCount:
		return fmt.Errorf("a count argument needs a plural text")
	}
	return nil
}

// androidKotlinType is an argument's parameter type in the accessors.
func androidKotlinType(t model.ArgType) (string, error) {
	switch t {
	case model.ArgText, model.ArgUser:
		return "String", nil
	case model.ArgCount, model.ArgInt:
		return "Int", nil
	}
	return "", fmt.Errorf("argument type %q can't be shown by the Android app", t)
}

// androidQualifier is a language's resource qualifier ("" for English).
func androidQualifier(l model.Language) string {
	if l.Pseudo {
		return androidPseudoQualifier
	}
	return l.Android
}

// androidTag is the locale the app hands Android for a language: the
// language's tag, whose resources values-<qualifier> holds.
func androidTag(l model.Language) string {
	if l.Pseudo {
		return androidPseudoTag
	}
	return l.Tag
}

// androidValuesDir is a language's resource directory.
func androidValuesDir(l model.Language) (string, error) {
	q := androidQualifier(l)
	switch {
	case l.IsSource() && q == "":
		return "values", nil
	case l.IsSource() || q == "":
		return "", fmt.Errorf("%s: English alone has the default resources (an empty android qualifier)", l.Code)
	case !androidQualifierPattern.MatchString(q):
		return "", fmt.Errorf("%s: %q is not a language qualifier for values-<qualifier>", l.Code, q)
	}
	return "values-" + q, nil
}

// androidBase is the language part of a tag or qualifier ("pt" for "pt-PT",
// "b+en+Qaaa" or "pt-rBR").
func androidBase(s string) string {
	s = strings.TrimPrefix(s, "b+")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	return s
}

// androidStringsFile writes one language's values file of a prefix.
func androidStringsFile(c *model.Catalog, l model.Language, prefix string, draft bool) ([]byte, error) {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"utf-8\"?>\n")
	src := model.StringsDir + "/" + model.SourceCode + "/" + prefix + ".json"
	if l.IsSource() {
		fmt.Fprintf(&b, "<!-- Generated by i18ngen from %s: do not edit, run make i18n (i18n/README.md). -->\n", src)
	} else if l.Pseudo {
		fmt.Fprintf(&b, "<!-- Generated by i18ngen: the pseudo-locale of %s. Do not edit, run make i18n (i18n/README.md). -->\n", src)
	} else {
		fmt.Fprintf(&b, "<!-- Generated by i18ngen from %s/%s/%s.json, English where it is missing or stale:\n     do not edit, run make i18n (i18n/README.md). -->\n", model.StringsDir, l.Code, prefix)
	}
	if draft {
		b.WriteString("<!-- " + DraftMarker + " -->\n")
	}

	var body strings.Builder
	tools := false
	for _, r := range c.Messages(l, prefix) {
		if !c.Serves(model.ConsumerAndroid, r.Root) {
			continue
		}
		if !r.Translate && !l.IsSource() {
			// Only the default resources hold it, marked translatable="false":
			// lint's ExtraTranslation is fatal for a copy in values-<q>.
			continue
		}
		name := model.AndroidName(r.Key)
		attrs := ""
		if !r.Translate {
			attrs += ` translatable="false"`
		}
		if !r.Text.Plural {
			v, err := androidValue(r.Entry, r.Text.Other)
			if err != nil {
				return nil, fmt.Errorf("%s (%s): %w", r.Key, l.Code, err)
			}
			if len(r.Args) == 0 && strings.Contains(r.Text.Other, "%") {
				attrs += ` formatted="false"`
			}
			fmt.Fprintf(&body, "    <string name=\"%s\"%s>%s</string>\n", name, attrs, v)
			continue
		}
		one, ignoreImplied := androidOneForm(l, r)
		if ignoreImplied {
			attrs += ` tools:ignore="ImpliedQuantity"`
			tools = true
		}
		oneV, err := androidValue(r.Entry, one)
		if err != nil {
			return nil, fmt.Errorf("%s (%s, one): %w", r.Key, l.Code, err)
		}
		otherV, err := androidValue(r.Entry, r.Text.Other)
		if err != nil {
			return nil, fmt.Errorf("%s (%s, other): %w", r.Key, l.Code, err)
		}
		fmt.Fprintf(&body, "    <plurals name=\"%s\"%s>\n", name, attrs)
		fmt.Fprintf(&body, "        <item quantity=\"one\">%s</item>\n", oneV)
		if androidManyLanguages[androidBase(l.Tag)] {
			fmt.Fprintf(&body, "        <item quantity=\"many\">%s</item>\n", otherV)
		}
		fmt.Fprintf(&body, "        <item quantity=\"other\">%s</item>\n", otherV)
		body.WriteString("    </plurals>\n")
	}
	if tools {
		b.WriteString("<resources xmlns:tools=\"http://schemas.android.com/tools\">\n")
	} else {
		b.WriteString("<resources>\n")
	}
	b.WriteString(body.String())
	b.WriteString("</resources>\n")
	return []byte(b.String()), nil
}

// androidOneForm returns the text for quantity "one", and whether lint must
// be told that it is right.
//
// The catalog lets "one" leave out the count where one means exactly 1 for
// the language's tag (Language.OneMeansOne). Android picks the quantity
// with the configuration locale's rules, so:
//   - In a language where "one" covers more than 1 (French: 0 too), a "one"
//     without the count can only be the English filling in for a missing
//     translation (Check requires the count in a translation); "other" is
//     written instead, so 0 never shows as "a photo".
//   - pt-PT's "one" is exactly 1, but values-pt is also what lint (and a
//     pt-BR configuration) reads with Brazilian rules, where 0 is "one":
//     lint's ImpliedQuantity is silenced, and the runtime hands Android the
//     pt-PT locale (Languages.kt) so that the text is chosen as written.
func androidOneForm(l model.Language, r model.Resolved) (string, bool) {
	cnt, _, _ := r.CountArg()
	if slices.Contains(model.Placeholders(r.Text.One), cnt.Name) {
		return r.Text.One, false
	}
	if !l.OneMeansOne() {
		return r.Text.Other, false
	}
	folder := model.Language{Tag: androidBase(androidQualifier(l))}
	if l.IsSource() {
		folder.Tag = model.SourceCode
	}
	return r.Text.One, !folder.OneMeansOne()
}

// androidPiece is one piece of a resource value: escaped text, or a space
// whose spelling depends on its neighbours.
type androidPiece struct {
	s     string
	space bool // a U+0020
	white bool // a line break or tab, written as \n or \t
}

// androidValue escapes one form for a <string> or <item> element.
func androidValue(e *model.Entry, form string) (string, error) {
	if !utf8.ValidString(form) {
		return "", fmt.Errorf("invalid UTF-8")
	}
	toks, err := model.Tokenize(form)
	if err != nil {
		return "", err
	}
	hasArgs := len(e.Args) > 0
	var ps []androidPiece
	for _, t := range toks {
		switch t.Kind {
		case model.Literal:
			for _, r := range t.Value {
				p, err := androidRune(r, len(ps) == 0, hasArgs, e.IsRich())
				if err != nil {
					return "", err
				}
				ps = append(ps, p)
			}
		case model.Placeholder:
			i := e.ArgIndex(t.Value)
			if i < 0 {
				return "", fmt.Errorf("undeclared placeholder {%s}", t.Value)
			}
			spec := fmt.Sprintf("%%%d$s", i+1)
			if e.Args[i].Type == model.ArgUser {
				spec = androidFSI + spec + androidPDI
			}
			ps = append(ps, androidPiece{s: spec})
		case model.OpenTag:
			ps = append(ps, androidPiece{s: "&lt;" + t.Value + "&gt;"})
		case model.CloseTag:
			ps = append(ps, androidPiece{s: "&lt;/" + t.Value + "&gt;"})
		}
	}
	var b strings.Builder
	for i, p := range ps {
		if !p.space {
			b.WriteString(p.s)
			continue
		}
		// aapt2 trims leading and trailing whitespace and collapses runs;
		// \u0020 is a space it leaves alone.
		calm := func(q androidPiece) bool { return !q.space && !q.white }
		if i > 0 && i < len(ps)-1 && calm(ps[i-1]) && calm(ps[i+1]) {
			b.WriteByte(' ')
		} else {
			b.WriteString(`\u0020`)
		}
	}
	return b.String(), nil
}

// androidRune escapes one literal character.
func androidRune(r rune, first, hasArgs, rich bool) (androidPiece, error) {
	switch {
	case r == ' ':
		return androidPiece{space: true}, nil
	case r == '\n':
		return androidPiece{s: `\n`, white: true}, nil
	case r == '\t':
		return androidPiece{s: `\t`, white: true}, nil
	case r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == 0xfffe || r == 0xffff || (r >= 0xd800 && r <= 0xdfff):
		return androidPiece{}, fmt.Errorf("character U+%04X can't be written in an Android resource", r)
	case rich && r >= 0xe000 && r <= 0xf8ff:
		return androidPiece{}, fmt.Errorf("private-use character U+%04X: RichText uses them as argument sentinels", r)
	case r < 0x10000 && unicode.Is(unicode.Cf, r):
		// Invisible (a joiner, a direction mark): spelled out for review.
		return androidPiece{s: fmt.Sprintf(`\u%04X`, r)}, nil
	}
	switch r {
	case '\\':
		return androidPiece{s: `\\`}, nil
	case '\'':
		return androidPiece{s: `\'`}, nil
	case '"':
		return androidPiece{s: `\"`}, nil
	case '&':
		return androidPiece{s: "&amp;"}, nil
	case '<':
		return androidPiece{s: "&lt;"}, nil
	case '>':
		return androidPiece{s: "&gt;"}, nil
	case '%':
		if hasArgs {
			return androidPiece{s: "%%"}, nil
		}
	case '@', '?':
		if first {
			return androidPiece{s: `\` + string(r)}, nil
		}
	}
	return androidPiece{s: string(r)}, nil
}
