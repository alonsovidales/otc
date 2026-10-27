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
)

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
