// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"bytes"
	"errors"
	"io/fs"
	"slices"
	"sort"
)

// Keys marked "stored": true are written to a database (alert keys in
// notifications.msg_key, with their arguments in msg_args), so rows written
// years ago must still render: such a key can never be deleted, lose its
// mark, or have its arguments renamed, retyped or reordered. i18n/stored.json
// is the record that makes this checkable: make i18n writes every stored
// key with its arguments there, and Check refuses a source that no longer
// matches it. The file is generated and committed like every other output.
//
//	{
//	  "dev.alert.photo_failed": [["name", "user"]]
//	}

func (c *Catalog) loadStored() {
	data, err := c.readFile(StoredFile)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		c.diag(StoredFile, 0, "", "", "", "%v", err)
		return
	}
	c.storedSet = true
	root, err := parseJSON(data)
	if err != nil {
		je := err.(*jsonError)
		c.diag(StoredFile, je.line, "", "", "", "%s", je.msg)
		return
	}
	if root.kind != kindObject {
		c.diag(StoredFile, root.line, "", "", "", "must be an object of key -> arguments")
		return
	}
	for _, m := range root.obj {
		var args []Arg
		ok := m.val.kind == kindArray
		if ok {
			for _, a := range m.val.arr {
				if a.kind != kindArray || len(a.arr) != 2 || a.arr[0].kind != kindString || a.arr[1].kind != kindString {
					ok = false
					break
				}
				args = append(args, Arg{a.arr[0].str, ArgType(a.arr[1].str)})
			}
		}
		if !ok {
			c.diag(StoredFile, m.line, m.key, "", "", "the arguments must be [[\"name\", \"type\"], ...]")
			continue
		}
		c.stored[m.key] = args
	}
}

// StoredRecord returns the content StoredFile should have: every key the
// record already holds (Check fails while one is gone from the sources)
// and every key now marked stored.
func (c *Catalog) StoredRecord() []byte {
	rec := map[string][]Arg{}
	for k, a := range c.stored {
		rec[k] = a
	}
	for _, e := range c.entries {
		if e.Stored {
			rec[e.Key] = e.Args
		}
	}
	keys := make([]string, 0, len(rec))
	for k := range rec {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	b.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n  ")
		writeString(&b, k)
		b.WriteString(": [")
		for j, a := range rec[k] {
			if j > 0 {
				b.WriteString(", ")
			}
			b.WriteString("[")
			writeString(&b, a.Name)
			b.WriteString(", ")
			writeString(&b, string(a.Type))
			b.WriteString("]")
		}
		b.WriteString("]")
	}
	if len(keys) > 0 {
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	return b.Bytes()
}

// checkStored compares the sources with the stored-key record.
func (c *Catalog) checkStored(add func(Diagnostic)) {
	keys := make([]string, 0, len(c.stored))
	for k := range c.stored {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args := c.stored[k]
		e := c.entries[k]
		switch {
		case e == nil:
			add(Diagnostic{File: StoredFile, Key: k, Msg: "a stored key was deleted: databases still hold it, so it must stay in the English sources"})
		case !e.Stored:
			add(Diagnostic{File: e.File, Line: e.Line, Key: k, Prefix: e.Prefix, Msg: "a stored key must keep \"stored\": true (" + StoredFile + " records it)"})
		case !slices.Equal(e.Args, args):
			add(Diagnostic{File: e.File, Line: e.Line, Key: k, Prefix: e.Prefix, Msg: "a stored key's arguments can never change: " + StoredFile + " records " + argsString(args) + ", the source has " + argsString(e.Args) + " (add a new key instead)"})
		}
	}
}

func argsString(args []Arg) string {
	var b bytes.Buffer
	b.WriteString("[")
	for i, a := range args {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(a.Name + " " + string(a.Type))
	}
	b.WriteString("]")
	return b.String()
}
