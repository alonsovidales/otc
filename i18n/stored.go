// SPDX-License-Identifier: AGPL-3.0-or-later

package i18n

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Alerts are stored as a key and its arguments (notifications.msg_key and
// msg_args, the rows of msg_lines) next to the English written with them,
// and rendered again in each reader's language. A row may come from a
// newer release (a key this one doesn't have), or be damaged: whatever
// doesn't match the key's declaration exactly is refused here, and the
// caller shows the stored English as a whole - never a mix, never a
// literal "{name}".

var (
	// ErrUnknownKey is a key the catalog doesn't have.
	ErrUnknownKey = errors.New("i18n: unknown key")
	// ErrArgs is arguments that don't have exactly the names and types
	// the key declares.
	ErrArgs = errors.New("i18n: arguments don't match the key's declaration")
)

// Check reports whether m's key exists and its arguments have exactly the
// names and types the key declares. A msg argument must name a key without
// arguments, plural forms or tags.
func (m Msg) Check() error { return std.check(m.Key, m.Args) }

// MarshalArgs encodes m's arguments as msg_args stores them,
// {"<name>": value} with the names sorted: "{}" without arguments.
func (m Msg) MarshalArgs() ([]byte, error) {
	if len(m.Args) == 0 {
		return []byte("{}"), nil
	}
	return json.Marshal(m.Args)
}

// ParseStored rebuilds a stored message from its key and msg_args (empty
// or "null" for none). It fails with ErrUnknownKey or ErrArgs when they
// don't match the catalog: show the stored English then.
func ParseStored(key string, args []byte) (Msg, error) { return std.parseStored(key, args) }

// RenderStored renders a stored message in lang, or returns english, the
// text stored with it, as it is: when lang is empty, unknown or English (a
// request without lang gets exactly today's text), when there is no key,
// or when the key and arguments don't match the catalog any more.
func RenderStored(lang, key string, args []byte, english string) string {
	return std.renderStored(lang, key, args, english)
}

func (b *bundle) renderStored(lang, key string, args []byte, english string) string {
	code := b.normalize(lang)
	if key == "" || (english != "" && (code == "" || code == b.langs[0].Code)) {
		return english
	}
	m, err := b.parseStored(key, args)
	if err != nil {
		return english
	}
	return b.render(code, m.Key, m.Args)
}

func (b *bundle) parseStored(key string, raw []byte) (Msg, error) {
	en := b.table(0).msgs[key]
	if en == nil {
		return Msg{}, ErrUnknownKey
	}
	args := map[string]any{}
	if raw = bytes.TrimSpace(raw); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var m map[string]any
		if err := dec.Decode(&m); err != nil || m == nil {
			return Msg{}, ErrArgs
		}
		if _, err := dec.Token(); err != io.EOF {
			return Msg{}, ErrArgs
		}
		for name, v := range m {
			if d, ok := en.decl(name); ok && d.typ != typText && d.typ != typUser && d.typ != typMsg {
				n, ok := toInt(v)
				if !ok {
					return Msg{}, ErrArgs
				}
				v = n
			}
			args[name] = v
		}
	}
	if err := b.checkArgs(en, args); err != nil {
		return Msg{}, err
	}
	if len(args) == 0 {
		args = nil
	}
	return Msg{Key: key, Args: args}, nil
}

func (b *bundle) check(key string, args map[string]any) error {
	en := b.table(0).msgs[key]
	if en == nil {
		return ErrUnknownKey
	}
	return b.checkArgs(en, args)
}

// checkArgs compares arguments with an English entry's declarations. The
// errors name arguments, never their values.
func (b *bundle) checkArgs(en *entry, args map[string]any) error {
	if len(args) != len(en.args) {
		for name := range args {
			if _, ok := en.decl(name); !ok {
				return fmt.Errorf("%w: no argument %q", ErrArgs, name)
			}
		}
	}
	for _, d := range en.args {
		v, ok := args[d.name]
		if !ok {
			return fmt.Errorf("%w: %s is missing", ErrArgs, d.name)
		}
		switch d.typ {
		case typText, typUser:
			_, ok = v.(string)
		case typMsg:
			var s string
			if s, ok = v.(string); ok {
				_, ok = b.sub(0, s)
			}
		default:
			_, ok = toInt(v)
		}
		if !ok {
			return fmt.Errorf("%w: %s is not a %s", ErrArgs, d.name, d.typ)
		}
	}
	return nil
}
