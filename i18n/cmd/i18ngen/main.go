// SPDX-License-Identifier: AGPL-3.0-or-later

// Command i18ngen is Off The Cloud's translation generator and checker
// (docs/i18n.md, i18n/README.md).
//
//	go run ./i18n/cmd/i18ngen            # make i18n: canonicalize sources, write generated files
//	go run ./i18n/cmd/i18ngen -check     # make i18n-check: validate, and compare generated files
//
// Flags:
//
//	-check       validate everything and fail if a source isn't canonical or a
//	             committed generated file differs from what would be generated
//	-draft       also write draft languages and the pseudo-locale (local testing
//	             only: the files carry a marker that -check refuses)
//	-release     missing, stale or unreviewed text in a shipping language (or one
//	             named with -lang) is an error
//	-strict      glossary findings are errors
//	-lang a,b    with -check: report only these languages; with -draft: the draft
//	             languages to write
//	-prefix a,b  with -check: report only these prefixes ("app" covers "app.photos")
//	-v           also print notes (missing and stale text in draft languages)
//	-root dir    the repository root (default: found from the working directory)
//
// It exits 0 when there is no error, 1 when there is, 2 on a usage error.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/alonsovidales/otc/i18n/model"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// config is a parsed command line.
type config struct {
	root     string
	check    bool
	draft    bool
	release  bool
	strict   bool
	verbose  bool
	langs    []string
	prefixes []string
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("i18ngen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cfg config
	var langs, prefixes string
	fs.BoolVar(&cfg.check, "check", false, "validate and compare generated files instead of writing them")
	fs.BoolVar(&cfg.draft, "draft", false, "also write draft languages and the pseudo-locale (never commit the result)")
	fs.BoolVar(&cfg.release, "release", false, "missing, stale or unreviewed text in shipping languages (and -lang ones) is an error")
	fs.BoolVar(&cfg.strict, "strict", false, "glossary findings are errors")
	fs.BoolVar(&cfg.verbose, "v", false, "also print notes")
	fs.StringVar(&langs, "lang", "", "comma-separated language codes")
	fs.StringVar(&prefixes, "prefix", "", "comma-separated prefixes (with -check)")
	fs.StringVar(&cfg.root, "root", "", "repository root (default: found from the working directory)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "i18ngen: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	cfg.langs, cfg.prefixes = splitList(langs), splitList(prefixes)
	switch {
	case cfg.check && cfg.draft:
		fmt.Fprintln(stderr, "i18ngen: -check compares the committed (shipping) output; -draft output is never committed")
		return 2
	case !cfg.check && len(cfg.prefixes) > 0:
		fmt.Fprintln(stderr, "i18ngen: -prefix only narrows -check; generation always covers the whole catalog")
		return 2
	case !cfg.check && !cfg.draft && len(cfg.langs) > 0:
		fmt.Fprintln(stderr, "i18ngen: -lang needs -check or -draft; generation always writes every shipping language")
		return 2
	}
	if cfg.root == "" {
		root, err := findRoot()
		if err != nil {
			fmt.Fprintf(stderr, "i18ngen: %v\n", err)
			return 2
		}
		cfg.root = root
	}
	for _, code := range cfg.langs {
		if code == model.SourceCode {
			fmt.Fprintln(stderr, "i18ngen: -lang takes translated languages; English is always checked")
			return 2
		}
	}
	return execute(cfg, stdout, stderr)
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// findRoot walks up from the working directory to the directory holding
// i18n/languages.json.
func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(model.LanguagesFile))); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no " + model.LanguagesFile + " here or above; run from the repository or pass -root")
		}
		dir = parent
	}
}
