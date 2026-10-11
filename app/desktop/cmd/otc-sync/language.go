// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/app/desktop/internal/oslang"
	"github.com/alonsovidales/otc/i18n"
)

// cmdLanguage is the tray's Language menu on the command line: the user's
// language for every Off The Cloud app (docs/i18n.md, "The stored
// choice"). Without an argument it says what is chosen and what that
// gives; `auto` follows the computer, a code picks that language. The
// choice is recorded in config.json, pending, for the running sync to
// send to the device - as the tray does. The command line itself stays
// English.
func cmdLanguage(args []string) error { return runLanguage(args, os.Stdout, time.Now()) }

func runLanguage(args []string, out io.Writer, now time.Time) error {
	if len(args) > 1 {
		return errors.New("usage: otc-sync language [auto|" + strings.Join(languageCodes(), "|") + "]")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	st := liveState()
	if len(args) == 0 {
		fmt.Fprint(out, languageReport(cfg, st))

		return nil
	}
	code := strings.ToLower(strings.TrimSpace(args[0]))
	if code == "auto" || code == "automatic" {
		code = ""
	} else if !oslang.Carried(code) {
		return fmt.Errorf("no language %q in this version - choose auto or one of: %s", args[0], strings.Join(languageCodes(), ", "))
	}
	if cfg.Language != code {
		var device *string
		if st != nil {
			device = st.DeviceLanguage
		}
		cfg.SetLanguage(code, device, now)
		if err := cfg.Save(); err != nil {
			return err
		}
	}
	fmt.Fprint(out, languageReport(cfg, st))

	return nil
}

// languageCodes lists the codes this build can show, English first.
func languageCodes() []string {
	var codes []string
	for _, l := range i18n.Languages() {
		codes = append(codes, l.Code)
	}

	return codes
}

// liveState is what the running sync last wrote; nil when none is running.
func liveState() *config.State {
	st, err := config.LoadState()
	if err != nil || st == nil || time.Since(st.Updated) > 30*time.Second {
		return nil
	}

	return st
}

// languageReport is what `otc-sync language` prints: the choice, the
// language that gives, and the device's side (st nil: no sync running).
func languageReport(cfg *config.Config, st *config.State) string {
	var b strings.Builder
	shows := oslang.Effective(cfg.Language)
	choice := "Automatic (" + oslang.Name(oslang.System()) + ")"
	if cfg.Language != "" {
		choice = oslang.Name(cfg.Language) + " (" + cfg.Language + ")"
		if !oslang.Carried(cfg.Language) {
			choice += " - not in this version, shown in English"
		}
	}
	fmt.Fprintf(&b, "language:  %s\n", choice)
	fmt.Fprintf(&b, "shows:     %s (%s)\n", oslang.Name(shows), shows)
	var device string
	switch {
	case st == nil:
		device = "unknown (the sync is not running)"
	case st.LanguageUnsupported:
		device = "can't store a language (older software: the choice stays on this computer)"
	case st.DeviceLanguage == nil:
		device = "unknown (not connected yet)"
	case *st.DeviceLanguage == "":
		device = "Automatic"
	default:
		device = oslang.Name(*st.DeviceLanguage) + " (" + *st.DeviceLanguage + ")"
	}
	fmt.Fprintf(&b, "device:    %s\n", device)
	if cfg.LanguagePending != nil {
		fmt.Fprintf(&b, "pending:   chosen here %s, sent to the device when the sync is connected\n",
			cfg.LanguagePending.Local().Format("2006-01-02 15:04"))
	}

	return b.String()
}
