// SPDX-License-Identifier: AGPL-3.0-or-later

// otc-fleet-agent reports its host to the admin panel's Fleet tab (package
// fleet/agent). It runs on every server of the bridge cluster as an
// unprivileged systemd service (bridge/cluster/fleet/); the passwords come
// as systemd credentials ($CREDENTIALS_DIRECTORY), never on the command
// line.
//
//	otc-fleet-agent -name bridge1 -roles bridge,mysql -redis 10.10.0.1:6379
//	otc-fleet-agent -once ...   # print one snapshot as JSON and exit
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/alonsovidales/otc/bridge/fleet"
	"github.com/alonsovidales/otc/bridge/fleet/agent"
)

func main() {
	host, _ := os.Hostname()
	creds := os.Getenv("CREDENTIALS_DIRECTORY")
	credFile := func(name string) string {
		if creds == "" {
			return ""
		}
		return filepath.Join(creds, name)
	}
	var (
		name      = flag.String("name", host, "this host's name in the Fleet tab (bridge1, bridge2, redis)")
		roles     = flag.String("roles", "", "comma-separated roles: bridge, mysql, redis, certbot")
		interval  = flag.Duration("interval", fleet.DefaultInterval, "how often to report")
		redisAddr = flag.String("redis", "10.10.0.1:6379", "Redis address")
		redisUser = flag.String("redis-user", "", "Redis ACL user (empty: the default user)")
		redisPass = flag.String("redis-pass-file", credFile("redis.pass"), "file holding the Redis password")
		myAddr    = flag.String("mysql", "/var/run/mysqld/mysqld.sock", "MySQL socket path or host:port (mysql role)")
		myUser    = flag.String("mysql-user", "otc-fleet", "MySQL monitoring user (mysql role)")
		myPass    = flag.String("mysql-pass-file", credFile("mysql.pass"), "file holding the monitoring user's password")
		certs     = flag.String("cert", "", "comma-separated certificate files whose expiry to report")
		units     = flag.String("units", "", "comma-separated extra systemd units to watch")
		once      = flag.Bool("once", false, "print one snapshot as JSON and exit, publishing nothing")
		version   = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()
	log.SetFlags(0) // journald stamps the lines
	if *version {
		fmt.Println(fleet.Version)
		return
	}
	if *name == "" {
		log.Fatal("-name is required")
	}
	cfg := agent.Config{
		Name:      *name,
		Roles:     list(*roles),
		Interval:  *interval,
		RedisAddr: *redisAddr,
		RedisUser: *redisUser,
		MySQLAddr: *myAddr,
		MySQLUser: *myUser,
		Certs:     list(*certs),
		Units:     list(*units),
	}
	var err error
	if cfg.RedisPass, err = secret(*redisPass); err != nil {
		log.Fatal("reading the Redis password: ", err)
	}
	if cfg.MySQLPass, err = secret(*myPass); err != nil {
		log.Fatal("reading the MySQL password: ", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *once {
		cfg.RedisAddr = "" // nothing published
		if contains(cfg.Roles, "redis") {
			cfg.RedisAddr = *redisAddr // only read, for its INFO
		}
		a := agent.New(cfg)
		a.Sample()
		time.Sleep(time.Second) // for the CPU and network rates
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(a.Collect(ctx))
		return
	}
	log.Printf("otc-fleet-agent %s: reporting %s (roles %q) every %s", fleet.Version, cfg.Name, cfg.Roles, cfg.Interval)
	agent.New(cfg).Run(ctx)
}

// secret reads a password file; no file named is no password. A missing
// systemd credential directory leaves the defaults empty.
func secret(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func list(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
