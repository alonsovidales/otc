// SPDX-License-Identifier: AGPL-3.0-or-later

package scan

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// The iOS, macOS and Android scanners. Each string literal is judged by
// where it stands:
//
//   - sink: the first argument of a view or helper that shows text
//     (Text("…"), Button("…"), .navigationTitle("…"), showToast("…"),
//     Toast.makeText(ctx, "…")), any word counts;
//   - label: an argument labelled as text (title:, message:,
//     contentDescription = …), any word counts;
//   - assign: a value given to a text-ish property (alertMessage = "…",
//     _error.value = "…", let deleteTitle = "…"), any word counts;
//   - prose: anywhere else, a literal that reads like a sentence.
//
// Literals for logs, keys, comparisons, file names and symbols are never
// counted (print, Log.d, hasPrefix, forKey:, systemImage:, case "…",
// dictionary keys, Text(verbatim:)).

type appRules struct {
	sinks        map[string]int // callee -> argument index that holds the text
	labels       map[string]bool
	denyCalls    map[string]bool
	denyLabels   map[string]bool
	denyRecv     map[string]bool // receivers whose calls are logs or storage
	keywordCalls map[string]bool // identifiers before ( that are not calls
}

// textProp matches the property and variable names that hold text.
var textProp = regexp.MustCompile(`(?i)(message|msg|error|err|status|title|text|txt|label|toast|caption|subtitle|hint|prompt|placeholder|note|detail|details|summary|description|banner|info|explain|explanation|tooltip|tip|body|heading|header|footer|warning|notice|reason|question|answer|result|confirm|line|name)$`)

// notTextProp are names textProp would take that hold keys or values.
var notTextProp = regexp.MustCompile(`(?i)^(tag|key|id|identifier|mime|type|kind|scheme|host|path|url|file|filename|filepath|basename|ext|extension|domain|endpoint|address|token|secret|password|hash|code|errorcode|state|source|query|format|pattern|regex|separator|delimiter|prefix|suffix|keyname|channelname|channelid|hostname|devicename|username|subsystem|category|queue|queuename|notificationname|classname|typename|symbolname|imagename|iconname|systemname|fontname|reuseidentifier|sfsymbol|bundleid|filetype|contenttype|accept|method|action|tagname|elementname|name)$`)

var swiftRules = appRules{
	sinks: sinkSet(0, "Text", "Button", "Label", "Toggle", "TextField", "SecureField", "TextEditor", "Picker", "Section",
		"Menu", "Link", "NavigationLink", "ProgressView", "ContentUnavailableView", "LabeledContent", "Stepper",
		"DatePicker", "GroupBox", "DisclosureGroup", "ShareLink", "Gauge", "Tab", "ControlGroup", "Alert", "ActionSheet",
		"navigationTitle", "navigationSubtitle", "alert", "confirmationDialog", "help", "accessibilityLabel",
		"accessibilityHint", "accessibilityValue", "badge", "NSMenuItem", "addItem",
		// the apps' own helpers that show their first argument
		"showToast", "toast", "say", "polite", "assertive", "announce", "row", "item", "barItem", "chip", "limitation",
		"legalLink", "failed", "mergeAnswered", "caption", "Caption", "RowButton", "InfoRow", "DetailRow", "ToggleRow",
		"BarItem", "SettingsRow", "Hint", "Footer", "Header", "EmptyState", "errorRow", "banner", "notice", "warn",
		"show", "flash", "setStatus", "setError", "setNote", "fail", "problem"),
	labels: boolSet("title", "message", "subtitle", "caption", "prompt", "placeholder", "text", "label", "hint",
		"detail", "details", "footer", "header", "body", "note", "info", "explanation", "help", "format", "withTitle",
		"informativeText", "messageText", "confirm", "confirmTitle", "cancelTitle", "actionTitle", "buttonTitle",
		"emptyText", "summary", "tooltip", "question", "reason", "description", "accessibilityLabel", "heading",
		"busyText", "doneText", "error", "status", "titleKey"),
	denyCalls: boolSet("print", "debugPrint", "dump", "NSLog", "os_log", "fatalError", "precondition",
		"preconditionFailure", "assert", "assertionFailure", "hasPrefix", "hasSuffix", "contains", "starts", "range",
		"replacingOccurrences", "components", "split", "trimmingCharacters", "firstRange", "firstIndex", "lastIndex",
		"appendingPathComponent", "appendingPathExtension", "deletingPathExtension", "URL", "URLComponents",
		"URLQueryItem", "Image", "NSImage", "UIImage", "Color", "UIColor", "NSColor", "Font", "custom", "Name",
		"Selector", "AppStorage", "SceneStorage", "Logger", "accessibilityIdentifier", "tag", "id", "scrollTo",
		"matchedGeometryEffect", "setValue", "object", "string", "data", "bool", "integer", "double",
		"removeObject", "set", "ask", "Notification", "NSNotification", "post", "addObserver", "UTType",
		"NSPredicate", "DispatchQueue", "OSLog", "Bundle", "forResource", "path", "url", "fileURLWithPath",
		"NSRegularExpression", "Regex", "compile", "matches", "hasDirectoryPath", "localizedStandardContains",
		"caseInsensitiveCompare", "compare", "sorted", "filter", "replacing", "dropFirst",
		"removing", "addingPercentEncoding", "setTitleWithMnemonic", "NSUserInterfaceItemIdentifier", "UIFont",
		"NSFont", "systemImage", "Keychain", "keychain", "SecItem", "evaluateJavaScript", "callAsyncJavaScript",
		"WKUserScript", "userContentController", "removeScriptMessageHandler", "Patience", "Data", "error", "info", "debug", "notice", "warning", "fault", "trace", "critical", "log"),
	denyLabels: boolSet("forKey", "systemImage", "systemName", "named", "id", "key", "subsystem", "category",
		"forHTTPHeaderField", "withExtension", "ofType", "identifier", "scheme", "verbatim", "imageName", "icon",
		"symbol", "mime", "mimeType", "extension", "path", "url", "string", "of", "with", "separator", "by",
		"forResource", "keyPath", "name", "domain", "code", "bundle", "tableName", "comment", "suiteName", "queue",
		"attributes", "endpoint", "host", "kind", "contentType", "accept", "method", "privacy"),
	denyRecv: boolSet("log", "logger", "Log", "Self.log", "os_log", "UserDefaults", "defaults", "standard",
		"NotificationCenter", "Keychain", "keychain"),
	keywordCalls: boolSet("if", "while", "switch", "return", "guard", "for", "in", "case", "where", "catch", "throw",
		"else", "repeat", "await", "try", "is", "as", "some", "any", "let", "var"),
}

var kotlinRules = appRules{
	sinks: mergeSinks(sinkSet(0, "Text", "Caption", "Section", "RowButton", "InfoRow", "DetailRow", "ToggleRow",
		"BarItem", "Item", "toast", "showToast", "say", "showSnackbar", "announceForAccessibility", "SectionHeader",
		"SettingsRow", "Hint", "Footer", "Header", "EmptyState", "ErrorRow", "Banner", "Notice", "TextButton",
		"setContentTitle", "setContentText", "setTicker", "setTitle", "setMessage", "setPositiveButton",
		"setNegativeButton", "setNeutralButton", "fail", "LabeledSwitch", "SwitchRow", "PasswordField",
		"OptionRow", "RadioRow", "ActionRow", "MenuItem", "DropdownMenuItem", "Chip", "chip", "Tab", "problem",
		"semantics", "row", "item", "section", "caption", "limitation", "legalLink"),
		sinkSet(1, "makeText")),
	labels: boolSet("title", "message", "subtitle", "caption", "prompt", "placeholder", "text", "label", "hint",
		"detail", "details", "footer", "header", "body", "note", "info", "explanation", "contentDescription",
		"onClickLabel", "onLongClickLabel", "stateDescription", "confirmText", "dismissText", "confirmLabel",
		"dismissLabel", "cancelLabel", "actionLabel", "buttonText", "description", "summary", "tooltip", "question",
		"reason", "emptyText", "busyText", "doneText", "error", "status", "heading", "supportingText", "removeLabel",
		"deleteConfirmText", "warning", "toast", "alert", "publishStatus", "loadError", "confirmError"),
	denyCalls: boolSet("println", "print", "error", "require", "check", "requireNotNull", "checkNotNull", "TODO",
		"startsWith", "endsWith", "contains", "split", "replace", "removePrefix", "removeSuffix", "substringBefore",
		"substringAfter", "substringAfterLast", "substringBeforeLast", "indexOf", "lastIndexOf", "equals", "matches",
		"Regex", "toRegex", "parse", "File", "getString", "putString", "getBoolean", "putBoolean", "getInt", "putInt",
		"getLong", "putLong", "optString", "optInt", "optBoolean", "optLong", "optJSONObject", "optJSONArray",
		"getJSONObject", "getJSONArray", "has", "remove", "put", "putExtra", "getStringExtra", "getBooleanExtra",
		"getSharedPreferences", "SuppressLint", "Suppress", "JvmName", "outlineIcon", "setHeader", "addHeader",
		"header", "url", "Intent", "ComponentName", "forName", "toMediaType", "toMediaTypeOrNull", "MediaType",
		"addJavascriptInterface", "evaluateJavascript", "loadUrl", "getColumnIndex", "getColumnIndexOrThrow",
		"query", "Uri", "fromParts", "withAppendedPath", "createNotificationChannel", "NotificationChannel",
		"getSystemService", "isEmpty", "startsWithAny", "ask", "Patience", "keepAsking", "fromString", "record",
		"putStringSet", "getStringSet", "edit", "appendQueryParameter", "appendPath", "scheme", "authority",
		"createTempFile", "resolve", "trimEnd", "trimStart", "padStart", "format", "getIdentifier", "Pattern",
		"compile", "MasterKey", "EncryptedSharedPreferences", "KeyGenParameterSpec", "getInstance", "Cipher",
		"KeyStore", "MessageDigest", "Mac", "SecretKeySpec", "getOrDefault", "mutableStateOf", "remember",
		"rememberSaveable", "stringPreferencesKey", "booleanPreferencesKey", "addTag", "testTag", "tag",
		"d", "i", "w", "e", "v", "wtf"),
	denyLabels: boolSet("key", "tag", "id", "name", "mimeType", "type", "scheme", "path", "url", "action",
		"channelId", "route", "testTag", "authority", "uri", "fileName", "ext", "extension", "kind", "separator",
		"prefix", "postfix", "contentType", "accept", "method", "host", "domain", "code", "endpoint", "token",
		"password", "query", "selection", "sortOrder", "pattern", "format"),
	denyRecv: boolSet("Log", "log", "logger", "Timber", "prefs", "editor", "sp", "json", "obj", "JSONObject",
		"Settings", "Secure", "intent", "bundle", "args", "savedState", "savedStateHandle", "headers"),
	keywordCalls: boolSet("if", "while", "when", "return", "for", "in", "catch", "throw", "else", "is", "as",
		"try", "val", "var", "fun", "object", "do", "super", "this"),
}

func sinkSet(arg int, names ...string) map[string]int {
	m := map[string]int{}
	for _, n := range names {
		m[n] = arg
	}
	return m
}

func mergeSinks(ms ...map[string]int) map[string]int {
	out := map[string]int{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func boolSet(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

// frame is an open bracket around a literal.
type frame struct {
	open     string // "(", "[", "{"
	callee   string // the identifier before "(", if a call
	receiver string // the identifier before ".callee", if any
	arg      int    // which argument
	label    string // the current argument's label
	atStart  bool   // the next token starts an argument
}

// scanApp judges every literal of one Swift or Kotlin file. A file with a
// literal or comment left open, or brackets that don't balance, is in the
// middle of an edit: it is an error, never a lower count that -update would
// lock in.
func (s *scanner) scanApp(rel string, src []byte, d cDialect, rules *appRules) error {
	toks, bad := lexCChecked(string(src), d)
	if bad != "" {
		return fmt.Errorf("%s doesn't parse: %s", rel, bad)
	}
	var stack []*frame
	unbalanced := 0
	top := func() *frame {
		if len(stack) == 0 {
			return nil
		}
		return stack[len(stack)-1]
	}
	tok := func(i int) ctok {
		if i < 0 || i >= len(toks) {
			return ctok{kind: tPunct}
		}
		return toks[i]
	}
	for i, t := range toks {
		f := top()
		switch t.kind {
		case tPunct:
			switch t.text {
			case "(", "[":
				nf := &frame{open: t.text, atStart: true}
				if p := tok(i - 1); t.text == "(" && p.kind == tIdent && !rules.keywordCalls[p.text] {
					nf.callee = p.text
					if a := tok(i - 2); a.kind == tPunct && a.text == "@" {
						nf.callee = "@" // an annotation: @Deprecated("…"), @available(…, message: "…")
					}
					if q := tok(i - 2); q.kind == tPunct && (q.text == "." || q.text == "?.") {
						nf.receiver = tok(i - 3).text
						if r := tok(i - 4); r.kind == tPunct && r.text == "." && tok(i-5).text == "Self" {
							nf.receiver = "Self." + nf.receiver
						}
					}
				}
				stack = append(stack, nf)
				continue
			case "{":
				stack = append(stack, &frame{open: "{"})
				continue
			case ")", "]", "}":
				want := map[string]string{")": "(", "]": "[", "}": "{"}[t.text]
				if len(stack) == 0 || stack[len(stack)-1].open != want {
					unbalanced++
				}
				for len(stack) > 0 {
					o := stack[len(stack)-1].open
					stack = stack[:len(stack)-1]
					if o == want {
						break
					}
				}
				continue
			case ",":
				if f != nil && f.open != "{" {
					f.arg++
					f.label = ""
					f.atStart = true
				}
				continue
			}
		case tIdent:
			if f != nil && f.open != "{" && f.atStart {
				n := tok(i + 1)
				if n.kind == tPunct && (d == dSwift && n.text == ":" || d == dKotlin && n.text == "=") {
					f.label = t.text
				}
			}
		case tString:
			s.judgeApp(rel, toks, i, f, rules, d)
		}
		if f != nil && f.open != "{" && !(t.kind == tPunct && (t.text == ":" || t.text == "=")) {
			if !(t.kind == tIdent && f.atStart && f.label == t.text) {
				f.atStart = false
			}
		}
	}
	if unbalanced > 0 || len(stack) > 0 {
		return fmt.Errorf("%s doesn't parse: its brackets don't balance", rel)
	}
	return nil
}

func (s *scanner) judgeApp(rel string, toks []ctok, i int, f *frame, rules *appRules, d cDialect) {
	t := toks[i]
	tok := func(j int) ctok {
		if j < 0 || j >= len(toks) {
			return ctok{kind: tPunct}
		}
		return toks[j]
	}
	prev, next := tok(i-1), tok(i+1)
	// comparisons, case labels, map keys, when branches
	if prev.kind == tPunct && (prev.text == "==" || prev.text == "!=" || prev.text == "~=" || prev.text == "===") ||
		prev.kind == tIdent && prev.text == "case" ||
		next.kind == tPunct && (next.text == "==" || next.text == "!=" || next.text == "->" || next.text == "~=") ||
		next.kind == tIdent && next.text == "to" {
		return
	}
	if next.kind == tPunct && next.text == ":" && f != nil && f.open == "[" && !(prev.kind == tPunct && prev.text == "?") {
		return // a dictionary key
	}
	if d == dSwift && prev.kind == tPunct && prev.text == ":" && tok(i-2).text == "verbatim" {
		return
	}
	// the enclosing call: a log, a lookup, a key
	if f != nil && f.open != "{" {
		if f.callee == "@" {
			return
		}
		_, sink := rules.sinks[f.callee]
		if rules.denyRecv[f.receiver] || rules.denyLabels[f.label] || rules.denyCalls[f.callee] && !sink {
			return
		}
	}
	text := t.text
	if f != nil && f.open == "(" {
		if arg, ok := rules.sinks[f.callee]; ok && f.arg == arg && (f.label == "" || rules.labels[f.label]) {
			if s.looseText(text) {
				s.add(rel, t.line, t.endLine, "sink:"+f.callee, text)
			}
			return
		}
		if f.label != "" && rules.labels[f.label] {
			if s.looseText(text) {
				s.add(rel, t.line, t.endLine, "label:"+f.label, text)
			}
			return
		}
	}
	// name = "…" (also Kotlin's named arguments outside the label table, and
	// the ternaries and elvis fallbacks after the =)
	if name := assignedName(toks, i); name != "" && textProp.MatchString(name) && !notTextProp.MatchString(name) {
		if s.looseText(text) {
			s.add(rel, t.line, t.endLine, "assign:"+name, text)
		}
		return
	}
	if s.prose(text, strict) {
		s.add(rel, t.line, t.endLine, "prose", text)
	}
}

// assignedName returns the name a literal is assigned to: "x = lit",
// "self.x = lit", "_x.value = lit", "let x = lit", "x = cond ? lit : lit",
// "x = y ?: lit", "x: String = lit".
func assignedName(toks []ctok, i int) string {
	tok := func(j int) ctok {
		if j < 0 || j >= len(toks) {
			return ctok{kind: tPunct}
		}
		return toks[j]
	}
	j := i - 1
	// walk back over a ternary or a fallback on the same right-hand side
	for steps := 0; steps < 12 && j >= 0; steps++ {
		t := tok(j)
		if t.kind == tPunct && (t.text == "=" || t.text == "+=") {
			break
		}
		if t.kind == tPunct && (t.text == "(" || t.text == ")" || t.text == "{" || t.text == "}" || t.text == "," ||
			t.text == ";" || t.text == "[" || t.text == "]") {
			return ""
		}
		if t.kind == tIdent && (t.text == "return" || t.text == "if" || t.text == "else") {
			return ""
		}
		j--
	}
	if t := tok(j); !(t.kind == tPunct && (t.text == "=" || t.text == "+=")) {
		return ""
	}
	k := j - 1
	if t := tok(k); t.kind == tPunct && (t.text == "?" || t.text == "!") {
		k-- // String? / String!
	}
	if t := tok(k); t.kind == tIdent && t.text == "String" && tok(k-1).text == ":" {
		k -= 2 // let x: String = "…", var x: String? = "…"
	}
	n := tok(k)
	if n.kind == tIdent && n.text == "value" && tok(k-1).text == "." {
		n = tok(k - 2) // _error.value = "…"
	}
	if n.kind != tIdent {
		return ""
	}
	return strings.TrimLeft(n.text, "_")
}

// appFiles lists a Swift or Kotlin tree's sources, leaving out tests,
// generated code and the localization layer itself.
func (s *scanner) appFiles(dir, ext string) ([]string, error) {
	return s.walk([]string{dir}, func(rel string) bool {
		base := path.Base(rel)
		return base == "i18n" || strings.HasSuffix(base, "Tests") || strings.HasSuffix(base, "UITests") ||
			base == "test" || base == "androidTest" || base == "proto-gen" || base == "generated"
	}, func(rel string) bool {
		base := path.Base(rel)
		if !strings.HasSuffix(base, ext) || strings.HasSuffix(base, ".pb.swift") || strings.HasPrefix(base, "S+") ||
			base == "Languages.swift" || base == "Languages.kt" || strings.HasPrefix(base, "L10n") {
			return false
		}
		return !strings.HasSuffix(strings.TrimSuffix(base, ext), "Test") && !strings.HasSuffix(strings.TrimSuffix(base, ext), "Tests")
	})
}

func (s *scanner) scanAppTree(dir, ext string, d cDialect, rules *appRules) error {
	files, err := s.appFiles(dir, ext)
	if err != nil {
		return err
	}
	for _, rel := range files {
		src, err := s.source(rel)
		if err != nil {
			return err
		}
		if err := s.scanApp(rel, src, d, rules); err != nil {
			return err
		}
	}
	return nil
}

func scanIOS(s *scanner) error {
	return s.scanAppTree("app/ios/OffTheCloud/OffTheCloud", ".swift", dSwift, &swiftRules)
}

func scanMacOS(s *scanner) error {
	return s.scanAppTree("app/macos/OffTheCloud/OffTheCloud", ".swift", dSwift, &swiftRules)
}

func scanAndroid(s *scanner) error {
	return s.scanAppTree("app/android/app/src/main/java", ".kt", dKotlin, &kotlinRules)
}
