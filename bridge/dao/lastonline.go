// SPDX-License-Identifier: AGPL-3.0-or-later

package dao

import (
	"context"
	"strings"
	"time"

	"github.com/alonsovidales/otc/log"
)

// devices.last_online_at (migration 012-last-online.sql): when the device
// was last connected to the bridge. A device stays connected whether or
// not anyone reaches it through the bridge, so this is the sign of use of
// a device in use only at home or over Tailscale, whose clients never pass
// through the bridge and leave no last_client_at: the inactivity job
// counts it (inactivity.go), and the admin panel shows it.
//
// Each node stamps the devices connected to it (websocket.Manager): as a
// device's first connection to the node registers, every cLastOnlineEvery
// while it stays connected, and as its last connection there closes. A
// node that stops without closing them (a restart, a crash) leaves a
// stamp at most cLastOnlineEvery and a tick old.
//
// A database without the column (the migration not run yet) stamps
// nothing, and every query that reads it falls back to the same query
// without it (optionalColumn): use is then sign-ins and relayed clients,
// as before the column.

// cLastOnlineEvery is how often, at most, a node writes a connected
// device's last_online_at. Ten minutes: the stamp has to be days fresh
// for the inactivity job (months), and the admin panel says "online now"
// from the live connections, so it matters only once a node is gone
// without saying goodbye; and it keeps the writes to the one primary at
// six an hour per device and node, a tenth of what relayed traffic may
// write to last_client_at.
const cLastOnlineEvery = 10 * time.Minute

// cLastOnlineLeft is how often, at most, a device whose last connection
// to a node closed is stamped for it: a device dropping and coming back in
// a loop writes once a minute, not once a drop.
const cLastOnlineLeft = time.Minute

// TouchLastOnline records that domains are connected to this node now:
// last_online_at for each of them this node hasn't written in the last
// cLastOnlineEvery. Best effort, like touchLastClient: a failed write is
// logged and tried again at the next call.
func (dao *Dao) TouchLastOnline(domains []string) {
	dao.touchLastOnline(domains, cLastOnlineEvery, time.Now())
}

// TouchLastOnlineLeft records that domain's last connection to this node
// just closed: it was online until now.
func (dao *Dao) TouchLastOnlineLeft(domain string) {
	dao.touchLastOnline([]string{domain}, cLastOnlineLeft, time.Now())
}

// touchLastOnline writes now as the last_online_at of those of domains
// this node last wrote at least every ago, in one statement per
// cOwnersChunk. A stamp never moves back: another node, or a write that
// was slower than a later one, may have written a later time.
func (dao *Dao) touchLastOnline(domains []string, every time.Duration, now time.Time) {
	if len(domains) == 0 {
		return
	}
	dao.lastOnlineMu.Lock()
	if dao.lastOnline == nil {
		dao.lastOnline = map[string]time.Time{}
	}
	var due []string
	prev := map[string]time.Time{}
	for _, d := range domains {
		// A domain listed twice is written once: the first sets it now.
		last, seen := dao.lastOnline[d]
		if seen && now.Sub(last) < every {
			continue
		}
		prev[d] = last
		dao.lastOnline[d] = now
		due = append(due, d)
	}
	dao.lastOnlineMu.Unlock()

	// Written from Go in UTC, whole seconds as the column keeps them (MySQL
	// would round), like every other time the bridge stores.
	at := now.UTC().Truncate(time.Second)
	for len(due) > 0 {
		n := min(len(due), cOwnersChunk)
		chunk := due[:n]
		due = due[n:]
		args := make([]any, 0, n+2)
		args = append(args, at)
		for _, d := range chunk {
			args = append(args, d)
		}
		args = append(args, at)
		err := dao.lastOnlineCol.run("devices", "last_online_at", func() error {
			ctx, cancel := context.WithTimeout(context.Background(), cRelayDBTimeout)
			defer cancel()
			_, err := dao.db.ExecContext(ctx, "update `devices` set `last_online_at` = ? where `domain` in (?"+strings.Repeat(", ?", n-1)+
				") and (`last_online_at` is null or `last_online_at` < ?)", args...)
			return err
		}, func() error { return nil })
		if err != nil {
			log.Error("error recording that", n, "devices are online:", err)
			// Not written: due again at the next call.
			dao.lastOnlineMu.Lock()
			for _, d := range chunk {
				if dao.lastOnline[d].Equal(now) {
					if prev[d].IsZero() {
						delete(dao.lastOnline, d)
					} else {
						dao.lastOnline[d] = prev[d]
					}
				}
			}
			dao.lastOnlineMu.Unlock()
		}
	}
}
