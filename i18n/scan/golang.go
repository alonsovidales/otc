// SPDX-License-Identifier: AGPL-3.0-or-later

package scan

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"strconv"
	"strings"
)

// The Go scanners (device, bridge, otc-sync) look at sinks, not sources: Go
// code is mostly not interface, and syntax alone can't tell a log line from a
// reply. A literal counts when it reaches
//
//   - a sink call: an alert or a push (AddErrorNotification, queueAlert,
//     Notify), an HTTP or JSON error (http.Error, writeError), a tray menu
//     label or a zenity dialog;
//   - a sink field: pb ErrorMessage/ErrorMsg, Title, Details, Body, ...,
//     assigned or in a composite literal, and the "error"/"message" values
//     of a map literal;
//   - or, in a package whose errors reach people (the inventories:
//     websocket, files_manager, social, ... on the device; accounts and api
//     on the bridge; the tray and the engine in otc-sync), any literal that
//     reads like a sentence: errors.New, fmt.Errorf, fmt.Sprintf, constants.
//
// Literals inside a log call (log.Error, logger.Printf), fmt.Print* (the
// command line stays English), panic, exec, strings/filepath/regexp/os
// helpers, comparisons, case labels, map keys and struct tags never count.

type goRules struct {
	dirs      []string
	skipDirs  []string          // relative directories left out
	skipFiles map[string]string // relative files left out -> why
	userPkgs  []string          // directories whose sentences reach people
}

// goSinkCalls are called with text a person reads (by the last name of the
// callee, whatever the receiver).
var goSinkCalls = boolSet(
	// device alerts and pushes
	"AddErrorNotification", "UpsertErrorNotification", "AddStorageNotification", "AddUpdateNotification",
	"AddNotification", "queueAlert", "alert", "processingAlert", "stillFailed", "Alert", "Push", "Notify",
	"NotifyMobile", "notify",
	// HTTP and JSON errors
	"Error", "writeError", "writeJSONErr", "writeJSONError", "jsonError", "sendError", "send_json",
	// mail
	"Send", "SendWith",
	// tray and dialogs
	"AddMenuItem", "AddMenuItemCheckbox", "AddSubMenuItem", "AddSubMenuItemCheckbox", "SetTitle", "SetTooltip",
	"setTitle", "setTooltip", "setStatus", "setNote", "SetWindowText", "setText", "SetText",
)

// goSinkPkgs are packages every call of which shows text.
var goSinkPkgs = boolSet("zenity", "systray")

// goSinkFields hold text a person reads.
var goSinkFields = boolSet("ErrorMessage", "ErrorMsg", "Title", "Details", "Detail", "Body", "Summary", "Message",
	"Hint", "Note", "Caption", "Tooltip", "Label", "Subject", "Text", "Prompt", "Reason")

// goSinkKeys are map keys whose values are text ({"error": "…"}).
var goSinkKeys = boolSet("error", "message", "title", "body", "detail", "details", "hint")

// goDenyPkgs: calls into these packages carry keys, paths, layouts and logs.
var goDenyPkgs = boolSet("log", "slog", "logger", "strings", "bytes", "regexp", "filepath", "path", "os", "exec",
	"strconv", "flag", "cfg", "reflect", "unicode", "utf8", "sql", "syscall", "unix", "windows", "atomic", "sort",
	"binary", "hex", "base64", "mime", "filepathx", "testing", "runtime", "debug", "user", "signal", "netip", "net",
	"websocket", "proto", "protojson", "json", "xml", "csv", "gzip", "zip", "tar", "sha256", "hmac", "rsa", "x509",
	"pem", "tls", "jwt", "redis", "mysql", "sqlmock", "pkcs", "ed25519", "rand", "big", "md5", "sha1", "subtle",
	"cipher", "aes", "elliptic", "ecdsa", "crc32", "time", "context", "errgroup", "fsnotify", "keyring", "registry",
	"onnx", "gocv", "ort")

// goDenyCalls by last name.
var goDenyCalls = boolSet("Printf", "Println", "Print", "Fprintf", "Fprintln", "Fprint", "panic", "Debugf",
	"Infof", "Warnf", "Warningf", "Fatalf", "Fatal", "Debug", "Info", "Warn", "Warning", "Logf", "Log", "Sscanf",
	"Query", "QueryRow", "QueryContext", "QueryRowContext", "Exec", "ExecContext", "Prepare", "PrepareContext",
	"Getenv", "Setenv", "LookupEnv", "MustCompile", "Compile", "Command", "CommandContext", "HandleFunc",
	"Handle", "NewRequest", "NewRequestWithContext", "Get", "Post", "Head", "Set", "Add", "Del", "Header",
	"GetStr", "GetInt", "GetBool", "GetFloat", "Parse", "ParseInLocation", "Format", "AppendFormat",
	"NewProc", "NewLazyDLL", "NewLazySystemDLL", "MustFind", "FindProc", "Is", "As", "Unwrap", "Join",
	"Contains", "HasPrefix", "HasSuffix", "TrimPrefix", "TrimSuffix", "Split", "Replace", "ReplaceAll", "Index",
	"Cut", "EqualFold", "Fields", "Trim", "TrimSpace", "Count", "Repeat", "Map", "Title", "ToLower", "ToUpper",
	"utf16", "UTF16PtrFromString", "StringToUTF16Ptr", "StringToUTF16", "Getwd", "Open", "Create",
	"OpenFile", "ReadFile", "WriteFile", "Stat", "Lstat", "MkdirAll", "Remove", "RemoveAll", "Rename",
	"Readlink", "Symlink", "Glob", "Abs", "Rel", "Base", "Dir", "Ext", "Clean", "Match", "SetCookie",
	"Cookie", "FormValue", "PostFormValue", "URLParam", "PathValue", "SetPathValue", "Sprint", "Sprintln")

// goDenyRecv: method calls on these receivers are logs.
var goDenyRecv = boolSet("log", "logger", "lg", "slog", "Logger", "t", "b", "tb", "f", "h", "mac", "w", "hdr",
	"header", "q", "values", "form", "rows", "row", "tx", "db", "stmt", "rdb", "redis", "cmd", "pipe", "mock")

func (s *scanner) scanGo(r goRules) error {
	files, err := s.walk(r.dirs, func(rel string) bool {
		for _, d := range r.skipDirs {
			if rel == d {
				return true
			}
		}
		b := path.Base(rel)
		return b == "vendor" || b == "generated" || b == "proto-gen"
	}, func(rel string) bool {
		_, skip := r.skipFiles[rel]
		return strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, "_test.go") && !skip
	})
	if err != nil {
		return err
	}
	for _, rel := range files {
		src, err := s.source(rel)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, rel, src, parser.ParseComments)
		if err != nil {
			// never count a file that doesn't parse as zero: -update would
			// lock in a count that comes back once the file is fixed
			return fmt.Errorf("%s doesn't parse: %w", rel, err)
		}
		if ast.IsGenerated(f) {
			continue
		}
		user := false
		dir := path.Dir(rel)
		for _, p := range r.userPkgs {
			if dir == p || strings.HasPrefix(dir, p+"/") {
				user = true
			}
		}
		s.scanGoFile(rel, fset, f, user)
	}
	return nil
}

func (s *scanner) scanGoFile(rel string, fset *token.FileSet, f *ast.File, user bool) {
	var stack []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		switch x := n.(type) {
		case *ast.ImportSpec:
			return false
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				if text, err := strconv.Unquote(x.Value); err == nil {
					s.judgeGo(rel, fset, x, text, stack, user)
				}
			}
		}
		stack = append(stack, n)
		return true
	})
}

// judgeGo decides a literal by walking up its ancestors.
func (s *scanner) judgeGo(rel string, fset *token.FileSet, lit *ast.BasicLit, text string, stack []ast.Node, user bool) {
	line := fset.Position(lit.Pos()).Line
	end := fset.Position(lit.End()).Line
	var child ast.Node = lit
	for i := len(stack) - 1; i >= 0; i-- {
		switch p := stack[i].(type) {
		case *ast.Field:
			return // a struct tag
		case *ast.CallExpr:
			if p.Fun == child {
				return
			}
			pkg, name := goCallName(p.Fun)
			switch {
			case pkg != "" && goSinkPkgs[pkg] || goSinkCalls[name] && !goDenyRecv[pkg]:
				if s.looseText(text) {
					s.add(rel, line, end, "sink:"+name, text)
				}
				return
			case pkg != "" && (goDenyPkgs[pkg] || goDenyRecv[pkg]) || goDenyCalls[name]:
				return
			}
		case *ast.BinaryExpr:
			if p.Op != token.ADD {
				return // a comparison
			}
		case *ast.ParenExpr, *ast.UnaryExpr, *ast.StarExpr, *ast.CompositeLit:
		case *ast.KeyValueExpr:
			if p.Key == child {
				return // a map key
			}
			switch k := p.Key.(type) {
			case *ast.Ident:
				if goSinkFields[k.Name] {
					if s.looseText(text) {
						s.add(rel, line, end, "field:"+k.Name, text)
					}
					return
				}
			case *ast.BasicLit:
				if kt, err := strconv.Unquote(k.Value); err == nil && goSinkKeys[kt] {
					if s.looseText(text) {
						s.add(rel, line, end, "key:"+kt, text)
					}
					return
				}
				goto decided // another map value: data unless it reads like a sentence
			}
		case *ast.AssignStmt:
			for j, rhs := range p.Rhs {
				if rhs != child || j >= len(p.Lhs) {
					continue
				}
				if name := goTargetName(p.Lhs[j]); goSinkFields[name] {
					if s.looseText(text) {
						s.add(rel, line, end, "field:"+name, text)
					}
					return
				}
			}
			goto decided
		case *ast.CaseClause:
			for _, e := range p.List {
				if e == child {
					return
				}
			}
			goto decided
		case *ast.IndexExpr:
			if p.Index == child {
				return
			}
		case *ast.SwitchStmt:
			return
		default:
			goto decided
		}
		child = stack[i]
	}
decided:
	if user && s.prose(text, loose) && !goPathLike(text) {
		s.add(rel, line, end, "prose", text)
	}
}

// goCallName returns the qualifier (package or receiver identifier) and the
// last name of a callee.
func goCallName(e ast.Expr) (string, string) {
	switch f := e.(type) {
	case *ast.Ident:
		return "", f.Name
	case *ast.SelectorExpr:
		switch x := f.X.(type) {
		case *ast.Ident:
			return x.Name, f.Sel.Name
		case *ast.SelectorExpr:
			return x.Sel.Name, f.Sel.Name // a.logger.Printf
		case *ast.CallExpr:
			return "", f.Sel.Name
		}
		return "", f.Sel.Name
	case *ast.IndexExpr:
		return goCallName(f.X)
	}
	return "", ""
}

// goTargetName is the field or variable an assignment writes.
func goTargetName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	}
	return ""
}

// goPathLike: a format for a path or a command line, not a sentence.
func goPathLike(s string) bool {
	t := strings.TrimSpace(s)
	return strings.HasPrefix(t, "/") || strings.HasPrefix(t, "-") || strings.Contains(t, "://")
}

func scanDevice(s *scanner) error {
	return s.scanGo(goRules{
		dirs:     []string{"."},
		skipDirs: []string{"app", "bridge", "i18n", "web", "proto", "scripts", "docs", "dist", "bin/test_client"},
		userPkgs: []string{"websocket", "files_manager", "social", "bridgeaccess", "tailscalefunnel", "session",
			"devicerestart", "storage", "network", "updater", "raidwatch", "wifiwatch", "status", "push",
			"settings", "profile"},
	})
}

func scanBridge(s *scanner) error {
	return s.scanGo(goRules{
		dirs:     []string{"bridge"},
		skipDirs: []string{"bridge/admin", "bridge/fleet", "bridge/fleetagent", "bridge/loadtest", "bridge/cluster"},
		skipFiles: map[string]string{
			// the inventory: render the names with Intl.DisplayNames from the ISO codes
			"bridge/accounts/countries.go": "country names, data keyed by ISO code",
		},
		userPkgs: []string{"bridge/accounts", "bridge/api", "bridge/websocket"},
	})
}

func scanOTCSync(s *scanner) error {
	return s.scanGo(goRules{
		dirs: []string{"app/desktop"},
		userPkgs: []string{"app/desktop/internal/tray", "app/desktop/internal/engine", "app/desktop/internal/flasher",
			"app/desktop/internal/selfupdate", "app/desktop/internal/wsclient", "app/desktop/internal/config",
			"app/desktop/internal/browser", "app/desktop/internal/autostart"},
	})
}
