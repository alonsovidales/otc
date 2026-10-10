// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
)

// The canonical form of a strings file: keys sorted bytewise, one entry
// per key, fields in a fixed order (text, args, note, max, rich, stored,
// review, reviewed, translate; or text, en, reviewed), fields at their
// default left out, plural texts with one form per line, two-space
// indent, UTF-8 written as is (no \u escapes, "<" and ">" included), and
// a final newline. make i18n rewrites every source file in this form so
// diffs show only real changes, and -check fails on a file that isn't.

// Canonical returns the file's content in canonical form. A reviewed value
// written as "<who> <date>" gets the fingerprint of the text it covers
// appended, which is how a reviewer records a review (see Fingerprint).
func (f *SourceFile) Canonical() []byte {
	var b bytes.Buffer
	b.WriteString("{")
	if f.IsSource() {
		entries := append([]*Entry(nil), f.Entries...)
		sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
		for i, e := range entries {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString("\n  ")
			writeString(&b, e.Key)
			b.WriteString(": {")
			writeEntryFields(&b, e)
			b.WriteString("\n  }")
		}
	} else {
		ts := append([]*Translation(nil), f.Translations...)
		sort.Slice(ts, func(i, j int) bool { return ts[i].Key < ts[j].Key })
		for i, t := range ts {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString("\n  ")
			writeString(&b, t.Key)
			b.WriteString(": {")
			field(&b, true, "text")
			writeText(&b, t.Text)
			field(&b, false, "en")
			writeText(&b, t.EN)
			if t.Reviewed != "" {
				field(&b, false, "reviewed")
				writeString(&b, withFingerprint(t.Reviewed, Fingerprint(t.Text, t.EN)))
			}
			b.WriteString("\n  }")
		}
	}
	if b.Len() > 1 {
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	return b.Bytes()
}

func writeEntryFields(b *bytes.Buffer, e *Entry) {
	field(b, true, "text")
	writeText(b, e.Text)
	if len(e.Args) > 0 {
		field(b, false, "args")
		b.WriteString("[")
		for i, a := range e.Args {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("[")
			writeString(b, a.Name)
			b.WriteString(", ")
			writeString(b, string(a.Type))
			b.WriteString("]")
		}
		b.WriteString("]")
	}
	if e.Note != "" {
		field(b, false, "note")
		writeString(b, e.Note)
	}
	if e.Max > 0 {
		field(b, false, "max")
		b.WriteString(strconv.Itoa(e.Max))
	}
	if len(e.Rich) > 0 {
		field(b, false, "rich")
		b.WriteString("[")
		for i, t := range e.Rich {
			if i > 0 {
				b.WriteString(", ")
			}
			writeString(b, t)
		}
		b.WriteString("]")
	}
	if e.Stored {
		field(b, false, "stored")
		b.WriteString("true")
	}
	if e.Review != "" {
		field(b, false, "review")
		writeString(b, e.Review)
	}
	if e.Reviewed != "" {
		field(b, false, "reviewed")
		writeString(b, withFingerprint(e.Reviewed, Fingerprint(e.Text)))
	}
	if !e.Translate {
		field(b, false, "translate")
		b.WriteString("false")
	}
}

func field(b *bytes.Buffer, first bool, name string) {
	if !first {
		b.WriteString(",")
	}
	b.WriteString("\n    ")
	writeString(b, name)
	b.WriteString(": ")
}

func writeText(b *bytes.Buffer, t Text) {
	if !t.Plural {
		writeString(b, t.Other)
		return
	}
	b.WriteString("{\n      \"one\": ")
	writeString(b, t.One)
	b.WriteString(",\n      \"other\": ")
	writeString(b, t.Other)
	b.WriteString("\n    }")
}

// writeString writes s as a JSON string without HTML escaping, so tags
// stay readable in the sources.
func writeString(b *bytes.Buffer, s string) {
	var tmp bytes.Buffer
	enc := json.NewEncoder(&tmp)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a string always encodes
	b.Write(bytes.TrimSuffix(tmp.Bytes(), []byte("\n")))
}
