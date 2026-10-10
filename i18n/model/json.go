// SPDX-License-Identifier: AGPL-3.0-or-later

package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"unicode/utf8"
)

// The catalog files are read through this small JSON tree instead of
// json.Unmarshal into structs, for three reasons: encoding/json keeps the
// last of two duplicate keys without a word (a translator pasting an entry
// twice would silently lose one), it can't say on which line a problem is,
// and it quietly replaces invalid UTF-8 with U+FFFD.

type nodeKind int

const (
	kindNull nodeKind = iota
	kindBool
	kindNumber
	kindString
	kindArray
	kindObject
)

func (k nodeKind) String() string {
	return [...]string{"null", "a boolean", "a number", "a string", "an array", "an object"}[k]
}

type node struct {
	kind nodeKind
	line int
	str  string // a string's value, or a number's literal
	b    bool
	arr  []*node
	obj  []member
}

type member struct {
	key  string
	line int
	val  *node
}

// jsonError is a syntax problem at a line of the file.
type jsonError struct {
	line int
	msg  string
}

func (e *jsonError) Error() string { return fmt.Sprintf("line %d: %s", e.line, e.msg) }

// lineIndex maps byte offsets to 1-based line numbers.
type lineIndex []int

func newLineIndex(data []byte) lineIndex {
	idx := lineIndex{0}
	for i, c := range data {
		if c == '\n' {
			idx = append(idx, i+1)
		}
	}
	return idx
}

func (l lineIndex) line(off int64) int {
	return sort.Search(len(l), func(i int) bool { return int64(l[i]) > off })
}

// parseJSON reads one JSON value, failing on invalid UTF-8, a byte order
// mark, duplicate object keys at any depth and anything after the value.
func parseJSON(data []byte) (*node, error) {
	if bytes.HasPrefix(data, []byte("\xef\xbb\xbf")) {
		return nil, &jsonError{1, "starts with a byte order mark"}
	}
	lines := newLineIndex(data)
	if !utf8.Valid(data) {
		for off := 0; off < len(data); {
			r, n := utf8.DecodeRune(data[off:])
			if r == utf8.RuneError && n <= 1 {
				return nil, &jsonError{lines.line(int64(off)), "invalid UTF-8"}
			}
			off += n
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	// The decoder's offset is just past the last token read, which is on
	// the token's own line for every token we report (keys and scalars
	// never span lines).
	here := func() int {
		off := dec.InputOffset() - 1
		if off < 0 {
			off = 0
		}
		return lines.line(off)
	}
	wrap := func(err error) error {
		var se *json.SyntaxError
		if errors.As(err, &se) {
			return &jsonError{lines.line(se.Offset), se.Error()}
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return &jsonError{len(lines), "unexpected end of file"}
		}
		return &jsonError{here(), err.Error()}
	}

	// The catalog's files nest four levels deep; anything far deeper is a
	// mistake, refused before the recursion below grows without bound.
	const maxDepth = 32
	depth := 0
	var value func(tok json.Token) (*node, error)
	value = func(tok json.Token) (*node, error) {
		n := &node{line: here()}
		if d, ok := tok.(json.Delim); ok && (d == '[' || d == '{') {
			if depth++; depth > maxDepth {
				return nil, &jsonError{n.line, "nested too deeply"}
			}
			defer func() { depth-- }()
		}
		switch t := tok.(type) {
		case nil:
			n.kind = kindNull
		case bool:
			n.kind, n.b = kindBool, t
		case json.Number:
			n.kind, n.str = kindNumber, string(t)
		case string:
			n.kind, n.str = kindString, t
		case json.Delim:
			switch t {
			case '[':
				n.kind = kindArray
				for dec.More() {
					tok, err := dec.Token()
					if err != nil {
						return nil, wrap(err)
					}
					v, err := value(tok)
					if err != nil {
						return nil, err
					}
					n.arr = append(n.arr, v)
				}
			case '{':
				n.kind = kindObject
				seen := map[string]int{}
				for dec.More() {
					tok, err := dec.Token()
					if err != nil {
						return nil, wrap(err)
					}
					key, _ := tok.(string)
					line := here()
					if first, dup := seen[key]; dup {
						return nil, &jsonError{line, fmt.Sprintf("duplicate key %q (first at line %d)", key, first)}
					}
					seen[key] = line
					tok, err = dec.Token()
					if err != nil {
						return nil, wrap(err)
					}
					v, err := value(tok)
					if err != nil {
						return nil, err
					}
					n.obj = append(n.obj, member{key: key, line: line, val: v})
				}
			}
			// The closing delimiter.
			if _, err := dec.Token(); err != nil {
				return nil, wrap(err)
			}
		}
		return n, nil
	}

	tok, err := dec.Token()
	if err != nil {
		return nil, wrap(err)
	}
	root, err := value(tok)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, &jsonError{here(), "unexpected data after the top-level value"}
	}
	return root, nil
}

// field returns an object's member by key.
func (n *node) field(key string) *node {
	for _, m := range n.obj {
		if m.key == key {
			return m.val
		}
	}
	return nil
}
