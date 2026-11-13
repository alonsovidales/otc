// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/alonsovidales/otc/bridge/accounts"
	"github.com/alonsovidales/otc/bridge/admin"
	"github.com/alonsovidales/otc/bridge/api"
	"github.com/alonsovidales/otc/bridge/cluster"
	"github.com/alonsovidales/otc/bridge/contact"
	"github.com/alonsovidales/otc/bridge/dao"
	"github.com/alonsovidales/otc/bridge/downloads"
	"github.com/alonsovidales/otc/bridge/fleet"
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
	"time"
)

func main() {
	env := "dev"
	if len(os.Args) > 1 {
		env = os.Args[1]
		cfg.Init("otc", env)

		log.SetLogger(
			logLevel(),
			cfg.GetStr("logger", "log_file"),
			cfg.GetInt("logger", "max_log_size_mb"),
		)
	} else {
		cfg.Init("otc", env)
	}
	runtime.GOMAXPROCS(runtime.NumCPU())

	// The contact form's messages from the shell (package contact):
	// `otc_bridge <env> messages [--all]`, `reply <id> [--again]` (the
	// answer read from stdin) and `mark-read <id>`. Before anything else
	// starts: they read and write contact_requests, a reply sends one
	// email, and nothing more happens - no pruning, no server. What they
	// say goes to the terminal, not to the log file.
	if len(os.Args) > 2 && contact.IsCommand(os.Args[2]) {
		os.Exit(contactCommand(os.Args[2], os.Args[3:]))
	}

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
	// A user's address its primary gives a new identity is emailed to the
	// owner, as a device name moved from setup or the account page is.
	webSocket.SetIdentityNotice(acc.NotifyNewIdentity)
	// Verification and password-reset emails ([smtp]).
	m, err := mailer.Init()
	if err != nil {
		log.Error("email is off:", err)
		m = nil
	} else {
		if m == nil {
			log.Info("no [smtp] section: account emails are off")
		}
		acc.SetMailer(m)
		webSocket.SetMailer(m)
	}
	startFleet(clu, adm, webSocket, m)
	// Every contact form message is mailed to the project too, out of the
	// email budget strangers share with the account emails.
	var contactNotes *contact.Notifier
	if m != nil {
		contactNotes = &contact.Notifier{Send: m.SendMail, To: m.Self(), Site: cfg.GetStr("otc-api", "tld"), Env: env, Allow: acc.AllowNote}
	}

	api.Init(
		webSocket,
		dao,
		adm,
		acc,
		contactNotes,
		clu,
		cfg.GetStr("otc-api", "static"),
		int(cfg.GetInt("otc-api", "port")),
		int(cfg.GetInt("otc-api", "ssl-port")),
		cfg.GetStr("otc-api", "ssl-cert"),
		cfg.GetStr("otc-api", "ssl-key"))

	// Issue #176: accounts unused for six months are removed (warned a
	// month before) - never while one of their devices is connected.
	acc.StartInactivityJob(webSocket.DropDomains, webSocket.IsOnline)

	// The admin panel's Growth tab: the GitHub releases' download counts.
	dl := downloads.New(dao)
	dl.Start()

	log.Info("System started...")
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, os.Kill, syscall.SIGTERM)
	// Block until a signal is received.
	<-c

	log.Info("Stopping all the services")
	dl.Stop()
	dao.Stop()
}

// startFleet runs this node's part of the admin panel's Fleet tab: its
// own report every 30 s, the tab's reader, and the email alerts (one node
// mails at a time, see fleet/alerts.go). Only in a cluster: the reports
// travel through its Redis.
func startFleet(clu *cluster.Cluster, adm *admin.Admin, ws *websocket.Manager, m *mailer.Mailer) {
	if !clu.Enabled() {
		return
	}
	rdb := clu.Redis()
	fleet.NewReporter(rdb, clu.Node(), cfg.GetStr("otc-api", "ssl-cert"), ws.FleetStats).Start()
	adm.FleetView = func(ctx context.Context) (*fleet.View, error) {
		return fleet.Read(ctx, rdb, time.Now())
	}
	if m == nil {
		log.Info("fleet alerts are off: no mailer ([smtp])")
		return
	}
	to := m.Self()
	if cfg.HasSection("fleet") && cfg.GetStr("fleet", "alert-to") != "" {
		to = cfg.GetStr("fleet", "alert-to")
	}
	link := ""
	if tld := cfg.GetStr("otc-api", "tld"); tld != "" {
		link = "https://" + tld + "/admin.html#fleet"
	}
	fleet.NewAlerter(rdb, clu.Node(), to, link, m.Send).Start()
	log.Info("fleet alerts are on ([fleet] alert-to, or the mailer's own address)")
}

// contactCommand runs one of package contact's commands with its
// arguments and returns the exit status: 0 done, 1 refused or failed, 2
// written wrong. Only reply needs the mailer ([smtp]).
func contactCommand(name string, args []string) int {
	cmd, err := contact.Parse(name, args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	d, err := dao.Open()
	if err != nil {
		fmt.Fprintln(os.Stderr, "otc_bridge:", err)
		return 1
	}
	defer d.Stop()
	sh := &contact.Shell{Store: d, In: os.Stdin, Out: os.Stdout, Err: os.Stderr, Interactive: term.IsTerminal(int(os.Stdin.Fd()))}
	if cmd.Name == "reply" {
		m, err := mailer.Init()
		if err != nil {
			fmt.Fprintln(os.Stderr, "otc_bridge:", err)
			return 1
		}
		if m != nil {
			sh.Send, sh.Self = m.SendMail, m.Self()
		}
	}
	if err := sh.Run(cmd); err != nil {
		fmt.Fprintln(os.Stderr, "otc_bridge:", err)
		return 1
	}
	return 0
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
// echo, or reads one line from a pipe. Either way admin.SetPassword then
// holds it to the panel's rules (at least 12 characters, at most 72 bytes).
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
	return string(first), nil
}
