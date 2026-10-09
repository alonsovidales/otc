// SPDX-License-Identifier: AGPL-3.0-or-later

package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// view is a fleet of the given hosts' checks, as Read would make it.
func view(hosts ...HostView) *View { return &View{Time: t0, Hosts: hosts} }

func host(name string, stale bool, checks ...Check) HostView {
	return HostView{Name: name, Stale: stale, Checks: checks}
}

func good(id string) Check { return Check{ID: id, Name: id, State: Good, Detail: "fine"} }
func bad(id, short string) Check {
	return Check{ID: id, Name: id, State: Bad, Detail: "broken", Short: short}
}
func warn(id, short string) Check {
	return Check{ID: id, Name: id, State: Warn, Detail: "meh", Short: short}
}

func kinds(evs []Event) string {
	var s []string
	for _, e := range evs {
		s = append(s, string(e.Kind)+":"+e.Entry.Host+"/"+e.Entry.ID)
	}
	return strings.Join(s, ",")
}

func TestStepBadNeedsTwoEvaluationsThenRemindsAndResolves(t *testing.T) {
	st := map[string]AlertEntry{}
	now := t0
	tick := func(v *View) []Event {
		var evs []Event
		st, evs = Step(st, v, now)
		now = now.Add(time.Minute)
		return evs
	}
	broken := view(host("bridge2", false, good("report"), bad("replication", "replication stopped")))
	if evs := tick(broken); len(evs) != 0 {
		t.Fatalf("first bad evaluation mails nothing: %s", kinds(evs))
	}
	if evs := tick(broken); kinds(evs) != "problem:bridge2/replication" {
		t.Fatalf("second: %s", kinds(evs))
	}
	for i := 0; i < 30; i++ {
		if evs := tick(broken); len(evs) != 0 {
			t.Fatalf("still bad, minute %d: %s", i, kinds(evs))
		}
	}
	// Six hours after it was mailed: one reminder, then quiet again.
	now = st["bridge2|replication"].SentAt.Add(RemindEvery)
	if evs := tick(broken); kinds(evs) != "reminder:bridge2/replication" {
		t.Fatalf("reminder: %s", kinds(evs))
	}
	if evs := tick(broken); len(evs) != 0 {
		t.Fatalf("after the reminder: %s", kinds(evs))
	}
	fixed := view(host("bridge2", false, good("report"), good("replication")))
	if evs := tick(fixed); kinds(evs) != "resolved:bridge2/replication" {
		t.Fatalf("resolved: %s", kinds(evs))
	}
	if evs := tick(fixed); len(evs) != 0 || len(st) != 0 {
		t.Fatalf("after resolving: %s %+v", kinds(evs), st)
	}
}

func TestStepFlappingAndWarnings(t *testing.T) {
	st := map[string]AlertEntry{}
	var evs []Event
	// Bad once, good, bad once: never two in a row, never mailed.
	for i, c := range []Check{bad("disk:/", "disk / 96%"), good("disk:/"), bad("disk:/", "disk / 96%"), good("disk:/")} {
		st, evs = Step(st, view(host("redis", false, c)), t0.Add(time.Duration(i)*time.Minute))
		if len(evs) != 0 {
			t.Fatalf("flap %d mailed: %s", i, kinds(evs))
		}
	}
	// A warning is mailed after WarnGrace evaluations, once.
	for i := 1; i <= WarnGrace+2; i++ {
		st, evs = Step(st, view(host("redis", false, warn("disk:/", "disk / 88%"))), t0.Add(time.Duration(i)*time.Minute))
		if want := i == WarnGrace; (len(evs) == 1) != want {
			t.Fatalf("warning, evaluation %d: %s", i, kinds(evs))
		}
	}
	// It gets worse: mailed again as bad (after the bad grace).
	st, evs = Step(st, view(host("redis", false, bad("disk:/", "disk / 96%"))), t0.Add(20*time.Minute))
	if len(evs) != 0 {
		t.Fatal(kinds(evs))
	}
	st, evs = Step(st, view(host("redis", false, bad("disk:/", "disk / 96%"))), t0.Add(21*time.Minute))
	if kinds(evs) != "problem:redis/disk:/" || evs[0].Entry.State != Bad {
		t.Fatal(kinds(evs))
	}
	// Quiet checks are never mailed.
	q := warn("updates", "2 security updates")
	q.Quiet = true
	for i := 0; i < 10; i++ {
		_, evs = Step(map[string]AlertEntry{}, view(host("redis", false, q)), t0)
		if len(evs) != 0 {
			t.Fatal(kinds(evs))
		}
	}
}

func TestStepStaleHostMailsAtOnceAndKeepsItsProblems(t *testing.T) {
	st := map[string]AlertEntry{}
	var evs []Event
	for i := 0; i < 2; i++ {
		st, evs = Step(st, view(host("bridge2", false, good("report"), bad("disk:/", "disk / 97%"))), t0.Add(time.Duration(i)*time.Minute))
	}
	if kinds(evs) != "problem:bridge2/disk:/" {
		t.Fatal(kinds(evs))
	}
	// It stops reporting: its report is mailed straight away, and the
	// disk problem is neither resolved nor mailed again.
	staleHost := host("bridge2", true, bad("report", "stopped reporting"))
	staleHost.HostSeen = t0
	staleView := view(staleHost)
	st, evs = Step(st, staleView, t0.Add(5*time.Minute))
	if kinds(evs) != "problem:bridge2/report" {
		t.Fatal(kinds(evs))
	}
	if _, ok := st["bridge2|disk:/"]; !ok {
		t.Fatal("the disk problem must be kept while the host is stale")
	}
	// Unknown checks (a stale bridge report's details) keep their state.
	st["bridge2|bridge-cert"] = AlertEntry{Host: "bridge2", ID: "bridge-cert", Sent: Bad, SentAt: t0}
	st, evs = Step(st, view(host("bridge2", false, good("report"), Check{ID: "bridge-cert", State: Unknown}, bad("disk:/", "disk / 97%"))), t0.Add(6*time.Minute))
	if kinds(evs) != "resolved:bridge2/report" {
		t.Fatal(kinds(evs))
	}
	if _, ok := st["bridge2|bridge-cert"]; !ok {
		t.Fatal("an unknown check keeps its entry")
	}
	// A host forgotten altogether: its problems are over.
	_, evs = Step(st, view(), t0.Add(7*time.Minute))
	if kinds(evs) != "resolved:bridge2/bridge-cert,resolved:bridge2/disk:/" {
		t.Fatal(kinds(evs))
	}
}

// An agent that has never reported (being deployed) gets the usual grace.
func TestStepNeverReportedWaits(t *testing.T) {
	v := view(host("bridge1", true, bad("report", "no agent report")))
	st, evs := Step(map[string]AlertEntry{}, v, t0)
	if len(evs) != 0 {
		t.Fatal(kinds(evs))
	}
	if _, evs = Step(st, v, t0.Add(time.Minute)); kinds(evs) != "problem:bridge1/report" {
		t.Fatal(kinds(evs))
	}
}

func TestComposeGroupsOneEmail(t *testing.T) {
	st := map[string]AlertEntry{}
	v := view(
		host("bridge2", false, good("report"), bad("replication", "replication stopped (IO connecting)")),
		host("redis", false, good("report"), bad("disk:/", "disk / 96%")),
	)
	var evs []Event
	for i := 0; i < 2; i++ {
		st, evs = Step(st, v, t0.Add(time.Duration(i)*time.Minute))
	}
	subject, body := Compose(evs, v, "bridge1", "https://off-the.cloud/admin.html#fleet", t0)
	if subject != "[OTC fleet] 2 problems: bridge2 replication stopped (IO connecting), redis disk / 96%" {
		t.Fatal(subject)
	}
	for _, want := range []string{"New problems:", "[BAD] bridge2 - replication: broken", "Fleet tab: https://off-the.cloud/admin.html#fleet", "Sent by bridge1"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body lacks %q:\n%s", want, body)
		}
	}
	// One fixed, one still going: a resolved line, nothing else.
	v2 := view(host("bridge2", false, good("report"), good("replication")), host("redis", false, good("report"), bad("disk:/", "disk / 96%")))
	_, evs = Step(st, v2, t0.Add(2*time.Minute))
	subject, body = Compose(evs, v2, "bridge1", "", t0)
	if subject != "[OTC fleet] resolved: bridge2 replication stopped (IO connecting)" || !strings.Contains(body, "Everything not good right now:\n  [BAD] redis - disk:/") {
		t.Fatalf("%s\n%s", subject, body)
	}
}

// fakeMailer records what it is asked to send, or fails.
type fakeMailer struct {
	sent []string
	fail bool
}

func (m *fakeMailer) send(to, subject, body string) error {
	if m.fail {
		return errors.New("smtp down")
	}
	m.sent = append(m.sent, to+": "+subject)
	return nil
}

// fleetRedis publishes one host whose disk is full, as an agent would.
func fleetRedis(t *testing.T, mr *miniredis.Miniredis, rdb *redis.Client, now time.Time, used uint64) {
	t.Helper()
	hs := healthy("redis")
	hs.Time = now
	hs.Disks[0].Used, hs.Disks[0].Avail = used, 1000-used
	b, _ := json.Marshal(hs)
	ctx := context.Background()
	rdb.Set(ctx, KeyHostPrefix+"redis", b, SnapshotTTL)
	rdb.HSet(ctx, KeyKnownHosts, "redis", now.Unix())
}

func TestAlerterOneNodeMailsAndRetriesAFailedMail(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	now := t0
	m1, m2 := &fakeMailer{}, &fakeMailer{}
	a1 := NewAlerter(rdb, "bridge1", "ops@example.org", "", m1.send)
	a2 := NewAlerter(rdb, "bridge2", "ops@example.org", "", m2.send)
	a1.now = func() time.Time { return now }
	a2.now = a1.now
	tick := func() {
		for _, a := range []*Alerter{a1, a2} {
			if err := a.Tick(ctx); err != nil && !m1.fail {
				t.Fatal(err)
			}
		}
		now = now.Add(time.Minute)
		mr.FastForward(time.Minute)
	}

	fleetRedis(t, mr, rdb, now, 980)
	tick()
	fleetRedis(t, mr, rdb, now, 980)
	tick()
	if len(m1.sent) != 1 || len(m2.sent) != 0 {
		t.Fatalf("bridge1 holds the lock and mails once: %v / %v", m1.sent, m2.sent)
	}
	if m1.sent[0] != "ops@example.org: [OTC fleet] 1 problem: redis disk / 98%" {
		t.Fatal(m1.sent[0])
	}

	// Fixed, but the mail fails: tried again the next minute.
	m1.fail = true
	fleetRedis(t, mr, rdb, now, 500)
	tick()
	m1.fail = false
	fleetRedis(t, mr, rdb, now, 500)
	tick()
	if len(m1.sent) != 2 || m1.sent[1] != "ops@example.org: [OTC fleet] resolved: redis disk / 98%" {
		t.Fatalf("%v", m1.sent)
	}

	// bridge1 dies: once its lock runs out, bridge2 carries on with the
	// same state (nothing to resend).
	mr.FastForward(AlertLockTTL)
	now = now.Add(AlertLockTTL)
	fleetRedis(t, mr, rdb, now, 500)
	if err := a2.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if held, _ := a2.Lock(ctx); !held {
		t.Fatal("bridge2 must take the lock over")
	}
	if held, _ := a1.Lock(ctx); held {
		t.Fatal("bridge1 must not take it back while bridge2 holds it")
	}
	if len(m2.sent) != 0 {
		t.Fatalf("nothing new to mail: %v", m2.sent)
	}
}
