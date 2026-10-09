// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alonsovidales/otc/bridge/fleet"
	"github.com/go-sql-driver/mysql"
)

// MySQL status through a monitoring user that can read nothing: the one
// the README creates has only REPLICATION CLIENT (SHOW REPLICA STATUS) and
// PROCESS (to count the replicas streaming from a primary), no grant on
// any database.

const cMySQLTimeout = 5 * time.Second

// mysqlDSN is the monitoring user's DSN: a unix socket path (starts with
// /) or host:port.
func mysqlDSN(addr, user, pass string) string {
	c := mysql.NewConfig()
	c.User, c.Passwd = user, pass
	if strings.HasPrefix(addr, "/") {
		c.Net, c.Addr = "unix", addr
	} else {
		c.Net, c.Addr = "tcp", addr
	}
	c.Timeout, c.ReadTimeout, c.WriteTimeout = cMySQLTimeout, cMySQLTimeout, cMySQLTimeout
	return c.FormatDSN()
}

// collectMySQL asks the server for its status. A server that doesn't
// answer is Up false with the reason (never with the DSN or password).
func collectMySQL(ctx context.Context, db *sql.DB) *fleet.MySQLStatus {
	ctx, cancel := context.WithTimeout(ctx, 2*cMySQLTimeout)
	defer cancel()
	st := &fleet.MySQLStatus{Replicas: -1}
	fail := func(what string, err error) *fleet.MySQLStatus {
		st.Up = false
		st.Err = what + ": " + mysqlErr(err)
		return st
	}
	var ro, sro sql.NullInt64
	if err := db.QueryRowContext(ctx, "SELECT @@version, @@read_only, @@max_connections").Scan(&st.Version, &ro, &st.MaxConns); err != nil {
		return fail("connect", err)
	}
	st.Up = true
	st.ReadOnly = ro.Int64 == 1
	// super_read_only is MySQL's; MariaDB has none.
	if err := db.QueryRowContext(ctx, "SELECT @@super_read_only").Scan(&sro); err == nil {
		st.SuperReadOnly = sro.Int64 == 1
	}

	rows, err := db.QueryContext(ctx, "SHOW GLOBAL STATUS WHERE Variable_name IN ('Uptime','Threads_connected','Threads_running','Max_used_connections','Slow_queries')")
	if err != nil {
		return fail("status", err)
	}
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) != nil {
			continue
		}
		n, _ := strconv.ParseInt(v, 10, 64)
		switch strings.ToLower(k) {
		case "uptime":
			st.Uptime = n
		case "threads_connected":
			st.Connected = n
		case "threads_running":
			st.Running = n
		case "max_used_connections":
			st.PeakConns = n
		case "slow_queries":
			st.SlowQueries = n
		}
	}
	rows.Close()

	rs, err := showReplicaStatus(ctx, db)
	if err != nil {
		// Without REPLICATION CLIENT the role can't be told: say so.
		st.Err = "replica status: " + mysqlErr(err)
	} else if rs != nil {
		st.Role = "replica"
		st.Replica = ReplicaFromRow(rs)
	} else {
		st.Role = "primary"
	}

	var n int64
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE COMMAND LIKE 'Binlog Dump%'").Scan(&n); err == nil {
		st.Replicas = n
	}
	return st
}

// showReplicaStatus is SHOW REPLICA STATUS's first row by column name, nil
// for a server that replicates from nobody.
func showReplicaStatus(ctx context.Context, db *sql.DB) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, "SHOW REPLICA STATUS")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		return nil, rows.Err()
	}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	m := make(map[string]string, len(cols))
	for i, c := range cols {
		if vals[i].Valid {
			m[c] = vals[i].String
		} else {
			m[c] = "NULL"
		}
	}
	return m, nil
}

// ReplicaFromRow maps SHOW REPLICA STATUS's columns, by MySQL 8's names
// (Replica_*, *_Source) or the older ones MariaDB still uses (Slave_*,
// *_Master).
func ReplicaFromRow(m map[string]string) *fleet.ReplicaStatus {
	get := func(names ...string) string {
		for _, n := range names {
			if v, ok := m[n]; ok {
				return v
			}
		}
		return ""
	}
	num := func(names ...string) int64 { n, _ := strconv.ParseInt(get(names...), 10, 64); return n }
	r := &fleet.ReplicaStatus{
		IORunning:  orUnknown(get("Replica_IO_Running", "Slave_IO_Running")),
		SQLRunning: orUnknown(get("Replica_SQL_Running", "Slave_SQL_Running")),
		IOErrno:    num("Last_IO_Errno"),
		IOError:    redactQuoted(get("Last_IO_Error")),
		SQLErrno:   num("Last_SQL_Errno"),
		SQLError:   redactQuoted(get("Last_SQL_Error")),
		Source:     get("Source_Host", "Master_Host"),
		Backlog:    -1,
	}
	if b := get("Seconds_Behind_Source", "Seconds_Behind_Master"); b != "" && b != "NULL" {
		if n, err := strconv.ParseInt(b, 10, 64); err == nil {
			r.Behind = &n
		}
	}
	retrieved, rerr := ParseGTIDSet(get("Retrieved_Gtid_Set"))
	executed, eerr := ParseGTIDSet(get("Executed_Gtid_Set"))
	if _, ok := m["Retrieved_Gtid_Set"]; ok && rerr == nil && eerr == nil {
		r.Backlog = GTIDMissing(retrieved, executed)
	}
	return r
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// reQuoted is a quoted value in a server message: a row's key in
// "Duplicate entry 'someone@example.com' for key ...", a statement. Taken
// out - the snapshot carries no personal data.
var reQuoted = regexp.MustCompile(`'[^']*'|"[^"]*"`)

func redactQuoted(s string) string {
	s = reQuoted.ReplaceAllString(s, "'…'")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// mysqlErr is an error's text fit for the snapshot: the server's number
// and message (quoted values out), or the driver's.
func mysqlErr(err error) string {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return fmt.Sprintf("error %d: %s", me.Number, redactQuoted(me.Message))
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "no answer in time"
	}
	return redactQuoted(err.Error())
}

// GTIDSet is a set of transaction numbers per source (uuid, or uuid:tag
// for MySQL 8.3's tagged GTIDs), as sorted, merged [from, to] intervals.
type GTIDSet map[string][][2]int64

// ParseGTIDSet reads "uuid:1-5:7,uuid2:1-3" (whitespace and newlines
// allowed, as SHOW REPLICA STATUS prints it).
func ParseGTIDSet(s string) (GTIDSet, error) {
	set := GTIDSet{}
	s = strings.Join(strings.Fields(s), "")
	if s == "" || s == "NULL" {
		return set, nil
	}
	for _, part := range strings.Split(s, ",") {
		if part == "" {
			continue
		}
		f := strings.Split(part, ":")
		if len(f) < 2 {
			return nil, fmt.Errorf("bad GTID set element %q", part)
		}
		key := strings.ToLower(f[0])
		src := key
		for _, iv := range f[1:] {
			a, b, isRange := strings.Cut(iv, "-")
			from, err := strconv.ParseInt(a, 10, 64)
			if err != nil {
				// A tag: the intervals after it are uuid:tag's.
				key = src + ":" + strings.ToLower(iv)
				continue
			}
			to := from
			if isRange {
				if to, err = strconv.ParseInt(b, 10, 64); err != nil || to < from {
					return nil, fmt.Errorf("bad GTID interval %q", iv)
				}
			}
			set[key] = append(set[key], [2]int64{from, to})
		}
	}
	for k := range set {
		set[k] = mergeIntervals(set[k])
	}
	return set, nil
}

func mergeIntervals(iv [][2]int64) [][2]int64 {
	sort.Slice(iv, func(i, j int) bool { return iv[i][0] < iv[j][0] })
	var out [][2]int64
	for _, x := range iv {
		if n := len(out); n > 0 && x[0] <= out[n-1][1]+1 {
			if x[1] > out[n-1][1] {
				out[n-1][1] = x[1]
			}
			continue
		}
		out = append(out, x)
	}
	return out
}

// GTIDMissing counts the transactions in a that b doesn't have.
func GTIDMissing(a, b GTIDSet) int64 {
	var n int64
	for k, ivs := range a {
		have := b[k]
		for _, x := range ivs {
			n += x[1] - x[0] + 1
			for _, y := range have {
				lo, hi := max(x[0], y[0]), min(x[1], y[1])
				if lo <= hi {
					n -= hi - lo + 1
				}
			}
		}
	}
	return n
}
