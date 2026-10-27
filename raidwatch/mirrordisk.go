// SPDX-License-Identifier: AGPL-3.0-or-later

package raidwatch

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/alonsovidales/otc/alerttext"
	"github.com/alonsovidales/otc/container"
	"github.com/alonsovidales/otc/i18n"
	"github.com/alonsovidales/otc/log"
)

// Settings › Storage: a disk raid_watch.py left out of the degraded mirror
// because something is on it (notadded.go) - most new disks and cards come
// formatted - can be erased and added at the owner's word. Candidates is
// what Status offers, from raid_watch.py's list; UseDisk hands the owner's
// choice to it, as root: a request on the service's own /run tmpfs
// directory, which it takes and checks again from scratch (the same disk,
// nothing holding it, the mirror still lacking a disk) before it erases
// anything, answering in useOutcomePath. The rebuild then shows in Status;
// an Alert says why when the disk wasn't added.
var (
	// useRequestPath is in otc.service's RuntimeDirectory (a tmpfs only
	// the otc user writes, emptied whenever the service stops).
	useRequestPath = "/run/otc/mirror-disk.request"
	// useOutcomePath is raid_watch.py's answer, in its own directory.
	useOutcomePath = "/run/otc-raid-watch/mirror-disk.json"
	// A request raid_watch.py hasn't taken after useNoAnswerAfter never
	// will (it isn't running): it is withdrawn. One taken and not answered
	// after useWaitFor is given up (the storage log says what happened).
	useNoAnswerAfter = time.Minute
	useWaitFor       = 3 * time.Minute
	useLookEvery     = time.Second
)

const (
	// useAnsweredFile holds the id of the last answer handled, so a restart
	// doesn't alert twice for it.
	useAnsweredFile = ".raid-use-answered"
	useMaxOutcome   = 4 << 10
)

var (
	reOfferID = regexp.MustCompile(`^[0-9a-f]{20}$`)
	reUseID   = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

// Candidate is a disk Settings may offer for the mirror.
type Candidate struct {
	// ID names it while it stays plugged in (raid_watch.py's offer id).
	ID string
	// Model is what the disk calls itself ("Generic Card Reader"); may be
	// empty.
	Model string
	// Size is in bytes.
	Size int64
}

// Candidates are the disks raid_watch.py left out of the mirror that
// Settings may offer: none in a container (otc-mirror adds what .env
// names), and none unless the mirror lacks a disk now.
func Candidates() []Candidate {
	if container.Enabled() {
		return nil
	}
	// The list first: almost always empty, and then md's sysfs is left
	// unread (every app asks for Status every few seconds).
	disks, _ := readNotAdded(notAddedPath)
	var out []Candidate
	for _, d := range disks {
		if d.offered() {
			out = append(out, Candidate{ID: d.Offer, Model: d.Model, Size: d.Size})
		}
	}
	if len(out) == 0 {
		return nil
	}
	if a, ok := readArray(watched()); !ok || !needsDisk(a) {
		return nil
	}
	return out
}

// needsDisk: the mirror lacks a disk - a slot with no disk in it, or only
// a faulty one - and has an up-to-date one to copy from. Not while a
// replacement is being copied onto (md says degraded until that ends):
// raid_watch.py's mirror_needs_disk.
func needsDisk(a array) bool {
	filled := map[int]bool{}
	working := false
	for _, m := range a.members {
		if m.Slot < 0 || m.Slot >= a.raidDisks || strings.Contains(m.State, "faulty") {
			continue
		}
		filled[m.Slot] = true
		working = working || m.inSync()
	}
	return working && len(filled) < a.raidDisks
}

// Refusal is UseDisk's answer when it asked nothing: Code is
// UseMirrorDisk's, Msg the words in each language.
type Refusal struct {
	Code string
	Msg  i18n.Msg
}

func (r *Refusal) Error() string { return r.Msg.Render("en") }

// Localized is the refusal in lang (websocket's errText).
func (r *Refusal) Localized(lang string) string { return r.Msg.Render(lang) }

type pendingUse struct {
	id, what string
	at       time.Time
}

var (
	useMu      sync.Mutex
	usePending *pendingUse

	targetMu      sync.Mutex
	targetStorage string
	targetNotify  Notify
	targetSet     bool
)

// UseDisk asks raid_watch.py to erase the disk Candidates offers as id and
// add it to the mirror, and returns at once: nil once asked. A *Refusal
// when nothing was asked: the mirror doesn't lack a disk ("not_degraded"),
// id isn't offered now ("not_candidate"), or another request hasn't been
// answered yet ("busy").
func UseDisk(id string) error {
	useMu.Lock()
	defer useMu.Unlock()
	a, ok := readArray(watched())
	if !ok || !needsDisk(a) || container.Enabled() {
		return &Refusal{Code: "not_degraded", Msg: i18n.DevDeviceMirrorNotDegraded()}
	}
	disk, ok := offeredDisk(id)
	if !ok {
		return &Refusal{Code: "not_candidate", Msg: i18n.DevDeviceMirrorNotCandidate()}
	}
	busy := &Refusal{Code: "busy", Msg: i18n.DevDeviceMirrorBusy()}
	if p := usePending; p != nil {
		if o, ok := readUseOutcome(useOutcomePath); (ok && o.ID == p.id) || time.Since(p.at) >= useWaitFor {
			usePending = nil // answered (checkUse alerts), or given up
		} else {
			return busy
		}
	}
	if _, err := os.Lstat(useRequestPath); !errors.Is(err, os.ErrNotExist) {
		return busy
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return err
	}
	rid := hex.EncodeToString(raw[:])
	req, err := json.Marshal(struct {
		V    int    `json:"v"`
		ID   string `json:"id"`
		Disk string `json:"disk"`
	}{1, rid, id})
	if err != nil {
		return err
	}
	if err := linkRequest(useRequestPath, req); err != nil {
		if errors.Is(err, os.ErrExist) {
			return busy
		}
		return err
	}
	usePending = &pendingUse{id: rid, what: disk.Disk, at: time.Now()}
	log.Info("raid: the owner asked for a disk to be erased and added to the mirror:", disk.Disk)
	startFollow()
	return nil
}

// startFollow starts followUse; the tests call checkUse themselves.
var startFollow = func() { go followUse() }

// offeredDisk is the disk raid_watch.py lists now that Settings offers as
// id.
func offeredDisk(id string) (notAddedDisk, bool) {
	if !reOfferID.MatchString(id) {
		return notAddedDisk{}, false
	}
	disks, _ := readNotAdded(notAddedPath)
	for _, d := range disks {
		if d.offered() && d.Offer == id {
			return d, true
		}
	}
	return notAddedDisk{}, false
}

// linkRequest writes req to a temporary file beside final (0600) and links
// it to final, which fails with os.ErrExist when a request is there: one
// is never replaced.
func linkRequest(final string, req []byte) error {
	f, err := os.CreateTemp(filepath.Dir(final), ".mirror-disk-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(req); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Link(tmp, final)
}

// setAlertTarget is where Watch's Alerts go, for followUse.
func setAlertTarget(storagePath string, n Notify) {
	targetMu.Lock()
	defer targetMu.Unlock()
	targetStorage, targetNotify, targetSet = storagePath, n, true
}

func alertTarget() (string, Notify, bool) {
	targetMu.Lock()
	defer targetMu.Unlock()
	return targetStorage, targetNotify, targetSet
}

// followUse looks for raid_watch.py's answer every useLookEvery after a
// request, so an Alert says at once why a disk wasn't added; check looks
// every minute too, for an answer no request of this process waits for (it
// was made before a restart).
func followUse() {
	for start := time.Now(); time.Since(start) <= useWaitFor+useLookEvery; {
		time.Sleep(useLookEvery)
		if storagePath, n, ok := alertTarget(); ok && checkUse(storagePath, n) {
			return
		}
	}
}

// useOutcome is raid_watch.py's answer to a request.
type useOutcome struct {
	ID   string `json:"id"`
	Disk string `json:"disk"`
	Code string `json:"code"`
	What string `json:"what"`
}

// useCodes are raid_watch.py's USE_CODES.
var useCodes = map[string]bool{"added": true, "gone": true, "not_degraded": true, "in_use": true, "failed": true, "bad_request": true}

// readUseOutcome is raid_watch.py's last answer; ok is false when there is
// none or it can't be read.
func readUseOutcome(path string) (o useOutcome, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return o, false
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, useMaxOutcome+1))
	if err != nil || len(raw) > useMaxOutcome || json.Unmarshal(raw, &o) != nil {
		return useOutcome{}, false
	}
	if (o.ID != "" && !reUseID.MatchString(o.ID)) || !useCodes[o.Code] {
		return useOutcome{}, false
	}
	o.What = cleanField(o.What)
	return o, true
}

// useAlert is the Alert for an answer: none when the disk was added (the
// rebuild shows in Status, and the mirror's own Alert says when it is
// whole again) or the request couldn't be read.
func useAlert(o useOutcome) (alerttext.Alert, bool) {
	var detail i18n.Msg
	switch o.Code {
	case "gone":
		detail = i18n.DevAlertRaidUseGoneDetail(o.What)
	case "not_degraded":
		detail = i18n.DevAlertRaidUseNotNeededDetail(o.What)
	case "in_use":
		detail = i18n.DevAlertRaidUseInUseDetail(o.What)
	case "failed":
		detail = i18n.DevAlertRaidUseFailedDetail(o.What)
	default:
		return alerttext.Alert{}, false
	}
	return alerttext.New(i18n.DevAlertRaidUseTitle(), detail), true
}

// checkUse handles raid_watch.py's answer: an Alert when the disk wasn't
// added, once per request (useAnsweredFile records the last one handled,
// first, so a crash right after can't alert twice). A request still
// waiting after useNoAnswerAfter is withdrawn, with an Alert. Reports
// whether the request this process made is over.
func checkUse(storagePath string, n Notify) bool {
	useMu.Lock()
	defer useMu.Unlock()
	o, ok := readUseOutcome(useOutcomePath)
	if ok && o.ID != "" && o.ID != loadUseAnswered(storagePath) {
		saveUseAnswered(storagePath, o.ID)
		if a, alert := useAlert(o); alert {
			log.Info("raid: the disk the owner chose was not added to the mirror:", o.Code)
			n.alert(a)
		} else if o.Code == "added" {
			log.Info("raid: the disk the owner chose was erased and added to the mirror")
		}
	}
	p := usePending
	switch {
	case p == nil:
		return true
	case ok && o.ID == p.id:
		usePending = nil
		return true
	case time.Since(p.at) >= useNoAnswerAfter:
		if _, err := os.Lstat(useRequestPath); err == nil {
			// Never taken: raid_watch.py isn't running.
			if err := os.Remove(useRequestPath); err == nil || errors.Is(err, os.ErrNotExist) {
				log.Error("raid: the storage watcher didn't take the request to use a disk for the mirror; withdrawn")
				n.alert(alerttext.New(i18n.DevAlertRaidUseTitle(), i18n.DevAlertRaidUseNoAnswerDetail(p.what)))
				usePending = nil
				return true
			}
		}
		if time.Since(p.at) >= useWaitFor {
			log.Error("raid: the storage watcher took the request to use a disk for the mirror and never answered")
			usePending = nil
			return true
		}
	}
	return false
}

func loadUseAnswered(storagePath string) string {
	b, err := os.ReadFile(filepath.Join(storagePath, useAnsweredFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// saveUseAnswered records the answer handled; a failure to record only
// means one may be said again.
func saveUseAnswered(storagePath, id string) {
	tmp := filepath.Join(storagePath, useAnsweredFile+".tmp")
	if os.WriteFile(tmp, []byte(id+"\n"), 0o600) != nil || os.Rename(tmp, filepath.Join(storagePath, useAnsweredFile)) != nil {
		log.Error("raid: could not record the storage watcher's answer")
		_ = os.Remove(tmp)
	}
}
