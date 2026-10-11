// SPDX-License-Identifier: AGPL-3.0-or-later

package flasher

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/alonsovidales/otc/i18n"
)

// The wizard's words (docs/i18n.md): the tray shows them in the user's
// language, the command line (`otc-sync flash`) in English. Each text has
// an English form - the one Label, Step, Error and NextSteps give, byte for
// byte what they always said - and a form in a language (LabelIn, StepIn,
// Msg) for the tray.

// msgError is a failure of ours. Error is its English, for the command
// line and the status file's "error"; Msg is the same words as a catalog
// message, which the tray renders in its language (tray.errorText). err is
// the system's error under it, if any.
type msgError struct {
	english string
	m       i18n.Msg
	err     error
}

func (e *msgError) Error() string { return e.english }

// Msg is the message, for the tray to render in its language.
func (e *msgError) Msg() i18n.Msg { return e.m }

func (e *msgError) Unwrap() error { return e.err }

// failure is the error m words; its English is m's.
func failure(m i18n.Msg) error { return &msgError{english: m.Render(""), m: m} }

// wrapped is failure with the system's error under it (m's {detail}).
func wrapped(m i18n.Msg, err error) error { return &msgError{english: m.Render(""), m: m, err: err} }

// failureIn is failure for words whose arguments are worded too (a disk's
// label, a size, an error of ours): msg makes the message with its
// arguments in a language - English for Error, lang for Msg.
func failureIn(lang string, msg func(lang string) i18n.Msg, err error) error {
	return &msgError{english: msg("").Render(""), m: msg(lang), err: err}
}

// textIn is err in lang: worded by its message when it is one of ours, as
// it comes otherwise (the system's own words).
func textIn(lang string, err error) string {
	var m interface{ Msg() i18n.Msg }
	if errors.As(err, &m) {
		return m.Msg().Render(lang)
	}

	return err.Error()
}

// isEnglish reports whether lang is English, or no language at all: what
// the command line asks for.
func isEnglish(lang string) bool {
	l := i18n.Normalize(lang)

	return l == "" || l == "en"
}

// HumanSizeIn is HumanSize in lang: in English the same "%.1f GB" it has
// always been; in another language the runtime's size format for it.
func HumanSizeIn(lang string, n int64) string {
	if isEnglish(lang) {
		return HumanSize(n)
	}

	return i18n.FormatBytes(lang, n)
}

// LabelIn is Label in lang: the disk's name (or "Unnamed disk"), its size
// and its short name.
func (d Disk) LabelIn(lang string) string {
	name := d.Name
	if name == "" {
		name = i18n.DeskFoldersFlashUnnamedDisk().Render(lang)
	}

	return fmt.Sprintf("%s · %s (%s)", name, HumanSizeIn(lang, d.Size), d.ShortIDIn(lang))
}

// ShortIDIn is ShortID in lang: sdb, disk 2.
func (d Disk) ShortIDIn(lang string) string {
	if n, ok := strings.CutPrefix(d.ID, `\\.\PhysicalDrive`); ok {
		return i18n.DeskFoldersFlashDiskNumber(n).Render(lang)
	}

	return filepath.Base(d.ID)
}

// StepIn is Step in lang.
func (p Progress) StepIn(lang string) string {
	var m i18n.Msg
	switch p.Phase {
	case "check":
		m = i18n.DeskFoldersFlashStepCheck()
	case "prepare":
		m = i18n.DeskFoldersFlashStepPrepare()
	case "write":
		m = i18n.DeskFoldersFlashStepWrite()
	case "verify":
		m = i18n.DeskFoldersFlashStepVerify()
	case "eject":
		m = i18n.DeskFoldersFlashStepEject()
	case "done":
		m = i18n.DeskFoldersFlashStepDone()
	default:
		return p.Phase // a phase this build doesn't know, as the writer said it
	}

	return m.Render(lang)
}

// NextStepsIn is NextSteps in lang.
func NextStepsIn(lang string) string { return i18n.DeskFoldersFlashNextSteps().Render(lang) }

// errorProgress is the status file's last line for err: its English, and
// its words as a catalog message when it is one of ours (the arguments as
// the writer worded them, in the language it was given), for the tray to
// show it in that language.
func errorProgress(err error) Progress {
	p := Progress{Phase: "error", Error: err.Error()}
	var m interface{ Msg() i18n.Msg }
	if errors.As(err, &m) {
		msg := m.Msg()
		if args, aerr := msg.MarshalArgs(); aerr == nil {
			p.ErrorKey, p.ErrorArgs = msg.Key, json.RawMessage(args)
		}
	}

	return p
}

// failed is an error line's error: its English, with its message when it
// carries one this build knows (i18n.ParseStored checks the key and its
// arguments' names and types).
func (p Progress) failed() error {
	if p.ErrorKey != "" {
		if m, err := i18n.ParseStored(p.ErrorKey, p.ErrorArgs); err == nil {
			return &msgError{english: p.Error, m: m}
		}
	}

	return errors.New(p.Error)
}
