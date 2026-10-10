// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/alonsovidales/otc/i18n/model"
)

// execute runs one generate or check pass and returns the exit code.
func execute(cfg config, stdout, stderr io.Writer) int {
	cat, err := model.Load(cfg.root)
	if err != nil {
		fmt.Fprintf(stderr, "i18ngen: %v\n", err)
		return 1
	}
	for _, code := range cfg.langs {
		if _, ok := cat.Language(code); !ok || code == model.PseudoLanguage.Code {
			fmt.Fprintf(stderr, "i18ngen: -lang %s: not a language in %s\n", code, model.LanguagesFile)
			return 2
		}
	}
	opts := model.CheckOptions{Release: cfg.release, Strict: cfg.strict, Langs: cfg.langs, Prefixes: cfg.prefixes}

	rewritten := 0
	if !cfg.check {
		// Sources first, then a fresh load, so that every line number
		// printed below is a line of the rewritten file.
		if rewritten, err = writeCanonical(cat); err != nil {
			fmt.Fprintf(stderr, "i18ngen: %v\n", err)
			return 1
		}
		if rewritten > 0 {
			if cat, err = model.Load(cfg.root); err != nil {
				fmt.Fprintf(stderr, "i18ngen: %v\n", err)
				return 1
			}
		}
	}

	ds := cat.Check(opts)
	if cfg.check {
		ds = append(ds, cat.NonCanonical(opts)...)
	}
	filtered := len(cfg.langs) > 0 || len(cfg.prefixes) > 0
	var written, removed, generated int
	if model.Count(ds, model.Error) == 0 {
		langs := cat.OutputLanguages(cfg.draft, cfg.langs)
		out, owned, eds := emitAll(cat, cfg, langs)
		ds = append(ds, eds...)
		generated = len(out)
		switch {
		case model.Count(ds, model.Error) > 0:
		case cfg.check && filtered:
			// A translator's narrowed check isn't stopped by someone
			// else's out-of-date output; the full check compares.
		case cfg.check:
			ds = append(ds, compareOutputs(cfg.root, out, owned)...)
		default:
			if written, removed, err = writeOutputs(cfg.root, out, owned); err != nil {
				fmt.Fprintf(stderr, "i18ngen: %v\n", err)
				return 1
			}
		}
	}

	showInfo := cfg.verbose || len(cfg.langs) > 0
	hidden := 0
	for _, d := range ds {
		if d.Severity == model.Info && !showInfo {
			hidden++
			continue
		}
		fmt.Fprintln(stderr, d.String())
	}
	if showInfo {
		for _, l := range cat.AllLanguages() {
			if l.IsSource() || (len(cfg.langs) > 0 && !slices.Contains(cfg.langs, l.Code)) {
				continue
			}
			fmt.Fprintf(stdout, "i18n: %s\n", coverageLine(cat, l))
		}
	}

	errs, warns := model.Count(ds, model.Error), model.Count(ds, model.Warning)
	var b strings.Builder
	fmt.Fprintf(&b, "i18n: %s in %s", count(len(cat.Entries()), "key"), count(len(cat.Prefixes()), "prefix"))
	switch {
	case errs > 0:
	case cfg.check && filtered:
		b.WriteString("; generated files not compared while -lang or -prefix narrows the check")
	case cfg.check:
		fmt.Fprintf(&b, "; %s checked", count(generated, "generated file"))
	default:
		fmt.Fprintf(&b, "; %s rewritten, %s written, %d removed", count(rewritten, "source file"), count(written, "generated file"), removed)
	}
	fmt.Fprintf(&b, "; %s, %s", count(errs, "error"), count(warns, "warning"))
	if hidden > 0 {
		fmt.Fprintf(&b, " (%s, -v shows them)", count(hidden, "note"))
	}
	if cfg.draft && errs == 0 && !cfg.check {
		b.WriteString("; DRAFT output: never commit it, run make i18n to go back")
	}
	fmt.Fprintln(stdout, b.String())
	if errs > 0 {
		return 1
	}
	return 0
}

// count writes "1 key" or "2 keys".
func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	if strings.HasSuffix(noun, "x") {
		return fmt.Sprintf("%d %ses", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// coverageLine summarises how much of a language is translated.
func coverageLine(cat *model.Catalog, l model.Language) string {
	cov := cat.Coverage(l)
	total := 0
	for _, n := range cov {
		total += n
	}
	s := fmt.Sprintf("%s (%s): %d/%d translated", l.Code, l.Status, cov[model.StateTranslated]+cov[model.StateUnreviewed], total-cov[model.StateSource])
	for _, st := range []model.State{model.StateMissing, model.StateStale, model.StateUnreviewed} {
		if cov[st] > 0 {
			s += fmt.Sprintf(", %d %s", cov[st], st)
		}
	}
	return s
}

// writeCanonical rewrites every readable source file that isn't in its
// canonical form, and returns how many it rewrote.
func writeCanonical(cat *model.Catalog) (int, error) {
	n := 0
	for _, f := range cat.Files() {
		if f.Broken {
			continue
		}
		if c := f.Canonical(); !bytes.Equal(c, f.Raw) {
			if err := writeFile(cat.Root(), f.Path, c); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// emitAll runs every emitter and returns their files with the catalog's
// own output (the stored-key record), plus every existing file the
// emitters own. Problems come back as error diagnostics.
func emitAll(cat *model.Catalog, cfg config, langs []model.Language) (map[string][]byte, map[string]string, []model.Diagnostic) {
	var ds []model.Diagnostic
	bad := func(file, format string, a ...any) {
		ds = append(ds, model.Diagnostic{Severity: model.Error, File: file, Msg: fmt.Sprintf(format, a...)})
	}
	out := map[string][]byte{model.StoredFile: cat.StoredRecord()}
	from := map[string]string{model.StoredFile: "i18ngen"}
	owned := map[string]string{} // existing owned file -> emitter
	served := map[string]bool{}
	for _, e := range emitters() {
		for _, c := range e.Consumers() {
			served[c] = true
		}
		files, err := e.Emit(cat, Options{Root: cfg.root, Draft: cfg.draft, Languages: langs})
		if err != nil {
			bad("", "emitter %s: %v", e.Name(), err)
			continue
		}
		paths := make([]string, 0, len(files))
		for p := range files {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			if err := checkOutputPath(p); err != nil {
				bad("", "emitter %s: %v", e.Name(), err)
				continue
			}
			if other, dup := from[p]; dup {
				bad(p, "generated by both %s and %s", other, e.Name())
				continue
			}
			if cfg.draft && !bytes.Contains(files[p], []byte(DraftMarker)) {
				bad(p, "emitter %s wrote draft output without DraftMarker", e.Name())
				continue
			}
			out[p], from[p] = files[p], e.Name()
		}
		for _, pat := range e.Owns() {
			matches, err := filepath.Glob(filepath.Join(cfg.root, filepath.FromSlash(pat)))
			if err != nil {
				bad("", "emitter %s: bad pattern %q: %v", e.Name(), pat, err)
				continue
			}
			for _, m := range matches {
				if rel, err := filepath.Rel(cfg.root, m); err == nil {
					owned[filepath.ToSlash(rel)] = e.Name()
				}
			}
		}
	}
	var unserved []string
	for _, c := range model.Consumers {
		if !served[c] {
			unserved = append(unserved, c)
		}
	}
	if len(unserved) > 0 {
		ds = append(ds, model.Diagnostic{Severity: model.Info, Msg: "no emitter registered for: " + strings.Join(unserved, ", ")})
	}
	return out, owned, ds
}

// compareOutputs reports every generated file that differs from what is
// committed, every owned file no longer generated, and any draft output.
func compareOutputs(root string, out map[string][]byte, owned map[string]string) []model.Diagnostic {
	var ds []model.Diagnostic
	bad := func(file, msg string) {
		ds = append(ds, model.Diagnostic{Severity: model.Error, File: file, Msg: msg})
	}
	paths := make([]string, 0, len(out))
	for p := range out {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		have, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			bad(p, "generated file is missing: run make i18n and commit it")
		case err != nil:
			bad(p, err.Error())
		case bytes.Contains(have, []byte(DraftMarker)):
			bad(p, "holds draft output (make i18n DRAFT=1): run make i18n before committing")
		case !bytes.Equal(have, out[p]):
			bad(p, "generated file is out of date: run make i18n and commit it")
		}
	}
	var extra []string
	for p := range owned {
		if _, ok := out[p]; !ok {
			extra = append(extra, p)
		}
	}
	sort.Strings(extra)
	for _, p := range extra {
		have, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		if bytes.Contains(have, []byte(DraftMarker)) {
			bad(p, "holds draft output (make i18n DRAFT=1): run make i18n before committing")
		} else {
			bad(p, "no longer generated (by "+owned[p]+"): run make i18n, which deletes it")
		}
	}
	return ds
}

// writeOutputs writes the generated files that changed and deletes owned
// files no longer generated.
func writeOutputs(root string, out map[string][]byte, owned map[string]string) (written, removed int, err error) {
	paths := make([]string, 0, len(out))
	for p := range out {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		have, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		if err == nil && bytes.Equal(have, out[p]) {
			continue
		}
		if err := writeFile(root, p, out[p]); err != nil {
			return written, removed, err
		}
		written++
	}
	for p := range owned {
		if _, ok := out[p]; ok {
			continue
		}
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(p))); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return written, removed, err
		}
		removed++
	}
	return written, removed, nil
}

// writeFile replaces a repository file through a temporary file and a
// rename, so an interrupted run never leaves half a file.
func writeFile(root, rel string, data []byte) error {
	dst := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".i18ngen-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}
