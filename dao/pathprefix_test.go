// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"reflect"
	"testing"
)

// Issue #173: a folder name is matched as itself - LIKE's wildcards in it
// are escaped - and "direct" leaves out its sub-folders.
func TestUnderPrefix(t *testing.T) {
	cond, args := underPrefix("`path`", "/a_b%c\\ (2020)/", true)
	if want := "`path` >= ? and `path` < concat(?, _utf8mb4 X'F48FBFBF') and `path` like ? collate utf8mb4_bin and `path` not like ? collate utf8mb4_bin"; cond != want {
		t.Errorf("cond = %q", cond)
	}
	want := []any{"/a_b%c\\ (2020)/", "/a_b%c\\ (2020)/", `/a\_b\%c\\ (2020)/%`, `/a\_b\%c\\ (2020)/%/%`}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("args = %q, want %q", args, want)
	}
	if cond, args := underPrefix("`path`", "/x/", false); len(args) != 3 || cond == "" {
		t.Errorf("recursive: %q %v", cond, args)
	}
}
