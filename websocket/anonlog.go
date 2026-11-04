// SPDX-License-Identifier: AGPL-3.0-or-later

package websocket

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alonsovidales/otc/log"
)

// Anyone can reach the device before signing in, and each request of theirs
// used to write a line at Info or Error: with the log rotated at 6 x 10 MB, a
// loop of them erased the owner's history in about a minute (Settings > Logs
// fills with strangers' lines). Routine requests are logged at Debug now, and
// the refusals worth knowing about are counted: one line a minute at most.
const (
	// cAnonOrigin: a socket from another site's page (originAllowed).
	cAnonOrigin = "sockets refused from another site's page" // i18n-ignore: the log, in English
	// cAnonNotUpgraded: a request to the socket's address that wasn't one.
	cAnonNotUpgraded = "requests to the socket that weren't one" // i18n-ignore: the log, in English
	// cAnonLockedOut: a password tried while its address was locked out.
	cAnonLockedOut = "passwords refused while their address was locked out" // i18n-ignore: the log, in English
	// cAnonUnreadable: a message before sign-in too large, with no room,
	// or not one at all.
	cAnonUnreadable = "messages refused before sign-in" // i18n-ignore: the log, in English
	// cAnonPanic: a request before sign-in that failed with a panic, past
	// the one a minute logged with its stack (recoveredPanic).
	cAnonPanic = "requests before sign-in that failed unexpectedly" // i18n-ignore: the log, in English
	// cAnonIdle: a connection that never signed in, closed once idle
	// (anonWatch).
	cAnonIdle = "connections closed idle before sign-in" // i18n-ignore: the log, in English
	// cAnonTooMany: a socket refused because its address, or every
	// address together, already holds as many as it may before signing in
	// (anonSlots).
	cAnonTooMany = "sockets refused, too many open before sign-in" // i18n-ignore: the log, in English
)

// anonPanics lets one panic a minute from a connection that hasn't signed
// in reach the log with its stack.
var anonPanics = &everyGate{every: cAnonLogEvery}

// recoveredPanic logs a request's recovered panic. A signed-in owner's or
// a friend's always goes to the log at Error with its stack. A stranger's
// - which anyone can send again and again, each a stack trace of a couple
// of KB - is logged so once a minute, and the rest only counted: a loop of
// them rotated the whole log away within minutes, the history anonLog
// exists to keep.
func recoveredPanic(anonymous bool, r any, stack []byte) {
	if anonymous && !anonPanics.allow(time.Now()) {
		log.Debug("recovered from panic handling a request before sign-in:", r)
		anonLog.note(cAnonPanic)
		return
	}
	log.Error("recovered from panic handling request:", r, string(stack))
}

// everyGate says yes at most once every `every`.
type everyGate struct {
	mu    sync.Mutex
	every time.Duration
	last  time.Time
}

func (g *everyGate) allow(now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.last.IsZero() && now.Sub(g.last) < g.every {
		return false
	}
	g.last = now
	return true
}

// cAnonLogEvery is how often minuteCounts writes what it counted.
const cAnonLogEvery = time.Minute

// minuteCounts counts events by kind and writes them in one line every
// cAnonLogEvery, when there were any.
type minuteCounts struct {
	mu     sync.Mutex
	counts map[string]int
	start  sync.Once
	write  func(line string)
}

var anonLog = &minuteCounts{write: func(line string) { log.Info(line) }}

// note counts one event of kind what.
func (c *minuteCounts) note(what string) {
	c.start.Do(func() {
		go func() {
			for range time.Tick(cAnonLogEvery) {
				c.flush()
			}
		}()
	})
	c.mu.Lock()
	if c.counts == nil {
		c.counts = map[string]int{}
	}
	c.counts[what]++
	c.mu.Unlock()
}

// flush writes the counts so far, in one line, and starts over.
func (c *minuteCounts) flush() {
	c.mu.Lock()
	counts := c.counts
	c.counts = nil
	c.mu.Unlock()
	if len(counts) == 0 {
		return
	}
	kinds := make([]string, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	parts := make([]string, len(kinds))
	for i, k := range kinds {
		parts[i] = fmt.Sprintf("%d %s", counts[k], k)
	}
	c.write("in the last minute: " + strings.Join(parts, ", ")) // i18n-ignore: the log, in English
}
