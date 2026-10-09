// SPDX-License-Identifier: AGPL-3.0-or-later

package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/alonsovidales/otc/log"
	"github.com/redis/go-redis/v9"
)

// Email alerts: every minute one bridge node - whichever holds
// KeyAlertLock - reads the same View the Fleet tab shows and mails the
// changes: a check that turned bad (after BadGrace evaluations in a row) or
// stayed warn for WarnGrace, a reminder every RemindEvery while it lasts,
// and one "resolved" when it is good again. What fires in the same minute
// goes in one email. Quiet checks (updates, reboot, CPU, load, log errors)
// are never mailed.
//
// The state (what was mailed, since when) is KeyAlertState in Redis, which
// keeps nothing on disk: after a Redis restart a problem still going on is
// mailed once more. If both bridge nodes are down nobody mails - the Mac's
// servercheck.sh still covers that.

const (
	KeyAlertLock  = "fleet:alert-lock"
	KeyAlertState = "fleet:alert-state"

	AlertEvery   = time.Minute
	AlertLockTTL = 150 * time.Second // the other node takes over this long after the holder dies
	RemindEvery  = 6 * time.Hour
	BadGrace     = 2 // evaluations in a row a check must be bad before it is mailed
	WarnGrace    = 5 // ... warn
)

// AlertEntry is one check that is, or was mailed as, not good.
type AlertEntry struct {
	Host   string    `json:"host"`
	ID     string    `json:"id"`
	Name   string    `json:"name"`
	State  State     `json:"state"`
	Detail string    `json:"detail"`
	Short  string    `json:"short"`
	Since  time.Time `json:"since"`
	// Streak counts evaluations in a row at warn or worse, BadStreak at
	// bad.
	Streak    int `json:"streak"`
	BadStreak int `json:"bad_streak"`
	// Sent is the state last mailed ("" for none), SentAt when.
	Sent   State     `json:"sent,omitempty"`
	SentAt time.Time `json:"sent_at,omitzero"`
}

// EventKind is what an alert email says about a check.
type EventKind string

const (
	EventProblem  EventKind = "problem"
	EventReminder EventKind = "reminder"
	EventResolved EventKind = "resolved"
)

// Event is one line of an alert email.
type Event struct {
	Kind  EventKind
	Entry AlertEntry // the check's entry (for a resolved one, as it was)
	Now   Check      // for a resolved one: the check as it is now
}

func alertKey(host, id string) string { return host + "|" + id }

// Step applies one evaluation of the fleet to the alert state: the new
// state, and what to mail. prev is not modified.
func Step(prev map[string]AlertEntry, v *View, now time.Time) (map[string]AlertEntry, []Event) {
	next := map[string]AlertEntry{}
	var events []Event
	seen := map[string]bool{}
	stale := map[string]bool{}
	for _, h := range v.Hosts {
		stale[h.Name] = h.Stale
		for _, c := range h.Checks {
			k := alertKey(h.Name, c.ID)
			seen[k] = true
			p, had := prev[k]
			switch {
			case c.State == Unknown:
				// Not judged this time (the bridge's report is stale): as it was.
				if had {
					next[k] = p
				}
			case c.Quiet || c.State == Good:
				if had && p.Sent != "" {
					events = append(events, Event{Kind: EventResolved, Entry: p, Now: c})
				}
			default:
				e := p
				if !had {
					e = AlertEntry{Host: h.Name, ID: c.ID, Since: now}
				}
				e.Name, e.State, e.Detail, e.Short = c.Name, c.State, c.Detail, c.Short
				e.Streak++
				if c.State == Bad {
					e.BadStreak++
				} else {
					e.BadStreak = 0
				}
				grace := BadGrace
				if c.ID == "report" && !h.HostSeen.IsZero() {
					// A host that stopped reporting is already minutes late;
					// one that never has (its agent being deployed) waits.
					grace = 1
				}
				switch {
				case c.State == Bad && e.BadStreak >= grace && e.Sent != Bad:
					e.Sent, e.SentAt = Bad, now
					events = append(events, Event{Kind: EventProblem, Entry: e})
				case c.State == Warn && e.Streak >= WarnGrace && e.Sent == "":
					e.Sent, e.SentAt = Warn, now
					events = append(events, Event{Kind: EventProblem, Entry: e})
				case e.Sent != "" && now.Sub(e.SentAt) >= RemindEvery:
					e.SentAt = now
					events = append(events, Event{Kind: EventReminder, Entry: e})
				}
				next[k] = e
			}
		}
	}
	// Checks no longer listed: a host that stopped reporting lists only
	// its report, so its other problems stay as they were; anything else
	// (a host forgotten, a disk unmounted) is over.
	keys := make([]string, 0, len(prev))
	for k := range prev {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if seen[k] {
			continue
		}
		p := prev[k]
		if stale[p.Host] {
			next[k] = p
			continue
		}
		if p.Sent != "" {
			events = append(events, Event{Kind: EventResolved, Entry: p, Now: Check{ID: p.ID, Name: p.Name, State: Good, Detail: "no longer checked"}})
		}
	}
	return next, events
}

// unsent is next with events' effects undone - the email didn't go, so the
// next evaluation tries again: problems and reminders are not marked
// mailed, and resolved entries are kept.
func unsent(prev, next map[string]AlertEntry, events []Event) map[string]AlertEntry {
	out := make(map[string]AlertEntry, len(next))
	for k, e := range next {
		out[k] = e
	}
	for _, ev := range events {
		k := alertKey(ev.Entry.Host, ev.Entry.ID)
		switch ev.Kind {
		case EventResolved:
			out[k] = prev[k]
		default:
			e := out[k]
			p := prev[k]
			e.Sent, e.SentAt = p.Sent, p.SentAt
			out[k] = e
		}
	}
	return out
}

// Compose writes the email for events: the subject names what changed,
// the body has the details and everything that is not good right now.
func Compose(events []Event, v *View, node, link string, now time.Time) (subject, body string) {
	var probs, reminds, resolved []Event
	for _, ev := range events {
		switch ev.Kind {
		case EventProblem:
			probs = append(probs, ev)
		case EventReminder:
			reminds = append(reminds, ev)
		case EventResolved:
			resolved = append(resolved, ev)
		}
	}
	item := func(e AlertEntry) string {
		s := e.Short
		if s == "" {
			s = strings.ToLower(e.Name)
		}
		return e.Host + " " + s
	}
	list := func(evs []Event) string {
		var parts []string
		for _, ev := range evs {
			parts = append(parts, item(ev.Entry))
		}
		return strings.Join(parts, ", ")
	}
	plural := func(n int, one, many string) string {
		if n == 1 {
			return "1 " + one
		}
		return fmt.Sprintf("%d %s", n, many)
	}
	var subj []string
	if len(probs) > 0 {
		subj = append(subj, plural(len(probs), "problem", "problems")+": "+list(probs))
	}
	if len(reminds) > 0 {
		subj = append(subj, "still "+plural(len(reminds), "problem", "problems")+": "+list(reminds))
	}
	if len(resolved) > 0 {
		subj = append(subj, "resolved: "+list(resolved))
	}
	subject = "[OTC fleet] " + strings.Join(subj, "; ")
	if r := []rune(subject); len(r) > 180 {
		subject = string(r[:179]) + "…"
	}

	var b strings.Builder
	line := func(st State, host, name, detail string) {
		fmt.Fprintf(&b, "  [%s] %s - %s: %s\n", strings.ToUpper(string(st)), host, name, detail)
	}
	if len(probs) > 0 {
		b.WriteString("New problems:\n")
		for _, ev := range probs {
			line(ev.Entry.State, ev.Entry.Host, ev.Entry.Name, ev.Entry.Detail)
		}
		b.WriteString("\n")
	}
	if len(reminds) > 0 {
		b.WriteString("Still not fixed (reminder every 6 hours):\n")
		for _, ev := range reminds {
			line(ev.Entry.State, ev.Entry.Host, ev.Entry.Name, ev.Entry.Detail+" (since "+stamp(ev.Entry.Since)+")")
		}
		b.WriteString("\n")
	}
	if len(resolved) > 0 {
		b.WriteString("Resolved:\n")
		for _, ev := range resolved {
			line(Good, ev.Entry.Host, ev.Entry.Name, ev.Now.Detail+" (was: "+ev.Entry.Detail+"; "+dur(now.Sub(ev.Entry.Since))+")")
		}
		b.WriteString("\n")
	}
	var notGood []string
	for _, h := range v.Hosts {
		for _, c := range h.Checks {
			if c.State == Warn || c.State == Bad {
				q := ""
				if c.Quiet {
					q = " (not mailed)"
				}
				notGood = append(notGood, fmt.Sprintf("  [%s] %s - %s: %s%s", strings.ToUpper(string(c.State)), h.Name, c.Name, c.Detail, q))
			}
		}
	}
	if len(notGood) == 0 {
		b.WriteString("Everything else is good.\n")
	} else {
		b.WriteString("Everything not good right now:\n" + strings.Join(notGood, "\n") + "\n")
	}
	b.WriteString("\n")
	if link != "" {
		b.WriteString("Fleet tab: " + link + "\n")
	}
	fmt.Fprintf(&b, "Sent by %s at %s.\n", node, stamp(now))
	return subject, b.String()
}

// Alerter runs the alerts on one bridge node.
type Alerter struct {
	rdb  *redis.Client
	node string
	to   string
	link string
	send func(to, subject, body string) error
	now  func() time.Time

	lastErr string // logged once per change
}

// NewAlerter mails to with send; link is the Fleet tab's address.
func NewAlerter(rdb *redis.Client, node, to, link string, send func(to, subject, body string) error) *Alerter {
	return &Alerter{rdb: rdb, node: node, to: to, link: link, send: send, now: time.Now}
}

// Start evaluates every AlertEvery for as long as the process runs.
func (a *Alerter) Start() {
	go func() {
		t := time.NewTicker(AlertEvery)
		defer t.Stop()
		for range t.C {
			a.logResult(a.Tick(context.Background()))
		}
	}()
}

func (a *Alerter) logResult(err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if msg != a.lastErr {
		if err != nil {
			log.Error("fleet alerts:", err)
		} else {
			log.Info("fleet alerts: working again")
		}
		a.lastErr = msg
	}
}

// lockScript takes KeyAlertLock for ARGV[1], or extends it if ARGV[1]
// already holds it: 1 when this node holds it now.
var lockScript = redis.NewScript(`
local v = redis.call('GET', KEYS[1])
if v == ARGV[1] then
  redis.call('PEXPIRE', KEYS[1], ARGV[2])
  return 1
end
if v then
  return 0
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
return 1`)

// Lock reports whether this node is the one that mails (taking or
// extending the lock).
func (a *Alerter) Lock(ctx context.Context) (bool, error) {
	n, err := lockScript.Run(ctx, a.rdb, []string{KeyAlertLock}, a.node, AlertLockTTL.Milliseconds()).Int()
	return n == 1, err
}

// Tick is one evaluation: nothing unless this node holds the lock.
func (a *Alerter) Tick(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	held, err := a.Lock(ctx)
	if err != nil {
		return fmt.Errorf("taking the lock: %w", err)
	}
	if !held {
		return nil
	}
	now := a.now()
	v, err := Read(ctx, a.rdb, now)
	if err != nil {
		return fmt.Errorf("reading the fleet: %w", err)
	}
	prev, err := a.loadState(ctx)
	if err != nil {
		return fmt.Errorf("reading the alert state: %w", err)
	}
	next, events := Step(prev, v, now)
	var sendErr error
	if len(events) > 0 {
		subject, body := Compose(events, v, a.node, a.link, now)
		if sendErr = a.send(a.to, subject, body); sendErr != nil {
			next = unsent(prev, next, events)
			sendErr = fmt.Errorf("mailing %q: %w", subject, sendErr)
		} else {
			log.Info("fleet alerts: mailed", subject)
		}
	}
	if err := a.saveState(ctx, next); err != nil {
		return errors.Join(sendErr, fmt.Errorf("saving the alert state: %w", err))
	}
	return sendErr
}

func (a *Alerter) loadState(ctx context.Context) (map[string]AlertEntry, error) {
	s, err := a.rdb.Get(ctx, KeyAlertState).Result()
	if errors.Is(err, redis.Nil) {
		return map[string]AlertEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]AlertEntry{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		// Unreadable (another version's): start afresh rather than stop.
		return map[string]AlertEntry{}, nil
	}
	return m, nil
}

func (a *Alerter) saveState(ctx context.Context, m map[string]AlertEntry) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return a.rdb.Set(ctx, KeyAlertState, b, 0).Err()
}
