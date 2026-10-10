// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/alonsovidales/otc/i18n/model"
)

// The Apple emitter writes, for the iPhone app and the Mac app, everything
// the apps' L10n runtime (app/<ios|macos>/OffTheCloud/OffTheCloud/i18n/
// L10n.swift, hand-written) reads:
//
//   - <Table>.xcstrings per prefix the app shows, Table being the prefix's
//     type name (Common, AppPhotos): a string catalog Xcode compiles into
//     <lang>.lproj/<Table>.strings and .stringsdict. Every entry is
//     "manual" and the file is written in Xcode's own layout, so that the
//     IDE's catalog sync (xcodebuild -exportLocalizations) leaves it
//     byte-identical. Never named Localizable: Xcode syncs the literals it
//     extracts from SwiftUI code into that table.
//   - S+<Table>.swift: one typed accessor per key, S.commonCancel or
//     S.appPhotosDeletedBy(name:count:), calling L(key, table:) - or
//     LRich for a key with tags, which returns an AttributedString.
//   - Languages.swift: the languages this build ships (L10nLanguage.all).
//   - InfoPlist.xcstrings, when the catalog has <app>.plist.<nskey> keys:
//     the permission texts (NSCameraUsageDescription...). They follow the
//     system language, as iOS and macOS show them themselves.
//
// The format strings follow docs/i18n.md: a text or user argument is
// %N$@, an int %N$lld, a literal % is %%, and a plural is the catalog's
// substitutions form: the value "%#@count@", whose substitution names the
// count's position (argNum), its type (lld) and the forms, each holding the
// whole text with the count written %arg. Positions are the declared
// order of the arguments in every language, so translations may reorder
// them freely.

func init() { register(appleEmitter{}) }

type appleEmitter struct{}

// appleApp is one app the emitter writes for.
type appleApp struct {
	consumer string
	dir      string // the i18n folder in the app's synchronized source group
	// plistRoot is the root whose "<root>.plist.<nskey>" keys hold the
	// app's Info.plist texts.
	plistRoot string
}

var appleApps = []appleApp{
	{model.ConsumerIOS, "app/ios/OffTheCloud/OffTheCloud/i18n", "ios"},
	{model.ConsumerMacOS, "app/macos/OffTheCloud/OffTheCloud/i18n", "macos"},
}

func (appleEmitter) Name() string { return "apple" }

func (appleEmitter) Consumers() []string { return []string{model.ConsumerIOS, model.ConsumerMacOS} }

// Owns covers every catalog and accessor file in the apps' i18n folders.
// The folder belongs to the generator, except L10n.swift.
func (appleEmitter) Owns() []string {
	var out []string
	for _, app := range appleApps {
		out = append(out, app.dir+"/*.xcstrings", app.dir+"/S+*.swift", app.dir+"/Languages.swift")
	}
	return out
}

// appleReservedTables are table names the apps can't use for a prefix:
// Xcode syncs extracted literals into Localizable, InfoPlist holds the
// permission texts.
var appleReservedTables = []string{"Localizable", "InfoPlist"}

// appleLprojPattern is what an .lproj name looks like ("en", "pt-PT",
// "zh-Hans", "en-XA").
var appleLprojPattern = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

func (appleEmitter) Emit(c *model.Catalog, opts Options) (map[string][]byte, error) {
	if len(opts.Languages) == 0 || !opts.Languages[0].IsSource() {
		return nil, fmt.Errorf("English must be the first language to write")
	}
	seen := map[string]string{}
	for _, l := range opts.Languages {
		if !appleLprojPattern.MatchString(l.Apple) {
			return nil, fmt.Errorf("%s: %q is not an Apple localization name (en, pt-PT)", l.Code, l.Apple)
		}
		if other, dup := seen[strings.ToLower(l.Apple)]; dup {
			return nil, fmt.Errorf("%s and %s both use the Apple localization %q", other, l.Code, l.Apple)
		}
		seen[strings.ToLower(l.Apple)] = l.Code
	}

	out := map[string][]byte{}
	for _, app := range appleApps {
		tables := map[string]string{} // lower-case table -> prefix: macOS's file system ignores case
		var plist []*model.Entry
		for _, prefix := range c.PrefixesFor(app.consumer) {
			var entries []*model.Entry
			for _, e := range c.EntriesIn(prefix) {
				if app.isPlistKey(c, e.Key) {
					plist = append(plist, e)
				} else {
					entries = append(entries, e)
				}
			}
			if len(entries) == 0 {
				continue // a prefix of Info.plist texts only
			}
			table := model.PrefixTypeName(prefix)
			for _, r := range appleReservedTables {
				if strings.EqualFold(table, r) {
					return nil, fmt.Errorf("prefix %s: its table would be named %s, which Xcode reserves", prefix, table)
				}
			}
			if other, dup := tables[strings.ToLower(table)]; dup {
				return nil, fmt.Errorf("prefixes %s and %s both become %s.xcstrings on a case-insensitive file system", other, prefix, table)
			}
			tables[strings.ToLower(table)] = prefix

			catalog, err := appleCatalog(c, entries, opts)
			if err != nil {
				return nil, err
			}
			out[path.Join(app.dir, table+".xcstrings")] = catalog
			accessors, err := appleAccessors(entries, prefix, table, app.consumer, opts.Draft)
			if err != nil {
				return nil, err
			}
			out[path.Join(app.dir, "S+"+table+".swift")] = accessors
		}
		if len(plist) > 0 {
			catalog, err := applePlistCatalog(c, app, plist, opts)
			if err != nil {
				return nil, err
			}
			out[path.Join(app.dir, "InfoPlist.xcstrings")] = catalog
		}
		out[path.Join(app.dir, "Languages.swift")] = appleLanguages(opts)
	}
	return out, nil
}

// isPlistKey reports whether a key is one of the app's Info.plist texts.
func (app appleApp) isPlistKey(c *model.Catalog, key string) bool {
	return c.Serves(app.consumer, app.plistRoot) && strings.HasPrefix(key, app.plistRoot+".plist.")
}

// appleState is the catalog state of a resolved text. Xcode compiles every
// state; the state only tells a reader of the catalog where the text
// stands.
func appleState(r model.Resolved) string {
	switch r.State {
	case model.StateMissing:
		return "new" // the English, until it is translated
	case model.StateStale, model.StateUnreviewed:
		return "needs_review"
	}
	return "translated"
}

// appleCatalog writes the string catalog of one prefix.
func appleCatalog(c *model.Catalog, entries []*model.Entry, opts Options) ([]byte, error) {
	strs := xcObject{}
	for _, e := range entries {
		locs := xcObject{}
		for _, l := range opts.Languages {
			r, ok := c.Resolve(l, e.Key)
			if !ok {
				return nil, fmt.Errorf("%s: no English entry", e.Key)
			}
			loc, err := appleLocalization(r)
			if err != nil {
				return nil, fmt.Errorf("%s (%s): %w", e.Key, l.Code, err)
			}
			locs[l.Apple] = loc
		}
		entry := xcObject{"extractionState": "manual", "localizations": locs}
		if opts.Draft {
			entry["comment"] = DraftMarker
		}
		strs[e.Key] = entry
	}
	return xcFile(strs), nil
}

// appleLocalization is one language's text of a key: a string unit, or for
// a plural the "%#@count@" unit and its substitution.
func appleLocalization(r model.Resolved) (xcObject, error) {
	state := appleState(r)
	unit := func(v string) xcObject { return xcObject{"stringUnit": xcObject{"state": state, "value": v}} }
	if r.Text.Plural != r.Entry.Text.Plural {
		return nil, fmt.Errorf("the text's plural shape differs from the English")
	}
	if !r.Text.Plural {
		v, err := appleFormat(r.Entry, r.Text.Other, false)
		if err != nil {
			return nil, err
		}
		return unit(v), nil
	}
	count, idx, ok := r.Entry.CountArg()
	if !ok {
		return nil, fmt.Errorf("a plural text needs a count argument")
	}
	forms := xcObject{}
	for _, f := range r.Text.Forms() {
		v, err := appleFormat(r.Entry, f.Text, true)
		if err != nil {
			return nil, err
		}
		forms[f.Name] = unit(v)
	}
	name := model.ArgName(count.Name)
	loc := unit("%#@" + name + "@")
	loc["substitutions"] = xcObject{name: xcObject{
		"argNum":          idx + 1,
		"formatSpecifier": "lld",
		"variations":      xcObject{"plural": forms},
	}}
	return loc, nil
}

// appleFormat turns one form into a format string for
// String(format:locale:arguments:). inPlural is set for the forms of a
// plural, where the count is the substitution's own %arg.
func appleFormat(e *model.Entry, form string, inPlural bool) (string, error) {
	toks, err := model.Tokenize(form)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, t := range toks {
		switch t.Kind {
		case model.Literal:
			b.WriteString(strings.ReplaceAll(t.Value, "%", "%%"))
		case model.Placeholder:
			i := e.ArgIndex(t.Value)
			if i < 0 {
				return "", fmt.Errorf("{%s} is not a declared argument", t.Value)
			}
			switch typ := e.Args[i].Type; typ {
			case model.ArgText, model.ArgUser:
				fmt.Fprintf(&b, "%%%d$@", i+1)
			case model.ArgInt:
				fmt.Fprintf(&b, "%%%d$lld", i+1)
			case model.ArgCount:
				if !inPlural {
					return "", fmt.Errorf("{%s} is a count in a text that isn't plural", t.Value)
				}
				b.WriteString("%arg")
			default:
				return "", fmt.Errorf("{%s}: the Apple apps can't show a %s argument", t.Value, typ)
			}
		case model.OpenTag, model.CloseTag:
			if !slices.Contains(e.Rich, t.Value) {
				return "", fmt.Errorf("<%s> is not one of the key's tags", t.Value)
			}
			if t.Kind == model.OpenTag {
				b.WriteString("<" + t.Value + ">")
			} else {
				b.WriteString("</" + t.Value + ">")
			}
		}
	}
	return b.String(), nil
}

// applePlistKeys are the Info.plist keys whose values may be localized,
// by their lower-case spelling: a key such as
// "ios.plist.nscamerausagedescription" names NSCameraUsageDescription.
var applePlistKeys = func() map[string]string {
	m := map[string]string{}
	for _, k := range strings.Fields(`CFBundleDisplayName CFBundleName CFBundleSpokenName NSHumanReadableCopyright
		NSAppleEventsUsageDescription NSAppleMusicUsageDescription NSBluetoothAlwaysUsageDescription
		NSBluetoothPeripheralUsageDescription NSCalendarsFullAccessUsageDescription NSCalendarsUsageDescription
		NSCalendarsWriteOnlyAccessUsageDescription NSCameraUsageDescription NSContactsUsageDescription
		NSDesktopFolderUsageDescription NSDocumentsFolderUsageDescription NSDownloadsFolderUsageDescription
		NSFaceIDUsageDescription NSFileProviderDomainUsageDescription NSFocusStatusUsageDescription
		NSHealthShareUsageDescription NSHealthUpdateUsageDescription NSHomeKitUsageDescription
		NSIdentityUsageDescription NSLocalNetworkUsageDescription NSLocationAlwaysAndWhenInUseUsageDescription
		NSLocationUsageDescription NSLocationWhenInUseUsageDescription NSMicrophoneUsageDescription
		NSMotionUsageDescription NSNearbyInteractionUsageDescription NSNetworkVolumesUsageDescription
		NSPhotoLibraryAddUsageDescription NSPhotoLibraryUsageDescription NSRemindersFullAccessUsageDescription
		NSRemindersUsageDescription NSRemovableVolumesUsageDescription NSSensorKitUsageDescription
		NSSiriUsageDescription NSSpeechRecognitionUsageDescription NSSystemAdministrationUsageDescription
		NSUserTrackingUsageDescription NSVideoSubscriberAccountUsageDescription`) {
		m[strings.ToLower(k)] = k
	}
	return m
}()

// applePlistCatalog writes InfoPlist.xcstrings. The system reads these
// values as they are (no format arguments), so a % stays a %.
//
// Xcode's catalog sync adds to this table every localizable key the target
// defines that the catalog lacks (CFBundleName, the INFOPLIST_KEY_* usage
// descriptions, NSHumanReadableCopyright) as "extracted_with_value"
// entries. Those entries are Xcode's: they are kept from the file as it
// stands, so that a file the IDE has synced is what make i18n writes, and
// only the "manual" entries are the catalog's.
func applePlistCatalog(c *model.Catalog, app appleApp, entries []*model.Entry, opts Options) ([]byte, error) {
	strs, err := appleXcodeEntries(opts.Root, path.Join(app.dir, "InfoPlist.xcstrings"))
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		rest := strings.TrimPrefix(e.Key, app.plistRoot+".plist.")
		nskey, ok := applePlistKeys[rest]
		switch {
		case !ok || strings.Contains(rest, "."):
			return nil, fmt.Errorf("%s: %q is not a localizable Info.plist key the apple emitter knows (add it to applePlistKeys)", e.Key, rest)
		case len(e.Args) > 0 || e.Text.Plural || e.IsRich():
			return nil, fmt.Errorf("%s: an Info.plist text can't have arguments, plural forms or tags", e.Key)
		}
		if prev, dup := strs[nskey].(xcObject); dup && prev["extractionState"] == "manual" {
			return nil, fmt.Errorf("%s: %s is defined twice", e.Key, nskey)
		}
		locs := xcObject{}
		for _, l := range opts.Languages {
			r, _ := c.Resolve(l, e.Key)
			toks, err := model.Tokenize(r.Text.Other)
			if err != nil {
				return nil, fmt.Errorf("%s (%s): %w", e.Key, l.Code, err)
			}
			var b strings.Builder
			for _, t := range toks {
				if t.Kind != model.Literal {
					return nil, fmt.Errorf("%s (%s): an Info.plist text can't have placeholders or tags", e.Key, l.Code)
				}
				b.WriteString(t.Value)
			}
			locs[l.Apple] = xcObject{"stringUnit": xcObject{"state": appleState(r), "value": b.String()}}
		}
		entry := xcObject{"extractionState": "manual", "localizations": locs}
		if opts.Draft {
			entry["comment"] = DraftMarker
		}
		strs[nskey] = entry
	}
	return xcFile(strs), nil
}

// appleXcodeEntries returns the entries Xcode added to an existing
// catalog: those whose extractionState isn't "manual". No file, no
// entries.
func appleXcodeEntries(root, rel string) (xcObject, error) {
	out := xcObject{}
	if root == "" {
		return out, nil
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	} else if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc struct {
		Strings map[string]any `json:"strings"`
	}
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%s: %v (fix it, or delete it and run make i18n)", rel, err)
	}
	for k, v := range doc.Strings {
		o, ok := v.(map[string]any)
		if !ok || o["extractionState"] == "manual" {
			continue
		}
		x, err := xcFromJSON(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %v", rel, k, err)
		}
		out[k] = x
	}
	return out, nil
}

// xcFromJSON converts a decoded catalog value to the writer's types.
func xcFromJSON(v any) (any, error) {
	switch v := v.(type) {
	case string, bool:
		return v, nil
	case json.Number:
		i, err := strconv.Atoi(string(v))
		if err != nil {
			return nil, fmt.Errorf("unexpected number %s", v)
		}
		return i, nil
	case map[string]any:
		o := xcObject{}
		for k, x := range v {
			c, err := xcFromJSON(x)
			if err != nil {
				return nil, err
			}
			o[k] = c
		}
		return o, nil
	}
	return nil, fmt.Errorf("unexpected JSON value %T", v)
}
