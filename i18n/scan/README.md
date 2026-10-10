# Hard-coded text scanners

`go run ./i18n/cmd/i18nscan` counts the text a person can read that is still written into the
source instead of coming from the catalog, per file, and fails when a file has more than its
committed baseline. It is the ratchet `make i18n-check` runs (docs/i18n.md, "Generated files"):
the counts only ever go down, as each unit moves its text into `i18n/strings/en/`.

```
go run ./i18n/cmd/i18nscan                                # check every surface
go run ./i18n/cmd/i18nscan -surface ios -v                # one surface, every finding
go run ./i18n/cmd/i18nscan -path web/src/components/Sidebar.tsx -v
go run ./i18n/cmd/i18nscan -update -surface ios           # write the lower counts
go run ./i18n/cmd/i18nscan -update -path app/android/app/src/main/java/cloud/offthe/otc/ui/settings
```

| flag | |
|---|---|
| `-update` | write the baseline from the current counts; with `-surface`/`-path`, only those entries. A count that rose is refused and nothing is written |
| `-allow-increase` | with `-update`: write counts that rose too (a file split, moved or renamed; a new scanner rule). Say why in the commit |
| `-surface a,b` | `web`, `ios`, `macos`, `android`, `device`, `bridge`, `otcsync`, `pages`, `wizard` |
| `-path p,q` | only files under these repository-relative paths (a unit checks and updates only its own files) |
| `-v` | every file's count and every finding (`file:line: rule: text`) |
| `-root dir`, `-node path`, `-typescript dir` | the repository, node, and the TypeScript module (default `web/node_modules/typescript`) |

Exit codes: 0 no count rose; 1 a count rose, a surface has no baseline, or `-update` refused; 2 a
usage error or a surface that could not be scanned. A failing check prints every finding of each
file that rose. When counts fell it says so and suggests `-update`; it never fails for that.

The baselines are `baseline/<surface>.json`, one per surface so that units working on different
platforms don't write the same file: `{"surface", "total", "files": {path: count}}`, sorted, files
at zero left out. `-update` writes them; never edit them by hand.

## When a unit is done

Its files are at zero, or every line left carries the escape hatch: a trailing comment
`i18n-ignore: <reason>` on (any line of) the literal. The reason is required; a bare marker is not
honoured.

```swift
Text("OTC-\(id)") // i18n-ignore: the device id, never translated
```
```tsx
<span>{name}</span> {/* i18n-ignore: a person's name */}
```
```html
<code>DEVICE_UUID=</code> <!-- i18n-ignore: identifier -->
```
```python
self.send_json(400, {"error": "not_found"})  # i18n-ignore: protocol value
```

Lines between `BEGIN GENERATED I18N` and `END GENERATED I18N` (the wizard's dictionary) never
count, nor do generated files and directories named `i18n` (every emitter writes there), tests and
protobuf bindings.

## What counts

The scanners are heuristics tuned on this repository. They look at where a literal stands, not at
the literal alone: syntax can't tell a log line from a reply, so each surface has *sinks* (the
places text reaches a person) and, where code is mostly interface, a *prose* rule for literals that
read like a sentence. Text made only of never-translated terms (`i18n/glossary/never.json`: "Off
The Cloud", "Tailscale", ...) never counts. Every finding names its rule.

| rule | meaning |
|---|---|
| `sink:<callee>` | the text argument of a call that shows it (`Text("…")`, `Toast.makeText(ctx, "…")`, `setError("…")`, `AddErrorNotification(…)`, `zenity.Title("…")`) - any word counts |
| `label:<name>` | an argument labelled as text (`title:`, `message:`, `contentDescription = `) |
| `assign:<name>`, `prop:<name>` | a value given to a text-ish property or variable (`alertMessage = "…"`, `_error.value = "…"`, `{ label: "…" }`, `el.textContent = '…'`) |
| `field:<Name>` | Go: a protobuf or struct field that carries text (`ErrorMessage`, `ErrorMsg`, `Title`, `Details`, `Body`, `Message`, ...) |
| `key:error` | a map's `"error"`/`"message"` value (`{"error": "…"}` in Go and Python) |
| `jsx-text`, `text` | a run of JSX or HTML text; inline elements (`<a>`, `<b>`, ...) don't split a sentence, block elements and elements on lines of their own do, `<code>`/`<kbd>`/`<pre>` are placeholders |
| `jsx-expr` | a literal shown as a JSX child (`{busy ? "Saving…" : "Save"}`) |
| `attr:<name>` | a text attribute (`aria-label`, `title`, `placeholder`, `alt`, the components' own `label`/`hint`/`message`/... props; `meta description`) |
| `fallback` | the text after `\|\|`/`??` (`r.error \|\| 'Could not sign in'`) |
| `json` | a value of a `<script type="application/json">` block in a page |
| `prose` | anywhere else, a literal that reads like a sentence |

Never counted: logs (`print`, `NSLog`, `Log.d`, `log.Error`, `console.*`), `fmt.Print*` (the
otc-sync command line stays English), comparisons, `case` labels, dictionary and map keys, struct
tags, keys for storage and preferences, symbols and image names (`systemImage:`, `Image(…)`),
`Text(verbatim:)`, annotations, SQL, CSS values and class lists, date formats, URLs and paths.

### Per surface

- **web** (`web/src/**/*.ts(x)` and `web/public/sw.js`; not `web/src/proto`, `web/src/i18n`, tests):
  the web app's own TypeScript compiler, run through node (`tsx.cjs`, embedded in the binary).
  A Go lexer was the alternative; JSX, generics and regular expressions make hand-lexing TSX
  fragile, and `web/node_modules` exists wherever the web app is built. Without node or the
  module the surface fails (exit 2) rather than passing silently: run `npm ci --prefix web`, or
  leave it out with `-surface`.
- **ios**, **macos** (`app/<ios|macos>/OffTheCloud/OffTheCloud/**/*.swift`; not `*.pb.swift`,
  `i18n/`, `S+*.swift`, `L10n*.swift`, tests) and **android**
  (`app/android/app/src/main/java/**/*.kt`; not `i18n/`): a lexer (`clex.go`: interpolation,
  raw and multi-line strings, nested comments) and the rules in `apps.go`: SwiftUI views and
  modifiers, Compose's `Text`, the apps' own helpers (`showToast`, `Caption`, `RowButton`, ...),
  `NSAlert` texts, view-model state.
- **device** (the root module, without `app/`, `bridge/`, `i18n/`, `web/`, `proto/`, `scripts/`),
  **bridge** (`bridge/`, without `admin`, `fleet`, `fleetagent`, `loadtest`, `cluster` and
  `accounts/countries.go`, data the inventory says to render with `Intl.DisplayNames`) and
  **otcsync** (`app/desktop/`): `go/ast`. Sinks everywhere; the prose rule only in the packages
  whose errors reach people, per the inventories (device: websocket, files_manager, social,
  bridgeaccess, tailscalefunnel, session, devicerestart, storage, network, updater, raidwatch,
  wifiwatch, status, push, settings, profile; bridge: accounts, api, websocket; otc-sync: tray,
  engine, flasher, selfupdate, wsclient, config, browser, autostart - not `cmd/otc-sync`).
- **pages** (`bridge/static/landing.html`, `account.html`, `disabled.html`, `unavailable.html`;
  not `admin.html`, `privacy.html`, `terms.html`, `index.html`): HTML text and attributes, and the
  inline scripts through a JavaScript lexer (`jslex.go`); markup built in a template literal is
  scanned as HTML, at its own lines.
- **wizard** (`scripts/setup_wizard.py`): its `PAGE` like the pages, and the backend's
  `{"error": "…"}`, `data.get("error", "…")` and `data.get("error") or "…"`.

A new page, package or helper that shows text has to be added here (`bridgePages` in `html.go`,
`userPkgs` in `golang.go`, the sink tables in `apps.go`, `web.go` and `html.go`), with a fixture
in `testdata/repo` and `go test ./i18n/scan -run TestFixtureRepo -golden` to rewrite
`testdata/repo.golden`.

### Known limits

- Text assembled far from where it is shown (a Go `title := fmt.Sprintf(...)` in a package outside
  the prose list, then passed to a sink) is missed; the runtime pseudo-locale check catches it.
- Some internal error sentences in the prose packages count (`malformed range: %q`); mark them
  `i18n-ignore` when the unit decides they never reach a person.
- The JavaScript lexer reads a slash after `)` as a division, so `if (x) /re/` is misread; the
  pages don't write that.
