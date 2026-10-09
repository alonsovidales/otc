// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"errors"
	"fmt"
	"github.com/alonsovidales/otc/bridge/accounts"
	"github.com/alonsovidales/otc/bridge/admin"
	"github.com/alonsovidales/otc/bridge/api"
	"github.com/alonsovidales/otc/bridge/cluster"
	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/bridge/mailer"
	"github.com/alonsovidales/otc/bridge/websocket"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/log"
	"github.com/google/uuid"
	"golang.org/x/term"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
)

func main() {
	if len(os.Args) > 1 {
		cfg.Init("otc", os.Args[1])

		log.SetLogger(
			logLevel(),
			cfg.GetStr("logger", "log_file"),
			cfg.GetInt("logger", "max_log_size_mb"),
		)
	} else {
		cfg.Init("otc", "dev")
	}
	runtime.GOMAXPROCS(runtime.NumCPU())

	dao := dao.Init()
	adm := admin.Init(dao, sessionSecret())

	// One-off admin bootstrap: `otc_bridge <env> set-admin-password <user>`
	// sets/changes an admin panel login and exits, rather than starting the
	// server. There's no HTTP endpoint for this on purpose - it should only
	// be settable by whoever already has shell access to the bridge. The
	// password is read from the terminal without echo (or from stdin when
	// piped), never from the command line, where `ps` and the shell's
	// history would keep it (issue #163).
	if len(os.Args) > 2 && os.Args[2] == "set-admin-password" {
		if len(os.Args) != 4 {
			log.Fatal("usage: otc_bridge <env> set-admin-password <username>  (the password is asked for)")
		}
		password, err := readAdminPassword()
		if err != nil {
			log.Fatal("error reading the password:", err)
		}
		if err := adm.SetPassword(os.Args[3], password); err != nil {
			log.Fatal("error setting admin password:", err)
		}
		log.Info("Admin password set for user:", os.Args[3])
		dao.Stop()
		return
	}

	webSocket := websocket.Init(cfg.GetStr("otc-api", "base-url"), dao)
	// Issue #144: one node of several, when [cluster] says so.
	clu := cluster.Init()
	webSocket.SetCluster(clu)
	adm.IsOnline = webSocket.IsOnline
	// Issue #124: user accounts, sharing the admin panel's session secret
	// (a different cookie, the same signing key).
	acc := accounts.Init(dao, sessionSecret(), cfg.GetStr("otc-api", "tld"))
	// Verification and password-reset emails ([smtp]).
	if m, err := mailer.Init(); err != nil {
		log.Error("email is off:", err)
	} else {
		if m == nil {
			log.Info("no [smtp] section: account emails are off")
		}
		acc.SetMailer(m)
		webSocket.SetMailer(m)
	}

	api.Init(
		webSocket,
		dao,
		adm,
		acc,
		clu,
		cfg.GetStr("otc-api", "static"),
		int(cfg.GetInt("otc-api", "port")),
		int(cfg.GetInt("otc-api", "ssl-port")),
		cfg.GetStr("otc-api", "ssl-cert"),
		cfg.GetStr("otc-api", "ssl-key"))

	// Issue #176: accounts unused for six months are removed (warned a
	// month before).
	acc.StartInactivityJob(webSocket.DropDomains)

	log.Info("System started...")
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, os.Kill, syscall.SIGTERM)
	// Block until a signal is received.
	<-c

	log.Info("Stopping all the services")
	dao.Stop()
}

// sessionSecret returns [admin] session-secret from config, so admin panel
// logins survive a restart. If it's not configured, falls back to a
// per-process random value - safe (never predictable), but every restart
// invalidates existing sessions until a persistent secret is set.
func sessionSecret() []byte {
	if s := cfg.GetStr("admin", "session-secret"); s != "" {
		return []byte(s)
	}
	log.Error("[admin] session-secret is not configured - admin panel sessions will not survive a restart. Set one in the config file.")
	return []byte(uuid.New().String())
}

// logLevel is [logger] level, any case; an unknown one stops the start
// rather than logging everything (issue #162).
func logLevel() int {
	l, err := log.ParseLevel(cfg.GetStr("logger", "level"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return l
}

// readAdminPassword asks for the password twice on a terminal, without
// echo, or reads one line from a pipe.
func readAdminPassword() (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fmt.Fprint(os.Stderr, "Password: ")
	first, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "Again: ")
	second, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(first) != string(second) {
		return "", errors.New("the passwords don't match")
	}
	if len(first) < 12 { // admin.cMinPasswordLen, the panel's minimum too
		return "", errors.New("use at least 12 characters")
	}
	return string(first), nil
}
