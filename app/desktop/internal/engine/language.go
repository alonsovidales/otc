// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"log"
	"time"

	"github.com/alonsovidales/otc/app/desktop/internal/config"
	"github.com/alonsovidales/otc/app/desktop/internal/oslang"
	pb "github.com/alonsovidales/otc/proto/generated"
)

// The user's language (docs/i18n.md, "The stored choice"). The device
// keeps it per user and reports it in its status (Status.language: absent
// on a device that predates localization, "" for Automatic); config.json
// keeps the local copy (config.Config.Language). A change made here (the
// tray, `otc-sync language`) is recorded there as pending and sent with
// SetLanguage until the device answers:
//   - ok: the pending mark is cleared, compare-and-clear, in config.json
//     first - a change made meanwhile goes next;
//   - Ack code "changed" (another app changed it since the value this one
//     was made against): cleared, and the device's value read and adopted;
//   - unknown_payload: a device too old to keep it - the choice stays on
//     this computer, still pending, and nothing more is sent until the
//     next connection, when the device may have been updated;
//   - anything else: kept, and sent again at the next connection.
//
// The device's value, polled with the status, is adopted (written to
// config.json) whenever it differs from the copy - except while a change
// made here is pending: a different value is then taken for one from
// before the change and ignored until the device reports the change, or
// languageWindow has passed. Another device or password drops a pending
// change (it was made against the old one's value) and keeps the copy.
// As SyncModel and LanguageSettings on the Mac.

// languageWindow: how long a pending change outweighs a different value
// the device reports.
const languageWindow = 30 * time.Second

// applyLanguageLocked has requests carry the language the copy gives.
// e.mu must be held (SetLang takes only the client's own lock, which it
// never holds while calling back into the engine).
func (e *Engine) applyLanguageLocked() {
	e.ws.SetLang(oslang.Effective(e.cfg.Language))
}

// languageAtConnect: a device updated since can store it now, and a change
// still pending goes first - before the status poll, whose answer would
// otherwise be taken for the device's word on it.
func (e *Engine) languageAtConnect() {
	e.mu.Lock()
	e.langUnsupported = false
	e.mu.Unlock()
	e.sendLanguage()
}

// sendLanguage sends the pending change, if there is one and nothing is
// under way, on a goroutine of its own; marked under way at once, so a
// status answer meanwhile isn't adopted over it.
func (e *Engine) sendLanguage() {
	connected := e.ws.IsConnected()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.langSending || e.langUnsupported || e.cfg.LanguagePending == nil || e.stopped || !connected {
		return
	}
	e.langSending = true
	e.langEpoch++
	sent, domain := e.cfg.Language, e.cfg.Domain
	var expected *string
	if x := e.cfg.LanguageExpected; x != nil {
		v := *x
		expected = &v
	}
	go e.sendLanguageNow(sent, expected, domain)
}

func (e *Engine) sendLanguageNow(sent string, expected *string, domain string) {
	resp, err := e.request(func(r *pb.ReqEnvelope) {
		r.Payload = &pb.ReqEnvelope_ReqSetLanguage{ReqSetLanguage: &pb.SetLanguage{Language: sent, Expected: expected}}
	})
	e.mu.Lock()
	e.langSending = false
	e.langEpoch++
	sameDevice := e.cfg.Domain == domain
	e.mu.Unlock()
	if !sameDevice {
		// Another device meanwhile: the answer is not about it, and the
		// change it was for was dropped (forgetLanguageLocked).
		return
	}
	switch ack := resp.GetRespAck(); {
	case err != nil:
		log.Printf("could not send the language to the device (tried again at the next connection): %v", err)

		return
	case unknownPayload(resp):
		log.Printf("the device can't store a language (an older release): it stays on this computer until the next connection")
		e.mu.Lock()
		e.langUnsupported = true
		e.mu.Unlock()
		e.notify()

		return
	case ack.GetCode() == "changed" || resp.GetErrorCode() == "changed":
		// Changed from another app since: that one stands.
		e.clearLanguagePending(sent, false)
		e.pollRaid()
	case resp.GetError() || ack == nil || !ack.GetOk():
		log.Printf("the device refused the language (tried again at the next connection): %s", resp.GetErrorMessage())

		return
	default:
		e.mu.Lock()
		s := sent
		e.devLanguage = &s
		e.mu.Unlock()
		e.clearLanguagePending(sent, true)
	}
	// A change made while this one was under way goes now.
	e.sendLanguage()
}

// clearLanguagePending drops the pending mark once the device has
// answered for sent - in config.json first, then in memory, each only
// while the choice is still sent. When the device now has sent
// (deviceHas), a newer change still pending counts against it from now on
// (config.SetLanguageExpected), or the device would take it for one made
// elsewhere. A config.json that can't be written leaves the mark: the
// change is sent again at the next connection, which the device answers
// the same way.
func (e *Engine) clearLanguagePending(sent string, deviceHas bool) {
	edit := func(cfg *config.Config) bool {
		return cfg.ClearLanguagePending(sent) || (deviceHas && cfg.SetLanguageExpected(sent))
	}
	if err := editConfigOnDisk(edit); err != nil {
		log.Printf("could not record the device's answer about the language: %v", err)

		return
	}
	e.mu.Lock()
	edit(e.cfg)
	e.mu.Unlock()
	e.notify()
}

// deviceLanguage takes the device's value from a status answer (nil: a
// device that predates localization - the copy stands). epoch is
// langEpoch when the status was asked for: an answer to a status asked
// for before a SetLanguage was sent or answered may predate it - the
// device answers each request on its own - and is left out altogether.
func (e *Engine) deviceLanguage(v *string, epoch uint64) {
	e.mu.Lock()
	if e.langSending || epoch != e.langEpoch {
		e.mu.Unlock()

		return
	}
	if v == nil {
		changed := e.devLanguage != nil
		e.devLanguage = nil
		e.mu.Unlock()
		if changed {
			e.notify()
		}

		return
	}
	device := *v
	e.devLanguage = &device
	cur, pending := e.cfg.Language, e.cfg.LanguagePending
	e.mu.Unlock()
	switch {
	case device == cur:
		if pending != nil {
			e.clearLanguagePending(cur, true) // the device has it
		}
	case pending != nil && time.Since(*pending) < languageWindow:
		// From before the change, most likely: ignored for now.
	default:
		e.adoptLanguage(device, cur, pending)
	}
}

// adoptLanguage makes the device's value the copy, in config.json first,
// in place of the choice the engine looked at (seen, pending): a change
// made meanwhile here stands. Not written, it is tried again at the next
// status answer.
func (e *Engine) adoptLanguage(device, seen string, pending *time.Time) {
	err := editConfigOnDisk(func(cfg *config.Config) bool { return cfg.AdoptLanguage(device, seen, pending) })
	if err != nil {
		log.Printf("could not record the language the device has: %v", err)

		return
	}
	e.mu.Lock()
	if e.cfg.AdoptLanguage(device, seen, pending) {
		e.applyLanguageLocked()
	}
	e.mu.Unlock()
	e.notify()
}

// languageChangedLocked: config.json brought a new choice or a new
// pending change (the tray, the command line). e.mu must be held.
func languageChangedLocked(old, cfg *config.Config) bool {
	if old.Language != cfg.Language || (old.LanguagePending == nil) != (cfg.LanguagePending == nil) {
		return true
	}

	return old.LanguagePending != nil && !old.LanguagePending.Equal(*cfg.LanguagePending)
}

// forgetLanguageLocked: another device or password - its value is not
// this one's, whether it can store one is asked again, and a change made
// against the old one's value isn't sent to it (the choice stays). It
// returns that change's mark, for dropLanguagePending once e.mu is
// released. e.mu must be held.
func (e *Engine) forgetLanguageLocked() *time.Time {
	e.devLanguage = nil
	e.langUnsupported = false
	at := e.cfg.LanguagePending
	e.cfg.LanguagePending, e.cfg.LanguageExpected = nil, nil

	return at
}

// dropLanguagePending clears in config.json the pending change marked at
// (forgetLanguageLocked), unless a newer one replaced it meanwhile.
func dropLanguagePending(at *time.Time) {
	if at == nil {
		return
	}
	if err := editConfigOnDisk(func(cfg *config.Config) bool { return cfg.DropLanguagePending(at) }); err != nil {
		log.Printf("could not drop the language change made for the previous device: %v", err)
	}
}

// languageStatusLocked fills the language part of a snapshot. e.mu must be
// held.
func (e *Engine) languageStatusLocked(st *config.State) {
	if e.devLanguage != nil {
		v := *e.devLanguage
		st.DeviceLanguage = &v
	}
	// A status that doesn't say is a device from before localization.
	st.LanguageUnsupported = e.langUnsupported || (e.devStatus != nil && e.devStatus.Language == nil)
}

// editConfigOnDisk applies edit to config.json, read again just before so
// whatever the tray or the command line wrote meanwhile stays, and saves
// it when edit says it changed something. A read or a write that races
// another process's rename-over (Windows) is tried again.
func editConfigOnDisk(edit func(*config.Config) bool) error {
	var err error
	for try := 0; try < 3; try++ {
		if try > 0 {
			time.Sleep(50 * time.Millisecond)
		}
		var cfg *config.Config
		if cfg, err = config.Load(); err != nil {
			continue
		}
		if !edit(cfg) {
			return nil
		}
		if err = saveConfig(cfg); err == nil {
			return nil
		}
	}

	return err
}
