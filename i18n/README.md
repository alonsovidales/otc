# Translations

Every text a user sees in an Off The Cloud app lives here once, in English, with its
translations next to it. `make i18n` turns this catalog into each platform's own files; `make
i18n-check` proves the catalog and those files are in order. The design, and why it is this way,
is in [`docs/i18n.md`](../docs/i18n.md).

- [Layout](#layout)
- [Adding or changing text](#adding-or-changing-text)
- [Keys, roots and prefixes](#keys-roots-and-prefixes)
- [English entries](#english-entries)
- [Translation entries](#translation-entries)
- [Reviews](#reviews)
- [What the checker refuses](#what-the-checker-refuses)
- [Glossaries and never-translated words](#glossaries-and-never-translated-words)
- [Commands](#commands)
- [Writing an emitter](#writing-an-emitter)

## Layout

```
i18n/
  languages.json              the languages and the routes (which programs show which keys)
  strings/en/<prefix>.json    the English source
  strings/<code>/<prefix>.json  translations
  glossary/<code>.json        terms per language (optional)
  glossary/never.json         words never translated (optional)
  stored.json                 generated: the keys databases hold (see "Stored keys")
  model/                      the catalog in memory: reading, checking, canonical form
  cmd/i18ngen/                the generator and checker, and the platform emitters
  catalog/<code>/<prefix>.json  generated: what the Go programs embed
  *.go                        package i18n, the Go runtime (device, bridge, otc-sync)
  scan/, cmd/i18nscan/        the hard-coded-text scanners and their baselines (scan/README.md)
```

`languages.json` is the only list of languages:

| field | meaning |
|---|---|
| `code` | what is stored, sent on the wire and used in file names (`pt`) |
| `tag` | the BCP 47 tag behind plural rules and number and date formats (`pt-PT`) |
| `apple` | the `.lproj` name |
| `android` | the qualifier after `values-` (empty for English, the default resources) |
| `name` | the language's own name, shown in the pickers |
| `status` | `shipping` (in the generated files) or `draft` (only with `make i18n DRAFT=1`) |

English comes first and always ships. `routes` maps each root (a key's first segment) to the
programs that show its keys: `web`, `ios`, `android`, `macos`, `otcsync`, `go` (everything the
device and the bridge render from Go: replies, alerts, pushes, emails, API errors, public pages)
and `wizard`.

## Adding or changing text

1. Pick the key (below) and add the entry to `strings/en/<prefix>.json`, with a `note` and its
   `args`.
2. Run `make i18n`. It puts the file in its canonical form and regenerates every platform file.
3. Use the generated accessor in the code, never the literal text.
4. Translations follow in `strings/<code>/<prefix>.json`; until then every language shows the
   English.
5. `make i18n-check` must pass before you commit.

Changing an English text makes every translation of it stale: the apps show the new English until
it is translated again. Changing what a text means deserves a new key.

## Keys, roots and prefixes

A key is lowercase segments joined by dots, at least two, with underscores only inside a segment
(`^[a-z][a-z0-9]*(\.[a-z0-9]+(_[a-z0-9]+)*)+$`), at most 96 characters (the device stores alert
keys in a `VARCHAR(96)`).

The **root**, its first segment, says who shows it:

| root | shown by |
|---|---|
| `common` | every client. Basic buttons and words; frozen: ask before adding |
| `web` | the web app |
| `app` | the iPhone and Android apps (always the same key in both) |
| `ios`, `android` | one phone app only |
| `desk` | the Mac app and otc-sync (always the same key in both) |
| `macos` | the Mac app only |
| `dev` | device replies and alerts |
| `push` | pushes, from the device and the bridge |
| `mail`, `api`, `site` | the bridge's emails, API errors and public pages |
| `wiz` | the setup wizard |

The **prefix** is the file a key lives in, `strings/en/<prefix>.json`. A root either has one file
named after it (`common.json`, holding every `common.*` key), or is split by area into files named
after its first two segments (`web.photos.json` holding `web.photos.*`, `web.files.json`...), never
both. Split roots suit the big surfaces (`web`, `app`, `desk`, `dev`) so that people working on
different screens never edit the same file; their keys have at least three segments. Translations
use the same file names. Generated files are split by prefix too.

Keys become identifiers on every platform, and the checker refuses two keys (or two prefixes) that
would become the same one:

| | `app.photos.deleted_by` |
|---|---|
| Android resource | `app_photos_deleted_by` |
| Swift and Kotlin | `appPhotosDeletedBy` |
| Go | `AppPhotosDeletedBy` |
| prefix `app.photos` as a file or type | `app_photos`, `AppPhotos` |

So `ios.a_b` and `ios.a.b` can't both exist, nor `ios.x1y` and `ios.x.1y`.

## English entries

```json
{
  "app.photos.deleted_by": {
    "text": {
      "one": "{name} deleted a photo",
      "other": "{name} deleted {count} photos"
    },
    "args": [["name", "user"], ["count", "count"]],
    "note": "Alert line in Notifications",
    "max": 60
  }
}
```

| field | |
|---|---|
| `text` | required: a string, or `{"one": ..., "other": ...}` |
| `note` | required: where and how the text is shown, for translators |
| `args` | the arguments, in order: `[["name", "type"], ...]` |
| `max` | the most characters a form may have, in every language: code points, tags not counted, each placeholder counted as written (`{name}` is 6) |
| `rich` | the tags the text may use (`["link"]`) |
| `stored` | `true` for keys written to a database (see "Stored keys") |
| `review` | `"required"` for consent, legal, security and destructive text (see "Reviews") |
| `reviewed` | the review of such a text |
| `translate` | `false` for text that stays English everywhere |

**Placeholders** are `{name}` and must match the declared arguments exactly: every placeholder is
declared, and every argument appears in every form. Position N in `args` is position N in every
language and every generated accessor. Argument names are lowercase words joined by underscores,
and can't be a keyword in a language the key is generated in (`in`, `default`, `type`, `when` for
Kotlin keys...). Types:

| type | |
|---|---|
| `text` | text the app supplies |
| `user` | user-controlled text (a name, a file name); isolated with FSI/PDI when shown |
| `count` | the number that picks the plural form; a key has one exactly when its text is plural |
| `int` | any other number, formatted for the language |
| `bytes`, `datetime`, `msg` | only for keys Go alone renders from stored data (`dev`, `push`, `mail`, `api`, `site`): a size, a unix time, a sub-key chosen from an allowlist in code |

Push keys may only use `{name}`, `{version}` and `{port}`: Apple and Google can read pushes.

**Plurals** are exactly `one` and `other`. A text for zero is a key of its own, chosen in code.
`one` may leave out the count ("a photo") where one means exactly 1; in French 0 is `one` too, so
there it must show `{count}`.

**Rich text** marks spans with tags: `Read the <link>privacy policy</link>`, with `"rich":
["link"]`. Tags are `<name>...</name>`, lowercase, without attributes; they don't nest; the code
decides what each tag does (a link's address always comes from code). Keys Go renders as plain
text (`dev`, `push`, `mail`, `api`) can't have tags; `site` can.

A literal `<`, `{` or `}` can't be written: say it in words. Quotes, `'`, `&`, `%`, `>`, `@`, `?`
and line breaks are fine; each emitter escapes them for its format.

## Translation entries

```json
{
  "app.photos.deleted_by": {
    "text": {
      "one": "{name} borró una foto",
      "other": "{name} borró {count} fotos"
    },
    "en": {
      "one": "{name} deleted a photo",
      "other": "{name} deleted {count} photos"
    }
  }
}
```

`en` is the English the translation was made from. When it no longer equals the English entry's
`text`, the translation is **stale**: the generated files use the English instead, and a release
check fails. Translating again means updating `text` and copying the current English into `en`.

A translation keeps the English's placeholders and tags (in any order), its links, email addresses
and domains verbatim, the never-translated words, and `max`. Keys with `"translate": false` may be
left out; if present, their text is the English.

## Reviews

A key with `"review": "required"` needs a review of its English and of every translation, written
as `"reviewed": "<who> <YYYY-MM-DD>"`. Run `make i18n` after writing it: the canonical rewrite
appends a fingerprint of the text as it stands (`"owner 2026-10-12 #1a2b3c4d"`). Any later change
to the text (in a translation, also to its `en`) no longer matches the fingerprint, and the review
counts as stale until someone writes a new `"<who> <date>"`. A missing or stale review is a
warning, and an error with `-release`.

## What the checker refuses

Always errors:

- JSON that isn't valid UTF-8 or starts with a byte order mark, duplicate keys at any depth,
  unknown fields, wrong types.
- Keys that don't match the pattern, are longer than 96 characters, sit in the wrong file, or have
  a root with no route; collisions after renaming; a root with both a whole file and area files.
- A missing `note`; plural forms other than exactly `one` and `other`.
- Undeclared placeholders, unused arguments (except the count in `one` where one means 1), unknown
  types, duplicate or reserved argument names, more than one count, a plural without a count or a
  count without a plural, stored-data types outside Go-only roots, push arguments other than
  `name`, `version`, `port`.
- Tags that are malformed, nested, unbalanced, have attributes, aren't listed in `rich`, are
  listed but unused, appear in a Go-rendered root, or differ from the English in a translation.
- Hostile content in any form: `<script`, event attributes (`on…=`), `javascript:`/`vbscript:`/
  `data:` URLs, HTML entities, control characters, bidi controls (U+202A-202E, U+2066-2069),
  private-use characters (the rich-text renderers use them), byte order marks and line
  separators, leading or trailing whitespace, double spaces, a space next to a line break, tabs,
  empty forms.
- In a translation: a URL, email address or domain that isn't in the English verbatim; a
  never-translated word dropped; a form over `max`; a key with no English entry or in the wrong
  file; `translate: false` text that differs from the English.
- A stored key deleted, unmarked or with its arguments changed.
- With `-check`: a source not in canonical form, a generated file out of date, missing, left over
  or holding draft output.

Pending work - missing, stale or unreviewed text - is a note for draft languages and a warning for
shipping ones; `-release` makes it an error for shipping languages and for those named with
`-lang`. Glossary findings are warnings; `-strict` makes them errors.

## Glossaries and never-translated words

`glossary/<code>.json`:

```json
{"code": "es",
 "terms": [{"en": "Collections", "text": "Colecciones", "note": "our word for image groups", "enforce": true}],
 "forbidden": [{"text": "álbum", "note": "we say colecciones"}]}
```

With `enforce`, whenever an English form contains the term (any case, as a whole word), its
translation must contain `text` (any case). A forbidden word (any case, as a whole word) must not
appear.

`glossary/never.json` lists words that must appear verbatim in a translation whenever the English
has them: `{"never": ["Off The Cloud", "Tailscale", ...]}`. The English must have the term as a
whole word, case included ("Mac" counts in "the Mac app", not in "Machine"); the translation may
make it part of a longer word (German "des iPhones"). Without the file the checker uses Off The
Cloud, Tailscale, GitHub, Google and Apple.

## Commands

```
make i18n                     canonicalize the sources, write every generated file
make i18n DRAFT=1             also the draft languages and the pseudo-locale - never commit this
make i18n DRAFT=1 LANGS=es    ... only Spanish among the drafts
make i18n-check               validate, compare the generated files, run the scanners'
                              ratchet, vet and build what embeds the catalog; writes nothing
make i18n-check I18N_RELEASE=1  ... missing, stale or unreviewed shipping text is an error
                              (what the release scripts run)
make i18n-check I18N_SCAN_FLAGS="-surface ios,android"  ... scan only these surfaces
                              (the web surface needs node and web/node_modules)
```

The scanners count hard-coded user-visible text per file and fail when a file's count rises
above `scan/baseline/<surface>.json`; after moving text into the catalog, lower your entries with
`go run ./i18n/cmd/i18nscan -update -path <your files>` in the same commit. See
[`scan/README.md`](scan/README.md).

`go run ./i18n/cmd/i18ngen [flags]`:

| flag | |
|---|---|
| `-check` | validate, and fail if a source isn't canonical or a committed generated file differs from what would be generated; writes nothing |
| `-draft` | also write draft languages and the pseudo-locale `qps` (accented, bracketed, padded English: untranslated text and tight layouts stand out). Every file then carries a marker that `-check` refuses: run `make i18n` before committing |
| `-release` | missing, stale or unreviewed text in shipping languages, and in languages named with `-lang`, is an error |
| `-strict` | glossary findings are errors |
| `-lang es,fr` | with `-check`: report only these languages (and English, which they depend on); with `-draft`: the draft languages to write |
| `-prefix app.photos` | with `-check`: report only these prefixes (`app` covers every `app.*` file) |
| `-v` | also print notes, and each language's coverage |
| `-root dir` | the repository root (default: found from the working directory) |

A narrowed check (`-lang`, `-prefix`) doesn't compare the generated files. A translator's check is
`go run ./i18n/cmd/i18ngen -check -release -lang es -prefix app.photos`.

Messages read `file:line: severity: key: message`. The exit code is 0 without errors, 1 with
errors, 2 for a usage error.

**Canonical form.** Keys sorted, one entry per key, fields in a fixed order (`text`, `args`,
`note`, `max`, `rich`, `stored`, `review`, `reviewed`, `translate`; `text`, `en`, `reviewed`),
defaults left out, plural forms one per line, two-space indent, characters written as they are,
a final newline. `make i18n` writes it; never fight it by hand.

**Stored keys.** Keys marked `"stored": true` are written to databases (alerts store their key and
arguments), so rows written long ago must still render: such a key can never be deleted, lose its
mark, or have its arguments renamed, retyped or reordered - add a new key instead. `make i18n`
records every stored key with its arguments in `stored.json` (generated, committed), and the
checker compares the sources with it.

## Writing an emitter

An emitter writes one platform's files. It lives in `cmd/i18ngen/emit_<platform>*.go` (package
`main`) and registers itself. A file name must not end in a GOOS or GOARCH suffix: Go silently
skips `emit_android.go` (hence `emit_android_res.go`).

```go
func init() { register(appleEmitter{}) }

type Emitter interface {
	Name() string                    // "apple"
	Consumers() []string             // the consumers it serves: model.ConsumerIOS, model.ConsumerMacOS
	Owns() []string                  // globs of the files it generates whole
	Emit(c *model.Catalog, opts Options) (map[string][]byte, error)
}

type Options struct {
	Root      string           // the repository root, to read a file it edits in part
	Draft     bool             // -draft: every file must contain DraftMarker
	Languages []model.Language // what to write, English first
}
```

The driver calls `Emit` only on a catalog without errors. `Emit` returns every file it generates
(repository-relative, slash-separated path -> content) and writes nothing: the driver writes the
files in generate mode, deletes files matching `Owns()` that `Emit` no longer returns (a removed
prefix, a draft language from an earlier `-draft` run), and in `-check` mode compares instead. A
file the emitter only edits in part (the wizard's block in `setup_wizard.py`) is read from
`opts.Root`, returned whole, and kept out of `Owns()`. In draft mode every returned file must
contain `DraftMarker` (in a comment, or a value the format ignores).

Output must be deterministic: sort everything, and depend only on the catalog and `opts`.

The catalog (`i18n/model`):

| | |
|---|---|
| `c.EntriesFor(consumer)`, `c.PrefixesFor(consumer)` | the keys and prefixes a consumer shows, through the routes |
| `c.EntriesIn(prefix)`, `c.Prefixes()`, `c.Entry(key)` | entries by file, every file, one key |
| `c.Messages(lang, prefix)`, `c.Resolve(lang, key)` | a key's text in a language, English filled in |
| `opts.Languages`, `c.OutputLanguages(draft, only)`, `c.Language(code)` | the languages; `model.PseudoLanguage` is the pseudo-locale |
| `c.Routes()`, `c.Serves(consumer, root)` | the routes |

An `Entry` has `Key`, `Root`, `Prefix`, `Text`, `Args` (`[]Arg{Name, Type}`, in order), `Note`,
`Max`, `Rich` (`IsRich()`), `Stored`, `Review`, `Translate`, and `CountArg()`/`ArgIndex(name)`. A
`Text` is `Plural`, `One`, `Other` (`Forms()` lists them; a text that isn't plural has the single
form `other`). `Resolved` embeds the entry and adds `Lang`, the `Text` to write and its `State`:

| state | text | |
|---|---|---|
| `StateSource` | English | the language is English, or `translate: false` |
| `StateTranslated` | translation | |
| `StateUnreviewed` | translation | a review-required key not reviewed yet (Apple: `needs_review`) |
| `StateMissing`, `StateStale` | English | `Fallback()` is true; `Translation` holds a stale entry |
| `StatePseudo` | pseudo-locale text | |

A `Language` has `Code`, `Tag` (for plural rules and formatters), `Apple`, `Android`, `Name`,
`Status`, `Pseudo`, and `OneMeansOne()`.

Rules every emitter follows (`docs/i18n.md`, "Rendering contract"):

- Split each form with `model.Tokenize` into literal, placeholder and tag tokens and work on the
  tokens; escape literals for the output format; never scan text or arguments for markup again.
  `Tokenize` returns an error for anything malformed: return it, never guess.
- Translations are data: never template source, never `template.HTML`. Write JSON with
  `encoding/json`, and inline dictionaries in HTML as `<script type="application/json">`.
- Name things with `model.AndroidName`, `CamelName`, `GoName`, `ArgName`, `PrefixFileName` and
  `PrefixTypeName`: the checker guarantees those never collide.
- Every language gets every key; `Resolve` already put English where a translation is missing or
  stale. The exception is the Go catalog and the wizard's dictionary, which hold only real
  translations: their runtimes fall back to English key by key, so English text never meets
  another language's plural rules.
- Plural rules come from `Language.Tag`, never from `Code`.

Tests sit next to the emitter in package `main`. `writeRepo(t, files)` in `main_test.go` builds a
repository from path -> content; `model.Load` reads it, and calling `Emit` directly (without
`Check`) is how a hostile catalog reaches an emitter: `</script><img onerror>`, `"""`, `{{.}}`,
`%s%n` and `$&` must come out inert, or as an error.
