// SPDX-License-Identifier: AGPL-3.0-or-later

package raidwatch

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/alonsovidales/otc/alerttext"
	"github.com/alonsovidales/otc/container"
)

const (
	offerA = "0123456789abcdef0123"
	offerB = "fedcba9876543210fedc"
	diskA  = "sdc 512.1 GB (Generic Card Reader, xhci-hcd.1 usb4 4-1 at 5000 Mb/s)"
)

// withUse points the request and the answer at a temporary /run, forgets
// any request a test before made, and keeps UseDisk from starting its
// follow-up (the tests call checkUse).
func withUse(t *testing.T, f *fakeSys) (request, outcome string) {
	t.Helper()
	run := filepath.Join(f.root, "run")
	request, outcome = filepath.Join(run, "otc", "mirror-disk.request"), filepath.Join(run, "otc-raid-watch", "mirror-disk.json")
	for _, d := range []string{filepath.Dir(request), filepath.Dir(outcome)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	saved := []any{useRequestPath, useOutcomePath, useNoAnswerAfter, useWaitFor, startFollow}
	useRequestPath, useOutcomePath = request, outcome
	startFollow = func() {}
	useMu.Lock()
	usePending = nil
	useMu.Unlock()
	t.Cleanup(func() {
		useRequestPath, useOutcomePath = saved[0].(string), saved[1].(string)
		useNoAnswerAfter, useWaitFor = saved[2].(time.Duration), saved[3].(time.Duration)
		startFollow = saved[4].(func())
		useMu.Lock()
		usePending = nil
		useMu.Unlock()
	})
	return request, outcome
}

// listNotAdded writes raid_watch.py's list of the disks it left alone.
func listNotAdded(t *testing.T, disks ...map[string]any) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"disks": disks})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(notAddedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notAddedPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func offeredEntry(offer, model string, size int64) map[string]any {
	return map[string]any{"id": "sdc|" + offer, "disk": diskA, "model": model, "size": size, "held": false, "offer": offer}
}

// degrade takes sdb out of md0: slot 1 empty, md says degraded.
func degrade(f *fakeSys) {
	f.unplug("sdb")
	f.write("block/md0/md/degraded", "1")
}

// Status offers what raid_watch.py lists with an offer id - nothing held,
// nothing without a size - and only while the mirror lacks a disk: not
// while it is whole, nor while a replacement is copied onto, nor in a
// container.
func TestCandidatesOnlyWhileTheMirrorLacksADisk(t *testing.T) {
	f := newFakeSys(t)
	withUse(t, f)
	held := offeredEntry(offerB, "Backup", 2_000_398_934_016)
	held["held"] = true
	listNotAdded(t,
		offeredEntry(offerA, "Generic Card Reader\x1b[31m", 512_110_190_592),
		held,
		map[string]any{"id": "sdd|x", "disk": "sdd 1.0 GB", "model": "No offer", "size": 1_000_000_000},
		offeredEntry("not-an-offer-id", "Bad id", 64_000_000_000),
		offeredEntry("00000000000000000000", "No size", 0),
	)
	if c := Candidates(); len(c) != 0 {
		t.Fatalf("a whole mirror offers %+v", c)
	}
	degrade(f)
	c := Candidates()
	if len(c) != 1 || c[0].ID != offerA || c[0].Model != "Generic Card Reader[31m" || c[0].Size != 512_110_190_592 {
		t.Fatalf("degraded: %+v", c)
	}

	// A replacement being copied onto fills the slot: md still says
	// degraded, but nothing is offered.
	f.plug("sde", "xhci-hcd.1", "usb4", "4-1", "EEE")
	f.member("sde", "1", "spare")
	if c := Candidates(); len(c) != 0 {
		t.Fatalf("while rebuilding: %+v", c)
	}
	// It failed: offered again.
	f.member("sde", "1", "faulty")
	if c := Candidates(); len(c) != 1 {
		t.Fatalf("after the replacement failed: %+v", c)
	}
	// No working disk to copy from: nothing.
	f.member("sda", "0", "faulty")
	if c := Candidates(); len(c) != 0 {
		t.Fatalf("no working disk: %+v", c)
	}
	f.member("sda", "0", "in_sync")

	t.Run("container", func(t *testing.T) {
		t.Setenv(container.EnvVar, "1")
		t.Setenv(container.EnvMirrorArray, "md0")
		if c := Candidates(); len(c) != 0 {
			t.Fatalf("in a container: %+v", c)
		}
	})
}

// UseDisk refuses, asking nothing, when the mirror doesn't lack a disk,
// when the disk isn't offered now and while a request waits; otherwise it
// links one request into place - never replacing one - and answers at
// once.
func TestUseDiskAsksOnceForAnOfferedDisk(t *testing.T) {
	f := newFakeSys(t)
	request, _ := withUse(t, f)
	listNotAdded(t, offeredEntry(offerA, "Generic Card Reader", 512_110_190_592))
	code := func(err error) string {
		t.Helper()
		var r *Refusal
		if !errors.As(err, &r) {
			t.Fatalf("not a refusal: %v", err)
		}
		return r.Code
	}
	if got := code(UseDisk(offerA)); got != "not_degraded" {
		t.Errorf("whole mirror: %s", got)
	}
	degrade(f)
	for _, id := range []string{offerB, "", "../../etc/passwd", strings.ToUpper(offerA)} {
		if got := code(UseDisk(id)); got != "not_candidate" {
			t.Errorf("%q: %s", id, got)
		}
	}
	if _, err := os.Lstat(request); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refusal asked: %v", err)
	}

	if err := UseDisk(offerA); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(request)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the request: %v %v", fi, err)
	}
	raw, _ := os.ReadFile(request)
	if !regexp.MustCompile(`^\{"v":1,"id":"[0-9a-f]{16}","disk":"` + offerA + `"\}$`).Match(raw) {
		t.Errorf("request %s", raw)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(request), ".mirror-disk-*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
	// Waiting for its answer: another is refused, and the one there kept.
	err = UseDisk(offerA)
	if code(err) != "busy" {
		t.Errorf("again: %v", err)
	}
	var r *Refusal
	errors.As(err, &r)
	if r.Localized("es") == r.Localized("en") || r.Error() != r.Localized("en") {
		t.Errorf("words: %q / %q", r.Localized("es"), r.Error())
	}
	if again, _ := os.ReadFile(request); string(again) != string(raw) {
		t.Errorf("the request was replaced: %s", again)
	}
	// A request file from elsewhere is never replaced either.
	useMu.Lock()
	usePending = nil
	useMu.Unlock()
	if got := code(UseDisk(offerA)); got != "busy" {
		t.Errorf("a request already there: %s", got)
	}
}

// raid_watch.py's answer: an Alert saying why the disk wasn't added, once
// per request - also for one made before a restart - and none when it was
// added or the request couldn't be read. A request it never took is
// withdrawn after a minute, with an Alert.
func TestUseAnswers(t *testing.T) {
	f := newFakeSys(t)
	request, outcome := withUse(t, f)
	store := t.TempDir()
	var alerts []alerttext.Alert
	n := Notify{AlertText: func(a alerttext.Alert) { alerts = append(alerts, a) }}
	answer := func(id, code string) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"v": 1, "id": id, "disk": offerA, "code": code, "what": diskA + "\n"})
		if err := os.WriteFile(outcome, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	degrade(f)
	listNotAdded(t, offeredEntry(offerA, "Generic Card Reader", 512_110_190_592))
	if err := UseDisk(offerA); err != nil {
		t.Fatal(err)
	}
	rid := regexp.MustCompile(`"id":"([0-9a-f]{16})"`).FindStringSubmatch(mustRead(t, request))[1]
	if checkUse(store, n) || len(alerts) != 0 {
		t.Fatalf("no answer yet: %+v", alerts)
	}

	// Taken, and refused: in use.
	os.Remove(request)
	answer(rid, "in_use")
	if !checkUse(store, n) || len(alerts) != 1 {
		t.Fatalf("refused: %+v", alerts)
	}
	a := alerts[0]
	if a.Key != "dev.alert.raid.use_title" || a.DetailKey != "dev.alert.raid.use_in_use_detail" || string(a.DetailArgs) != `{"disk":"`+diskA+`"}` ||
		a.Title != "The disk wasn't added to the mirror" || !strings.HasPrefix(a.Detail, "It was in use (mounted, or part of another disk array)") {
		t.Errorf("the alert: %+v", a)
	}
	checkUse(store, n)
	checkUse(store, n)
	if len(alerts) != 1 {
		t.Fatalf("said again: %+v", alerts)
	}

	// Every reason has its words; an added disk and an unreadable request
	// none. An answer to a request made before a restart is said too.
	for i, c := range []struct{ code, detail string }{
		{"gone", "dev.alert.raid.use_gone_detail"},
		{"not_degraded", "dev.alert.raid.use_not_needed_detail"},
		{"failed", "dev.alert.raid.use_failed_detail"},
		{"added", ""},
	} {
		alerts = nil
		answer(strings.Repeat(string(rune('1'+i)), 16), c.code)
		checkUse(store, n)
		if c.detail == "" && len(alerts) != 0 || c.detail != "" && (len(alerts) != 1 || alerts[0].DetailKey != c.detail) {
			t.Errorf("%s: %+v", c.code, alerts)
		}
	}
	alerts = nil
	answer("", "bad_request")
	checkUse(store, n)
	for _, body := range []string{"{not json", `{"v":1,"id":"xyz","code":"failed"}`, `{"v":1,"id":"` + strings.Repeat("b", 16) + `","code":"rm -rf"}`} {
		os.WriteFile(outcome, []byte(body), 0o644)
		checkUse(store, n)
	}
	if len(alerts) != 0 {
		t.Errorf("unreadable answers alerted: %+v", alerts)
	}

	// Never taken (raid_watch.py isn't running): withdrawn after a minute.
	listNotAdded(t, offeredEntry(offerB, "Generic Card Reader", 512_110_190_592))
	os.Remove(outcome)
	if err := UseDisk(offerB); err != nil {
		t.Fatal(err)
	}
	if checkUse(store, n) || len(alerts) != 0 {
		t.Fatalf("within the minute: %+v", alerts)
	}
	useNoAnswerAfter = 0
	if !checkUse(store, n) || len(alerts) != 1 || alerts[0].DetailKey != "dev.alert.raid.use_no_answer_detail" {
		t.Fatalf("never taken: %+v", alerts)
	}
	if _, err := os.Lstat(request); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the request stayed: %v", err)
	}
	// ...and another may be asked at once.
	if err := UseDisk(offerB); err != nil {
		t.Errorf("after the withdrawal: %v", err)
	}
}

// A disk something holds (mounted, another array) isn't offered, and its
// Alert says what to do instead of pointing to Settings.
func TestNotAddedHeldDiskIsNotSentToSettings(t *testing.T) {
	f := newFakeSys(t)
	withUse(t, f)
	degrade(f)
	held := offeredEntry(offerA, "Backup", 512_110_190_592)
	held["held"] = true
	free := offeredEntry(offerB, "New", 512_110_190_592)
	free["id"] = "sdd|new"
	listNotAdded(t, held, free)
	var alerts []alerttext.Alert
	checkNotAdded(t.TempDir(), Notify{AlertText: func(a alerttext.Alert) { alerts = append(alerts, a) }})
	if len(alerts) != 2 || alerts[0].DetailKey != "dev.alert.raid.not_added_detail_in_use" || alerts[1].DetailKey != "dev.alert.raid.not_added_detail" {
		t.Fatalf("%+v", alerts)
	}
	if !strings.Contains(alerts[1].Detail, "Open Settings to use this disk for the mirror (everything on it will be erased)") ||
		strings.Contains(alerts[0].Detail, "Settings") {
		t.Errorf("words: %q / %q", alerts[0].Detail, alerts[1].Detail)
	}
	if c := Candidates(); len(c) != 1 || c[0].ID != offerB {
		t.Errorf("offered: %+v", c)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The files exactly as raid_watch.py wrote them in the Lima VM's test
// (json.dumps with sorted keys), so the two sides agree on their shape.
func TestReadsWhatRaidWatchWrites(t *testing.T) {
	f := newFakeSys(t)
	_, outcome := withUse(t, f)
	degrade(f)
	if err := os.MkdirAll(filepath.Dir(notAddedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	notAdded := `{"disks": [{"disk": "sdb 268 MB (Linux scsi_debug)", "held": false, "id": "sdb|268435456|Linux scsi_debug|", "model": "Linux scsi_debug", "offer": "db95d505d3df70037939", "size": 268435456}, ` +
		`{"disk": "sdd 268 MB (Linux scsi_debug)", "held": true, "id": "sdd|268435456|Linux scsi_debug|", "model": "Linux scsi_debug", "size": 268435456}]}`
	if err := os.WriteFile(notAddedPath, []byte(notAdded), 0o644); err != nil {
		t.Fatal(err)
	}
	if c := Candidates(); len(c) != 1 || c[0] != (Candidate{ID: "db95d505d3df70037939", Model: "Linux scsi_debug", Size: 268435456}) {
		t.Errorf("candidates: %+v", c)
	}
	answer := `{"code": "added", "disk": "9e7796af4a41094dfabd", "id": "f2b6f03a97899ef6", "v": 1, "what": "sdd 268 MB (Linux scsi_debug)"}` + "\n"
	if err := os.WriteFile(outcome, []byte(answer), 0o644); err != nil {
		t.Fatal(err)
	}
	if o, ok := readUseOutcome(outcome); !ok || o != (useOutcome{ID: "f2b6f03a97899ef6", Disk: "9e7796af4a41094dfabd", Code: "added", What: "sdd 268 MB (Linux scsi_debug)"}) {
		t.Errorf("answer: %+v %v", o, ok)
	}
}
