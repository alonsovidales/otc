// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestRepositoryCatalog checks the catalog actually committed: no errors
// or warnings, every source canonical, the stored-key record current and
// the routes covering every consumer.
func TestRepositoryCatalog(t *testing.T) {
	root := filepath.Join("..", "..")
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	wantClean(t, c.Check(CheckOptions{}))
	wantClean(t, c.NonCanonical(CheckOptions{}))
	have, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(StoredFile)))
	if err != nil {
		t.Fatalf("%s: %v (run make i18n)", StoredFile, err)
	}
	if !bytes.Equal(have, c.StoredRecord()) {
		t.Errorf("%s is out of date: run make i18n", StoredFile)
	}
	served := map[string]bool{}
	for _, cs := range c.Routes() {
		for _, x := range cs {
			served[x] = true
		}
	}
	for _, x := range Consumers {
		if !served[x] {
			t.Errorf("no root is routed to %s", x)
		}
	}
	if en, ok := c.Language(SourceCode); !ok || en.Status != Shipping {
		t.Error("English must ship")
	}
	if pt, _ := c.Language("pt"); pt.Tag != "pt-PT" || pt.Apple != "pt-PT" || pt.Android != "pt" {
		t.Errorf("pt must be European Portuguese everywhere: %+v", pt)
	}
	// common.* is shared by every client: no arguments, no tags.
	for _, e := range c.EntriesIn("common") {
		if len(e.Args) > 0 || len(e.Rich) > 0 {
			t.Errorf("%s: common keys take no arguments or tags", e.Key)
		}
	}
}
