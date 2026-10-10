// SPDX-License-Identifier: AGPL-3.0-or-later

package scan

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// The web app is scanned with its own TypeScript compiler (decided over a Go
// lexer: JSX, generics and regular expressions make hand lexing TSX
// fragile, and web/node_modules is there wherever the web app is built).
// tsx.cjs prints every literal with where it stands; the decisions are made
// here. Without node or web/node_modules/typescript the surface fails with
// an error rather than passing silently.

//go:embed tsx.cjs
var tsxScript string

// TypeScriptModule is where the compiler is looked for, under the root.
const TypeScriptModule = "web/node_modules/typescript"

type webCandidate struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	End      int    `json:"end"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Recv     string `json:"recv"`
	Fallback bool   `json:"fallback"`
	Text     string `json:"text"`
}

// webTextAttrs are the JSX attributes a person reads (the components' own
// props included: <Confirm message=…>, <Field hint=…>).
var webTextAttrs = boolSet("aria-label", "aria-description", "aria-placeholder", "aria-valuetext",
	"aria-roledescription", "title", "placeholder", "alt", "label", "data-tip", "data-title", "data-tooltip", "text",
	"busyText", "doneText", "confirm", "confirmLabel", "cancelLabel", "actionLabel", "okLabel", "unavailable",
	"caption", "heading", "message", "hint", "subtitle", "description", "tooltip", "tip", "emptyText", "empty",
	"summary", "note", "prompt", "header", "footer", "question", "explain", "detail", "details", "body", "banner",
	"notice", "warning", "action", "submitLabel", "buttonLabel", "loadingText", "errorText", "helperText")

// webSinkCall: setters and helpers that show their argument.
var webSinkCall = regexp.MustCompile(`^(?:set\w*(?:Error|Err|Note|Status|Message|Msg|Text|Txt|Title|Label|Hint|Toast|Banner|Info|Notice|Warning|Result|Prompt|Caption|Problem|Reason|Question|Summary|Detail|Explain)|showToast|showMsg|showMessage|showError|toast|say|announce|alert|confirm|prompt|notify|flash|fail)$`)

var webDenyCalls = boolSet("addEventListener", "removeEventListener", "querySelector", "querySelectorAll",
	"getElementById", "createElement", "startsWith", "endsWith", "includes", "indexOf", "lastIndexOf", "split",
	"replace", "replaceAll", "match", "matchAll", "test", "closest", "matches", "getItem", "setItem", "removeItem",
	"getPropertyValue", "setProperty", "getAttribute", "setAttribute", "hasAttribute", "removeAttribute",
	"postMessage", "importKey", "digest", "encode", "decode", "fetch", "open", "require", "useMediaQuery",
	"useMedia", "useRef", "matchMedia", "getContext", "toDataURL", "toBlob", "RegExp", "URL", "URLSearchParams",
	"Blob", "File", "Worker", "BroadcastChannel", "EventSource", "WebSocket", "CustomEvent", "Event",
	"dispatchEvent", "get", "has", "delete", "cls", "clsx", "classNames", "padStart", "padEnd", "join",
	"localeCompare", "toLocaleDateString", "toLocaleString", "toLocaleTimeString", "DateTimeFormat",
	"NumberFormat", "PluralRules", "RelativeTimeFormat", "ListFormat", "DisplayNames", "keys", "navigate",
	"setTab", "setSource", "setView", "setMode", "setKind", "setSort", "setFilter", "setPath", "setQuery",
	"setStep", "setPhase", "setRestartPhase", "setAsked", "switchView", "fetchPage", "getRegistration",
	"register", "subscribe", "showNotification", "createObjectURL", "revokeObjectURL", "setTimeout",
	"setInterval", "requestAnimationFrame", "scrollTo", "scrollIntoView", "focus", "assign", "pushState",
	"replaceState", "back", "forward", "go", "insertAdjacentHTML", "execCommand", "writeText", "readText",
	"canPlayType", "isTypeSupported", "atob", "btoa", "Number", "BigInt", "parseInt", "parseFloat", "Symbol",
	"defineProperty", "freeze", "lazy", "memo", "forwardRef", "createContext", "useContext", "Function", "eval")

var webDenyRecv = boolSet("console", "localStorage", "sessionStorage", "classList", "dataset", "style",
	"document", "navigator", "location", "history", "JSON", "Math", "crypto", "subtle", "Reflect", "Object",
	"performance", "params", "searchParams", "headers", "registration", "clients", "self", "caches", "Intl",
	"Promise", "Array", "Number", "String", "Date", "Symbol", "BigInt", "path", "url", "ts")

func (s *scanner) webFiles() ([]string, error) {
	files, err := s.walk([]string{"web/src"}, func(rel string) bool {
		b := path.Base(rel)
		return rel == "web/src/proto" || rel == "web/src/i18n" || b == "__tests__" || b == "__mocks__"
	}, func(rel string) bool {
		b := path.Base(rel)
		if strings.HasSuffix(b, ".d.ts") || strings.Contains(b, ".test.") || strings.Contains(b, ".spec.") {
			return false
		}
		return strings.HasSuffix(b, ".ts") || strings.HasSuffix(b, ".tsx")
	})
	if err != nil {
		return nil, err
	}
	if s.exists("web/public/sw.js") {
		files = append(files, "web/public/sw.js")
	}
	return files, nil
}

func scanWeb(s *scanner) error {
	files, err := s.webFiles()
	if err != nil || len(files) == 0 {
		return err
	}
	tsDir := s.typescript
	if tsDir == "" {
		tsDir = filepath.Join(s.root, filepath.FromSlash(TypeScriptModule))
	}
	if _, err := os.Stat(filepath.Join(tsDir, "package.json")); err != nil {
		return fmt.Errorf("needs the TypeScript compiler in %s (npm ci --prefix web), or leave the surface out with -surface", TypeScriptModule)
	}
	node := s.node
	if node == "" {
		node = "node"
	}
	if _, err := exec.LookPath(node); err != nil {
		return fmt.Errorf("needs node on PATH to run the TypeScript compiler, or leave the surface out with -surface")
	}
	type inFile struct {
		Rel string `json:"rel"`
		Abs string `json:"abs"`
	}
	in := struct {
		TypeScript string   `json:"typescript"`
		Files      []inFile `json:"files"`
	}{TypeScript: tsDir}
	for _, rel := range files {
		if _, err := s.source(rel); err != nil {
			return err
		}
		in.Files = append(in.Files, inFile{Rel: rel, Abs: filepath.Join(s.root, filepath.FromSlash(rel))})
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return err
	}
	cmd := exec.Command(node, "-e", tsxScript)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return fmt.Errorf("the TypeScript extractor failed: %s", strings.TrimSpace(stderr.String()))
		}
		return err
	}
	var cands []webCandidate
	if err := json.Unmarshal(stdout.Bytes(), &cands); err != nil {
		return fmt.Errorf("the TypeScript extractor's output: %w", err)
	}
	for _, c := range cands {
		if c.Kind == "parse-error" {
			return fmt.Errorf("%s:%d doesn't parse: %s", c.File, c.Line, c.Text)
		}
	}
	for _, c := range cands {
		s.judgeWeb(c)
	}
	return nil
}

func (s *scanner) judgeWeb(c webCandidate) {
	add := func(rule string) { s.add(c.File, c.Line, c.End, rule, c.Text) }
	switch c.Kind {
	case "jsx-text":
		if s.textRun(c.Text) {
			add("jsx-text")
		}
		return
	case "attr":
		if webTextAttrs[c.Name] && s.looseText(c.Text) {
			add("attr:" + c.Name)
		}
		return // className, href, key, style, ...: never text
	case "jsx-expr":
		if s.looseText(c.Text) {
			add("jsx-expr")
		}
		return
	case "call":
		if webDenyRecv[c.Recv] || jsDenyRecv[c.Recv] {
			return
		}
		if webSinkCall.MatchString(c.Name) || jsSinkCalls[c.Name] && c.Recv == "" {
			if s.looseText(c.Text) {
				add("sink:" + c.Name)
			}
			return
		}
		if webDenyCalls[c.Name] {
			return
		}
	case "prop", "assign":
		if c.Name != "" && (textProp.MatchString(c.Name) || jsTextProps[c.Name]) && !notTextProp.MatchString(c.Name) {
			if s.looseText(c.Text) {
				add(c.Kind + ":" + c.Name)
			}
			return
		}
	}
	if c.Fallback && s.looseText(c.Text) {
		add("fallback")
		return
	}
	if s.prose(c.Text, strict) {
		add("prose")
	}
}
