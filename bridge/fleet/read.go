// SPDX-License-Identifier: AGPL-3.0-or-later

package fleet

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// View is every known server, judged - GET /admin/api/fleet's answer.
type View struct {
	Time  time.Time  `json:"time"`
	Hosts []HostView `json:"hosts"`
}

// Read loads every snapshot from Redis and evaluates each host as of now.
// A host is any name an agent or a bridge node ever reported under (the
// known-hosts / known-bridges hashes), so one that stopped reporting is
// still listed - as stale. A snapshot that doesn't decode is treated as
// missing, never as a failure of the whole view.
func Read(ctx context.Context, rdb *redis.Client, now time.Time) (*View, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	knownHosts, err := rdb.HGetAll(ctx, KeyKnownHosts).Result()
	if err != nil {
		return nil, err
	}
	knownBridges, err := rdb.HGetAll(ctx, KeyKnownBridges).Result()
	if err != nil {
		return nil, err
	}
	byName := map[string]*HostView{}
	get := func(name string) *HostView {
		h := byName[name]
		if h == nil {
			h = &HostView{Name: name}
			byName[name] = h
		}
		return h
	}
	for name, ts := range knownHosts {
		get(name).HostSeen = unixTime(ts)
	}
	for name, ts := range knownBridges {
		get(name).BridgeSeen = unixTime(ts)
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)

	if len(names) > 0 {
		keys := make([]string, 0, 2*len(names))
		for _, n := range names {
			keys = append(keys, KeyHostPrefix+n, KeyBridgePrefix+n)
		}
		vals, err := rdb.MGet(ctx, keys...).Result()
		if err != nil {
			return nil, err
		}
		for i, n := range names {
			h := byName[n]
			if s, ok := vals[2*i].(string); ok && len(s) <= MaxSnapshotBytes {
				var hs HostSnapshot
				if json.Unmarshal([]byte(s), &hs) == nil && hs.Name == n {
					h.Host = &hs
				}
			}
			if s, ok := vals[2*i+1].(string); ok && len(s) <= MaxSnapshotBytes {
				var bs BridgeSnapshot
				if json.Unmarshal([]byte(s), &bs) == nil && bs.Node == n {
					h.Bridge = &bs
				}
			}
		}
	}

	v := &View{Time: now.UTC(), Hosts: make([]HostView, 0, len(names))}
	for _, n := range names {
		h := byName[n]
		Evaluate(h, now)
		v.Hosts = append(v.Hosts, *h)
	}
	return v, nil
}

func unixTime(s string) time.Time {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.Unix(n, 0).UTC()
}
