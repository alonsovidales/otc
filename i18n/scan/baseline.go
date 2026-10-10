// SPDX-License-Identifier: AGPL-3.0-or-later

package scan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// BaselineDir holds one committed file per surface, <surface>.json, with
// the count of hard-coded texts per file. The check fails when a file's
// count rises above it; -update writes the new, lower counts.
const BaselineDir = "i18n/scan/baseline"

// Baseline is one surface's committed counts.
type Baseline struct {
	Surface string         `json:"surface"`
	Total   int            `json:"total"`
	Files   map[string]int `json:"files"`
}

// BaselineFile is the repository-relative path of a surface's baseline.
func BaselineFile(surface string) string { return path.Join(BaselineDir, surface+".json") }

// LoadBaseline reads a surface's baseline; ok is false when there is none.
func LoadBaseline(root, surface string) (b *Baseline, ok bool, err error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(BaselineFile(surface))))
	if errors.Is(err, fs.ErrNotExist) {
		return &Baseline{Surface: surface, Files: map[string]int{}}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	b = &Baseline{}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(b); err != nil {
		return nil, false, fmt.Errorf("%s: %w", BaselineFile(surface), err)
	}
	if b.Surface != surface {
		return nil, false, fmt.Errorf("%s: \"surface\" is %q", BaselineFile(surface), b.Surface)
	}
	if b.Files == nil {
		b.Files = map[string]int{}
	}
	for f, n := range b.Files {
		if n <= 0 {
			return nil, false, fmt.Errorf("%s: %s: a count must be positive (files at zero are left out)", BaselineFile(surface), f)
		}
	}
	return b, true, nil
}

// Encode returns the baseline's canonical form: sorted keys, two-space
// indent, the total recomputed, a final newline.
func (b *Baseline) Encode() []byte {
	total := 0
	files := map[string]int{}
	for f, n := range b.Files {
		if n > 0 {
			files[f] = n
			total += n
		}
	}
	out := struct {
		Surface string         `json:"surface"`
		Total   int            `json:"total"`
		Files   map[string]int `json:"files"`
	}{b.Surface, total, files}
	data, _ := json.MarshalIndent(out, "", "  ")
	return append(data, '\n')
}

// Write saves the baseline atomically.
func (b *Baseline) Write(root string) error {
	file := filepath.Join(root, filepath.FromSlash(BaselineFile(b.Surface)))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), "."+b.Surface+"-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b.Encode()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}

// Change is one file's count against the baseline.
type Change struct {
	File     string
	Was, Now int
}

// Comparison of a surface's counts with its baseline, restricted to the
// files a filter keeps.
type Comparison struct {
	Rose []Change // above the baseline (a file the baseline lacks counts from 0)
	Fell []Change // below it, including files gone or at zero
}

// PathFilter keeps repository-relative files under any of the prefixes (a
// file or a directory); no prefixes keep everything.
func PathFilter(prefixes []string) func(string) bool {
	var clean []string
	for _, p := range prefixes {
		p = strings.TrimSuffix(strings.TrimPrefix(filepath.ToSlash(p), "./"), "/")
		if p != "" && p != "." {
			clean = append(clean, p)
		}
	}
	return func(rel string) bool {
		if len(clean) == 0 {
			return true
		}
		for _, p := range clean {
			if rel == p || strings.HasPrefix(rel, p+"/") {
				return true
			}
		}
		return false
	}
}

// Compare compares counts with the baseline for the files keep accepts.
func Compare(b *Baseline, counts map[string]int, keep func(string) bool) Comparison {
	var c Comparison
	seen := map[string]bool{}
	for f, now := range counts {
		if !keep(f) {
			continue
		}
		seen[f] = true
		was := b.Files[f]
		switch {
		case now > was:
			c.Rose = append(c.Rose, Change{f, was, now})
		case now < was:
			c.Fell = append(c.Fell, Change{f, was, now})
		}
	}
	for f, was := range b.Files {
		if keep(f) && !seen[f] {
			c.Fell = append(c.Fell, Change{f, was, 0})
		}
	}
	byFile := func(cs []Change) {
		sort.Slice(cs, func(i, j int) bool { return cs[i].File < cs[j].File })
	}
	byFile(c.Rose)
	byFile(c.Fell)
	return c
}

// Updated returns the baseline with the files keep accepts set to counts and
// every other file as it was.
func Updated(b *Baseline, counts map[string]int, keep func(string) bool) *Baseline {
	out := &Baseline{Surface: b.Surface, Files: map[string]int{}}
	for f, n := range b.Files {
		if !keep(f) {
			out.Files[f] = n
		}
	}
	for f, n := range counts {
		if keep(f) && n > 0 {
			out.Files[f] = n
		}
	}
	return out
}
