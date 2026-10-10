// SPDX-License-Identifier: AGPL-3.0-or-later

package scan

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("golden", false, "rewrite testdata/repo.golden")

// repoTypeScript is the real repository's TypeScript compiler, which the
// web surface's tests borrow; "" when it isn't installed.
func repoTypeScript(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", TypeScriptModule))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
		return ""
	}
	if _, err := exec.LookPath("node"); err != nil {
		return ""
	}
	return dir
}

// TestFixtureRepo scans testdata/repo, one small file per surface, and
// compares every finding with testdata/repo.golden.
func TestFixtureRepo(t *testing.T) {
	ts := repoTypeScript(t)
	surfaces := SurfaceNames()
	if ts == "" {
		t.Log("no node or web/node_modules/typescript: the web surface is left out")
		surfaces = slices.DeleteFunc(slices.Clone(surfaces), func(s string) bool { return s == "web" })
	}
	res, err := Run(Options{Root: filepath.Join("testdata", "repo"), Surfaces: surfaces, TypeScript: ts})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, s := range res.Surfaces {
		fmt.Fprintf(&b, "# %s: %d found, %d ignored\n", s, len(res.Findings[s]), res.Ignored[s])
		for _, f := range res.Findings[s] {
			fmt.Fprintf(&b, "%s:%d: %s: %s\n", f.File, f.Line, f.Rule, f.Text)
		}
	}
	got := b.String()
	golden := filepath.Join("testdata", "repo.golden")
	if *updateGolden {
		if ts == "" {
			t.Fatal("-golden needs the web surface: install web/node_modules first")
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	data, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	want := string(data)
	if ts == "" {
		want = dropSection(want, "web")
	}
	if got != want {
		t.Errorf("findings differ from %s (go test ./i18n/scan -run TestFixtureRepo -golden rewrites it)\n%s", golden, lineDiff(want, got))
	}
}

func dropSection(golden, surface string) string {
	var out []string
	skip := false
	for _, l := range strings.SplitAfter(golden, "\n") {
		if strings.HasPrefix(l, "# ") {
			skip = strings.HasPrefix(l, "# "+surface+":")
		}
		if !skip {
			out = append(out, l)
		}
	}
	return strings.Join(out, "")
}

func lineDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	var b strings.Builder
	for _, l := range w {
		if !slices.Contains(g, l) {
			fmt.Fprintf(&b, "- %s\n", l)
		}
	}
	for _, l := range g {
		if !slices.Contains(w, l) {
			fmt.Fprintf(&b, "+ %s\n", l)
		}
	}
	return b.String()
}

// scanOne writes one file into a fresh repository and returns its
// findings as "rule: text".
func scanOne(t *testing.T, surface, rel, src string) []string {
	t.Helper()
	root := t.TempDir()
	file := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := Options{Root: root, Surfaces: []string{surface}}
	if surface == "web" {
		if opts.TypeScript = repoTypeScript(t); opts.TypeScript == "" {
			t.Skip("no node or web/node_modules/typescript")
		}
	}
	res, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range res.Findings[surface] {
		out = append(out, f.Rule+": "+f.Text)
	}
	return out
}

func expectFindings(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("findings:\n  got  %q\n  want %q", got, want)
	}
}

func TestSwift(t *testing.T) {
	const rel = "app/ios/OffTheCloud/OffTheCloud/V.swift"
	cases := []struct {
		name, src string
		want      []string
	}{
		{"view sinks", `Text("Hello there"); Button("OK") {}; .navigationTitle("Settings")`,
			[]string{"sink:Text: Hello there", "sink:Button: OK", "sink:navigationTitle: Settings"}},
		{"interpolation and ternary", `Text("\(n) of \(m) photos"); Text(n == 1 ? "One photo" : "Many photos")`,
			[]string{"sink:Text: {} of {} photos", "sink:Text: One photo", "sink:Text: Many photos"}},
		{"nested literal in an interpolation is part of it", `Text("Saved \(ok ? "now" : "later")")`,
			[]string{"sink:Text: Saved {}"}},
		{"verbatim, symbols, keys, logs", `Text(verbatim: "Raw Name"); Image(systemName: "trash"); UserDefaults.standard.set(1, forKey: "Some Key Name"); print("Deleting it now"); log.error("Failed to load it")`,
			nil},
		{"comparisons and cases", `if a == "Some Long Value" {}; switch a { case "Another Long Value": break }`, nil},
		{"never-translated only", `Text("Off The Cloud"); Text("Tailscale")`, nil},
		{"assignments", `alertMessage = "Could not delete it."; self.status = "Done"; viewMode = "list"; let deleteTitle: String = "Delete?"`,
			[]string{"assign:alertMessage: Could not delete it.", "assign:status: Done", "assign:deleteTitle: Delete?"}},
		{"labels", `NSMenuItem(title: "Quit", action: nil, keyEquivalent: "q"); Foo(message: "Are you sure?")`,
			[]string{"sink:NSMenuItem: Quit", "label:message: Are you sure?"}},
		{"prose anywhere else", `static let explain = "Photos here are not tagged."; let id = "otc-setup"`,
			[]string{"assign:explain: Photos here are not tagged."}},
		{"comments and raw strings", "// Text(\"In a comment\")\n/* outer /* nested \"Quoted text\" */ still */\nlet x = #\"A \"raw\" one here.\"#",
			[]string{"prose: A \"raw\" one here."}},
		{"multi-line literal", "let body = \"\"\"\n    First line here\n    and the second.\n    \"\"\"",
			[]string{"assign:body: First line here and the second."}},
		{"annotation", `@available(*, deprecated, message: "Use the other one instead")`, nil},
		{"ignore marker", "Text(\"Kept as it is\") // i18n-ignore: product wording\nText(\"Not kept\") // i18n-ignore:", []string{"sink:Text: Not kept"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			expectFindings(t, scanOne(t, "ios", rel, c.src), c.want)
		})
	}
}

func TestKotlin(t *testing.T) {
	const rel = "app/android/app/src/main/java/cloud/offthe/otc/ui/V.kt"
	cases := []struct {
		name, src string
		want      []string
	}{
		{"compose sinks", `Text("Storage"); Text(text = "Used $n of ${vm.total} on the disk"); Caption("Kept on the device.")`,
			[]string{"sink:Text: Storage", "sink:Text: Used {} of {} on the disk", "sink:Caption: Kept on the device."}},
		{"named text arguments", `Icon(x, contentDescription = "Close"); AlertDialog(title = "Delete it?")`,
			[]string{"label:contentDescription: Close", "label:title: Delete it?"}},
		{"toast's second argument", `Toast.makeText(ctx, "Saved to the device", Toast.LENGTH_SHORT)`,
			[]string{"sink:makeText: Saved to the device"}},
		{"logs, preferences, maps and when branches", `Log.d(TAG, "Saving it now"); prefs.getString("Some Pref Key", null); mapOf("Header Key Name" to 1); when (x) { "Kind With Spaces" -> {} }`,
			nil},
		{"view model state", `_error.value = "Could not save it."; toast = "Saved"; val TAG = "Settings"; var note: String? = "Kept here"`,
			[]string{"assign:error: Could not save it.", "assign:toast: Saved", "assign:note: Kept here"}},
		{"raw string and char literal", "val q = '\"'\nval s = \"\"\"\n  A raw string\n  over lines.\n\"\"\"",
			[]string{"prose: A raw string over lines."}},
		{"annotation", `@Deprecated("Deprecated in Java") fun f() {}`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			expectFindings(t, scanOne(t, "android", rel, c.src), c.want)
		})
	}
}

func TestGo(t *testing.T) {
	const head = "package websocket\n\nimport (\n\t\"errors\"\n\t\"fmt\"\n)\n\n"
	cases := []struct {
		name, rel, src string
		want           []string
	}{
		{"sink fields", "websocket/x.go", head + `func f(r *Resp, err error) { r.ErrorMessage = fmt.Sprintf("error trying to delete: %s", err); _ = &Ack{ErrorMsg: "Too many attempts"} }`,
			[]string{"field:ErrorMessage: error trying to delete: %s", "field:ErrorMsg: Too many attempts"}},
		{"user package errors", "websocket/x.go", head + `var e = errors.New("no such job"); var w = fmt.Errorf("%s: %w", a, b)`,
			[]string{"prose: no such job"}},
		{"internal package errors", "segcrypt/x.go", head + `var e = errors.New("bad segment header")`, nil},
		{"internal package sink", "dao/x.go", head + `func f() *Ack { return &Ack{ErrorMsg: "The database is closed"} }`,
			[]string{"field:ErrorMsg: The database is closed"}},
		{"logs, prints, comparisons, tags", "websocket/x.go", head + "type T struct{ A string `json:\"a b c\"` }\n" +
			`func f(s string) { log.Error("could not open it", s); fmt.Println("listening on the port"); if s == "some long value" {}; _ = map[string]int{"key with words": 1} }`,
			nil},
		{"alerts and map errors", "files_manager/x.go", head + `func f() { mg.alert("could not be read", p, err); _ = map[string]any{"error": "sign in first", "code": "login_required"} }`,
			[]string{"sink:alert: could not be read", "key:error: sign in first"}},
		{"sql", "websocket/x.go", head + `const q = "SELECT name FROM files WHERE hash = ?"`, nil},
		{"command line stays English", "app/desktop/cmd/otc-sync/x.go", head + `func f() { fmt.Println("usage: otc-sync status"); _ = errors.New("no such folder") }`,
			nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			surface := "device"
			if strings.HasPrefix(c.rel, "app/desktop/") {
				surface = "otcsync"
			}
			expectFindings(t, scanOne(t, surface, c.rel, c.src), c.want)
		})
	}
}

func TestTray(t *testing.T) {
	src := "package tray\n\nfunc f() {\n" +
		"\tsystray.AddMenuItem(\"Open Web App\", \"\")\n" +
		"\tzenity.Error(err.Error(), zenity.Title(\"Off The Cloud\"))\n" +
		"\tu.setTitle(item, \"Checking…\")\n}\n"
	expectFindings(t, scanOne(t, "otcsync", "app/desktop/internal/tray/x.go", src),
		[]string{"sink:AddMenuItem: Open Web App", "sink:setTitle: Checking…"})
}

func TestHTML(t *testing.T) {
	const rel = "bridge/static/landing.html"
	cases := []struct {
		name, src string
		want      []string
	}{
		{"a sentence with inline markup counts once", `<p>Read the <a href="/privacy">privacy notice</a> first.</p>`,
			[]string{"text: Read the privacy notice first."}},
		{"block elements and lines split", "<ul><li>One thing</li><li>Another</li></ul>\n<nav><a>Sign in</a>\n<a>Create an account</a></nav>",
			[]string{"text: One thing", "text: Another", "text: Sign in", "text: Create an account"}},
		{"code is a placeholder", `<p>Run <code>sudo otc-sync flash</code> as root.</p><pre>make all</pre>`,
			[]string{"text: Run {} as root."}},
		{"attributes", `<img alt="A photo" src="/a.png"><input placeholder="Your name"><input type="submit" value="Send"><meta name="description" content="A NAS for families.">`,
			[]string{"attr:alt: A photo", "attr:placeholder: Your name", "attr:value: Send", "attr:content: A NAS for families."}},
		{"style, comments, never-translated", `<style>p { content: "Not text"; }</style><!-- A comment --><span>Off The Cloud</span>`, nil},
		{"script sinks", "<script>\nel.textContent = 'Copied!';\nalert(\"Could not copy it\");\nconst x = r.error || 'Failed';\nif (s === 'Some Long Value') {}\nconsole.log('Copying it now');\nel.className = ok ? 'msg' : 'msg bad';\n</script>",
			[]string{"assign:textContent: Copied!", "sink:alert: Could not copy it", "fallback: Failed"}},
		{"markup built in a script", "<script>\nview.innerHTML = `<h2>Hello, ${name}</h2>\n<p>Welcome back.</p>`;\nconst cmd = 'curl -fsSL https://x.org | bash -s -- <subdomain>';\n</script>",
			[]string{"text: Hello, {}", "text: Welcome back."}},
		{"json block", `<script type="application/json">{"api": {"bad_body": "invalid request body"}}</script>`,
			[]string{"json: invalid request body"}},
		{"identifiers only", `<pre class="id">DEVICE_UUID=</pre><p>DEVICE_UUID={x} BRIDGE_SECRET={y}</p>`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			expectFindings(t, scanOne(t, "pages", rel, c.src), c.want)
		})
	}
	// other pages are not scanned
	expectFindings(t, scanOne(t, "pages", "bridge/static/privacy.html", "<p>Legal text stays English.</p>"), nil)
}

func TestHTMLLines(t *testing.T) {
	root := t.TempDir()
	src := "<html>\n<body>\n<h1>Title here</h1>\n<script>\nview.innerHTML = `\n  <h2>${a}</h2>\n  <p>On line seven</p>`;\n</script>\n</body>\n</html>\n"
	if err := os.MkdirAll(filepath.Join(root, "bridge", "static"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bridge", "static", "account.html"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Run(Options{Root: root, Surfaces: []string{"pages"}})
	if err != nil {
		t.Fatal(err)
	}
	var lines []int
	for _, f := range res.Findings["pages"] {
		lines = append(lines, f.Line)
	}
	if !slices.Equal(lines, []int{3, 7}) {
		t.Errorf("lines %v, want [3 7]", lines)
	}
}

func TestWizard(t *testing.T) {
	src := "def f(self, data):\n" +
		"    self.send_json(400, {\"error\": \"choose a network\"})\n" +
		"    self.send_json(400, {\"error\": f\"{ssid} needs a password\", \"code\": \"x\"})\n" +
		"    self.send_json(400, {\"error\": \"not_found\"})\n" +
		"    msg = data.get(\"error\") or \"could not sign in\"\n" +
		"    print(\"Starting the wizard\")\n" +
		"# BEGIN GENERATED I18N\n" +
		"I18N = {\"en\": {\"error\": \"from the catalog\"}}\n" +
		"# END GENERATED I18N\n" +
		"PAGE = r\"\"\"<!doctype html><h1>Set up your device</h1><script>status.textContent = 'Joining…';</script>\"\"\"\n"
	expectFindings(t, scanOne(t, "wizard", "scripts/setup_wizard.py", src), []string{
		"key:error: choose a network", "key:error: {} needs a password", "fallback: could not sign in",
		"text: Set up your device", "assign:textContent: Joining…",
	})
}

func TestWeb(t *testing.T) {
	const rel = "web/src/components/V.tsx"
	cases := []struct {
		name, src string
		want      []string
	}{
		{"jsx text and attributes", `export const V = () => <div className="row x-1" aria-label="Close the panel"><h2>Your <b>photos</b> here</h2></div>;`,
			[]string{"attr:aria-label: Close the panel", "jsx-text: Your photos here"}},
		{"expressions and sinks", `export const V = ({ busy }) => { setError("Could not save it."); return <p>{busy ? "Saving…" : "Save"}</p>; };`,
			[]string{"sink:setError: Could not save it.", "jsx-expr: Saving…", "jsx-expr: Save"}},
		{"keys, logs, comparisons, classes", `const K = "otc_menu_open"; console.log("Saving it now"); if (tab === "Photos and videos") {}; localStorage.setItem("Some Key Name", "1"); const c = "flex items-center gap-2";`,
			nil},
		{"fallbacks and properties", `const m = e.message || "Something went wrong"; const items = [{ label: "Delete", id: "del" }];`,
			[]string{"fallback: Something went wrong", "prop:label: Delete"}},
		{"elements on lines of their own", "export const V = () => (\n  <div>\n    <span>Name</span>\n    <span>Size</span>\n  </div>\n);",
			[]string{"jsx-text: Name", "jsx-text: Size"}},
		{"ignore marker in JSX", "export const V = () => (\n  <div>\n    <span>Kept</span> {/* i18n-ignore: product wording */}\n  </div>\n);", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			expectFindings(t, scanOne(t, "web", rel, c.src), c.want)
		})
	}
}

func TestWebNeedsTypeScript(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "web", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "web", "src", "a.tsx"), []byte(`export const a = <p>Hi there</p>;`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Run(Options{Root: root, Surfaces: []string{"web"}})
	if err == nil || !strings.Contains(err.Error(), "TypeScript") {
		t.Fatalf("err = %v, want a missing TypeScript error", err)
	}
}

func TestUnknownSurface(t *testing.T) {
	if _, err := Run(Options{Root: t.TempDir(), Surfaces: []string{"nope"}}); err == nil {
		t.Fatal("no error for an unknown surface")
	}
}

func TestGoParseErrorFails(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "websocket"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "websocket", "a.go"), []byte("package websocket\nfunc (\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(Options{Root: root, Surfaces: []string{"device"}}); err == nil || !strings.Contains(err.Error(), "doesn't parse") {
		t.Fatalf("err = %v, want a parse error", err)
	}
}

// A file in the middle of an edit fails the surface instead of counting
// less (which -update would lock in).
func TestBrokenFilesFail(t *testing.T) {
	cases := []struct{ surface, rel, src string }{
		{"ios", "app/ios/OffTheCloud/OffTheCloud/V.swift", "Text(\"open string\n"},
		{"ios", "app/ios/OffTheCloud/OffTheCloud/V.swift", "struct V { var body: some View { Text(\"x\") }"},
		{"macos", "app/macos/OffTheCloud/OffTheCloud/V.swift", "/* never closed"},
		{"android", "app/android/app/src/main/java/cloud/offthe/otc/V.kt", "fun f() { Text(\"x\") ))"},
		{"web", "web/src/V.tsx", "export const V = () => <div><p>Hi</div>;"},
	}
	for _, c := range cases {
		t.Run(c.surface+" "+c.src, func(t *testing.T) {
			root := t.TempDir()
			file := filepath.Join(root, filepath.FromSlash(c.rel))
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(c.src), 0o644); err != nil {
				t.Fatal(err)
			}
			opts := Options{Root: root, Surfaces: []string{c.surface}}
			if c.surface == "web" {
				if opts.TypeScript = repoTypeScript(t); opts.TypeScript == "" {
					t.Skip("no node or web/node_modules/typescript")
				}
			}
			if _, err := Run(opts); err == nil || !strings.Contains(err.Error(), "doesn't parse") {
				t.Fatalf("err = %v, want a parse error", err)
			}
		})
	}
}
