// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/alonsovidales/otc/i18n/model"
)

// An Emitter writes one platform's generated files from the catalog. Each
// lives in this package, in emit_<platform>*.go, and registers itself from
// an init function:
//
//	func init() { register(appleEmitter{}) }
//
// The driver calls Emit only on a catalog Check found no errors in, with
// the languages to write in opts. Emit is pure: it returns every file it
// generates (repository-relative, slash-separated path -> content) and
// writes nothing; the driver writes them in generate mode, and in -check
// mode compares them with the committed files.
type Emitter interface {
	// Name identifies the emitter in messages ("apple").
	Name() string
	// Consumers are the routes' consumers this emitter serves
	// (model.ConsumerIOS, ...): it writes the keys of every root routed to
	// them, which it gets from Catalog.PrefixesFor / EntriesFor.
	Consumers() []string
	// Owns lists glob patterns (path.Match syntax, repository-relative)
	// matching every file this emitter generates whole. A file matching
	// one that Emit didn't return is a leftover - a prefix that was
	// removed, a draft language from an earlier -draft run - which
	// generate deletes and -check reports. A file the emitter only edits
	// in part (the wizard's generated block in setup_wizard.py) must not
	// match.
	Owns() []string
	// Emit returns the generated files. In draft mode (opts.Draft) every
	// file returned must contain DraftMarker - in a comment, or as a value
	// the format ignores - so that -check refuses it if it is committed.
	Emit(c *model.Catalog, opts Options) (map[string][]byte, error)
}

// Options are what an emitter is asked to write.
type Options struct {
	// Root is the repository root, for an emitter that rewrites part of an
	// existing file (it reads the file, and returns it whole).
	Root string
	// Draft is set by -draft: Languages then holds draft languages and the
	// pseudo-locale too, and the output must never be committed.
	Draft bool
	// Languages are the languages to write, English first: the shipping
	// ones, plus with Draft the draft ones and model.PseudoLanguage.
	// Every language gets every key (Catalog.Resolve fills in English).
	Languages []model.Language
}

// DraftMarker must appear in every file an emitter writes in draft mode.
// -check refuses any generated file that contains it.
const DraftMarker = "OTC-I18N-DRAFT-OUTPUT-DO-NOT-COMMIT"

// registry holds the emitters, by name.
var registry = map[string]Emitter{}

// register adds an emitter; a second one with the same name is a bug.
func register(e Emitter) {
	if _, dup := registry[e.Name()]; dup {
		panic("i18ngen: emitter " + e.Name() + " registered twice")
	}
	for _, c := range e.Consumers() {
		if !slices.Contains(model.Consumers, c) {
			panic(fmt.Sprintf("i18ngen: emitter %s serves unknown consumer %q", e.Name(), c))
		}
	}
	registry[e.Name()] = e
}

// emitters returns the registered emitters sorted by name.
func emitters() []Emitter {
	out := make([]Emitter, 0, len(registry))
	for _, e := range registry {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// checkOutputPath refuses a generated path outside the repository or
// inside the catalog's own sources.
func checkOutputPath(p string) error {
	switch {
	case p == "" || path.IsAbs(p) || strings.Contains(p, "\\"):
		return fmt.Errorf("%q is not a relative slash-separated path", p)
	case path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../"):
		return fmt.Errorf("%q is not a clean path inside the repository", p)
	case p == model.LanguagesFile || p == model.StoredFile ||
		strings.HasPrefix(p, model.StringsDir+"/") || strings.HasPrefix(p, model.GlossaryDir+"/"):
		return fmt.Errorf("%q is a catalog source", p)
	}
	return nil
}
